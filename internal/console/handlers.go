package console

import (
	"encoding/json"
	"errors"
	"html"
	"io"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/theo-mrn/kourrier/internal/activity"
	"github.com/theo-mrn/kourrier/internal/cloudevent"
	"github.com/theo-mrn/kourrier/internal/deadevent"
	"github.com/theo-mrn/kourrier/internal/dispatch"
	"github.com/theo-mrn/kourrier/internal/mail"
	"github.com/theo-mrn/kourrier/internal/observability"
	"github.com/theo-mrn/kourrier/internal/queue"
	"github.com/theo-mrn/kourrier/internal/routing"
	"github.com/theo-mrn/kourrier/internal/status"
	"github.com/theo-mrn/kourrier/internal/templates"
	"github.com/theo-mrn/kourrier/internal/tenant"
)

const (
	defaultPageSize = 50
	maxPageSize     = 200
	// maxIDs bounds the entries of one action.
	maxIDs = 100
)

// ---------- Sessions ----------

// SessionResponse describes the current session.
type SessionResponse struct {
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	exp, _ := s.sessions.verify(r)
	writeJSON(w, http.StatusOK, SessionResponse{ExpiresAt: exp})
}

// LoginRequest is the body of POST /api/session.
type LoginRequest struct {
	Token string `json:"token"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if !decode(w, r, &req) {
		return
	}
	if !s.sessions.checkToken(req.Token) {
		s.d.Logger.WarnContext(r.Context(), "console: sign-in refused", "remote", r.RemoteAddr)
		writeProblem(w, http.StatusUnauthorized, "invalid-token", "the admin token is not valid")
		return
	}
	exp := s.sessions.issue(w)
	s.audit(r, "signed in")
	writeJSON(w, http.StatusOK, SessionResponse{ExpiresAt: exp})
}

func (s *Server) handleLogout(w http.ResponseWriter, _ *http.Request) {
	s.sessions.clear(w)
	w.WriteHeader(http.StatusNoContent)
}

// ---------- Overview ----------

// QueueStats counts the jobs of the e-mail queue.
type QueueStats struct {
	Ready   int64 `json:"ready"`
	Pending int64 `json:"pending"`
	Delayed int64 `json:"delayed"`
	Dead    int64 `json:"dead"`
}

// EventsOverview summarises the event ingress.
type EventsOverview struct {
	Routes  int   `json:"routes"`
	Sources int   `json:"sources"`
	Dead    int64 `json:"dead"`
}

// Overview is returned by GET /api/overview.
type Overview struct {
	Info
	Queue    QueueStats `json:"queue"`
	Tenants  int        `json:"tenants"`
	Activity bool       `json:"activity"`
	// Events is null when the event ingress is disabled.
	Events *EventsOverview `json:"events"`
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	st, err := s.d.Queue.Stats(r.Context())
	if err != nil {
		s.unavailable(w, r, "read queue stats", err)
		return
	}
	out := Overview{
		Info:     s.d.Info,
		Queue:    QueueStats{Ready: st.Ready, Pending: st.Pending, Delayed: st.Delayed, Dead: st.Dead},
		Tenants:  s.d.Tenants.Len(),
		Activity: s.d.Activity != nil,
	}
	if ev := s.d.Events; ev != nil {
		dead, err := ev.Dead.Len(r.Context())
		if err != nil {
			s.unavailable(w, r, "count dead events", err)
			return
		}
		out.Events = &EventsOverview{Routes: len(ev.Routes.Router.Routes()), Sources: len(ev.Routes.NATS), Dead: dead}
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------- Activity ----------

// ActivityResponse is returned by GET /api/activity.
type ActivityResponse struct {
	Enabled bool             `json:"enabled"`
	Entries []activity.Entry `json:"entries"`
	Next    string           `json:"next,omitempty"`
}

var states = []status.State{status.Queued, status.Retrying, status.Sent, status.Failed}

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	if s.d.Activity == nil {
		writeJSON(w, http.StatusOK, ActivityResponse{Entries: []activity.Entry{}})
		return
	}
	q := r.URL.Query()
	state := status.State(q.Get("state"))
	if state != "" && !slices.Contains(states, state) {
		writeProblem(w, http.StatusBadRequest, "invalid-filter", "unknown state "+string(state))
		return
	}
	limit, ok := pageSize(w, q.Get("limit"))
	if !ok {
		return
	}
	page, err := s.d.Activity.List(r.Context(), activity.Query{
		Tenant: q.Get("tenant"), State: state, EmailID: q.Get("email"), Before: q.Get("before"), Limit: limit,
	})
	if err != nil {
		s.unavailable(w, r, "read activity", err)
		return
	}
	writeJSON(w, http.StatusOK, ActivityResponse{Enabled: true, Entries: page.Entries, Next: page.Next})
}

// EmailResponse is returned by GET /api/emails/{id}.
type EmailResponse struct {
	Tenant string         `json:"tenant,omitempty"`
	Status *status.Status `json:"status"`
	// Activity lists the known status changes of the e-mail, newest first.
	Activity []activity.Entry `json:"activity"`
}

func (s *Server) handleEmail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	st, err := s.d.Statuses.Get(r.Context(), id)
	if err != nil {
		s.unavailable(w, r, "read status", err)
		return
	}
	out := EmailResponse{Status: st, Activity: []activity.Entry{}}
	if st != nil {
		out.Tenant = st.TenantID
	}
	if s.d.Activity != nil {
		page, err := s.d.Activity.List(r.Context(), activity.Query{EmailID: id, Limit: defaultPageSize})
		if err != nil {
			s.unavailable(w, r, "read activity", err)
			return
		}
		out.Activity = page.Entries
		if out.Tenant == "" && len(page.Entries) > 0 {
			out.Tenant = page.Entries[0].Tenant
		}
	}
	if st == nil && len(out.Activity) == 0 {
		writeProblem(w, http.StatusNotFound, "not-found", "no e-mail "+id+" (unknown, or its status expired)")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------- E-mail dead-letter queue ----------

// DeadJob is an entry of the e-mail dead-letter queue.
type DeadJob struct {
	ID       string   `json:"id"`
	EmailID  string   `json:"email_id,omitempty"`
	Tenant   string   `json:"tenant,omitempty"`
	Template string   `json:"template,omitempty"`
	To       []string `json:"to,omitempty"`
	Attempts int      `json:"attempts"`
	Reason   string   `json:"reason"`
	FailedAt string   `json:"failed_at"`
	// Readable is false when the job cannot be decoded: it cannot be
	// replayed, only deleted.
	Readable bool `json:"readable"`
}

// DLQResponse is returned by GET /api/dlq.
type DLQResponse struct {
	Entries []DeadJob `json:"entries"`
	Next    string    `json:"next,omitempty"`
}

func (s *Server) handleDLQ(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageSize(w, r.URL.Query().Get("limit"))
	if !ok {
		return
	}
	jobs, err := s.d.Queue.ListDeadAfter(r.Context(), r.URL.Query().Get("after"), int64(limit))
	if err != nil {
		s.unavailable(w, r, "list the dead-letter queue", err)
		return
	}
	out := DLQResponse{Entries: make([]DeadJob, 0, len(jobs))}
	for _, dj := range jobs {
		out.Entries = append(out.Entries, deadJobView(dj))
	}
	if len(jobs) == limit {
		out.Next = jobs[len(jobs)-1].ID
	}
	writeJSON(w, http.StatusOK, out)
}

func deadJobView(dj queue.DeadJob) DeadJob {
	v := DeadJob{ID: dj.ID, Reason: dj.Reason, FailedAt: dj.FailedAt, Readable: dj.Job != nil}
	if j := dj.Job; j != nil {
		v.EmailID, v.Tenant, v.Attempts = j.ID, j.TenantID, j.Attempts
		v.To = observability.MaskAddresses(j.Request.To)
		if j.Request.Template != nil {
			v.Template = j.Request.Template.Name
		}
	}
	return v
}

// IDsRequest is the body of the actions on dead letters.
type IDsRequest struct {
	IDs []string `json:"ids"`
}

func readIDs(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	var req IDsRequest
	if !decode(w, r, &req) {
		return nil, false
	}
	if len(req.IDs) == 0 || len(req.IDs) > maxIDs {
		writeProblem(w, http.StatusBadRequest, "invalid-ids", "ids must hold between 1 and "+strconv.Itoa(maxIDs)+" entries")
		return nil, false
	}
	return req.IDs, true
}

// CountResponse reports how many entries an action affected.
type CountResponse struct {
	Count int64 `json:"count"`
}

func (s *Server) handleDLQReplay(w http.ResponseWriter, r *http.Request) {
	ids, ok := readIDs(w, r)
	if !ok {
		return
	}
	jobs, err := s.d.Queue.DeadJobs(r.Context(), ids...)
	if err != nil {
		s.unavailable(w, r, "read the dead-letter queue", err)
		return
	}
	n, err := s.d.Queue.ReplayDead(r.Context(), jobs)
	s.audit(r, "replayed dead-letter jobs", "requested", len(ids), "replayed", n)
	if err != nil {
		s.unavailable(w, r, "replay the dead-letter queue", err)
		return
	}
	writeJSON(w, http.StatusOK, CountResponse{Count: int64(n)})
}

func (s *Server) handleDLQDelete(w http.ResponseWriter, r *http.Request) {
	ids, ok := readIDs(w, r)
	if !ok {
		return
	}
	n, err := s.d.Queue.DeleteDead(r.Context(), ids...)
	if err != nil {
		s.unavailable(w, r, "delete dead-letter jobs", err)
		return
	}
	s.audit(r, "deleted dead-letter jobs", "requested", len(ids), "deleted", n)
	writeJSON(w, http.StatusOK, CountResponse{Count: n})
}

// ---------- Dead events ----------

// DeadEvent is an event kept by the event ingress (ADR 015).
type DeadEvent struct {
	ID         string           `json:"id"`
	Tenant     string           `json:"tenant"`
	Source     string           `json:"source"`
	Reason     deadevent.Reason `json:"reason"`
	Detail     string           `json:"detail,omitempty"`
	Routes     []string         `json:"routes,omitempty"`
	FailedAt   time.Time        `json:"failed_at"`
	Replayable bool             `json:"replayable"`
	EventType  string           `json:"event_type,omitempty"`
	EventID    string           `json:"event_id,omitempty"`
	// Event is the CloudEvent in structured mode (GET /api/dead-events/{id}
	// only), or Payload when it is not valid JSON.
	Event   json.RawMessage `json:"event,omitempty"`
	Payload string          `json:"payload,omitempty"`
}

// DeadEventsResponse is returned by GET /api/dead-events.
type DeadEventsResponse struct {
	Enabled bool        `json:"enabled"`
	Entries []DeadEvent `json:"entries"`
	Next    string      `json:"next,omitempty"`
}

func deadEventView(e deadevent.Entry, withEvent bool) DeadEvent {
	v := DeadEvent{
		ID: e.ID, Tenant: e.Tenant, Source: e.Source, Reason: e.Reason, Detail: e.Detail,
		Routes: e.Routes, FailedAt: e.FailedAt, Replayable: e.Replayable(),
	}
	if ce, err := cloudevent.ParseStructured(e.Event); err == nil {
		v.EventType, v.EventID = ce.Type, ce.ID
	}
	if withEvent {
		if json.Valid(e.Event) {
			v.Event = json.RawMessage(e.Event)
		} else {
			v.Payload = string(e.Event)
		}
	}
	return v
}

func (s *Server) handleDeadEvents(w http.ResponseWriter, r *http.Request) {
	ev := s.d.Events
	if ev == nil {
		writeJSON(w, http.StatusOK, DeadEventsResponse{Entries: []DeadEvent{}})
		return
	}
	limit, ok := pageSize(w, r.URL.Query().Get("limit"))
	if !ok {
		return
	}
	entries, err := ev.Dead.ListAfter(r.Context(), r.URL.Query().Get("after"), int64(limit))
	if err != nil {
		s.unavailable(w, r, "list dead events", err)
		return
	}
	out := DeadEventsResponse{Enabled: true, Entries: make([]DeadEvent, 0, len(entries))}
	for _, e := range entries {
		out.Entries = append(out.Entries, deadEventView(e, false))
	}
	if len(entries) == limit {
		out.Next = entries[len(entries)-1].ID
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeadEvent(w http.ResponseWriter, r *http.Request) {
	if s.d.Events == nil {
		writeProblem(w, http.StatusNotFound, "events-disabled", "the event ingress is disabled")
		return
	}
	entries, err := s.d.Events.Dead.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.unavailable(w, r, "read dead event", err)
		return
	}
	if len(entries) == 0 {
		writeProblem(w, http.StatusNotFound, "not-found", "no dead event "+r.PathValue("id"))
		return
	}
	writeJSON(w, http.StatusOK, deadEventView(entries[0], true))
}

// ReplayResponse is returned by POST /api/dead-events/replay.
type ReplayResponse struct {
	Results []dispatch.ReplayResult `json:"results"`
	// Interrupted explains why the replay stopped early, if it did.
	Interrupted string `json:"interrupted,omitempty"`
}

func (s *Server) handleDeadEventsReplay(w http.ResponseWriter, r *http.Request) {
	ev := s.d.Events
	if ev == nil {
		writeProblem(w, http.StatusNotFound, "events-disabled", "the event ingress is disabled")
		return
	}
	ids, ok := readIDs(w, r)
	if !ok {
		return
	}
	entries, err := ev.Dead.Get(r.Context(), ids...)
	if err != nil {
		s.unavailable(w, r, "read dead events", err)
		return
	}
	results, err := ev.Dispatcher.ReplayDead(r.Context(), ev.Dead, s.d.Tenants, entries)
	out := ReplayResponse{Results: results}
	if errors.Is(err, dispatch.ErrReplayInterrupted) {
		out.Interrupted = err.Error()
	} else if err != nil {
		s.unavailable(w, r, "replay dead events", err)
		return
	}
	s.audit(r, "replayed dead events", "requested", len(ids), "processed", len(results), "interrupted", out.Interrupted != "")
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeadEventsDelete(w http.ResponseWriter, r *http.Request) {
	ev := s.d.Events
	if ev == nil {
		writeProblem(w, http.StatusNotFound, "events-disabled", "the event ingress is disabled")
		return
	}
	ids, ok := readIDs(w, r)
	if !ok {
		return
	}
	if err := ev.Dead.Delete(r.Context(), ids...); err != nil {
		s.unavailable(w, r, "delete dead events", err)
		return
	}
	s.audit(r, "deleted dead events", "count", len(ids))
	writeJSON(w, http.StatusOK, CountResponse{Count: int64(len(ids))})
}

// ---------- Tenants, templates and routes ----------

// TenantKey describes an API key without its digest.
type TenantKey struct {
	Scopes []string `json:"scopes"`
}

// Tenant is a tenant as shown in the console.
type Tenant struct {
	ID             string       `json:"id"`
	DefaultSender  mail.Address `json:"default_sender"`
	AllowedSenders []string     `json:"allowed_senders"`
	APIKeys        []TenantKey  `json:"api_keys"`
}

func (s *Server) handleTenants(w http.ResponseWriter, _ *http.Request) {
	all := s.d.Tenants.All()
	out := make([]Tenant, 0, len(all))
	for _, t := range all {
		v := Tenant{ID: t.ID, DefaultSender: t.DefaultSender, AllowedSenders: t.AllowedSenders, APIKeys: []TenantKey{}}
		for _, k := range t.APIKeys {
			scopes := k.Scopes
			if len(scopes) == 0 {
				// Keys without scopes keep every scope (ADR 015).
				scopes = []string{tenant.ScopeEmails, tenant.ScopeEvents}
			}
			v.APIKeys = append(v.APIKeys, TenantKey{Scopes: scopes})
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

// TenantTemplates lists the templates of a tenant.
type TenantTemplates struct {
	Tenant    string           `json:"tenant"`
	Templates []templates.Info `json:"templates"`
	// Error is set when the storage could not be listed for this tenant.
	Error string `json:"error,omitempty"`
}

func (s *Server) handleTemplates(w http.ResponseWriter, r *http.Request) {
	ids := []string{}
	if one := r.URL.Query().Get("tenant"); one != "" {
		if _, ok := s.d.Tenants.Get(one); !ok {
			writeProblem(w, http.StatusNotFound, "not-found", "no tenant "+one)
			return
		}
		ids = append(ids, one)
	} else {
		for _, t := range s.d.Tenants.All() {
			ids = append(ids, t.ID)
		}
	}
	out := make([]TenantTemplates, 0, len(ids))
	for _, id := range ids {
		v := TenantTemplates{Tenant: id, Templates: []templates.Info{}}
		infos, err := s.d.Templates.List(r.Context(), id)
		if err != nil {
			s.d.Logger.WarnContext(r.Context(), "console: list templates", "tenant", id, "error", err)
			v.Error = "the template storage could not be listed"
		} else {
			v.Templates = infos
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

// PreviewResponse is a rendered template.
type PreviewResponse struct {
	Locale  string `json:"locale"`
	Subject string `json:"subject"`
	HTML    string `json:"html,omitempty"`
	Text    string `json:"text,omitempty"`
}

// render renders the template designated by the request, writing the problem
// itself on failure. Data comes as a JSON object in the "data" parameter, so
// that the HTML preview can be loaded by an iframe with a plain GET.
func (s *Server) render(w http.ResponseWriter, r *http.Request) (*templates.Rendered, bool) {
	tenantID, name := r.PathValue("tenant"), r.PathValue("name")
	if _, ok := s.d.Tenants.Get(tenantID); !ok || !mail.NamePattern.MatchString(name) {
		writeProblem(w, http.StatusNotFound, "template-not-found", "no template "+name+" for tenant "+tenantID)
		return nil, false
	}
	q := r.URL.Query()
	locale := q.Get("locale")
	if locale != "" && !mail.LocalePattern.MatchString(locale) {
		writeProblem(w, http.StatusBadRequest, "invalid-locale", "locale must be a language tag such as fr or en-GB")
		return nil, false
	}
	var data map[string]any
	if raw := q.Get("data"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &data); err != nil {
			writeProblem(w, http.StatusBadRequest, "invalid-data", "data must be a JSON object")
			return nil, false
		}
	}
	out, err := s.d.Templates.Render(r.Context(), tenantID, mail.TemplateRef{Name: name, Locale: locale, Data: data})
	switch {
	case errors.Is(err, templates.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "template-not-found", "no template "+name+" for tenant "+tenantID)
	case errors.Is(err, templates.ErrInvalid):
		writeProblem(w, http.StatusUnprocessableEntity, "template-invalid", err.Error())
	case err != nil:
		s.unavailable(w, r, "render template", err)
	default:
		return out, true
	}
	return nil, false
}

func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	out, ok := s.render(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, PreviewResponse{Locale: out.Locale, Subject: out.Subject, HTML: out.HTML, Text: out.Text})
}

// handlePreviewHTML serves the rendered HTML for a sandboxed iframe of the
// console: no script can run, and the template's inline styles and remote
// images are allowed in this response only.
func (s *Server) handlePreviewHTML(w http.ResponseWriter, r *http.Request) {
	out, ok := s.render(w, r)
	if !ok {
		return
	}
	h := w.Header()
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'; "+
		"img-src https: data:; font-src https: data:; frame-ancestors 'self'")
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("X-Kourrier-Subject", mime.QEncoding.Encode("utf-8", out.Subject))
	body := out.HTML
	if body == "" {
		body = "<!doctype html><meta charset=utf-8><pre style=\"white-space:pre-wrap;font:14px/1.5 monospace\">" +
			html.EscapeString(out.Text) + "</pre>"
	}
	_, _ = io.WriteString(w, body)
}

// NATSSource describes a NATS source of the routes file.
type NATSSource struct {
	Name           string   `json:"name"`
	Tenant         string   `json:"tenant"`
	Stream         string   `json:"stream"`
	Consumer       string   `json:"consumer"`
	FilterSubjects []string `json:"filter_subjects,omitempty"`
	Bind           bool     `json:"bind"`
	OnUnrouted     string   `json:"on_unrouted,omitempty"`
	DeliverPolicy  string   `json:"deliver_policy,omitempty"`
}

// RoutesResponse is returned by GET /api/routes.
type RoutesResponse struct {
	Enabled bool           `json:"enabled"`
	Routes  []routing.Info `json:"routes"`
	Sources []NATSSource   `json:"sources"`
}

func (s *Server) handleRoutes(w http.ResponseWriter, _ *http.Request) {
	out := RoutesResponse{Routes: []routing.Info{}, Sources: []NATSSource{}}
	if ev := s.d.Events; ev != nil {
		out.Enabled = true
		for _, rt := range ev.Routes.Router.Routes() {
			out.Routes = append(out.Routes, rt.Info())
		}
		for _, src := range ev.Routes.NATS {
			out.Sources = append(out.Sources, NATSSource{
				Name: src.Name, Tenant: src.Tenant, Stream: src.Stream, Consumer: src.Consumer,
				FilterSubjects: src.FilterSubjects, Bind: src.Bind, OnUnrouted: src.OnUnrouted, DeliverPolicy: src.DeliverPolicy,
			})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// pageSize parses the "limit" parameter.
func pageSize(w http.ResponseWriter, raw string) (int, bool) {
	if raw == "" {
		return defaultPageSize, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > maxPageSize {
		writeProblem(w, http.StatusBadRequest, "invalid-limit", "limit must be between 1 and "+strconv.Itoa(maxPageSize))
		return 0, false
	}
	return n, true
}
