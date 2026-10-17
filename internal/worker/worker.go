// Package worker consumes the queue, renders e-mails and hands them to the
// provider, retrying transient failures with exponential backoff.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"sync"
	"time"

	"github.com/theo-mrn/kourrier/internal/mail"
	"github.com/theo-mrn/kourrier/internal/observability"
	"github.com/theo-mrn/kourrier/internal/provider"
	"github.com/theo-mrn/kourrier/internal/queue"
	"github.com/theo-mrn/kourrier/internal/ratelimit"
	"github.com/theo-mrn/kourrier/internal/retry"
	"github.com/theo-mrn/kourrier/internal/status"
	"github.com/theo-mrn/kourrier/internal/templates"
)

// Renderer renders a stored template.
type Renderer interface {
	Render(ctx context.Context, tenant string, ref mail.TemplateRef) (*templates.Rendered, error)
}

// StatusStore reads and writes delivery statuses.
type StatusStore interface {
	Get(ctx context.Context, id string) (*status.Status, error)
	Put(ctx context.Context, st *status.Status) error
}

// Config tunes the pool.
type Config struct {
	Concurrency int
	// SendTimeout bounds a single provider call.
	SendTimeout time.Duration
	// SettleTimeout bounds the Redis calls made to settle a job, including
	// during shutdown.
	SettleTimeout time.Duration
	Retry         retry.Policy
}

// Pool runs Concurrency workers.
type Pool struct {
	cfg      Config
	queue    queue.Queue
	renderer Renderer
	provider provider.Provider
	limiter  ratelimit.Limiter
	statuses StatusStore
	metrics  *observability.Metrics
	logger   *slog.Logger
	now      func() time.Time
}

// Deps groups the collaborators of a Pool.
type Deps struct {
	Queue    queue.Queue
	Renderer Renderer
	Provider provider.Provider
	Limiter  ratelimit.Limiter
	Statuses StatusStore
	Metrics  *observability.Metrics
	Logger   *slog.Logger
}

// New creates a Pool.
func New(cfg Config, d Deps) *Pool {
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	if cfg.SendTimeout <= 0 {
		cfg.SendTimeout = 30 * time.Second
	}
	if cfg.SettleTimeout <= 0 {
		cfg.SettleTimeout = 10 * time.Second
	}
	if d.Limiter == nil {
		d.Limiter = ratelimit.Unlimited{}
	}
	return &Pool{
		cfg: cfg, queue: d.Queue, renderer: d.Renderer, provider: d.Provider,
		limiter: d.Limiter, statuses: d.Statuses, metrics: d.Metrics, logger: d.Logger,
		now: time.Now,
	}
}

// Run starts the workers and blocks until ctx is cancelled and every in-flight
// job has been settled.
func (p *Pool) Run(ctx context.Context) {
	p.logger.InfoContext(ctx, "workers started", "concurrency", p.cfg.Concurrency, "provider", p.provider.Name())
	var wg sync.WaitGroup
	for range p.cfg.Concurrency {
		wg.Go(func() { p.loop(ctx) })
	}
	wg.Wait()
	p.logger.Info("workers stopped")
}

func (p *Pool) loop(ctx context.Context) {
	backoff := time.Duration(0)
	for ctx.Err() == nil {
		d, err := p.queue.Receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			backoff = min(max(backoff*2, 100*time.Millisecond), 5*time.Second)
			p.logger.ErrorContext(ctx, "queue receive failed", "error", err, "retry_in", backoff)
			sleep(ctx, backoff)
			continue
		}
		backoff = 0
		if d == nil {
			continue
		}
		p.metrics.WorkersBusy.Inc()
		p.Process(ctx, d)
		p.metrics.WorkersBusy.Dec()
	}
}

// Process handles a single delivery. ctx cancellation (shutdown) interrupts
// waiting for the rate limiter but never a provider call already in flight.
func (p *Pool) Process(ctx context.Context, d *queue.Delivery) {
	job := d.Job
	log := p.logger.With("email_id", job.ID, "tenant", job.TenantID, "attempt", job.Attempts+1)
	// Settling must succeed even when ctx is cancelled during shutdown.
	settleCtx := context.WithoutCancel(ctx)

	// At-least-once delivery means a job can come back after it was sent (the
	// ack was lost, or the pod died right after sending). The status store
	// tells us whether this happened.
	if st, err := p.statuses.Get(ctx, job.ID); err == nil && st != nil && st.State == status.Sent {
		log.InfoContext(ctx, "skipping job already sent")
		p.ack(settleCtx, log, d)
		return
	}

	msg, err := p.build(ctx, job)
	if err != nil {
		if ctx.Err() != nil {
			p.requeue(settleCtx, log, d)
			return
		}
		p.fail(settleCtx, log, d, err)
		return
	}

	waitStart := p.now()
	if err := p.limiter.Wait(ctx); err != nil {
		if ctx.Err() != nil {
			// Shutting down: hand the job back right away instead of letting
			// it wait for the visibility timeout. This is not a failed attempt.
			p.requeue(settleCtx, log, d)
			return
		}
		p.fail(settleCtx, log, d, err)
		return
	}
	p.metrics.RateLimitWait.Observe(p.now().Sub(waitStart).Seconds())

	sendCtx, cancel := context.WithTimeout(settleCtx, p.cfg.SendTimeout)
	start := p.now()
	res, err := p.provider.Send(sendCtx, msg)
	cancel()
	elapsed := p.now().Sub(start)
	if err != nil {
		p.metrics.SendDuration.WithLabelValues(p.provider.Name(), "error").Observe(elapsed.Seconds())
		p.fail(settleCtx, log, d, err)
		return
	}
	p.metrics.SendDuration.WithLabelValues(p.provider.Name(), "success").Observe(elapsed.Seconds())

	job.Attempts++
	p.putStatus(settleCtx, log, &status.Status{
		ID: job.ID, TenantID: job.TenantID, State: status.Sent, Attempts: job.Attempts,
		Provider: p.provider.Name(), ProviderMessageID: res.MessageID, CreatedAt: job.EnqueuedAt,
	})
	p.ack(settleCtx, log, d)

	p.metrics.EmailsSent.WithLabelValues(job.TenantID, p.provider.Name()).Inc()
	p.metrics.DeliveryLatency.WithLabelValues(job.TenantID).Observe(p.now().Sub(job.EnqueuedAt).Seconds())
	log.InfoContext(ctx, "email sent",
		"provider", p.provider.Name(),
		"provider_message_id", res.MessageID,
		"to", observability.MaskAddresses(msg.To),
		"template", templateName(job),
		"duration_ms", elapsed.Milliseconds(),
	)
}

