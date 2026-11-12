package console

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/theo-mrn/kourrier/internal/activity"
	"github.com/theo-mrn/kourrier/internal/deadevent"
	"github.com/theo-mrn/kourrier/internal/dispatch"
	"github.com/theo-mrn/kourrier/internal/idempotency"
	"github.com/theo-mrn/kourrier/internal/mail"
	"github.com/theo-mrn/kourrier/internal/observability"
	"github.com/theo-mrn/kourrier/internal/queue"
	"github.com/theo-mrn/kourrier/internal/routing"
	"github.com/theo-mrn/kourrier/internal/status"
	"github.com/theo-mrn/kourrier/internal/storage"
	"github.com/theo-mrn/kourrier/internal/submit"
	"github.com/theo-mrn/kourrier/internal/templates"
	"github.com/theo-mrn/kourrier/internal/tenant"
)

const (
	adminToken = "console-test-token-0123456789abcdef"
	shopKey    = "kr_shop-test-key"
)

const testRoutes = `
apiVersion: kourrier/v1alpha1
sources:
  nats:
    - { name: orders, tenant: shop, stream: ORDERS, consumer: kourrier }
routes:
  - name: welcome
    tenant: shop
    match: { types: [user.created], when: data.verified }
    email: { template: welcome, to: data.email }
`

