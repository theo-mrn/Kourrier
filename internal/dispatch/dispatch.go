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
	"github.com/theo-mrn/kourrier/internal/deadevent"
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

var (
	// ErrBackpressure is the cause of a Retry when the e-mail queue is above
	// its configured depth.
	ErrBackpressure = errors.New("e-mail queue is full, event ingestion paused")
	// ErrTenantRateLimited is the cause of a Retry when the tenant exceeds
	// its e-mail rate (limits in the routes file).
	ErrTenantRateLimited = errors.New("tenant e-mail rate exceeded")
)

// Submitter enqueues e-mail requests. *submit.Service implements it.
type Submitter interface {
	Submit(ctx context.Context, r *submit.Request) (submit.Result, error)
}

// Gate tells whether ingestion must pause. *DepthGate implements it.
type Gate interface {
	Overloaded() bool
}

// TenantLimiter takes a token from a per-tenant bucket. *ratelimit.Keyed
// implements it.
type TenantLimiter interface {
	Allow(ctx context.Context, key string, perSecond float64, burst int) (time.Duration, error)
}

// DeadLetters keeps events that could not be fully processed.
// *deadevent.Store implements it.
type DeadLetters interface {
	Add(ctx context.Context, e deadevent.Entry) (string, error)
}

// SourceReplay labels replays of dead-lettered events in metrics and logs.
const SourceReplay = "replay"

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
	// DeadLetterID is set when the event was kept in the event dead-letter
	// queue (rejected, partially failed, or unrouted on a strict source).
	DeadLetterID string
	// RetryAfter suggests when to redeliver a Retry; 0 lets the transport
	// choose.
	RetryAfter time.Duration
}

// Dispatcher routes events and submits the resulting e-mails.
type Dispatcher struct {
	router      *routing.Router
	submit      Submitter
	gate        Gate
	deadLetters DeadLetters
	strict      map[string]bool
	limiter     TenantLimiter
	limits      routing.Limits
	metrics     *observability.Metrics
	logger      *slog.Logger
}

// Deps groups the collaborators of a Dispatcher. Gate is optional.
type Deps struct {
	Router    *routing.Router
	Submitter Submitter
	Gate      Gate
	// DeadLetters is optional. Without it, rejected events are only logged.
	DeadLetters DeadLetters
	// DeadLetterUnrouted lists the sources whose unrouted events are kept
	// in the dead-letter queue instead of being ignored.
	DeadLetterUnrouted []string
	// Limiter and Limits cap the e-mails each tenant's events may create.
	Limiter TenantLimiter
	Limits  routing.Limits
	Metrics *observability.Metrics
	Logger  *slog.Logger
}

// New creates a Dispatcher.
func New(d Deps) *Dispatcher {
	strict := map[string]bool{SourceReplay: true}
	for _, s := range d.DeadLetterUnrouted {
		strict[s] = true
	}
	return &Dispatcher{
		router: d.Router, submit: d.Submitter, gate: d.Gate, deadLetters: d.DeadLetters, strict: strict,
		limiter: d.Limiter, limits: d.Limits,
		metrics: d.Metrics, logger: d.Logger,
	}
}

// Dispatch routes e with the routes of tenant t. source names the transport
// for metrics and logs ("http" or a NATS source name).
func (d *Dispatcher) Dispatch(ctx context.Context, source string, t *tenant.Tenant, e *cloudevent.Event) Report {
	return d.run(ctx, source, t, e, nil)
}

// Replay dispatches a dead-lettered event again, restricted to routes (all
// routes when empty). Routes that succeeded before are deduplicated by
// idempotency anyway; restricting them avoids resending after the
// idempotency TTL. If the event still fails, it is dead-lettered again.
func (d *Dispatcher) Replay(ctx context.Context, t *tenant.Tenant, e *cloudevent.Event, routes []string) Report {
	return d.run(ctx, SourceReplay, t, e, routes)
}

