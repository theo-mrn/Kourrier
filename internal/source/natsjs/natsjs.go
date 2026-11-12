// Package natsjs consumes CloudEvents from NATS JetStream durable pull
// consumers (ADR 014) and hands them to the dispatcher.
//
// Acknowledgements follow the dispatch outcome:
//
//	accepted, ignored  -> Ack
//	rejected, invalid  -> kept in the event dead-letter queue, then Term
//	                      (never redelivered by the broker); if it cannot be
//	                      kept, NakWithDelay instead
//	retry              -> NakWithDelay: the tenant limiter's delay, a fixed
//	                      delay on backpressure, or an exponential delay
//	                      based on the delivery count
//
// The consumer is created or updated at startup unless the source is
// configured with bind: true. Streams are never created: they belong to the
// producers or to the platform team.
package natsjs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/theo-mrn/kourrier/internal/cloudevent"
	"github.com/theo-mrn/kourrier/internal/deadevent"
	"github.com/theo-mrn/kourrier/internal/dispatch"
	"github.com/theo-mrn/kourrier/internal/observability"
	"github.com/theo-mrn/kourrier/internal/routing"
	"github.com/theo-mrn/kourrier/internal/tenant"
)

// Defaults applied to consumers created by Kourrier.
const (
	defaultMaxAckPending = 256
	// pullBatch bounds the messages buffered client-side. AckWait runs from
	// delivery, not from the start of processing: a small batch keeps the
	// last buffered message well within AckWait even when Redis is slow.
	pullBatch = 4
	// handleTimeout bounds the processing of one message, including during
	// shutdown. It stays below the minimum AckWait accepted by the routes
	// file (15s), so a message is never redelivered while being processed.
	handleTimeout = 10 * time.Second
	// backpressureDelay is the redelivery delay while the e-mail queue is full.
	backpressureDelay = 30 * time.Second
	maxRetryDelay     = time.Minute
	maxTermReason     = 256
)

// Dispatcher routes events. *dispatch.Dispatcher implements it.
type Dispatcher interface {
	Dispatch(ctx context.Context, source string, t *tenant.Tenant, e *cloudevent.Event) dispatch.Report
}

// Config holds the connection settings. Credentials are file paths so that
// they can be mounted from Kubernetes Secrets and rotated without exposing
// them in the environment.
type Config struct {
	URL string
	// Name identifies the connection on the server (e.g. the pod name).
	Name string
	// CredsFile is a NATS credentials file (JWT + NKey seed).
	CredsFile string
	// NKeyFile is an NKey seed file, for servers without JWT auth.
	NKeyFile string
	// CAFile verifies the server certificate; CertFile and KeyFile enable
	// mutual TLS.
	CAFile   string
	CertFile string
	KeyFile  string
	// MaxBytes caps the size of one event (default 256 KiB).
	MaxBytes int64
}

// Consumer runs one JetStream pull consumer per configured source.
type Consumer struct {
	cfg         Config
	sources     []routing.NATSSource
	tenants     *tenant.Registry
	dispatcher  Dispatcher
	deadLetters dispatch.DeadLetters
	metrics     *observability.Metrics
	logger      *slog.Logger

	connected atomic.Bool
	consuming map[string]*atomic.Bool // by source name
}

// Deps groups the collaborators of a Consumer.
type Deps struct {
	Tenants    *tenant.Registry
	Dispatcher Dispatcher
	// DeadLetters keeps invalid payloads; optional.
	DeadLetters dispatch.DeadLetters
	Metrics     *observability.Metrics
	Logger      *slog.Logger
}

// New creates a Consumer. It does not connect: Run does.
func New(cfg Config, sources []routing.NATSSource, d Deps) (*Consumer, error) {
	for _, s := range sources {
		if _, ok := d.Tenants.Get(s.Tenant); !ok {
			return nil, fmt.Errorf("natsjs: source %q: unknown tenant %q", s.Name, s.Tenant)
		}
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = 256 << 10
	}
	c := &Consumer{
		cfg: cfg, sources: sources, tenants: d.Tenants, dispatcher: d.Dispatcher, deadLetters: d.DeadLetters,
		metrics: d.Metrics, logger: d.Logger,
		consuming: make(map[string]*atomic.Bool, len(sources)),
	}
	for _, s := range sources {
		c.consuming[s.Name] = &atomic.Bool{}
		c.metrics.EventSourceUp.WithLabelValues(s.Name).Set(0)
	}
	return c, nil
}

