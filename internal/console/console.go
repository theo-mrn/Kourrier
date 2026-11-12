// Package console serves the optional web administration console (ADR 016):
// a React single-page application embedded in the binary and the JSON admin
// API it uses, on a dedicated port, behind an admin token.
//
// The console only reads what Kourrier already stores (queue, statuses,
// activity feed, dead letters, templates, tenants, routes) and acts on the
// dead-letter queues. It never exposes the digests of the API keys.
package console

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/theo-mrn/kourrier/internal/activity"
	"github.com/theo-mrn/kourrier/internal/deadevent"
	"github.com/theo-mrn/kourrier/internal/dispatch"
	"github.com/theo-mrn/kourrier/internal/mail"
	"github.com/theo-mrn/kourrier/internal/queue"
	"github.com/theo-mrn/kourrier/internal/routing"
	"github.com/theo-mrn/kourrier/internal/status"
	"github.com/theo-mrn/kourrier/internal/templates"
	"github.com/theo-mrn/kourrier/internal/tenant"
)

// Queue is the part of the e-mail queue the console reads and acts on.
// *queue.RedisStreams implements it.
type Queue interface {
	Stats(ctx context.Context) (queue.Stats, error)
	ListDeadAfter(ctx context.Context, after string, count int64) ([]queue.DeadJob, error)
	DeadJobs(ctx context.Context, ids ...string) ([]queue.DeadJob, error)
	ReplayDead(ctx context.Context, entries []queue.DeadJob) (int, error)
	DeleteDead(ctx context.Context, ids ...string) (int64, error)
}

// StatusReader reads delivery statuses. *status.Store implements it.
type StatusReader interface {
	Get(ctx context.Context, id string) (*status.Status, error)
}

// Templates lists and renders templates. *templates.Renderer implements it.
type Templates interface {
	List(ctx context.Context, tenant string) ([]templates.Info, error)
	Render(ctx context.Context, tenant string, ref mail.TemplateRef) (*templates.Rendered, error)
}

// DeadEvents is the store of events kept by the event ingress.
// *deadevent.Store implements it.
type DeadEvents interface {
	ListAfter(ctx context.Context, after string, count int64) ([]deadevent.Entry, error)
	Get(ctx context.Context, ids ...string) ([]deadevent.Entry, error)
	Delete(ctx context.Context, ids ...string) error
	Len(ctx context.Context) (int64, error)
}

// Replayer replays kept events. *dispatch.Dispatcher implements it.
type Replayer interface {
	ReplayDead(ctx context.Context, store dispatch.DeadEventDeleter, tenants dispatch.TenantLookup,
		entries []deadevent.Entry) ([]dispatch.ReplayResult, error)
}

// Events groups what the console shows of the event ingress. It is nil when
// no routes file is configured.
type Events struct {
	Routes     *routing.Config
	Dead       DeadEvents
	Dispatcher Replayer
}

// Info describes the running instance on the overview page.
type Info struct {
	Version   string    `json:"version"`
	Mode      string    `json:"mode"`
	Mailer    string    `json:"mailer"`
	Storage   string    `json:"storage"`
	StartedAt time.Time `json:"started_at"`
}

// Deps groups the collaborators of a Server.
type Deps struct {
	// Token is the admin token (see LoadToken).
	Token      string
	SessionTTL time.Duration
	// SecureCookie sets the Secure attribute of the session cookie.
	SecureCookie bool
	Info         Info
	Tenants      *tenant.Registry
	Queue        Queue
	Statuses     StatusReader
	// Activity is nil when the activity feed is disabled.
	Activity  *activity.Store
	Templates Templates
	// Events is nil when the event ingress is disabled.
	Events *Events
	Logger *slog.Logger
}

// Server is the console HTTP server.
type Server struct {
	d        Deps
	sessions *sessions
}

