package dispatch

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/theo-mrn/kourrier/internal/observability"
	"github.com/theo-mrn/kourrier/internal/queue"
)

// StatsFunc reads the size of the e-mail queue.
type StatsFunc func(ctx context.Context) (queue.Stats, error)

// DepthGate pauses event ingestion while the e-mail queue holds more than Max
// jobs waiting to be sent (ready + delayed).
//
// Brokers such as JetStream keep their backlog on disk and are built for it;
// Redis keeps the e-mail queue in memory. When sending is slower than event
// production (provider quota, outage), the backlog must stay in the broker
// rather than grow in Redis until it runs out of memory. The gate reads the
// queue size periodically, so the limit is approximate by design.
type DepthGate struct {
	stats    StatsFunc
	max      int64
	interval time.Duration
	metrics  *observability.Metrics
	logger   *slog.Logger

	overloaded atomic.Bool
}

// NewDepthGate creates a gate. Call Run to start polling.
func NewDepthGate(stats StatsFunc, maxDepth int64, interval time.Duration, m *observability.Metrics, logger *slog.Logger) *DepthGate {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &DepthGate{stats: stats, max: maxDepth, interval: interval, metrics: m, logger: logger}
}

// Overloaded reports whether ingestion should pause.
func (g *DepthGate) Overloaded() bool { return g.overloaded.Load() }

// Run polls the queue size until ctx is done. When the size cannot be read,
// the previous state is kept: a Redis outage already makes submissions fail.
func (g *DepthGate) Run(ctx context.Context) {
	t := time.NewTicker(g.interval)
	defer t.Stop()
	for {
		g.check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (g *DepthGate) check(ctx context.Context) {
	s, err := g.stats(ctx)
	if err != nil {
		if ctx.Err() == nil {
			g.logger.WarnContext(ctx, "backpressure gate: cannot read queue size", "error", err)
		}
		return
	}
	depth := s.Ready + s.Delayed
	over := depth > g.max
	if g.overloaded.Swap(over) != over {
		if over {
			g.logger.WarnContext(ctx, "e-mail queue above its limit, pausing event ingestion", "depth", depth, "max", g.max)
		} else {
			g.logger.InfoContext(ctx, "e-mail queue back under its limit, resuming event ingestion", "depth", depth, "max", g.max)
		}
	}
	v := 0.0
	if over {
		v = 1
	}
	g.metrics.EventsBackpressured.Set(v)
}
