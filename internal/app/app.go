// Package app is the composition root: it builds every component from the
// configuration and manages their lifecycle.
package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/theo-mrn/kourrier/internal/activity"
	"github.com/theo-mrn/kourrier/internal/api"
	"github.com/theo-mrn/kourrier/internal/config"
	"github.com/theo-mrn/kourrier/internal/idempotency"
	"github.com/theo-mrn/kourrier/internal/observability"
	"github.com/theo-mrn/kourrier/internal/provider"
	"github.com/theo-mrn/kourrier/internal/provider/console"
	"github.com/theo-mrn/kourrier/internal/provider/filesystem"
	"github.com/theo-mrn/kourrier/internal/provider/ses"
	"github.com/theo-mrn/kourrier/internal/provider/smtp"
	"github.com/theo-mrn/kourrier/internal/queue"
	"github.com/theo-mrn/kourrier/internal/ratelimit"
	"github.com/theo-mrn/kourrier/internal/retry"
	"github.com/theo-mrn/kourrier/internal/status"
	"github.com/theo-mrn/kourrier/internal/storage"
	"github.com/theo-mrn/kourrier/internal/storage/fsstore"
	"github.com/theo-mrn/kourrier/internal/storage/s3store"
	"github.com/theo-mrn/kourrier/internal/submit"
	"github.com/theo-mrn/kourrier/internal/templates"
	"github.com/theo-mrn/kourrier/internal/tenant"
	"github.com/theo-mrn/kourrier/internal/worker"
)

