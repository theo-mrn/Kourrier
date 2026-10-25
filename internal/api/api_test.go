package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/theo-mrn/kourrier/internal/idempotency"
	"github.com/theo-mrn/kourrier/internal/mail"
	"github.com/theo-mrn/kourrier/internal/observability"
	"github.com/theo-mrn/kourrier/internal/queue"
	"github.com/theo-mrn/kourrier/internal/status"
	"github.com/theo-mrn/kourrier/internal/storage"
	"github.com/theo-mrn/kourrier/internal/templates"
	"github.com/theo-mrn/kourrier/internal/tenant"
)

const (
	ynerKey  = "kr_yner-test-key"
	otherKey = "kr_other-test-key"
)

type fixture struct {
	handler http.Handler
	queue   *queue.RedisStreams
	mr      *miniredis.Miniredis
}

// testTemplates are the templates of the fixture's tenants.
func testTemplates() *storage.Memory {
	return storage.NewMemory(map[string]string{
		"yner/welcome/fr/subject.txt":  "Bienvenue {{.name}}",
		"yner/welcome/fr/body.html":    "<p>Bonjour {{.name}}</p>",
		"yner/welcome/fr/body.txt":     "Bonjour {{.name}}",
		"yner/welcome/en/subject.txt":  "Welcome {{.name}}",
		"yner/welcome/en/body.txt":     "Hello {{.name}}",
		"yner/broken/fr/subject.txt":   "{{.name",
		"yner/receipt/en/subject.txt":  `Receipt of {{date "02/01/2006" .paid_at}}`,
		"yner/receipt/en/body.txt":     "Total",
		"other/welcome/en/subject.txt": "Other tenant",
		"other/welcome/en/body.txt":    "Other tenant",
	})
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureWithStore(t, testTemplates())
}

func newFixtureWithStore(t *testing.T, store storage.Provider) *fixture {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	tenants, err := tenant.NewRegistry(
		&tenant.Tenant{
			ID:             "yner",
			APIKeys:        []tenant.APIKey{{SHA256: tenant.HashKey(ynerKey)}},
			DefaultSender:  mail.Address{Email: "no-reply@yner.fr", Name: "Yner"},
			AllowedSenders: []string{"no-reply@yner.fr", "*@news.yner.fr"},
		},
		&tenant.Tenant{
			ID:             "other",
			APIKeys:        []tenant.APIKey{{SHA256: tenant.HashKey(otherKey)}},
			AllowedSenders: []string{"*@other.com"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	q, err := queue.NewRedisStreams(context.Background(), rdb, queue.RedisConfig{Consumer: "test", Block: 10 * time.Millisecond}, logger)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Deps{
		Tenants:     tenants,
		Queue:       q,
		Idempotency: idempotency.NewStore(rdb, "kourrier", time.Hour),
		Statuses:    status.NewStore(rdb, "kourrier", time.Hour),
		Templates:   templates.NewRenderer(store, templates.Options{DefaultLocale: "en"}),
		Metrics:     observability.NewMetrics("test"),
		Logger:      logger,
	})
	return &fixture{handler: srv.Handler(), queue: q, mr: mr}
}

func (f *fixture) do(method, path, key, body string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

const validBody = `{"to":[{"email":"Jane@Example.com","name":"Jane"}],"template":{"name":"welcome","locale":"fr","data":{"name":"Jane"}}}`

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func TestSendAcceptsAndEnqueues(t *testing.T) {
	f := newFixture(t)
	rec := f.do("POST", "/v1/emails", ynerKey, validBody)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	resp := decode[SendResponse](t, rec)
	if resp.Status != status.Queued || rec.Header().Get("Location") != "/v1/emails/"+resp.ID {
		t.Fatalf("unexpected response %+v, headers %v", resp, rec.Header())
	}

	d, err := f.queue.Receive(context.Background())
	if err != nil || d == nil {
		t.Fatalf("job not enqueued: %v", err)
	}
	job := d.Job
	if job.ID != resp.ID || job.TenantID != "yner" {
		t.Fatalf("unexpected job %+v", job)
	}
	if job.Request.From.Email != "no-reply@yner.fr" || job.Request.To[0].Email != "jane@example.com" {
		t.Fatalf("default sender and normalization not applied: %+v", job.Request)
	}

	rec = f.do("GET", "/v1/emails/"+resp.ID, ynerKey, "")
	if rec.Code != http.StatusOK || decode[status.Status](t, rec).State != status.Queued {
		t.Fatalf("GET status = %d, body = %s", rec.Code, rec.Body)
	}
}

func TestAuthentication(t *testing.T) {
	f := newFixture(t)
	for _, key := range []string{"", "kr_wrong"} {
		rec := f.do("POST", "/v1/emails", key, validBody)
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("key %q: status = %d", key, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
			t.Errorf("errors must be problem+json, got %q", ct)
		}
	}
}

func TestSendRejectsInvalidRequests(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		name string
		body string
		code int
	}{
		{"malformed json", `{"to":`, http.StatusBadRequest},
		{"unknown field", `{"to":[{"email":"a@b.fr"}],"template":{"name":"x"},"bogus":1}`, http.StatusBadRequest},
		{"trailing data", validBody + `{}`, http.StatusBadRequest},
		{"invalid fields", `{"to":[{"email":"nope"}],"template":{"name":"../x"}}`, http.StatusUnprocessableEntity},
		{"sender not allowed", `{"from":{"email":"ceo@yner.fr"},"to":[{"email":"a@b.fr"}],"template":{"name":"x"}}`, http.StatusForbidden},
		{"too large", `{"to":[{"email":"a@b.fr"}],"content":{"subject":"x","text":"` + strings.Repeat("a", 2<<20) + `"}}`, http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.do("POST", "/v1/emails", ynerKey, tt.body)
			if rec.Code != tt.code {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tt.code, rec.Body)
			}
		})
	}

	rec := f.do("POST", "/v1/emails", ynerKey, `{"to":[{"email":"nope"}],"template":{"name":"x"}}`)
	p := decode[Problem](t, rec)
	if len(p.Errors) == 0 || p.Errors[0].Field != "to[0].email" || p.RequestID == "" {
		t.Fatalf("validation problem should list fields: %+v", p)
	}
}