// New creates a Server. The token must have been checked by LoadToken.
func New(d Deps) (*Server, error) {
	if len(d.Token) < MinTokenLength {
		return nil, errors.New("console: the admin token is too short")
	}
	if d.SessionTTL <= 0 {
		d.SessionTTL = 12 * time.Hour
	}
	return &Server{d: d, sessions: newSessions(d.Token, d.SessionTTL, d.SecureCookie)}, nil
}

// maxBodyBytes bounds the JSON bodies of the admin API.
const maxBodyBytes = 64 << 10

// Handler returns the console handler: the admin API under /api/ and the
// single-page application everywhere else.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// Sessions.
	mux.HandleFunc("GET /api/session", s.authenticated(s.handleSession))
	mux.HandleFunc("POST /api/session", s.handleLogin)
	mux.HandleFunc("DELETE /api/session", s.handleLogout)
	// Reads.
	mux.HandleFunc("GET /api/overview", s.authenticated(s.handleOverview))
	mux.HandleFunc("GET /api/activity", s.authenticated(s.handleActivity))
	mux.HandleFunc("GET /api/emails/{id}", s.authenticated(s.handleEmail))
	mux.HandleFunc("GET /api/dlq", s.authenticated(s.handleDLQ))
	mux.HandleFunc("GET /api/dead-events", s.authenticated(s.handleDeadEvents))
	mux.HandleFunc("GET /api/dead-events/{id}", s.authenticated(s.handleDeadEvent))
	mux.HandleFunc("GET /api/tenants", s.authenticated(s.handleTenants))
	mux.HandleFunc("GET /api/templates", s.authenticated(s.handleTemplates))
	mux.HandleFunc("GET /api/templates/{tenant}/{name}/preview", s.authenticated(s.handlePreview))
	mux.HandleFunc("GET /api/templates/{tenant}/{name}/preview.html", s.authenticated(s.handlePreviewHTML))
	mux.HandleFunc("GET /api/routes", s.authenticated(s.handleRoutes))
	// Actions.
	mux.HandleFunc("POST /api/dlq/replay", s.authenticated(s.handleDLQReplay))
	mux.HandleFunc("POST /api/dlq/delete", s.authenticated(s.handleDLQDelete))
	mux.HandleFunc("POST /api/dead-events/replay", s.authenticated(s.handleDeadEventsReplay))
	mux.HandleFunc("POST /api/dead-events/delete", s.authenticated(s.handleDeadEventsDelete))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, http.StatusNotFound, "not-found", "no such endpoint")
	})
	mux.Handle("/", spaHandler())
	return securityHeaders(s.guard(mux))
}

// guard enforces the same-origin rules on every request that changes state,
// login included, and disables caching of the admin API.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/" {
			w.Header().Set("Cache-Control", "no-store")
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if !sameOrigin(r) {
				s.d.Logger.WarnContext(r.Context(), "console: cross-origin request refused",
					"method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)
				writeProblem(w, http.StatusForbidden, "forbidden", "cross-origin request refused")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// authenticated requires a valid session.
func (s *Server) authenticated(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.sessions.verify(r); err != nil {
			writeProblem(w, http.StatusUnauthorized, "unauthorized", "sign in with the admin token")
			return
		}
		next(w, r)
	}
}

// audit logs an action of the operator.
func (s *Server) audit(r *http.Request, action string, attrs ...any) {
	s.d.Logger.InfoContext(r.Context(), "console: "+action, append([]any{"remote", r.RemoteAddr}, attrs...)...)
}

// Problem is an RFC 9457 error, like the public API.
type Problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func writeProblem(w http.ResponseWriter, status int, kind, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Problem{Type: kind, Title: http.StatusText(status), Status: status, Detail: detail})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// decode reads a JSON body, writing the problem itself on failure.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeProblem(w, http.StatusBadRequest, "malformed", "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// unavailable reports a backend failure.
func (s *Server) unavailable(w http.ResponseWriter, r *http.Request, what string, err error) {
	s.d.Logger.ErrorContext(r.Context(), "console: "+what, "error", err)
	writeProblem(w, http.StatusServiceUnavailable, "unavailable", what+" failed, try again")
}