// Run connects to NATS and consumes every source until ctx is done, then
// drains: messages already received are processed and acknowledged before it
// returns. A server that is not reachable yet is retried in the background,
// so a NATS outage never prevents the HTTP API from starting.
func (c *Consumer) Run(ctx context.Context) error {
	nc, err := c.connect()
	if err != nil {
		return err
	}
	defer nc.Close()
	c.connected.Store(nc.IsConnected())
	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("natsjs: %w", err)
	}

	var wg sync.WaitGroup
	for _, src := range c.sources {
		wg.Go(func() { c.runSource(ctx, js, src) })
	}
	wg.Wait()
	return nil
}

func (c *Consumer) connect() (*nats.Conn, error) {
	opts := []nats.Option{
		nats.Name(c.cfg.Name),
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2 * time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			c.logger.Warn("nats disconnected", "error", err)
			c.connected.Store(false)
			c.refreshAll()
		}),
		// With RetryOnFailedConnect, the first successful connection calls the
		// connect handler, later ones the reconnect handler.
		nats.ConnectHandler(c.onConnect),
		nats.ReconnectHandler(c.onConnect),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			c.logger.Warn("nats asynchronous error", "error", err)
		}),
	}
	if c.cfg.CredsFile != "" {
		opts = append(opts, nats.UserCredentials(c.cfg.CredsFile))
	}
	if c.cfg.NKeyFile != "" {
		opt, err := nats.NkeyOptionFromSeed(c.cfg.NKeyFile)
		if err != nil {
			return nil, fmt.Errorf("natsjs: nkey: %w", err)
		}
		opts = append(opts, opt)
	}
	if c.cfg.CAFile != "" {
		opts = append(opts, nats.RootCAs(c.cfg.CAFile))
	}
	if c.cfg.CertFile != "" {
		opts = append(opts, nats.ClientCert(c.cfg.CertFile, c.cfg.KeyFile))
	}
	nc, err := nats.Connect(c.cfg.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("natsjs: connect: %w", err)
	}
	return nc, nil
}

func (c *Consumer) onConnect(nc *nats.Conn) {
	c.logger.Info("nats connected", "server", nc.ConnectedUrlRedacted())
	c.connected.Store(true)
	c.refreshAll()
}

// refresh publishes kourrier_event_source_up: a source is up when it is
// consuming and the connection is established.
func (c *Consumer) refresh(source string) {
	v := 0.0
	if c.connected.Load() && c.consuming[source].Load() {
		v = 1
	}
	c.metrics.EventSourceUp.WithLabelValues(source).Set(v)
}

func (c *Consumer) refreshAll() {
	for _, s := range c.sources {
		c.refresh(s.Name)
	}
}

func (c *Consumer) setConsuming(source string, v bool) {
	c.consuming[source].Store(v)
	c.refresh(source)
}

// runSource keeps one source consuming: it (re)binds the consumer with a
// capped exponential backoff whenever it is missing, deleted, or when the
// client stops consuming on its own.
func (c *Consumer) runSource(ctx context.Context, js jetstream.JetStream, src routing.NATSSource) {
	log := c.logger.With("source", src.Name, "stream", src.Stream, "consumer", src.Consumer)
	t, _ := c.tenants.Get(src.Tenant)
	backoff := time.Second
	for ctx.Err() == nil {
		sig := signals{failed: make(chan error, 1), suspect: make(chan struct{}, 1)}
		cons, cc, err := c.consume(ctx, js, src, t, log, sig)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Error("cannot start consuming, retrying", "error", err, "retry_in", backoff)
			sleep(ctx, backoff)
			backoff = min(backoff*2, time.Minute)
			continue
		}
		backoff = time.Second
		c.setConsuming(src.Name, true)
		log.Info("consuming events")

		reason := c.watch(ctx, cons, cc, sig)
		if ctx.Err() != nil {
			// Messages already received are processed and acknowledged.
			cc.Drain()
			<-cc.Closed()
			c.setConsuming(src.Name, false)
			log.Info("stopped consuming events")
			return
		}
		cc.Stop()
		<-cc.Closed()
		c.setConsuming(src.Name, false)
		log.Error("consumer lost, rebinding", "reason", reason)
	}
}