// Run starts the service and blocks until ctx is cancelled (SIGTERM) and the
// graceful shutdown is complete.
func Run(ctx context.Context, cfg *config.Config, version string, logger *slog.Logger) error {
	metrics := observability.NewMetrics(version)

	rdb := NewRedis(cfg.Redis, cfg.Worker.Concurrency)
	defer rdb.Close()
	if err := waitForRedis(ctx, rdb, logger); err != nil {
		return err
	}

	q, err := NewQueue(ctx, cfg, rdb, logger)
	if err != nil {
		return err
	}
	statuses := status.NewStore(rdb, cfg.Redis.KeyPrefix, cfg.Status.TTL)
	feed := NewActivity(cfg, rdb)

	checks := map[string]api.Check{"redis": func(ctx context.Context) error { return rdb.Ping(ctx).Err() }}
	var (
		apiServer *http.Server
		pool      *worker.Pool
		// ingress components (event consumers) stop first at shutdown,
		// background components with the workers; an error is fatal.
		ingress    []func(context.Context) error
		background []func(context.Context) error
	)

	// Templates are read by the workers (rendering) and by the API (checks,
	// list and preview, ADR 011): one renderer, one cache.
	store, err := NewStorage(ctx, cfg.Storage)
	if err != nil {
		return err
	}
	renderer := templates.NewRenderer(store, templates.Options{
		CacheTTL: cfg.Templates.CacheTTL, DefaultLocale: cfg.Templates.DefaultLocale, Metrics: metrics,
	})
	// Storage is a soft dependency: it is checked at startup only, so that a
	// brief S3 outage does not take every pod out of rotation.
	pingCtx, cancelPing := context.WithTimeout(ctx, 5*time.Second)
	if err := store.Ping(pingCtx); err != nil {
		logger.Warn("template storage is not reachable yet", "error", err)
	}
	cancelPing()

	if cfg.RunsAPI() {
		tenants, err := tenant.LoadFile(cfg.TenantsFile)
		if err != nil {
			return err
		}
		logger.Info("tenants loaded", "count", tenants.Len())
		submitter := submit.New(submit.Deps{
			Queue:       q,
			Idempotency: idempotency.NewStore(cfg.Redis.KeyPrefix, cfg.Idempotency.TTL),
			Statuses:    statuses,
			Templates:   renderer,
			Activity:    feed,
			Metrics:     metrics,
			Logger:      logger,
		})
		events, err := newEventIngress(cfg, rdb, tenants, submitter, q, metrics, logger)
		if err != nil {
			return err
		}
		ingress = events.run
		srv := api.New(api.Deps{
			Tenants:       tenants,
			Submitter:     submitter,
			Statuses:      statuses,
			Templates:     renderer,
			Events:        events.dispatcher,
			Metrics:       metrics,
			Logger:        logger,
			MaxBodyBytes:  cfg.MaxBodyBytes,
			MaxEventBytes: cfg.Events.MaxBytes,
		})
		apiServer = newHTTPServer(cfg.HTTPAddr, srv.Handler(), logger)
	}

	if cfg.RunsWorkers() {
		mailer, err := NewProvider(ctx, cfg.Mailer)
		if err != nil {
			return err
		}
		if mailer.Name() == config.ProviderConsole || mailer.Name() == config.ProviderFilesystem {
			logger.Warn("development mailer in use: no e-mail will actually be sent", "provider", mailer.Name())
		}
		pool = worker.New(worker.Config{
			Concurrency: cfg.Worker.Concurrency,
			SendTimeout: cfg.Worker.SendTimeout,
			Retry: retry.Policy{
				MaxAttempts: cfg.Worker.MaxAttempts,
				Base:        cfg.Worker.BackoffBase,
				Max:         cfg.Worker.BackoffMax,
			},
		}, worker.Deps{
			Queue:    q,
			Renderer: renderer,
			Provider: mailer,
			Limiter:  NewLimiter(cfg.RateLimit, rdb, cfg.Redis.KeyPrefix),
			Statuses: statuses,
			Activity: feed,
			Metrics:  metrics,
			Logger:   logger,
		})
		background = append(background,
			func(ctx context.Context) error { q.RunScheduler(ctx, cfg.Queue.SchedulerInterval); return nil },
			func(ctx context.Context) error { collectQueueStats(ctx, q, metrics, logger); return nil },
		)
	}

	adminServer := newHTTPServer(cfg.AdminAddr, api.AdminHandler(metrics, checks), logger)

	// Lifecycle: servers and workers run until ctx is cancelled, then shut
	// down in order (stop accepting requests, drain workers).
	errCh := make(chan error, 2+len(ingress)+len(background))
	serve := func(s *http.Server, name string) {
		logger.Info("http server listening", "server", name, "addr", s.Addr)
		if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("%s server: %w", name, err)
		}
	}
	go serve(adminServer, "admin")
	if apiServer != nil {
		go serve(apiServer, "api")
	}

	start := func(wg *sync.WaitGroup, ctx context.Context, fns []func(context.Context) error) {
		for _, fn := range fns {
			wg.Go(func() {
				if err := fn(ctx); err != nil {
					errCh <- err
				}
			})
		}
	}
	ingressCtx, stopIngress := context.WithCancel(context.WithoutCancel(ctx))
	defer stopIngress()
	var ingressWG sync.WaitGroup
	start(&ingressWG, ingressCtx, ingress)
	workCtx, stopWork := context.WithCancel(context.WithoutCancel(ctx))
	var wg sync.WaitGroup
	start(&wg, workCtx, background)
	if pool != nil {
		wg.Go(func() { pool.Run(workCtx) })
	}
	logger.Info("kourrier started", "version", version, "mode", cfg.Mode)

	var runErr error
	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case runErr = <-errCh:
		logger.Error("fatal error, shutting down", "error", runErr)
	}

	// Shutdown order: stop taking new work (HTTP and event consumers, in
	// parallel), then stop the workers. Event consumers drain the few
	// messages they already hold; whatever is not acknowledged in time is
	// redelivered by the broker, and atomic submission (see internal/submit)
	// makes an interrupted event safe to process again.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	stopIngress()
	if apiServer != nil {
		if err := apiServer.Shutdown(shutdownCtx); err != nil {
			logger.Error("api server shutdown", "error", err)
		}
	}
	wait := func(wg *sync.WaitGroup, what string) {
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-shutdownCtx.Done():
			logger.Warn("shutdown timeout reached with " + what + " in flight; they will be redelivered")
		}
	}
	wait(&ingressWG, "events")
	stopWork()
	wait(&wg, "jobs")
	_ = adminServer.Shutdown(shutdownCtx)
	logger.Info("kourrier stopped")
	return runErr
}

// NewActivity returns the activity feed (ADR 016), or nil when it is
// disabled.
func NewActivity(cfg *config.Config, rdb redis.UniversalClient) *activity.Store {
	if cfg.Activity.MaxLen <= 0 {
		return nil
	}
	return activity.NewStore(rdb, cfg.Redis.KeyPrefix, cfg.Activity.MaxLen)
}