func TestSendAllowsWildcardSender(t *testing.T) {
	f := newFixture(t)
	rec := f.do("POST", "/v1/emails", ynerKey, `{"from":{"email":"weekly@news.yner.fr"},"to":[{"email":"a@b.fr"}],"template":{"name":"welcome"}}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
}

func TestIdempotency(t *testing.T) {
	f := newFixture(t)
	first := f.do("POST", "/v1/emails", ynerKey, validBody, "Idempotency-Key", "order-42")
	second := f.do("POST", "/v1/emails", ynerKey, validBody, "Idempotency-Key", "order-42")
	if first.Code != http.StatusAccepted || second.Code != http.StatusAccepted {
		t.Fatalf("status = %d, %d", first.Code, second.Code)
	}
	if decode[SendResponse](t, first).ID != decode[SendResponse](t, second).ID {
		t.Fatal("replayed request must return the original id")
	}
	if second.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatal("missing Idempotent-Replayed header")
	}
	if s, _ := f.queue.Stats(context.Background()); s.Ready != 1 {
		t.Fatalf("job enqueued %d times", s.Ready)
	}

	other := `{"to":[{"email":"someone-else@example.com"}],"template":{"name":"welcome"}}`
	if rec := f.do("POST", "/v1/emails", ynerKey, other, "Idempotency-Key", "order-42"); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("key reuse with another payload: status = %d", rec.Code)
	}
	if rec := f.do("POST", "/v1/emails", ynerKey, validBody, "Idempotency-Key", "bad key"); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid key: status = %d", rec.Code)
	}
}

func TestStatusIsScopedToTenant(t *testing.T) {
	f := newFixture(t)
	id := decode[SendResponse](t, f.do("POST", "/v1/emails", ynerKey, validBody)).ID
	if rec := f.do("GET", "/v1/emails/"+id, otherKey, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("other tenant got status %d", rec.Code)
	}
	if rec := f.do("GET", "/v1/emails/not-a-uuid", ynerKey, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("invalid id: status = %d", rec.Code)
	}
}

func TestSendReturns503WhenRedisIsDown(t *testing.T) {
	f := newFixture(t)
	f.mr.Close()
	rec := f.do("POST", "/v1/emails", ynerKey, validBody, "Idempotency-Key", "k")
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
}

func TestUnknownRoute(t *testing.T) {
	f := newFixture(t)
	if rec := f.do("GET", "/nope", "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestAdminHandler(t *testing.T) {
	m := observability.NewMetrics("test")
	m.EmailsSent.WithLabelValues("yner", "console").Inc()
	h := AdminHandler(m, map[string]Check{"redis": func(context.Context) error { return nil }})

	for path, want := range map[string]int{"/livez": 200, "/readyz": 200, "/metrics": 200} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("%s: status = %d", path, rec.Code)
		}
		if path == "/metrics" && !strings.Contains(rec.Body.String(), `kourrier_emails_sent_total{provider="console",tenant="yner"} 1`) {
			t.Errorf("metrics output missing counter:\n%s", rec.Body)
		}
	}

	failing := AdminHandler(m, map[string]Check{"redis": func(context.Context) error { return io.ErrUnexpectedEOF }})
	rec := httptest.NewRecorder()
	failing.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz with failing check: status = %d", rec.Code)
	}
}
