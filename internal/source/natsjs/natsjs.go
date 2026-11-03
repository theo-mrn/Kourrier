// Package natsjs consumes CloudEvents from NATS JetStream durable pull
// consumers (ADR 014) and hands them to the dispatcher.
//
// Acknowledgements follow the dispatch outcome:
//
//	accepted, ignored  -> Ack
//	rejected, invalid  -> Term (never redelivered; JetStream publishes a
//	                      MSG_TERMINATED advisory that can be captured)
//	retry              -> NakWithDelay, with an exponential delay based on
//	                      the delivery count, or a fixed delay on backpressure
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
	"github.com/theo-mrn/kourrier/internal/dispatch"
	"github.com/theo-mrn/kourrier/internal/observability"
	"github.com/theo-mrn/kourrier/internal/routing"
	"github.com/theo-mrn/kourrier/internal/tenant"
)

// Defaults applied to consumers created by Kourrier.
const (
	defaultMaxAckPending = 256
	// pullBatch bounds the messages buffered client-side; they are dropped on
	// a hard stop and redelivered after AckWait, so the batch stays small.
	pullBatch = 32
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
}

// Consumer runs one JetStream pull consumer per configured source.
type Consumer struct {
	cfg        Config
	sources    []routing.NATSSource
	tenants    *tenant.Registry
	dispatcher Dispatcher
	metrics    *observability.Metrics
	logger     *slog.Logger

	connected atomic.Bool
	consuming map[string]*atomic.Bool // by source name
}

// Deps groups the collaborators of a Consumer.
type Deps struct {
	Tenants    *tenant.Registry
	Dispatcher Dispatcher
	Metrics    *observability.Metrics
	Logger     *slog.Logger
}

// New creates a Consumer. It does not connect: Run does.
func New(cfg Config, sources []routing.NATSSource, d Deps) (*Consumer, error) {
	for _, s := range sources {
		if _, ok := d.Tenants.Get(s.Tenant); !ok {
			return nil, fmt.Errorf("natsjs: source %q: unknown tenant %q", s.Name, s.Tenant)
		}
	}
	c := &Consumer{
		cfg: cfg, sources: sources, tenants: d.Tenants, dispatcher: d.Dispatcher, metrics: d.Metrics, logger: d.Logger,
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
		nats.ReconnectHandler(func(nc *nats.Conn) {
			c.logger.Info("nats connected", "server", nc.ConnectedUrlRedacted())
			c.connected.Store(true)
			c.refreshAll()
		}),
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
// capped exponential backoff whenever it is missing or gets deleted.
func (c *Consumer) runSource(ctx context.Context, js jetstream.JetStream, src routing.NATSSource) {
	log := c.logger.With("source", src.Name, "stream", src.Stream, "consumer", src.Consumer)
	t, _ := c.tenants.Get(src.Tenant)
	backoff := time.Second
	for ctx.Err() == nil {
		failed := make(chan error, 1)
		cc, err := c.consume(ctx, js, src, t, log, failed)
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

		select {
		case <-ctx.Done():
			cc.Drain()
			<-cc.Closed()
			c.setConsuming(src.Name, false)
			log.Info("stopped consuming events")
			return
		case err := <-failed:
			cc.Stop()
			<-cc.Closed()
			c.setConsuming(src.Name, false)
			log.Error("consumer lost, rebinding", "error", err)
		}
	}
}

func (c *Consumer) consume(ctx context.Context, js jetstream.JetStream, src routing.NATSSource, t *tenant.Tenant,
	log *slog.Logger, failed chan<- error,
) (jetstream.ConsumeContext, error) {
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
	}
	if err != nil {
		return nil, err
	}

	return cons.Consume(
		func(msg jetstream.Msg) { c.handle(ctx, src, t, log, msg) },
		jetstream.PullMaxMessages(pullBatch),
		jetstream.ConsumeErrHandler(func(_ jetstream.ConsumeContext, err error) {
			// Terminal errors require a rebind; the others (missing
			// heartbeats during a reconnection) are handled by the client.
			if errors.Is(err, jetstream.ErrConsumerDeleted) || errors.Is(err, jetstream.ErrConsumerNotFound) {
				select {
				case failed <- err:
				default:
				}
				return
			}
			log.Warn("consume error", "error", err)
		}),
	)
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

	e, err := cloudevent.ParseNATS(msg.Headers(), msg.Data())
	if err != nil {
		c.metrics.EventsReceived.WithLabelValues(src.Name, string(dispatch.Rejected)).Inc()
		log.WarnContext(hctx, "invalid event terminated", "subject", msg.Subject(), "error", err)
		c.settle(hctx, log, msg, func() error { return msg.TermWithReason(truncate("invalid cloudevent: " + err.Error())) })
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
		delay := backpressureDelay
		if !errors.Is(rep.Err, dispatch.ErrBackpressure) {
			delay = retryDelay(msg)
		}
		c.settle(hctx, log, msg, func() error { return msg.NakWithDelay(delay) })
	}
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
