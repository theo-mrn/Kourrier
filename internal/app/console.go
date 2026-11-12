package app

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/theo-mrn/kourrier/internal/activity"
	"github.com/theo-mrn/kourrier/internal/config"
	"github.com/theo-mrn/kourrier/internal/console"
	"github.com/theo-mrn/kourrier/internal/queue"
	"github.com/theo-mrn/kourrier/internal/status"
	"github.com/theo-mrn/kourrier/internal/templates"
	"github.com/theo-mrn/kourrier/internal/tenant"
)

// consoleDeps groups what the console reads from the other components.
type consoleDeps struct {
	version   string
	tenants   *tenant.Registry
	queue     *queue.RedisStreams
	statuses  *status.Store
	activity  *activity.Store
	templates *templates.Renderer
	events    eventIngress
}

// newConsole builds the web administration console server (ADR 016).
func newConsole(cfg *config.Config, d consoleDeps, logger *slog.Logger) (*http.Server, error) {
	token, err := console.LoadToken(cfg.Console.TokenFile)
	if err != nil {
		return nil, err
	}
	deps := console.Deps{
		Token: token, SessionTTL: cfg.Console.SessionTTL, SecureCookie: cfg.Console.SecureCookie,
		Info: console.Info{
			Version: d.version, Mode: cfg.Mode, Mailer: cfg.Mailer.Provider, Storage: cfg.Storage.Provider,
			StartedAt: time.Now().UTC(),
		},
		Tenants: d.tenants, Queue: d.queue, Statuses: d.statuses, Activity: d.activity, Templates: d.templates,
		Logger: logger,
	}
	if d.events.dispatcher != nil {
		deps.Events = &console.Events{Routes: d.events.routes, Dead: d.events.dead, Dispatcher: d.events.dispatcher}
	}
	srv, err := console.New(deps)
	if err != nil {
		return nil, err
	}
	if !cfg.Console.SecureCookie {
		logger.Warn("console session cookie without the Secure attribute: serve the console over HTTPS in production")
	}
	return newHTTPServer(cfg.Console.Addr, srv.Handler(), logger), nil
}