// NewRedis creates the Redis client, sized for the given number of workers.
func NewRedis(cfg config.Redis, workers int) *redis.Client {
	opts := &redis.Options{
		Addr:     cfg.Addr,
		Username: cfg.Username,
		Password: cfg.Password,
		DB:       cfg.DB,
		// Every worker may hold a connection in a blocking XREADGROUP, on top
		// of the connections used by the API and the settling calls.
		PoolSize: max(32, workers*2+16),
	}
	if cfg.TLS {
		opts.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return redis.NewClient(opts)
}

// NewQueue creates the Redis Streams queue.
func NewQueue(ctx context.Context, cfg *config.Config, rdb redis.UniversalClient, logger *slog.Logger) (*queue.RedisStreams, error) {
	consumer, _ := os.Hostname()
	return queue.NewRedisStreams(ctx, rdb, queue.RedisConfig{
		Prefix:            cfg.Redis.KeyPrefix,
		Consumer:          consumer,
		VisibilityTimeout: cfg.Queue.VisibilityTimeout,
		ReclaimInterval:   cfg.Queue.ReclaimInterval,
		DeadLetterMaxLen:  cfg.Queue.DeadLetterMaxLen,
		WaitReplicas:      cfg.Redis.WaitReplicas,
		WaitTimeout:       cfg.Redis.WaitTimeout,
	}, logger)
}

// NewStorage creates the template storage selected by KOURRIER_STORAGE_PROVIDER.
func NewStorage(ctx context.Context, cfg config.Storage) (storage.Provider, error) {
	switch cfg.Provider {
	case config.StorageFilesystem:
		return fsstore.New(cfg.Dir)
	case config.StorageS3:
		return s3store.New(ctx, s3store.Config{
			Bucket: cfg.S3.Bucket, Prefix: cfg.S3.Prefix, Region: cfg.S3.Region, Endpoint: cfg.S3.Endpoint,
			AccessKeyID: cfg.S3.AccessKeyID, SecretAccessKey: cfg.S3.SecretAccessKey, UsePathStyle: cfg.S3.UsePathStyle,
		})
	default:
		return nil, fmt.Errorf("unknown storage provider %q", cfg.Provider)
	}
}

// NewProvider creates the mailer selected by KOURRIER_MAILER_PROVIDER.
func NewProvider(ctx context.Context, cfg config.Mailer) (provider.Provider, error) {
	switch cfg.Provider {
	case config.ProviderSES:
		return ses.New(ctx, ses.Config{Region: cfg.SES.Region, ConfigurationSet: cfg.SES.ConfigurationSet})
	case config.ProviderSMTP:
		return smtp.New(smtp.Config{
			Host: cfg.SMTP.Host, Port: cfg.SMTP.Port, Username: cfg.SMTP.Username,
			Password: cfg.SMTP.Password, TLS: cfg.SMTP.TLS, Timeout: cfg.SMTP.Timeout,
		})
	case config.ProviderConsole:
		return console.New(os.Stdout), nil
	case config.ProviderFilesystem:
		return filesystem.New(cfg.Filesystem.Dir)
	default:
		return nil, fmt.Errorf("unknown mailer provider %q", cfg.Provider)
	}
}

// NewLimiter creates the sending rate limiter.
func NewLimiter(cfg config.RateLimit, rdb redis.UniversalClient, prefix string) ratelimit.Limiter {
	switch {
	case cfg.PerSecond == 0:
		return ratelimit.Unlimited{}
	case cfg.Scope == "local":
		return ratelimit.NewLocal(cfg.PerSecond, cfg.Burst)
	default:
		return ratelimit.NewRedis(rdb, prefix, cfg.PerSecond, cfg.Burst)
	}
}

func newHTTPServer(addr string, h http.Handler, logger *slog.Logger) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
}

// waitForRedis retries the initial connection for a while, so that the pod
// survives Redis starting slightly after it.
func waitForRedis(ctx context.Context, rdb *redis.Client, logger *slog.Logger) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := rdb.Ping(ctx).Err()
		if err == nil {
			return nil
		}
		var opErr *net.OpError
		if time.Now().After(deadline) || ctx.Err() != nil || (!errors.As(err, &opErr) && !errors.Is(err, context.DeadlineExceeded)) {
			return fmt.Errorf("connect to redis: %w", err)
		}
		logger.Warn("waiting for redis", "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func collectQueueStats(ctx context.Context, q queue.Queue, m *observability.Metrics, logger *slog.Logger) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		stats, err := q.Stats(ctx)
		if err == nil {
			m.QueueDepth.WithLabelValues("ready").Set(float64(stats.Ready))
			m.QueueDepth.WithLabelValues("pending").Set(float64(stats.Pending))
			m.QueueDepth.WithLabelValues("delayed").Set(float64(stats.Delayed))
			m.QueueDepth.WithLabelValues("dead").Set(float64(stats.Dead))
		} else if ctx.Err() == nil {
			logger.Warn("failed to collect queue stats", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
