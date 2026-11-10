package app

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/theo-mrn/kourrier/internal/config"
	"github.com/theo-mrn/kourrier/internal/dispatch"
	"github.com/theo-mrn/kourrier/internal/observability"
	"github.com/theo-mrn/kourrier/internal/queue"
	"github.com/theo-mrn/kourrier/internal/routing"
	"github.com/theo-mrn/kourrier/internal/source/natsjs"
	"github.com/theo-mrn/kourrier/internal/submit"
	"github.com/theo-mrn/kourrier/internal/tenant"
)

// gateInterval is how often the backpressure gate reads the queue size.
const gateInterval = 5 * time.Second

// eventIngress groups the components of the event ingress (ADR 012). Its
// zero value means the ingress is disabled.
type eventIngress struct {
	// dispatcher is nil when no routes file is configured, which disables
	// POST /v1/events.
	dispatcher *dispatch.Dispatcher
	// run lists the components to start (backpressure gate, consumers).
	run []func(context.Context) error
}

func newEventIngress(cfg *config.Config, tenants *tenant.Registry, submitter *submit.Service, q queue.Queue,
	metrics *observability.Metrics, logger *slog.Logger,
) (eventIngress, error) {
	var in eventIngress
	if cfg.Events.RoutesFile == "" {
		return in, nil
	}
	routes, err := routing.LoadFile(cfg.Events.RoutesFile, tenants)
	if err != nil {
		return in, err
	}
	logger.Info("event routes loaded", "routes", len(routes.Router.Routes()), "nats_sources", len(routes.NATS))

	var gate dispatch.Gate
	if cfg.Events.MaxQueueDepth > 0 {
		g := dispatch.NewDepthGate(q.Stats, cfg.Events.MaxQueueDepth, gateInterval, metrics, logger)
		gate = g
		in.run = append(in.run, func(ctx context.Context) error { g.Run(ctx); return nil })
	}
	in.dispatcher = dispatch.New(dispatch.Deps{
		Router: routes.Router, Submitter: submitter, Gate: gate, Metrics: metrics, Logger: logger,
	})

	if len(routes.NATS) > 0 {
		name, _ := os.Hostname()
		n := cfg.Events.NATS
		consumer, err := natsjs.New(natsjs.Config{
			URL: n.URL, Name: "kourrier-" + name, CredsFile: n.CredsFile, NKeyFile: n.NKeyFile,
			CAFile: n.TLSCAFile, CertFile: n.TLSCertFile, KeyFile: n.TLSKeyFile,
		}, routes.NATS, natsjs.Deps{Tenants: tenants, Dispatcher: in.dispatcher, Metrics: metrics, Logger: logger})
		if err != nil {
			return in, err
		}
		in.run = append(in.run, consumer.Run)
	}
	return in, nil
}
