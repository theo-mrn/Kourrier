package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/theo-mrn/kourrier/internal/id"
	"github.com/theo-mrn/kourrier/internal/observability"
	"github.com/theo-mrn/kourrier/internal/tenant"
)

type ctxKey int

const (
	ctxRequestID ctxKey = iota
	ctxTenant
	ctxLogInfo
)

// logInfo lets inner handlers enrich the access log written by the outer
// observability middleware.
type logInfo struct {
	tenant string
}

func requestIDFrom(ctx context.Context) string {
	s, _ := ctx.Value(ctxRequestID).(string)
	return s
}

func tenantFrom(ctx context.Context) *tenant.Tenant {
	t, _ := ctx.Value(ctxTenant).(*tenant.Tenant)
	return t
}

// statusRecorder captures the response status code.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// withRequestID propagates the caller's X-Request-ID, or generates one.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get("X-Request-ID")
		if rid == "" || len(rid) > 128 {
			rid = id.New()
		}
		w.Header().Set("X-Request-ID", rid)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequestID, rid)))
	})
}

// withObservability logs every request and records HTTP metrics. The route
// label is the matched ServeMux pattern, never the raw path, to keep
// cardinality bounded.
func withObservability(logger *slog.Logger, m *observability.Metrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		info := &logInfo{}
		r = r.WithContext(context.WithValue(r.Context(), ctxLogInfo, info))
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		elapsed := time.Since(start)

		route := r.Pattern
		if route == "" {
			route = "unmatched"
		} else if _, path, ok := strings.Cut(route, " "); ok {
			route = path
		}
		m.HTTPRequests.WithLabelValues(r.Method, route, strconv.Itoa(rec.status)).Inc()
		m.HTTPDuration.WithLabelValues(r.Method, route).Observe(elapsed.Seconds())

		attrs := []any{
			"method", r.Method, "route", route, "status", rec.status,
			"duration_ms", elapsed.Milliseconds(), "request_id", requestIDFrom(r.Context()),
		}
		if info.tenant != "" {
			attrs = append(attrs, "tenant", info.tenant)
		}
		level := slog.LevelInfo
		if rec.status >= 500 {
			level = slog.LevelError
		}
		logger.Log(r.Context(), level, "http request", attrs...)
	})
}

// withRecover turns panics into 500 responses.
func withRecover(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
				logger.ErrorContext(r.Context(), "panic in handler", "panic", v, "stack", string(debug.Stack()))
				writeProblem(w, r, http.StatusInternalServerError, "internal", "")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// requireTenant authenticates the request with a Bearer API key.
func (s *Server) requireTenant(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		t, found := s.tenants.Authenticate(strings.TrimSpace(key))
		if !ok || !found {
			w.Header().Set("WWW-Authenticate", `Bearer realm="kourrier"`)
			writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "a valid API key is required")
			return
		}
		if info, ok := r.Context().Value(ctxLogInfo).(*logInfo); ok {
			info.tenant = t.ID
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxTenant, t)))
	}
}