func (p *Pool) build(ctx context.Context, job *queue.Job) (*mail.Message, error) {
	req := job.Request
	msg := &mail.Message{
		ID: job.ID, From: req.From, To: req.To, Cc: req.Cc, Bcc: req.Bcc, ReplyTo: req.ReplyTo,
		Headers: req.Headers,
		Tags:    map[string]string{"kourrier_tenant": job.TenantID},
	}
	maps.Copy(msg.Tags, req.Tags)

	switch {
	case req.Template != nil:
		out, err := p.renderer.Render(ctx, job.TenantID, *req.Template)
		if err != nil {
			return nil, err
		}
		msg.Subject, msg.HTML, msg.Text = out.Subject, out.HTML, out.Text
		msg.Tags["kourrier_template"] = req.Template.Name
	case req.Content != nil:
		msg.Subject, msg.HTML, msg.Text = req.Content.Subject, req.Content.HTML, req.Content.Text
	default:
		return nil, retry.Permanent(errors.New("job has neither template nor content"))
	}
	return msg, nil
}

// fail retries transient errors and dead-letters permanent ones, or jobs that
// exhausted their attempts.
func (p *Pool) fail(ctx context.Context, log *slog.Logger, d *queue.Delivery, cause error) {
	job := d.Job
	job.Attempts++
	providerName := p.provider.Name()
	st := &status.Status{
		ID: job.ID, TenantID: job.TenantID, Attempts: job.Attempts,
		Provider: providerName, LastError: cause.Error(), CreatedAt: job.EnqueuedAt,
	}

	permanent := retry.IsPermanent(cause)
	if permanent || p.cfg.Retry.Exhausted(job.Attempts) {
		reason := observability.ReasonExhausted
		if permanent {
			reason = observability.ReasonPermanent
		}
		st.State = status.Failed
		p.putStatus(ctx, log, st)
		if err := p.settle(ctx, func(ctx context.Context) error { return p.queue.DeadLetter(ctx, d, cause.Error()) }); err != nil {
			log.ErrorContext(ctx, "failed to dead-letter job", "error", err)
		}
		p.metrics.EmailsFailed.WithLabelValues(job.TenantID, providerName, reason).Inc()
		log.ErrorContext(ctx, "email failed", "reason", reason, "error", cause, "template", templateName(job))
		return
	}

	delay := p.cfg.Retry.Backoff(job.Attempts)
	next := p.now().Add(delay).UTC()
	st.State, st.NextAttemptAt = status.Retrying, &next
	p.putStatus(ctx, log, st)
	if err := p.settle(ctx, func(ctx context.Context) error { return p.queue.Retry(ctx, d, next) }); err != nil {
		log.ErrorContext(ctx, "failed to schedule retry", "error", err)
	}
	p.metrics.JobRetries.WithLabelValues(job.TenantID, providerName).Inc()
	log.WarnContext(ctx, "email delivery failed, will retry", "error", cause, "retry_in", delay.String())
}

func (p *Pool) requeue(ctx context.Context, log *slog.Logger, d *queue.Delivery) {
	if err := p.settle(ctx, func(ctx context.Context) error { return p.queue.Retry(ctx, d, p.now()) }); err != nil {
		log.ErrorContext(ctx, "failed to requeue job on shutdown", "error", err)
	}
}

func (p *Pool) ack(ctx context.Context, log *slog.Logger, d *queue.Delivery) {
	if err := p.settle(ctx, func(ctx context.Context) error { return p.queue.Ack(ctx, d) }); err != nil {
		// The job will be redelivered after the visibility timeout and then
		// skipped thanks to its "sent" status.
		log.ErrorContext(ctx, "failed to ack job", "error", err)
	}
}

func (p *Pool) putStatus(ctx context.Context, log *slog.Logger, st *status.Status) {
	st.UpdatedAt = p.now().UTC()
	if err := p.settle(ctx, func(ctx context.Context) error { return p.statuses.Put(ctx, st) }); err != nil {
		log.ErrorContext(ctx, "failed to update status", "error", err)
	}
}

func (p *Pool) settle(ctx context.Context, fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, p.cfg.SettleTimeout)
	defer cancel()
	return fn(ctx)
}

func templateName(job *queue.Job) string {
	if job.Request.Template != nil {
		return job.Request.Template.Name
	}
	return ""
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