// signals carries what the consume error handler observed.
type signals struct {
	failed  chan error    // terminal: the consumer was deleted
	suspect chan struct{} // missing heartbeats or no responders: maybe gone
}

// watch blocks until ctx is done or the consumption must be restarted, and
// returns why.
func (c *Consumer) watch(ctx context.Context, cons jetstream.Consumer, cc jetstream.ConsumeContext, sig signals) string {
	for {
		select {
		case <-ctx.Done():
			return "shutdown"
		case err := <-sig.failed:
			return err.Error()
		case <-cc.Closed():
			// The client gave up on its own (e.g. a bad request error).
			return "consumption stopped by the client"
		case <-sig.suspect:
			// A consumer deleted while the connection was down only shows up
			// as missing heartbeats: check that it still exists.
			ictx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, err := cons.Info(ictx)
			cancel()
			if errors.Is(err, jetstream.ErrConsumerNotFound) {
				return "consumer not found"
			}
		}
	}
}

func (c *Consumer) consume(ctx context.Context, js jetstream.JetStream, src routing.NATSSource, t *tenant.Tenant,
	log *slog.Logger, sig signals,
) (jetstream.Consumer, jetstream.ConsumeContext, error) {
	setupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var (
		cons jetstream.Consumer
		err  error
	)
	if src.Bind {
		cons, err = js.Consumer(setupCtx, src.Stream, src.Consumer)
	} else {
		cons, err = js.CreateOrUpdateConsumer(setupCtx, src.Stream, consumerConfig(src))
		if err != nil {
			if _, getErr := js.Consumer(setupCtx, src.Stream, src.Consumer); getErr == nil {
				// It exists but cannot be updated: an immutable field (deliver
				// policy...) differs from what Kourrier would create.
				err = fmt.Errorf("consumer %q exists with a configuration Kourrier cannot update "+
					"(e.g. its deliver policy); delete it or set bind: true to use it as is: %w", src.Consumer, err)
			}
		}
	}
	if err != nil {
		return nil, nil, err
	}

	notify := func(ch chan struct{}) {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	cc, err := cons.Consume(
		func(msg jetstream.Msg) { c.handle(ctx, src, t, log, msg) },
		jetstream.PullMaxMessages(pullBatch),
		jetstream.ConsumeErrHandler(func(_ jetstream.ConsumeContext, err error) {
			switch {
			case errors.Is(err, jetstream.ErrConsumerDeleted):
				select {
				case sig.failed <- err:
				default:
				}
			case errors.Is(err, jetstream.ErrNoHeartbeat) || errors.Is(err, nats.ErrNoResponders):
				log.Warn("consume error", "error", err)
				notify(sig.suspect)
			default:
				log.Warn("consume error", "error", err)
			}
		}),
	)
	if err != nil {
		return nil, nil, err
	}
	return cons, cc, nil
}

func consumerConfig(src routing.NATSSource) jetstream.ConsumerConfig {
	cfg := jetstream.ConsumerConfig{
		Durable:        src.Consumer,
		Description:    "Kourrier: CloudEvents to e-mails",
		AckPolicy:      jetstream.AckExplicitPolicy,
		AckWait:        src.AckWait,
		MaxDeliver:     src.MaxDeliver,
		MaxAckPending:  src.MaxAckPending,
		FilterSubjects: src.FilterSubjects,
		DeliverPolicy:  jetstream.DeliverNewPolicy,
	}
	if src.DeliverPolicy == routing.DeliverAll {
		cfg.DeliverPolicy = jetstream.DeliverAllPolicy
	}
	if cfg.MaxDeliver == 0 {
		// Transient failures (Redis down) must not lose events; poison
		// messages are terminated explicitly instead.
		cfg.MaxDeliver = -1
	}
	if cfg.MaxAckPending == 0 {
		cfg.MaxAckPending = defaultMaxAckPending
	}
	return cfg
}

// handle processes one message. It runs on a context detached from ctx so
// that a shutdown never interrupts an event halfway through its routes.
func (c *Consumer) handle(ctx context.Context, src routing.NATSSource, t *tenant.Tenant, log *slog.Logger, msg jetstream.Msg) {
	hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), handleTimeout)
	defer cancel()
	// Restart the AckWait timer: the message may have waited in the client
	// buffer behind slower ones.
	if err := msg.InProgress(); err != nil {
		log.DebugContext(hctx, "cannot extend the ack deadline", "error", err)
	}

	var (
		e   *cloudevent.Event
		err error
	)
	if size := len(msg.Data()); int64(size) > c.cfg.MaxBytes {
		err = fmt.Errorf("event of %d bytes exceeds the %d bytes limit", size, c.cfg.MaxBytes)
	} else {
		e, err = cloudevent.ParseNATS(msg.Headers(), msg.Data())
	}
	if err != nil {
		c.invalid(hctx, src, log, msg, err)
		return
	}

	rep := c.dispatcher.Dispatch(hctx, src.Name, t, e)
	switch rep.Outcome {
	case dispatch.Accepted, dispatch.Ignored:
		c.settle(hctx, log, msg, msg.Ack)
	case dispatch.Rejected:
		reason := "rejected"
		if rep.Err != nil {
			reason += ": " + rep.Err.Error()
		} else if len(rep.Errors) > 0 {
			reason += ": " + rep.Errors[0].Detail
		}
		c.settle(hctx, log, msg, func() error { return msg.TermWithReason(truncate(reason)) })
	default: // dispatch.Retry
		var delay time.Duration
		switch {
		case rep.RetryAfter > 0:
			delay = min(rep.RetryAfter, maxRetryDelay)
		case errors.Is(rep.Err, dispatch.ErrBackpressure):
			delay = backpressureDelay
		default:
			delay = retryDelay(msg)
		}
		c.settle(hctx, log, msg, func() error { return msg.NakWithDelay(delay) })
	}
}