func (d *Dispatcher) run(ctx context.Context, source string, t *tenant.Tenant, e *cloudevent.Event, only []string) Report {
	start := time.Now()
	rep := d.dispatch(ctx, t, e, only)
	if rep.Outcome != Retry {
		d.deadLetter(ctx, source, t, e, &rep)
	}
	d.metrics.EventDispatch.WithLabelValues(source).Observe(time.Since(start).Seconds())
	d.metrics.EventsReceived.WithLabelValues(source, string(rep.Outcome)).Inc()

	log := d.logger.With("source", source, "tenant", t.ID,
		"event_id", e.ID, "event_type", e.Type, "event_source", e.Source, "outcome", string(rep.Outcome))
	switch rep.Outcome {
	case Ignored:
		log.DebugContext(ctx, "event ignored: no matching route")
	case Accepted:
		log.InfoContext(ctx, "event dispatched", "emails", len(rep.Emails), "route_errors", rep.Errors, "dead_letter_id", rep.DeadLetterID)
	case Rejected:
		log.WarnContext(ctx, "event rejected", "error", rep.Err, "route_errors", rep.Errors, "dead_letter_id", rep.DeadLetterID)
	case Retry:
		log.WarnContext(ctx, "event dispatch failed, it will be redelivered", "error", rep.Err)
	}
	return rep
}

// deadLetter keeps the event when part of it was not processed. If the event
// cannot be stored, the outcome becomes Retry: the broker keeps the event
// rather than Kourrier losing it.
func (d *Dispatcher) deadLetter(ctx context.Context, source string, t *tenant.Tenant, e *cloudevent.Event, rep *Report) {
	var reason deadevent.Reason
	switch {
	case rep.Outcome == Rejected:
		reason = deadevent.Rejected
	case rep.Outcome == Accepted && len(rep.Errors) > 0:
		reason = deadevent.Partial
	case rep.Outcome == Ignored && d.strict[source]:
		reason = deadevent.Unrouted
	default:
		return
	}
	if d.deadLetters == nil {
		return
	}
	entry := deadevent.Entry{Tenant: t.ID, Source: source, Reason: reason}
	for _, re := range rep.Errors {
		entry.Routes = append(entry.Routes, re.Route)
	}
	switch {
	case rep.Err != nil:
		entry.Detail = rep.Err.Error()
	case len(rep.Errors) > 0:
		entry.Detail = rep.Errors[0].Route + ": " + rep.Errors[0].Detail
	default:
		entry.Detail = "no route matches type " + e.Type
	}
	raw, err := e.MarshalStructured()
	if err == nil {
		entry.Event = raw
		rep.DeadLetterID, err = d.deadLetters.Add(ctx, entry)
	}
	if err != nil {
		*rep = Report{Outcome: Retry, Err: fmt.Errorf("%w: cannot keep the event in the dead-letter queue: %w", submit.ErrUnavailable, err)}
		return
	}
	d.metrics.EventsDeadLettered.WithLabelValues(source, string(reason)).Inc()
}

func (d *Dispatcher) dispatch(ctx context.Context, t *tenant.Tenant, e *cloudevent.Event, only []string) Report {
	results, err := d.router.RouteOnly(t.ID, e, only)
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
	limit := d.limits.For(t.ID)
	for _, res := range results {
		route := res.Route.Name
		if res.Err != nil {
			rep.Errors = append(rep.Errors, RouteError{Route: route, Detail: res.Err.Error()})
			d.metrics.EventRoutes.WithLabelValues(t.ID, route, "rejected").Inc()
			continue
		}
		if d.limiter != nil && limit.EmailsPerSecond > 0 {
			wait, err := d.limiter.Allow(ctx, t.ID, limit.EmailsPerSecond, max(limit.Burst, 1))
			if err != nil {
				rep.Err = fmt.Errorf("%w: %w", submit.ErrUnavailable, err)
				break
			}
			if wait > 0 {
				// The remaining routes run on redelivery; the ones already
				// submitted are deduplicated by idempotency.
				d.metrics.EventRoutes.WithLabelValues(t.ID, route, "throttled").Inc()
				rep.Err, rep.RetryAfter = ErrTenantRateLimited, wait
				break
			}
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
