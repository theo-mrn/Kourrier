// Package submit is the application service that turns a validated e-mail
// request into a queued job. It is the single entry point to the queue: the
// HTTP API and the event ingress (ADR 012) both go through it, so validation,
// sender authorisation, idempotency and status tracking behave identically
// whatever the origin of the request. It also rejects requests whose template
// does not exist or does not compile (ADR 011).
package submit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/theo-mrn/kourrier/internal/id"
	"github.com/theo-mrn/kourrier/internal/idempotency"
	"github.com/theo-mrn/kourrier/internal/mail"
	"github.com/theo-mrn/kourrier/internal/observability"
	"github.com/theo-mrn/kourrier/internal/queue"
	"github.com/theo-mrn/kourrier/internal/status"
	"github.com/theo-mrn/kourrier/internal/templates"
	"github.com/theo-mrn/kourrier/internal/tenant"
)

// Origin tells where a submission comes from. It selects the idempotency
// namespace and is reported in logs.
type Origin string

const (
	OriginAPI   Origin = "api"
	OriginEvent Origin = "event"
)

var (
	// ErrSenderNotAllowed means the From address is not in the tenant's
	// allowed_senders.
	ErrSenderNotAllowed = errors.New("sender not allowed for this tenant")
	// ErrIdempotencyConflict means the idempotency key was already used with a
	// different request.
	ErrIdempotencyConflict = errors.New("idempotency key reused with a different request")
	// ErrUnavailable wraps backend failures (Redis). The caller may retry.
	ErrUnavailable = errors.New("backend unavailable")
)

// Enqueuer adds a job and its companion keys atomically.
// *queue.RedisStreams implements it.
type Enqueuer interface {
	EnqueueAtomic(ctx context.Context, job *queue.Job, guard *queue.Write, writes ...queue.Write) ([]byte, error)
}

// TemplateChecker tells whether a template exists and compiles.
// *templates.Renderer implements it.
type TemplateChecker interface {
	Check(ctx context.Context, tenant, name, locale string) error
}

// templateCheckTimeout bounds the storage round-trip added to a submission
// when the template is not cached yet.
const templateCheckTimeout = 2 * time.Second

// Service validates and enqueues e-mail requests.
type Service struct {
	queue       Enqueuer
	idempotency *idempotency.Store
	statuses    *status.Store
	templates   TemplateChecker
	metrics     *observability.Metrics
	logger      *slog.Logger
	now         func() time.Time
}

// Deps groups the collaborators of a Service.
type Deps struct {
	Queue       Enqueuer
	Idempotency *idempotency.Store
	Statuses    *status.Store
	// Templates enables the template check (ADR 011). Nil disables it.
	Templates TemplateChecker
	Metrics   *observability.Metrics
	Logger    *slog.Logger
}

// New creates a Service.
func New(d Deps) *Service {
	return &Service{
		queue: d.Queue, idempotency: d.Idempotency, statuses: d.Statuses, templates: d.Templates,
		metrics: d.Metrics, logger: d.Logger, now: time.Now,
	}
}

// Request is a submission.
type Request struct {
	Tenant *tenant.Tenant
	Mail   mail.Request
	Origin Origin
	// IdempotencyKey deduplicates submissions within the tenant and origin.
	// Empty disables deduplication.
	IdempotencyKey string
	// Fingerprint identifies the content behind IdempotencyKey. When empty,
	// it is computed from Mail, so that a key reused with a different body is
	// rejected with ErrIdempotencyConflict.
	Fingerprint string
	// LogAttrs are added to the "email queued" log line.
	LogAttrs []any
}

// Result describes an accepted submission.
type Result struct {
	JobID string
	// Replayed is true when the idempotency key had already been used: no new
	// job was created and JobID is the original one.
	Replayed bool
}

