package observability

import (
	"runtime"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

const namespace = "kourrier"

// Failure reasons used as the "reason" label of EmailsFailed.
const (
	ReasonPermanent = "permanent"
	ReasonExhausted = "retries_exhausted"
)

// Metrics groups every Prometheus collector exposed on /metrics. Labels are
// restricted to bounded sets (tenant IDs come from configuration, never from
// request payloads) to keep cardinality under control.
type Metrics struct {
	Registry *prometheus.Registry

	HTTPRequests *prometheus.CounterVec
	HTTPDuration *prometheus.HistogramVec

	JobsEnqueued      *prometheus.CounterVec
	IdempotentReplays *prometheus.CounterVec
	EmailsSent        *prometheus.CounterVec
	EmailsFailed      *prometheus.CounterVec
	JobRetries        *prometheus.CounterVec
	SendDuration      *prometheus.HistogramVec
	DeliveryLatency   *prometheus.HistogramVec

	TemplateRender *prometheus.HistogramVec
	TemplateCache  *prometheus.CounterVec
	RateLimitWait  prometheus.Histogram
	QueueDepth     *prometheus.GaugeVec
	WorkersBusy    prometheus.Gauge

	// Event ingress (ADR 012). The "source" label is "http" or the name of a
	// configured NATS source, and "route" a configured route name: both are
	// bounded by configuration.
	EventsReceived      *prometheus.CounterVec
	EventRoutes         *prometheus.CounterVec
	EventDispatch       *prometheus.HistogramVec
	EventsBackpressured prometheus.Gauge
	EventsDeadLettered  *prometheus.CounterVec
	DeadEvents          prometheus.Gauge
	EventSourceUp       *prometheus.GaugeVec
}

// NewMetrics creates the collectors on a dedicated registry, along with the
// standard Go runtime and process collectors.
func NewMetrics(version string) *Metrics {
	reg := prometheus.NewRegistry()
	f := func(c prometheus.Collector) { reg.MustRegister(c) }

	m := &Metrics{
		Registry: reg,
		HTTPRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "http", Name: "requests_total",
			Help: "HTTP requests handled by the API, by route and status code.",
		}, []string{"method", "route", "code"}),
		HTTPDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Subsystem: "http", Name: "request_duration_seconds",
			Help:    "HTTP request latency.",
			Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5},
		}, []string{"method", "route"}),
		JobsEnqueued: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "jobs_enqueued_total",
			Help: "E-mails accepted by the API and pushed to the queue.",
		}, []string{"tenant"}),
		IdempotentReplays: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "idempotent_replays_total",
			Help: "Requests answered from the idempotency store instead of being enqueued again.",
		}, []string{"tenant"}),
		EmailsSent: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "emails_sent_total",
			Help: "E-mails successfully handed to the provider.",
		}, []string{"tenant", "provider"}),
		EmailsFailed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "emails_failed_total",
			Help: "E-mails moved to the dead-letter queue.",
		}, []string{"tenant", "provider", "reason"}),
		JobRetries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "job_retries_total",
			Help: "Delivery attempts that failed with a transient error and were rescheduled.",
		}, []string{"tenant", "provider"}),
		SendDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Name: "provider_send_duration_seconds",
			Help:    "Latency of provider Send calls.",
			Buckets: []float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
		}, []string{"provider", "outcome"}),
		DeliveryLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Name: "delivery_latency_seconds",
			Help:    "Time between API acceptance and successful hand-off to the provider.",
			Buckets: []float64{.05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 300, 900, 3600},
		}, []string{"tenant"}),
		TemplateRender: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace, Name: "template_render_duration_seconds",
			Help:    "Time spent loading and rendering templates.",
			Buckets: []float64{.0005, .001, .005, .01, .05, .1, .5, 1},
		}, []string{"outcome"}),
		TemplateCache: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "template_cache_total",
			Help: "Template cache lookups by result (hit or miss).",
		}, []string{"result"}),
		RateLimitWait: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace, Name: "rate_limit_wait_seconds",
			Help:    "Time workers spent waiting for the sending rate limiter.",
			Buckets: []float64{0, .01, .05, .1, .25, .5, 1, 2.5, 5, 10},
		}),
		QueueDepth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "queue_depth",
			Help: "Number of jobs per queue state (ready, pending, delayed, dead).",
		}, []string{"state"}),
		WorkersBusy: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Name: "workers_busy",
			Help: "Workers currently processing a job.",
		}),
	}

	m.EventsReceived = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "events_received_total",
		Help: "CloudEvents received, by source and outcome (accepted, ignored, rejected, retry).",
	}, []string{"source", "outcome"})
	m.EventRoutes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "event_routes_total",
		Help: "Route evaluations that matched an event, by outcome (queued, replayed, rejected, failed, throttled).",
	}, []string{"tenant", "route", "outcome"})
	m.EventDispatch = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Name: "event_dispatch_duration_seconds",
		Help:    "Time spent routing an event and enqueuing the resulting e-mails.",
		Buckets: []float64{.0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1},
	}, []string{"source"})
	m.EventsBackpressured = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Name: "events_backpressure",
		Help: "1 while event ingestion is paused because the e-mail queue is above its limit.",
	})
	m.EventsDeadLettered = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "events_dead_lettered_total",
		Help: "Events kept in the event dead-letter queue, by source and reason (rejected, partial, unrouted, invalid).",
	}, []string{"source", "reason"})
	m.DeadEvents = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Name: "dead_events",
		Help: "Events waiting in the event dead-letter queue.",
	})
	m.EventSourceUp = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Name: "event_source_up",
		Help: "1 when the event source is connected and consuming.",
	}, []string{"source"})

	f(collectors.NewGoCollector())
	f(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	for _, c := range []prometheus.Collector{
		m.HTTPRequests, m.HTTPDuration, m.JobsEnqueued, m.IdempotentReplays,
		m.EmailsSent, m.EmailsFailed, m.JobRetries, m.SendDuration, m.DeliveryLatency,
		m.TemplateRender, m.TemplateCache, m.RateLimitWait, m.QueueDepth, m.WorkersBusy,
		m.EventsReceived, m.EventRoutes, m.EventDispatch, m.EventsBackpressured, m.EventSourceUp,
		m.EventsDeadLettered, m.DeadEvents,
	} {
		f(c)
	}

	buildInfo := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Name: "build_info",
		Help: "Build information, always 1.",
	}, []string{"version", "goversion"})
	buildInfo.WithLabelValues(version, runtime.Version()).Set(1)
	f(buildInfo)

	return m
}
