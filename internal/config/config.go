// Package config loads the service configuration from KOURRIER_* environment
// variables (12-factor). See docs/configuration.md for the full reference.
package config

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/caarlos0/env/v11"
)

// Run modes. "all" runs the API and the workers in the same process (ADR 001);
// "api" and "worker" allow scaling them independently later on.
const (
	ModeAll    = "all"
	ModeAPI    = "api"
	ModeWorker = "worker"
)

// Mailer providers (ADR 004).
const (
	ProviderSES        = "aws-ses"
	ProviderSMTP       = "smtp"
	ProviderConsole    = "console"
	ProviderFilesystem = "filesystem"
)

// Storage providers (ADR 003).
const (
	StorageS3         = "s3"
	StorageFilesystem = "filesystem"
)

// Config is the full service configuration.
type Config struct {
	Mode            string        `env:"MODE" envDefault:"all"`
	HTTPAddr        string        `env:"HTTP_ADDR" envDefault:":8080"`
	AdminAddr       string        `env:"ADMIN_ADDR" envDefault:":9090"`
	LogFormat       string        `env:"LOG_FORMAT" envDefault:"json"`
	LogLevel        string        `env:"LOG_LEVEL" envDefault:"info"`
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"25s"`
	MaxBodyBytes    int64         `env:"MAX_BODY_BYTES" envDefault:"1048576"`
	TenantsFile     string        `env:"TENANTS_FILE" envDefault:"/etc/kourrier/tenants.yaml"`

	Redis       Redis       `envPrefix:"REDIS_"`
	Queue       Queue       `envPrefix:"QUEUE_"`
	Worker      Worker      `envPrefix:"WORKER_"`
	RateLimit   RateLimit   `envPrefix:"RATE_LIMIT_"`
	Idempotency Idempotency `envPrefix:"IDEMPOTENCY_"`
	Status      Status      `envPrefix:"STATUS_"`
	Templates   Templates   `envPrefix:"TEMPLATES_"`
	Storage     Storage     `envPrefix:"STORAGE_"`
	Mailer      Mailer      `envPrefix:"MAILER_"`
}

// Redis connection settings. Requires Redis >= 7.
type Redis struct {
	Addr      string `env:"ADDR" envDefault:"localhost:6379"`
	Username  string `env:"USERNAME"`
	Password  string `env:"PASSWORD"`
	DB        int    `env:"DB" envDefault:"0"`
	TLS       bool   `env:"TLS" envDefault:"false"`
	KeyPrefix string `env:"KEY_PREFIX" envDefault:"kourrier"`
}

// Queue settings (ADR 006).
type Queue struct {
	VisibilityTimeout time.Duration `env:"VISIBILITY_TIMEOUT" envDefault:"5m"`
	ReclaimInterval   time.Duration `env:"RECLAIM_INTERVAL" envDefault:"30s"`
	SchedulerInterval time.Duration `env:"SCHEDULER_INTERVAL" envDefault:"1s"`
	DeadLetterMaxLen  int64         `env:"DEAD_LETTER_MAX_LEN" envDefault:"10000"`
}

// Worker settings.
type Worker struct {
	Concurrency int           `env:"CONCURRENCY" envDefault:"8"`
	SendTimeout time.Duration `env:"SEND_TIMEOUT" envDefault:"30s"`
	MaxAttempts int           `env:"MAX_ATTEMPTS" envDefault:"8"`
	BackoffBase time.Duration `env:"BACKOFF_BASE" envDefault:"10s"`
	BackoffMax  time.Duration `env:"BACKOFF_MAX" envDefault:"30m"`
}

// RateLimit settings (ADR 009). PerSecond = 0 disables rate limiting.
type RateLimit struct {
	PerSecond float64 `env:"PER_SECOND" envDefault:"10"`
	Burst     int     `env:"BURST" envDefault:"10"`
	// Scope is "global" (shared through Redis by every pod) or "local".
	Scope string `env:"SCOPE" envDefault:"global"`
}

// Idempotency settings (ADR 007).
type Idempotency struct {
	TTL time.Duration `env:"TTL" envDefault:"24h"`
}

// Status retention.
type Status struct {
	TTL time.Duration `env:"TTL" envDefault:"168h"`
}

// Templates settings.
type Templates struct {
	CacheTTL      time.Duration `env:"CACHE_TTL" envDefault:"1m"`
	DefaultLocale string        `env:"DEFAULT_LOCALE" envDefault:"en"`
}

// Storage settings (ADR 003).
type Storage struct {
	Provider string `env:"PROVIDER" envDefault:"s3"`
	// Dir is used by the filesystem provider.
	Dir string `env:"DIR" envDefault:"./templates"`
	S3  S3     `envPrefix:"S3_"`
}

// S3 settings. Endpoint and static credentials are only needed for MinIO and
// other S3-compatible services.
type S3 struct {
	Bucket          string `env:"BUCKET" envDefault:"kourrier-templates"`
	Prefix          string `env:"PREFIX"`
	Region          string `env:"REGION" envDefault:"us-east-1"`
	Endpoint        string `env:"ENDPOINT"`
	AccessKeyID     string `env:"ACCESS_KEY_ID"`
	SecretAccessKey string `env:"SECRET_ACCESS_KEY"`
	UsePathStyle    bool   `env:"USE_PATH_STYLE" envDefault:"false"`
}

// Mailer settings (ADR 004). The default provider is "console" so that a
// misconfigured deployment can never send real e-mails by accident.
type Mailer struct {
	Provider   string     `env:"PROVIDER" envDefault:"console"`
	SES        SES        `envPrefix:"SES_"`
	SMTP       SMTP       `envPrefix:"SMTP_"`
	Filesystem Filesystem `envPrefix:"FILESYSTEM_"`
}

// SES settings.
type SES struct {
	Region           string `env:"REGION" envDefault:"eu-west-3"`
	ConfigurationSet string `env:"CONFIGURATION_SET"`
}

// SMTP settings.
type SMTP struct {
	Host     string        `env:"HOST" envDefault:"localhost"`
	Port     int           `env:"PORT" envDefault:"1025"`
	Username string        `env:"USERNAME"`
	Password string        `env:"PASSWORD"`
	TLS      string        `env:"TLS" envDefault:"none"`
	Timeout  time.Duration `env:"TIMEOUT" envDefault:"15s"`
}

// Filesystem mailer settings.
type Filesystem struct {
	Dir string `env:"DIR" envDefault:"./tmp/emails"`
}

// Load parses the environment and validates the result.
func Load() (*Config, error) {
	var c Config
	if err := env.ParseWithOptions(&c, env.Options{Prefix: "KOURRIER_"}); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Validate checks values that the type system cannot.
func (c *Config) Validate() error {
	var errs []error
	check := func(ok bool, format string, args ...any) {
		if !ok {
			errs = append(errs, fmt.Errorf(format, args...))
		}
	}
	check(slices.Contains([]string{ModeAll, ModeAPI, ModeWorker}, c.Mode),
		"KOURRIER_MODE must be one of all, api, worker (got %q)", c.Mode)
	check(slices.Contains([]string{ProviderSES, ProviderSMTP, ProviderConsole, ProviderFilesystem}, c.Mailer.Provider),
		"KOURRIER_MAILER_PROVIDER must be one of aws-ses, smtp, console, filesystem (got %q)", c.Mailer.Provider)
	check(slices.Contains([]string{StorageS3, StorageFilesystem}, c.Storage.Provider),
		"KOURRIER_STORAGE_PROVIDER must be one of s3, filesystem (got %q)", c.Storage.Provider)
	check(slices.Contains([]string{"global", "local"}, c.RateLimit.Scope),
		"KOURRIER_RATE_LIMIT_SCOPE must be global or local (got %q)", c.RateLimit.Scope)
	check(c.RateLimit.PerSecond >= 0, "KOURRIER_RATE_LIMIT_PER_SECOND must be >= 0")
	check(c.Worker.Concurrency >= 1, "KOURRIER_WORKER_CONCURRENCY must be >= 1")
	check(c.Worker.MaxAttempts >= 1, "KOURRIER_WORKER_MAX_ATTEMPTS must be >= 1")
	check(c.Worker.BackoffBase > 0 && c.Worker.BackoffMax >= c.Worker.BackoffBase,
		"KOURRIER_WORKER_BACKOFF_BASE must be > 0 and <= KOURRIER_WORKER_BACKOFF_MAX")
	check(c.Queue.VisibilityTimeout > c.Worker.SendTimeout,
		"KOURRIER_QUEUE_VISIBILITY_TIMEOUT must be greater than KOURRIER_WORKER_SEND_TIMEOUT")
	check(c.Storage.Provider != StorageS3 || c.Storage.S3.Bucket != "", "KOURRIER_STORAGE_S3_BUCKET is required")
	return errors.Join(errs...)
}

// RunsAPI reports whether the HTTP API is enabled.
func (c *Config) RunsAPI() bool { return c.Mode == ModeAll || c.Mode == ModeAPI }

// RunsWorkers reports whether the workers are enabled.
func (c *Config) RunsWorkers() bool { return c.Mode == ModeAll || c.Mode == ModeWorker }