// invalid keeps an unusable payload in the event dead-letter queue, for
// inspection, then terminates it. Oversized payloads are not stored. If the
// dead-letter queue is unavailable the message is redelivered later rather
// than lost.
func (c *Consumer) invalid(ctx context.Context, src routing.NATSSource, log *slog.Logger, msg jetstream.Msg, cause error) {
	c.metrics.EventsReceived.WithLabelValues(src.Name, string(dispatch.Rejected)).Inc()
	if c.deadLetters != nil {
		entry := deadevent.Entry{
			Tenant: src.Tenant, Source: src.Name, Reason: deadevent.Invalid,
			Detail: "subject " + msg.Subject() + ": " + cause.Error(),
		}
		if int64(len(msg.Data())) <= c.cfg.MaxBytes {
			entry.Event = msg.Data()
		}
		if _, err := c.deadLetters.Add(ctx, entry); err != nil {
			log.ErrorContext(ctx, "cannot keep the invalid event, it will be redelivered", "error", err)
			c.settle(ctx, log, msg, func() error { return msg.NakWithDelay(retryDelay(msg)) })
			return
		}
		c.metrics.EventsDeadLettered.WithLabelValues(src.Name, string(deadevent.Invalid)).Inc()
	}
	log.WarnContext(ctx, "invalid event terminated", "subject", msg.Subject(), "error", cause)
	c.settle(ctx, log, msg, func() error { return msg.TermWithReason(truncate("invalid cloudevent: " + cause.Error())) })
}

// settle sends an acknowledgement. A lost acknowledgement only causes a
// redelivery after AckWait, which idempotency absorbs.
func (c *Consumer) settle(ctx context.Context, log *slog.Logger, msg jetstream.Msg, fn func() error) {
	if err := fn(); err != nil {
		log.WarnContext(ctx, "cannot acknowledge message, it will be redelivered", "subject", msg.Subject(), "error", err)
	}
}

// retryDelay grows exponentially with the delivery count: 1s, 2s, 4s... up
// to maxRetryDelay.
func retryDelay(msg jetstream.Msg) time.Duration {
	n := uint64(1)
	if md, err := msg.Metadata(); err == nil && md.NumDelivered > 0 {
		n = md.NumDelivered
	}
	if n > 7 {
		return maxRetryDelay
	}
	return min(time.Second<<(n-1), maxRetryDelay)
}

func truncate(s string) string {
	if len(s) <= maxTermReason {
		return s
	}
	return strings.ToValidUTF8(s[:maxTermReason], "")
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
