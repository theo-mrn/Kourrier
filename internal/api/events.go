package api

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/theo-mrn/kourrier/internal/cloudevent"
	"github.com/theo-mrn/kourrier/internal/dispatch"
	"github.com/theo-mrn/kourrier/internal/mail"
	"github.com/theo-mrn/kourrier/internal/tenant"
)

// EventDispatcher routes CloudEvents to e-mails. *dispatch.Dispatcher
// implements it.
type EventDispatcher interface {
	Dispatch(ctx context.Context, source string, t *tenant.Tenant, e *cloudevent.Event) dispatch.Report
}

// sourceHTTP labels events received by the HTTP sink in metrics and logs.
const sourceHTTP = "http"

// EventResponse is returned by POST /v1/events when the event is accepted or
// ignored.
type EventResponse struct {
	Outcome dispatch.Outcome      `json:"outcome"`
	Emails  []dispatch.Email      `json:"emails"`
	Errors  []dispatch.RouteError `json:"errors,omitempty"`
}

// handleEvent is a CloudEvents HTTP sink (structured and binary content
// modes). The status codes follow the usual push delivery contract: 2xx
// acknowledges the event, 4xx rejects it for good (the sender should
// dead-letter it), 5xx makes the sender retry, after Retry-After.
func (s *Server) handleEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.maxBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeProblem(w, r, http.StatusRequestEntityTooLarge, "too-large", "")
			return
		}
		writeProblem(w, r, http.StatusBadRequest, "malformed", "cannot read the request body")
		return
	}

	e, err := cloudevent.Parse(r.Header, body)
	if err != nil {
		s.metrics.EventsReceived.WithLabelValues(sourceHTTP, string(dispatch.Rejected)).Inc()
	}
	switch {
	case errors.Is(err, cloudevent.ErrNotCloudEvent):
		writeProblem(w, r, http.StatusUnsupportedMediaType, "not-a-cloudevent",
			"send a CloudEvent in structured mode (Content-Type: application/cloudevents+json) or binary mode (ce-* headers)")
		return
	case errors.Is(err, cloudevent.ErrUnsupported):
		writeProblem(w, r, http.StatusUnsupportedMediaType, "unsupported-cloudevent", err.Error())
		return
	case err != nil:
		writeProblem(w, r, http.StatusBadRequest, "malformed-cloudevent", err.Error())
		return
	}

	rep := s.events.Dispatch(ctx, sourceHTTP, tenantFrom(ctx), e)
	switch rep.Outcome {
	case dispatch.Accepted, dispatch.Ignored:
		if rep.Emails == nil {
			rep.Emails = []dispatch.Email{}
		}
		writeJSON(w, http.StatusAccepted, EventResponse{Outcome: rep.Outcome, Emails: rep.Emails, Errors: rep.Errors})
	case dispatch.Rejected:
		p := &Problem{
			Type: problemBase + "event-rejected", Title: "Event rejected", Status: http.StatusUnprocessableEntity,
			Detail: "no e-mail could be built from this event",
		}
		if rep.Err != nil {
			p.Detail = rep.Err.Error()
		}
		for _, re := range rep.Errors {
			p.Errors = append(p.Errors, mail.FieldError{Field: "routes." + re.Route, Message: re.Detail})
		}
		writeProblemDetails(w, r, p)
	default: // dispatch.Retry
		if errors.Is(rep.Err, dispatch.ErrBackpressure) {
			w.Header().Set("Retry-After", "30")
			writeProblem(w, r, http.StatusServiceUnavailable, "backpressure", rep.Err.Error())
			return
		}
		s.unavailable(w, r, "dispatch event", rep.Err)
	}
}
