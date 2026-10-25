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
	"time"

	"github.com/theo-mrn/kourrier/internal/id"
	"github.com/theo-mrn/kourrier/internal/idempotency"
	"github.com/theo-mrn/kourrier/internal/mail"
	"github.com/theo-mrn/kourrier/internal/observability"
	"github.com/theo-mrn/kourrier/internal/queue"
	"github.com/theo-mrn/kourrier/internal/status"
	"github.com/theo-mrn/kourrier/internal/tenant"
)

// Enqueuer pushes jobs to the queue.
type Enqueuer interface {
	Enqueue(ctx context.Context, job *queue.Job) error
}

// IdempotencyStore remembers Idempotency-Key values.
type IdempotencyStore interface {
	Reserve(ctx context.Context, tenant, key string, rec idempotency.Record) (*idempotency.Record, error)
	Release(ctx context.Context, tenant, key string) error
}

// StatusStore reads and writes delivery statuses.
type StatusStore interface {
	Get(ctx context.Context, id string) (*status.Status, error)
	Put(ctx context.Context, st *status.Status) error
}

// Server is the public HTTP API.
type Server struct {
	tenants     *tenant.Registry
	queue       Enqueuer
	idempotency IdempotencyStore
	statuses    StatusStore
	templates   Templates
	metrics     *observability.Metrics
	logger      *slog.Logger
	maxBody     int64
}

// Deps groups the collaborators of a Server.
type Deps struct {
	Tenants     *tenant.Registry
	Queue       Enqueuer
	Idempotency IdempotencyStore
	Statuses    StatusStore
	Templates   Templates
	Metrics     *observability.Metrics
	Logger      *slog.Logger
	// MaxBodyBytes limits request bodies (default 1 MiB).
	MaxBodyBytes int64
}

// New creates a Server.
func New(d Deps) *Server {
	if d.MaxBodyBytes <= 0 {
		d.MaxBodyBytes = 1 << 20
	}
	return &Server{
		tenants: d.Tenants, queue: d.Queue, idempotency: d.Idempotency, statuses: d.Statuses,
		templates: d.Templates, metrics: d.Metrics, logger: d.Logger, maxBody: d.MaxBodyBytes,
	}
}

// Handler returns the HTTP handler with its middleware chain.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/emails", s.requireTenant(s.handleSend))
	mux.HandleFunc("GET /v1/emails/{id}", s.requireTenant(s.handleGet))
	mux.HandleFunc("GET /v1/templates", s.requireTenant(s.handleListTemplates))
	mux.HandleFunc("POST /v1/templates/{name}/preview", s.requireTenant(s.handlePreview))
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
	t := tenantFrom(ctx)

	var req mail.Request
	if p := s.decode(w, r, &req); p != nil {
		writeProblemDetails(w, r, p)
		return
	}
	req.Normalize()
	if req.From.Email == "" {
		req.From = t.DefaultSender
	}
	if err := req.Validate(); err != nil {
		var ve *mail.ValidationError
		errors.As(err, &ve)
		writeProblemDetails(w, r, &Problem{
			Type: problemBase + "validation", Title: "Invalid request", Status: http.StatusUnprocessableEntity,
			Detail: "the request body contains invalid fields", Errors: ve.Errors,
		})
		return
	}
	if !t.CanSendFrom(req.From.Email) {
		writeProblem(w, r, http.StatusForbidden, "sender-not-allowed",
			"sender "+req.From.Email+" is not allowed for this API key")
		return
	}
	// Fail fast on a template that would never render, instead of answering
	// 202 and dead-lettering the e-mail later (ADR 011).
	if req.Template != nil {
		if p := s.checkTemplate(ctx, t.ID, req.Template); p != nil {
			writeProblemDetails(w, r, p)
			return
		}
	}

	jobID := id.New()
	idemKey := r.Header.Get("Idempotency-Key")
	if idemKey != "" {
		if !idempotency.ValidKey(idemKey) {
			writeProblem(w, r, http.StatusBadRequest, "idempotency-key", "Idempotency-Key must be 1-255 printable ASCII characters")
			return
		}
		fp, err := idempotency.Fingerprint(&req)
		if err != nil {
			s.internalError(w, r, "fingerprint request", err)
			return
		}
		existing, err := s.idempotency.Reserve(ctx, t.ID, idemKey, idempotency.Record{JobID: jobID, Fingerprint: fp})
		if err != nil {
			s.unavailable(w, r, "reserve idempotency key", err)
			return
		}
		if existing != nil {
			if existing.Fingerprint != fp {
				writeProblem(w, r, http.StatusUnprocessableEntity, "idempotency-key-reused",
					"this Idempotency-Key was already used with a different request body")
				return
			}
			s.metrics.IdempotentReplays.WithLabelValues(t.ID).Inc()
			w.Header().Set("Idempotent-Replayed", "true")
			s.accepted(w, existing.JobID)
			return
		}
	}

	now := time.Now().UTC()
	// The status is written before the job is enqueued so that a fast worker
	// can never have its "sent" status overwritten by "queued".
	err := s.statuses.Put(ctx, &status.Status{ID: jobID, TenantID: t.ID, State: status.Queued, CreatedAt: now, UpdatedAt: now})
	if err == nil {
		err = s.queue.Enqueue(ctx, &queue.Job{ID: jobID, TenantID: t.ID, EnqueuedAt: now, Request: req})
	}
	if err != nil {
		if idemKey != "" {
			if relErr := s.idempotency.Release(context.WithoutCancel(ctx), t.ID, idemKey); relErr != nil {
				s.logger.ErrorContext(ctx, "failed to release idempotency key", "error", relErr)
			}
		}
		s.unavailable(w, r, "enqueue job", err)
		return
	}

	s.metrics.JobsEnqueued.WithLabelValues(t.ID).Inc()
	s.logger.InfoContext(ctx, "email queued",
		"email_id", jobID, "tenant", t.ID, "request_id", requestIDFrom(ctx),
		"to", observability.MaskAddresses(req.To), "template", templateName(&req))
	s.accepted(w, jobID)
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

func templateName(req *mail.Request) string {
	if req.Template != nil {
		return req.Template.Name
	}
	return ""
}
