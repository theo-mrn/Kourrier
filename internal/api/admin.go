package api

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/theo-mrn/kourrier/internal/observability"
)

// Check is a readiness probe for a dependency.
type Check func(ctx context.Context) error

// AdminHandler serves the operational endpoints on a separate port, so they
// are never exposed through the public ingress:
//
//	/metrics  Prometheus metrics
//	/livez    liveness: the process is running
//	/readyz   readiness: every dependency check passes
func AdminHandler(m *observability.Metrics, checks map[string]Check) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{Registry: m.Registry}))
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		results := make(map[string]string, len(checks))
		code := http.StatusOK
		for name, check := range checks {
			if err := check(ctx); err != nil {
				results[name] = err.Error()
				code = http.StatusServiceUnavailable
			} else {
				results[name] = "ok"
			}
		}
		writeJSON(w, code, results)
	})
	return mux
}