type fixture struct {
	srv      *Server
	handler  http.Handler
	queue    *queue.RedisStreams
	statuses *status.Store
	activity *activity.Store
	dead     *deadevent.Store
	cookie   *http.Cookie
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	metrics := observability.NewMetrics("test")

	shop := &tenant.Tenant{
		ID:             "shop",
		APIKeys:        []tenant.APIKey{{SHA256: tenant.HashKey(shopKey)}, {SHA256: tenant.HashKey("kr_events"), Scopes: []string{tenant.ScopeEvents}}},
		DefaultSender:  mail.Address{Email: "no-reply@shop.fr"},
		AllowedSenders: []string{"no-reply@shop.fr"},
	}
	tenants, err := tenant.NewRegistry(shop)
	if err != nil {
		t.Fatal(err)
	}
	q, err := queue.NewRedisStreams(context.Background(), rdb, queue.RedisConfig{Consumer: "test", Block: 10 * time.Millisecond}, logger)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		queue:    q,
		statuses: status.NewStore(rdb, "kourrier", time.Hour),
		activity: activity.NewStore(rdb, "kourrier", 100),
		dead:     deadevent.NewStore(rdb, "kourrier", 100),
	}
	renderer := templates.NewRenderer(storage.NewMemory(map[string]string{
		"shop/welcome/en/subject.txt": "Welcome {{.name}}",
		"shop/welcome/en/body.html":   `<p style="color:red">Hi {{.name}}</p>`,
		"shop/receipt/fr/subject.txt": "Reçu",
		"shop/receipt/fr/body.txt":    "Total : {{.total}}",
		"shop/broken/en/subject.txt":  "{{.oops",
	}), templates.Options{DefaultLocale: "en"})
	routes, err := routing.Parse([]byte(testRoutes), tenants)
	if err != nil {
		t.Fatal(err)
	}
	submitter := submit.New(submit.Deps{
		Queue: q, Idempotency: idempotency.NewStore("kourrier", time.Hour), Statuses: f.statuses,
		Activity: f.activity, Metrics: metrics, Logger: logger,
	})
	dispatcher := dispatch.New(dispatch.Deps{
		Router: routes.Router, Submitter: submitter, DeadLetters: f.dead, Metrics: metrics, Logger: logger,
	})

	f.srv, err = New(Deps{
		Token: adminToken, SessionTTL: time.Hour, SecureCookie: true,
		Info:    Info{Version: "test", Mode: "all", Mailer: "console", Storage: "filesystem"},
		Tenants: tenants, Queue: q, Statuses: f.statuses, Activity: f.activity, Templates: renderer,
		Events: &Events{Routes: routes, Dead: f.dead, Dispatcher: dispatcher},
		Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.handler = f.srv.Handler()
	return f
}

// do sends a request as the console would, with the session cookie once
// signed in and the CSRF header on writes.
func (f *fixture) do(t *testing.T, method, target, body string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if f.cookie != nil {
		req.AddCookie(f.cookie)
	}
	if method != http.MethodGet {
		req.Header.Set(csrfHeader, "1")
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func (f *fixture) login(t *testing.T) {
	t.Helper()
	rec := f.do(t, "POST", "/api/session", `{"token":"`+adminToken+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			f.cookie = c
		}
	}
	if f.cookie == nil {
		t.Fatal("no session cookie")
	}
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func TestLoadToken(t *testing.T) {
	dir := t.TempDir()
	short := filepath.Join(dir, "short")
	_ = os.WriteFile(short, []byte("too-short\n"), 0o600)
	if _, err := LoadToken(short); err == nil {
		t.Fatal("a short token must be refused")
	}
	good := filepath.Join(dir, "good")
	_ = os.WriteFile(good, []byte(adminToken+"\n"), 0o600)
	if tok, err := LoadToken(good); err != nil || tok != adminToken {
		t.Fatalf("LoadToken() = %q, %v", tok, err)
	}
	if _, err := New(Deps{Token: "short"}); err == nil {
		t.Fatal("New must refuse a short token")
	}
}

func TestAuthentication(t *testing.T) {
	f := newFixture(t)
	if rec := f.do(t, "GET", "/api/overview", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous overview: %d", rec.Code)
	}
	if rec := f.do(t, "POST", "/api/session", `{"token":"wrong-token-wrong-token-wrong-token"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", rec.Code)
	}

	f.login(t)
	c := f.cookie
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.MaxAge != 3600 {
		t.Fatalf("insecure session cookie: %+v", c)
	}
	if rec := f.do(t, "GET", "/api/session", ""); rec.Code != http.StatusOK {
		t.Fatalf("session: %d", rec.Code)
	}

	// A forged or tampered cookie is refused.
	forged := *c
	forged.Value = strings.Replace(c.Value, ".", ".9", 1)
	f.cookie = &forged
	if rec := f.do(t, "GET", "/api/overview", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("forged cookie: %d", rec.Code)
	}

	// Expired sessions are refused.
	f.cookie = c
	f.srv.sessions.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if rec := f.do(t, "GET", "/api/overview", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired session: %d", rec.Code)
	}
	f.srv.sessions.now = time.Now

	// Signing out clears the cookie.
	rec := f.do(t, "DELETE", "/api/session", "")
	if rec.Code != http.StatusNoContent || !strings.Contains(rec.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Fatalf("logout: %d %v", rec.Code, rec.Header())
	}

	// Another token signs other sessions: rotating it revokes them.
	other, _ := New(Deps{Token: strings.Repeat("z", MinTokenLength), Logger: f.srv.d.Logger})
	req := httptest.NewRequest(http.MethodGet, "/api/overview", nil)
	req.AddCookie(c)
	if _, err := other.sessions.verify(req); err == nil {
		t.Fatal("a session must not survive a token rotation")
	}
}

func TestCrossOriginWritesAreRefused(t *testing.T) {
	f := newFixture(t)
	body := `{"token":"` + adminToken + `"}`
	send := func(headers ...string) int {
		req := httptest.NewRequest(http.MethodPost, "http://console.local/api/session", strings.NewReader(body))
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, req)
		return rec.Code
	}
	for name, headers := range map[string][]string{
		"no CSRF header": nil,
		"other origin":   {csrfHeader, "1", "Origin", "https://evil.example"},
		"cross site":     {csrfHeader, "1", "Sec-Fetch-Site", "cross-site"},
	} {
		if code := send(headers...); code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, code)
		}
	}
	if code := send(csrfHeader, "1", "Origin", "http://console.local", "Sec-Fetch-Site", "same-origin"); code != http.StatusOK {
		t.Fatalf("same origin: %d", code)
	}
}

func TestSecurityHeadersAndSPA(t *testing.T) {
	f := newFixture(t)
	rec := f.do(t, "GET", "/dlq", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("SPA route: %d %s", rec.Code, rec.Body)
	}
	h := rec.Header()
	if !strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") || h.Get("X-Frame-Options") != "DENY" ||
		h.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("missing security headers: %v", h)
	}
	if rec := f.do(t, "GET", "/api/nope", ""); rec.Code != http.StatusNotFound || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unknown API path: %d %v", rec.Code, rec.Header())
	}
}

func TestSPAServesBuildFiles(t *testing.T) {
	h := spa(fstest.MapFS{
		"dist/index.html":     {Data: []byte("<html>app</html>")},
		"dist/assets/app.js":  {Data: []byte("console.log(1)")},
		"dist/kourrier.svg":   {Data: []byte("<svg/>")},
		"dist/assets/sub/x.c": {Data: []byte("x")},
	})
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	if rec := get("/assets/app.js"); rec.Body.String() != "console.log(1)" || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset: %q %v", rec.Body, rec.Header())
	}
	if rec := get("/templates/shop"); rec.Body.String() != "<html>app</html>" || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("client route: %q", rec.Body)
	}
	if rec := get("/assets"); rec.Body.String() != "<html>app</html>" {
		t.Fatalf("directory: %q", rec.Body)
	}
}

func TestOverviewAndActivity(t *testing.T) {
	f := newFixture(t)
	f.login(t)
	ctx := context.Background()
	_ = f.activity.Record(ctx, activity.Entry{EmailID: "e1", Tenant: "shop", State: status.Queued})
	_ = f.activity.Record(ctx, activity.Entry{EmailID: "e1", Tenant: "shop", State: status.Sent, Attempt: 1})
	_ = f.statuses.Put(ctx, &status.Status{ID: "e1", TenantID: "shop", State: status.Sent, Attempts: 1})

	ov := decodeBody[Overview](t, f.do(t, "GET", "/api/overview", ""))
	if ov.Version != "test" || ov.Tenants != 1 || !ov.Activity || ov.Events == nil || ov.Events.Routes != 1 || ov.Events.Sources != 1 {
		t.Fatalf("overview = %+v", ov)
	}

	act := decodeBody[ActivityResponse](t, f.do(t, "GET", "/api/activity?state=sent", ""))
	if !act.Enabled || len(act.Entries) != 1 || act.Entries[0].EmailID != "e1" {
		t.Fatalf("activity = %+v", act)
	}
	if rec := f.do(t, "GET", "/api/activity?state=lost", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown state: %d", rec.Code)
	}
	if rec := f.do(t, "GET", "/api/activity?limit=100000", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("huge limit: %d", rec.Code)
	}

	em := decodeBody[EmailResponse](t, f.do(t, "GET", "/api/emails/e1", ""))
	if em.Tenant != "shop" || em.Status == nil || em.Status.State != status.Sent || len(em.Activity) != 2 {
		t.Fatalf("email = %+v", em)
	}
	if rec := f.do(t, "GET", "/api/emails/unknown", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown e-mail: %d", rec.Code)
	}
}

func TestDeadLetterQueue(t *testing.T) {
	f := newFixture(t)
	f.login(t)
	ctx := context.Background()
	for _, id := range []string{"j1", "j2"} {
		job := &queue.Job{ID: id, TenantID: "shop", Request: mail.Request{
			To: []mail.Address{{Email: "jane@example.com"}}, Template: &mail.TemplateRef{Name: "welcome"},
		}}
		_ = f.queue.Enqueue(ctx, job)
		d, err := f.queue.Receive(ctx)
		if err != nil || d == nil {
			t.Fatalf("receive: %v", err)
		}
		_ = f.queue.DeadLetter(ctx, d, "550 rejected")
	}

	dlq := decodeBody[DLQResponse](t, f.do(t, "GET", "/api/dlq?limit=1", ""))
	if len(dlq.Entries) != 1 || dlq.Next == "" {
		t.Fatalf("first page = %+v", dlq)
	}
	e := dlq.Entries[0]
	if e.EmailID != "j1" || e.Template != "welcome" || e.Reason != "550 rejected" || !e.Readable ||
		len(e.To) != 1 || e.To[0] == "jane@example.com" {
		t.Fatalf("entry = %+v (recipients must be masked)", e)
	}
	second := decodeBody[DLQResponse](t, f.do(t, "GET", "/api/dlq?limit=1&after="+url.QueryEscape(dlq.Next), ""))
	if len(second.Entries) != 1 || second.Entries[0].EmailID != "j2" {
		t.Fatalf("second page = %+v", second)
	}

	if r := decodeBody[CountResponse](t, f.do(t, "POST", "/api/dlq/replay", `{"ids":["`+e.ID+`"]}`)); r.Count != 1 {
		t.Fatalf("replay = %+v", r)
	}
	if r := decodeBody[CountResponse](t, f.do(t, "POST", "/api/dlq/delete", `{"ids":["`+second.Entries[0].ID+`"]}`)); r.Count != 1 {
		t.Fatalf("delete = %+v", r)
	}
	if st, _ := f.queue.Stats(ctx); st.Dead != 0 || st.Ready != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if rec := f.do(t, "POST", "/api/dlq/delete", `{"ids":[]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty ids: %d", rec.Code)
	}
}

func TestDeadEvents(t *testing.T) {
	f := newFixture(t)
	f.login(t)
	ctx := context.Background()
	event := `{"specversion":"1.0","id":"evt-1","source":"/users","type":"user.created","data":{"verified":true,"email":"jane@example.com"}}`
	id, _ := f.dead.Add(ctx, deadevent.Entry{Tenant: "shop", Source: "orders", Reason: deadevent.Rejected, Detail: "boom", Event: []byte(event)})
	bad, _ := f.dead.Add(ctx, deadevent.Entry{Tenant: "shop", Source: "orders", Reason: deadevent.Invalid, Event: []byte("garbage")})

	list := decodeBody[DeadEventsResponse](t, f.do(t, "GET", "/api/dead-events", ""))
	if !list.Enabled || len(list.Entries) != 2 || list.Entries[0].EventType != "user.created" || list.Entries[0].Event != nil {
		t.Fatalf("list = %+v", list)
	}
	one := decodeBody[DeadEvent](t, f.do(t, "GET", "/api/dead-events/"+id, ""))
	if one.EventID != "evt-1" || len(one.Event) == 0 {
		t.Fatalf("entry = %+v", one)
	}
	if raw := decodeBody[DeadEvent](t, f.do(t, "GET", "/api/dead-events/"+bad, "")); raw.Payload != "garbage" || raw.Replayable {
		t.Fatalf("invalid entry = %+v", raw)
	}

	rep := decodeBody[ReplayResponse](t, f.do(t, "POST", "/api/dead-events/replay", `{"ids":["`+id+`","`+bad+`"]}`))
	if len(rep.Results) != 2 || rep.Results[0].Outcome != dispatch.Accepted || rep.Results[0].Emails != 1 || rep.Results[1].Skipped == "" {
		t.Fatalf("replay = %+v", rep)
	}
	if n, _ := f.dead.Len(ctx); n != 1 {
		t.Fatalf("%d dead events left, want 1 (the skipped one)", n)
	}
	if r := decodeBody[CountResponse](t, f.do(t, "POST", "/api/dead-events/delete", `{"ids":["`+bad+`"]}`)); r.Count != 1 {
		t.Fatalf("delete = %+v", r)
	}
}

func TestTenantsNeverExposeKeyDigests(t *testing.T) {
	f := newFixture(t)
	f.login(t)
	rec := f.do(t, "GET", "/api/tenants", "")
	if strings.Contains(rec.Body.String(), tenant.HashKey(shopKey)) {
		t.Fatal("the console must never return API key digests")
	}
	ts := decodeBody[[]Tenant](t, rec)
	if len(ts) != 1 || len(ts[0].APIKeys) != 2 || len(ts[0].APIKeys[0].Scopes) != 2 || ts[0].APIKeys[1].Scopes[0] != tenant.ScopeEvents {
		t.Fatalf("tenants = %+v", ts)
	}
}

func TestTemplatesAndPreview(t *testing.T) {
	f := newFixture(t)
	f.login(t)
	list := decodeBody[[]TenantTemplates](t, f.do(t, "GET", "/api/templates", ""))
	if len(list) != 1 || len(list[0].Templates) != 3 {
		t.Fatalf("templates = %+v", list)
	}

	data := url.QueryEscape(`{"name":"Jane"}`)
	p := decodeBody[PreviewResponse](t, f.do(t, "GET", "/api/templates/shop/welcome/preview?data="+data, ""))
	if p.Subject != "Welcome Jane" || p.Locale != "en" || !strings.Contains(p.HTML, "Hi Jane") {
		t.Fatalf("preview = %+v", p)
	}

	rec := f.do(t, "GET", "/api/templates/shop/welcome/preview.html?data="+data, "")
	csp := rec.Header().Get("Content-Security-Policy")
	if rec.Code != http.StatusOK || !strings.HasPrefix(csp, "sandbox;") || strings.Contains(csp, "script-src") ||
		rec.Header().Get("X-Frame-Options") != "SAMEORIGIN" || !strings.Contains(rec.Body.String(), "Hi Jane") {
		t.Fatalf("HTML preview: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	// A text-only template is shown escaped.
	if rec := f.do(t, "GET", "/api/templates/shop/receipt/preview.html?locale=fr&data="+url.QueryEscape(`{"total":"<b>3</b>"}`), ""); !strings.Contains(rec.Body.String(), "&lt;b&gt;3") {
		t.Fatalf("text preview: %s", rec.Body)
	}

	for path, code := range map[string]int{
		"/api/templates/shop/missing/preview":          http.StatusNotFound,
		"/api/templates/nobody/welcome/preview":        http.StatusNotFound,
		"/api/templates/shop/broken/preview":           http.StatusUnprocessableEntity,
		"/api/templates/shop/welcome/preview?data=[1]": http.StatusBadRequest,
		"/api/templates/shop/welcome/preview?locale=x": http.StatusBadRequest,
	} {
		if rec := f.do(t, "GET", path, ""); rec.Code != code {
			t.Errorf("%s: %d, want %d", path, rec.Code, code)
		}
	}
}

func TestRoutes(t *testing.T) {
	f := newFixture(t)
	f.login(t)
	r := decodeBody[RoutesResponse](t, f.do(t, "GET", "/api/routes", ""))
	if !r.Enabled || len(r.Routes) != 1 || r.Routes[0].When != "data.verified" || len(r.Sources) != 1 || r.Sources[0].Stream != "ORDERS" {
		t.Fatalf("routes = %+v", r)
	}

	f.srv.d.Events = nil
	r = decodeBody[RoutesResponse](t, f.do(t, "GET", "/api/routes", ""))
	if r.Enabled || len(r.Routes) != 0 {
		t.Fatalf("routes without ingress = %+v", r)
	}
	if ev := decodeBody[DeadEventsResponse](t, f.do(t, "GET", "/api/dead-events", "")); ev.Enabled {
		t.Fatalf("dead events without ingress = %+v", ev)
	}
}

func TestStats(t *testing.T) {
	f := newFixture(t)
	f.login(t)
	ctx := context.Background()
	_ = f.activity.Record(ctx, activity.Entry{EmailID: "e1", Tenant: "shop", Template: "welcome", State: status.Queued})
	_ = f.activity.Record(ctx, activity.Entry{EmailID: "e1", Tenant: "shop", Template: "welcome", State: status.Sent})

	st := decodeBody[StatsResponse](t, f.do(t, "GET", "/api/stats?range=24h", ""))
	if !st.Enabled || st.Range != "24h" || len(st.Buckets) != 48 || st.Totals.Sent != 1 || st.Latency == nil {
		t.Fatalf("stats = %+v", st)
	}
	if one := decodeBody[StatsResponse](t, f.do(t, "GET", "/api/stats", "")); len(one.Buckets) != 60 {
		t.Fatalf("default range has %d buckets", len(one.Buckets))
	}
	if rec := f.do(t, "GET", "/api/stats?range=7d", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown range: %d", rec.Code)
	}
	f.srv.d.Activity = nil
	if off := decodeBody[StatsResponse](t, f.do(t, "GET", "/api/stats", "")); off.Enabled {
		t.Fatalf("stats without the activity feed = %+v", off)
	}
}