// Submit normalises, validates and enqueues r.Mail. Errors are either a
// *mail.ValidationError, ErrSenderNotAllowed, ErrIdempotencyConflict, wrap
// templates.ErrNotFound or templates.ErrInvalid, or wrap ErrUnavailable;
// r.Mail is modified in place by normalisation.
func (s *Service) Submit(ctx context.Context, r *Request) (Result, error) {
	t := r.Tenant
	req := &r.Mail
	if err := Prepare(t, req); err != nil {
		return Result{}, err
	}
	// Fail fast on a template that would never render, instead of accepting
	// the e-mail and dead-lettering it later (ADR 011).
	if err := s.checkTemplate(ctx, t.ID, req.Template); err != nil {
		return Result{}, err
	}

	jobID := id.New()
	now := s.now().UTC()
	// The "queued" status, the idempotency record and the job are written in
	// one atomic step: a record never exists without its job, and a retried
	// request after a lost reply finds the record instead of duplicating the
	// e-mail. Writing the status before the job is visible also guarantees
	// that a fast worker's "sent" is never overwritten by "queued".
	stKey, stVal, stTTL, err := s.statuses.Prepare(&status.Status{
		ID: jobID, TenantID: t.ID, State: status.Queued, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return Result{}, fmt.Errorf("encode status: %w", err)
	}
	var guard *queue.Write
	fp := r.Fingerprint
	if r.IdempotencyKey != "" {
		if fp == "" {
			if fp, err = idempotency.Fingerprint(req); err != nil {
				return Result{}, fmt.Errorf("fingerprint request: %w", err)
			}
		}
		key, val, ttl, err := s.idempotency.Prepare(idempotencyScope(t.ID, r.Origin), r.IdempotencyKey,
			idempotency.Record{JobID: jobID, Fingerprint: fp})
		if err != nil {
			return Result{}, fmt.Errorf("encode idempotency record: %w", err)
		}
		guard = &queue.Write{Key: key, Value: val, TTL: ttl}
	}

	prev, err := s.queue.EnqueueAtomic(ctx, &queue.Job{ID: jobID, TenantID: t.ID, EnqueuedAt: now, Request: *req},
		guard, queue.Write{Key: stKey, Value: stVal, TTL: stTTL})
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if prev != nil {
		existing, err := idempotency.Decode(prev)
		if err != nil {
			return Result{}, err
		}
		if existing.Fingerprint != fp {
			return Result{}, ErrIdempotencyConflict
		}
		s.metrics.IdempotentReplays.WithLabelValues(t.ID).Inc()
		return Result{JobID: existing.JobID, Replayed: true}, nil
	}

	s.metrics.JobsEnqueued.WithLabelValues(t.ID).Inc()
	attrs := append([]any{
		"email_id", jobID, "tenant", t.ID, "origin", string(r.Origin),
		"to", observability.MaskAddresses(req.To), "template", templateName(req),
	}, r.LogAttrs...)
	s.logger.InfoContext(ctx, "email queued", attrs...)
	return Result{JobID: jobID}, nil
}

// Prepare applies the tenant defaults to req, normalises and validates it,
// and checks the sender. It has no side effect outside req, which lets tools
// such as "kourrier routes test" show exactly what would be accepted.
func Prepare(t *tenant.Tenant, req *mail.Request) error {
	req.Normalize()
	if req.From.Email == "" {
		req.From = t.DefaultSender
	}
	if err := req.Validate(); err != nil {
		return err
	}
	if !t.CanSendFrom(req.From.Email) {
		return fmt.Errorf("%w: %s", ErrSenderNotAllowed, req.From.Email)
	}
	return nil
}

// checkTemplate returns an error wrapping templates.ErrNotFound or
// templates.ErrInvalid. When the storage cannot be read, the request is
// accepted anyway: the worker retries, and a storage outage must not stop
// e-mails from being queued.
func (s *Service) checkTemplate(ctx context.Context, tenantID string, ref *mail.TemplateRef) error {
	if s.templates == nil || ref == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, templateCheckTimeout)
	defer cancel()
	err := s.templates.Check(ctx, tenantID, ref.Name, ref.Locale)
	if err == nil || errors.Is(err, templates.ErrNotFound) || errors.Is(err, templates.ErrInvalid) {
		return err
	}
	s.logger.WarnContext(ctx, "template not checked: storage unavailable",
		"tenant", tenantID, "template", ref.Name, "error", err)
	return nil
}

// idempotencyScope keeps API and event keys in separate namespaces. API keys
// keep the bare tenant ID (unchanged since ADR 007); tenant IDs cannot contain
// ':' so the event scope can never collide with another tenant.
func idempotencyScope(tenantID string, o Origin) string {
	if o == OriginEvent {
		return tenantID + ":events"
	}
	return tenantID
}

func templateName(req *mail.Request) string {
	if req.Template != nil {
		return req.Template.Name
	}
	return ""
}
