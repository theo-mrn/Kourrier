// Package dispatch turns a CloudEvent into queued e-mails (ADR 012). It is
// transport-agnostic: the HTTP sink and the NATS consumer only translate its
// Report into their own acknowledgement semantics.
//
// Delivery from a broker is at-least-once, so every e-mail derived from an
// event is submitted with an idempotency key built from the route name and
// the event identity (source, id). A redelivered event, or one retried after
// a partial failure, never produces the same e-mail twice within the
// idempotency TTL.
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/theo-mrn/kourrier/internal/cloudevent"
	"github.com/theo-mrn/kourrier/internal/mail"
	"github.com/theo-mrn/kourrier/internal/observability"
	"github.com/theo-mrn/kourrier/internal/routing"
	"github.com/theo-mrn/kourrier/internal/submit"
	"github.com/theo-mrn/kourrier/internal/tenant"
)

// Outcome summarises what happened to an event. Transports map it to an
// acknowledgement: Accepted and Ignored are positive acknowledgements,
// Rejected must not be redelivered, Retry must be.
type Outcome string

const (
	// Accepted: at least one e-mail was queued (or had already been queued)
	// and nothing needs to be retried.
	Accepted Outcome = "accepted"
	// Ignored: no route matched the event.
	Ignored Outcome = "ignored"
	// Rejected: every matching route failed permanently, or the event payload
	// is unusable. Redelivering it would fail the same way.
	Rejected Outcome = "rejected"
	// Retry: a transient failure (backend unavailable, backpressure). The
	// event must be redelivered; e-mails already queued are deduplicated.
	Retry Outcome = "retry"
)

// ErrBackpressure is the cause of a Retry when the e-mail queue is above its
// configured depth.
var ErrBackpressure = errors.New("e-mail queue is full, event ingestion paused")

// Submitter enqueues e-mail requests. *submit.Service implements it.
type Submitter interface {
	Submit(ctx context.Context, r *submit.Request) (submit.Result, error)
}

// Gate tells whether ingestion must pause. *DepthGate implements it.
type Gate interface {
	Overloaded() bool
}

// Email is an e-mail queued for an event.
type Email struct {
	Route    string `json:"route"`
	ID       string `json:"id"`
	Replayed bool   `json:"replayed,omitempty"`
}

// RouteError is a permanent failure of one route.
type RouteError struct {
	Route  string `json:"route"`
	Detail string `json:"detail"`
}

// Report is the result of dispatching one event.
type Report struct {
	Outcome Outcome
	Emails  []Email
	Errors  []RouteError
	// Err is the cause of a Retry, or of a Rejected outcome when the event
	// payload itself is unusable.
	Err error
}

// Dispatcher routes events and submits the resulting e-mails.
type Dispatcher struct {
	router  *routing.Router
	submit  Submitter
	gate    Gate
	metrics *observability.Metrics
	logger  *slog.Logger
}

// Deps groups the collaborators of a Dispatcher. Gate is optional.
type Deps struct {
	Router    *routing.Router
	Submitter Submitter
	Gate      Gate
	Metrics   *observability.Metrics
	Logger    *slog.Logger
}

// New creates a Dispatcher.
func New(d Deps) *Dispatcher {
	return &Dispatcher{router: d.Router, submit: d.Submitter, gate: d.Gate, metrics: d.Metrics, logger: d.Logger}
}

// Dispatch routes e with the routes of tenant t. source names the transport
// for metrics and logs ("http" or a NATS source name).
func (d *Dispatcher) Dispatch(ctx context.Context, source string, t *tenant.Tenant, e *cloudevent.Event) Report {
	start := time.Now()
	rep := d.dispatch(ctx, t, e)
	d.metrics.EventDispatch.WithLabelValues(source).Observe(time.Since(start).Seconds())
	d.metrics.EventsReceived.WithLabelValues(source, string(rep.Outcome)).Inc()

	log := d.logger.With("source", source, "tenant", t.ID,
		"event_id", e.ID, "event_type", e.Type, "event_source", e.Source, "outcome", string(rep.Outcome))
	switch rep.Outcome {
	case Ignored:
		log.DebugContext(ctx, "event ignored: no matching route")
	case Accepted:
		log.InfoContext(ctx, "event dispatched", "emails", len(rep.Emails), "route_errors", len(rep.Errors))
	case Rejected:
		log.WarnContext(ctx, "event rejected", "error", rep.Err, "route_errors", rep.Errors)
	case Retry:
		log.WarnContext(ctx, "event dispatch failed, it will be redelivered", "error", rep.Err)
	}
	return rep
}

func (d *Dispatcher) dispatch(ctx context.Context, t *tenant.Tenant, e *cloudevent.Event) Report {
	results, err := d.router.Route(t.ID, e)
	if err != nil {
		return Report{Outcome: Rejected, Err: err}
	}
	if len(results) == 0 {
		return Report{Outcome: Ignored}
	}
	// Checked after routing so that unrouted events are still drained
	// while the queue is full: they cost nothing.
	if d.gate != nil && d.gate.Overloaded() {
		return Report{Outcome: Retry, Err: ErrBackpressure}
	}

	var rep Report
	for _, res := range results {
		route := res.Route.Name
		if res.Err != nil {
			rep.Errors = append(rep.Errors, RouteError{Route: route, Detail: res.Err.Error()})
			d.metrics.EventRoutes.WithLabelValues(t.ID, route, "rejected").Inc()
			continue
		}
		key := idempotencyKey(route, e)
		out, err := d.submit.Submit(ctx, &submit.Request{
			Tenant:         t,
			Mail:           *res.Request,
			Origin:         submit.OriginEvent,
			IdempotencyKey: key,
			// The event identity is the content: a redelivered event is the
			// same event even if the routes changed in between.
			Fingerprint: key,
			LogAttrs:    []any{"route", route, "event_id", e.ID},
		})
		switch {
		case err == nil:
			rep.Emails = append(rep.Emails, Email{Route: route, ID: out.JobID, Replayed: out.Replayed})
			outcome := "queued"
			if out.Replayed {
				outcome = "replayed"
			}
			d.metrics.EventRoutes.WithLabelValues(t.ID, route, outcome).Inc()
		case errors.Is(err, submit.ErrUnavailable) || ctx.Err() != nil:
			d.metrics.EventRoutes.WithLabelValues(t.ID, route, "failed").Inc()
			if rep.Err == nil {
				rep.Err = err
			}
		default:
			rep.Errors = append(rep.Errors, RouteError{Route: route, Detail: describe(err)})
			d.metrics.EventRoutes.WithLabelValues(t.ID, route, "rejected").Inc()
		}
	}

	switch {
	case rep.Err != nil:
		rep.Outcome = Retry
	case len(rep.Emails) > 0:
		rep.Outcome = Accepted
	default:
		rep.Outcome = Rejected
	}
	return rep
}

// idempotencyKey identifies the e-mail produced by route for event e. The
// CloudEvents specification makes (source, id) unique per event; the source
// is length-prefixed so that no (source, id) pair can collide with another.
func idempotencyKey(route string, e *cloudevent.Event) string {
	return route + "|" + strconv.Itoa(len(e.Source)) + ":" + e.Source + "|" + e.ID
}

// describe renders a permanent submission error for operators.
func describe(err error) string {
	var ve *mail.ValidationError
	if errors.As(err, &ve) {
		return fmt.Sprintf("the e-mail built by the route is invalid: %v", ve)
	}
	return err.Error()
}
