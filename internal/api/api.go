// Package api exposes the HTTP API used by client applications to submit
// e-mails (ADR 002). It validates and enqueues requests and answers
// 202 Accepted immediately: rendering and sending happen in the workers. It
// also exposes the tenant's templates: list and preview (ADR 011).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/theo-mrn/kourrier/internal/id"
	"github.com/theo-mrn/kourrier/internal/idempotency"
	"github.com/theo-mrn/kourrier/internal/mail"
	"github.com/theo-mrn/kourrier/internal/observability"
	"github.com/theo-mrn/kourrier/internal/status"
	"github.com/theo-mrn/kourrier/internal/submit"
	"github.com/theo-mrn/kourrier/internal/templates"
	"github.com/theo-mrn/kourrier/internal/tenant"
)

// Submitter validates and enqueues e-mail requests.
type Submitter interface {
	Submit(ctx context.Context, r *submit.Request) (submit.Result, error)
}

// StatusReader reads delivery statuses.
type StatusReader interface {
	Get(ctx context.Context, id string) (*status.Status, error)
}

// Server is the public HTTP API.
type Server struct {
	tenants   *tenant.Registry
	submit    Submitter
	statuses  StatusReader
	templates Templates
	events    EventDispatcher
	metrics   *observability.Metrics
	logger    *slog.Logger
	maxBody   int64
	maxEvent  int64
}

// Deps groups the collaborators of a Server.
type Deps struct {
	Tenants   *tenant.Registry
	Submitter Submitter
	Statuses  StatusReader
	Templates Templates
	// Events enables POST /v1/events (ADR 012). Nil disables the endpoint.
	Events  EventDispatcher
	Metrics *observability.Metrics
	Logger  *slog.Logger
	// MaxBodyBytes limits request bodies (default 1 MiB).
	MaxBodyBytes int64
	// MaxEventBytes limits POST /v1/events bodies (default 256 KiB).
	MaxEventBytes int64
}

// New creates a Server.
func New(d Deps) *Server {
	if d.MaxBodyBytes <= 0 {
		d.MaxBodyBytes = 1 << 20
	}
	if d.MaxEventBytes <= 0 {
		d.MaxEventBytes = 256 << 10
	}
	return &Server{
		tenants: d.Tenants, submit: d.Submitter, statuses: d.Statuses, templates: d.Templates, events: d.Events,
		metrics: d.Metrics, logger: d.Logger, maxBody: d.MaxBodyBytes, maxEvent: d.MaxEventBytes,
	}
}

// Handler returns the HTTP handler with its middleware chain.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/emails", s.requireTenant(tenant.ScopeEmails, s.handleSend))
	// Any scope may read statuses: event producers follow the e-mails their
	// events created.
	mux.HandleFunc("GET /v1/emails/{id}", s.requireTenant("", s.handleGet))
	// Any scope may read the templates too: event routes choose them as well.
	mux.HandleFunc("GET /v1/templates", s.requireTenant("", s.handleListTemplates))
	mux.HandleFunc("POST /v1/templates/{name}/preview", s.requireTenant("", s.handlePreview))
	if s.events != nil {
		mux.HandleFunc("POST /v1/events", s.requireTenant(tenant.ScopeEvents, s.handleEvent))
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, http.StatusNotFound, "not-found", "no such endpoint")
	})
	return withRecover(s.logger, withRequestID(withObservability(s.logger, s.metrics, mux)))
}

// SendResponse is returned by POST /v1/emails.
type SendResponse struct {
	ID     string       `json:"id"`
	Status status.State `json:"status"`
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req mail.Request
	if p := s.decode(w, r, &req); p != nil {
		writeProblemDetails(w, r, p)
		return
	}
	idemKey := r.Header.Get("Idempotency-Key")
	if idemKey != "" && !idempotency.ValidKey(idemKey) {
		writeProblem(w, r, http.StatusBadRequest, "idempotency-key", "Idempotency-Key must be 1-255 printable ASCII characters")
		return
	}

	res, err := s.submit.Submit(ctx, &submit.Request{
		Tenant:         tenantFrom(ctx),
		Mail:           req,
		Origin:         submit.OriginAPI,
		IdempotencyKey: idemKey,
		LogAttrs:       []any{"request_id", requestIDFrom(ctx)},
	})
	if err != nil {
		s.submitError(w, r, err, req.Template)
		return
	}
	if res.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	s.accepted(w, res.JobID)
}

// submitError maps the errors of submit.Service to problem responses. tpl is
// the template of the request, if any.
func (s *Server) submitError(w http.ResponseWriter, r *http.Request, err error, tpl *mail.TemplateRef) {
	var ve *mail.ValidationError
	switch {
	case errors.As(err, &ve):
		writeProblemDetails(w, r, &Problem{
			Type: problemBase + "validation", Title: "Invalid request", Status: http.StatusUnprocessableEntity,
			Detail: "the request body contains invalid fields", Errors: ve.Errors,
		})
	case errors.Is(err, submit.ErrSenderNotAllowed):
		writeProblem(w, r, http.StatusForbidden, "sender-not-allowed", err.Error())
	case errors.Is(err, templates.ErrNotFound):
		writeProblemDetails(w, r, templateNotFound(tpl.Name, http.StatusUnprocessableEntity))
	case errors.Is(err, templates.ErrInvalid):
		writeProblemDetails(w, r, templateInvalid(err))
	case errors.Is(err, submit.ErrIdempotencyConflict):
		writeProblem(w, r, http.StatusUnprocessableEntity, "idempotency-key-reused",
			"this Idempotency-Key was already used with a different request body")
	case errors.Is(err, submit.ErrUnavailable):
		s.unavailable(w, r, "submit", err)
	default:
		s.internalError(w, r, "submit", err)
	}
}

func (s *Server) accepted(w http.ResponseWriter, jobID string) {
	w.Header().Set("Location", "/v1/emails/"+jobID)
	writeJSON(w, http.StatusAccepted, SendResponse{ID: jobID, Status: status.Queued})
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	emailID := r.PathValue("id")
	if !id.Valid(emailID) {
		writeProblem(w, r, http.StatusNotFound, "not-found", "unknown e-mail id")
		return
	}
	st, err := s.statuses.Get(r.Context(), emailID)
	if err != nil {
		s.unavailable(w, r, "get status", err)
		return
	}
	// Statuses of other tenants are reported as missing, not forbidden, to
	// avoid leaking which IDs exist.
	if st == nil || st.TenantID != tenantFrom(r.Context()).ID {
		writeProblem(w, r, http.StatusNotFound, "not-found", "unknown e-mail id")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// decode reads a strict JSON body into v.
func (s *Server) decode(w http.ResponseWriter, r *http.Request, v any) *Problem {
	bad := func(detail string) *Problem {
		return &Problem{Type: problemBase + "malformed", Title: "Malformed request", Status: http.StatusBadRequest, Detail: detail}
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, s.maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return &Problem{Type: problemBase + "too-large", Title: "Request too large", Status: http.StatusRequestEntityTooLarge}
		}
		return bad(err.Error())
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return bad("request body must contain a single JSON object")
	}
	return nil
}

func (s *Server) unavailable(w http.ResponseWriter, r *http.Request, op string, err error) {
	s.logger.ErrorContext(r.Context(), "backend unavailable", "op", op, "error", err, "request_id", requestIDFrom(r.Context()))
	w.Header().Set("Retry-After", "5")
	writeProblem(w, r, http.StatusServiceUnavailable, "unavailable", "the service is temporarily unavailable, retry later")
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, op string, err error) {
	s.logger.ErrorContext(r.Context(), "internal error", "op", op, "error", err, "request_id", requestIDFrom(r.Context()))
	writeProblem(w, r, http.StatusInternalServerError, "internal", "")
}
