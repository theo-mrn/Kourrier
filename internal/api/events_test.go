package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/theo-mrn/kourrier/internal/dispatch"
)

const eventRoutes = `
apiVersion: kourrier/v1alpha1
routes:
  - name: welcome
    tenant: yner
    match: { types: [com.yner.user.created] }
    email: { template: welcome, to: data.email, locale: 'data.?locale.orValue("")' }
`

const structuredEvent = `{"specversion":"1.0","id":"evt-1","source":"/users","type":"com.yner.user.created",
  "data":{"email":"jane@example.com","locale":"fr"}}`

func TestEventStructuredMode(t *testing.T) {
	f := newFixtureWithRoutes(t, eventRoutes)
	rec := f.do("POST", "/v1/events", ynerKey, structuredEvent, "Content-Type", "application/cloudevents+json")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	resp := decode[EventResponse](t, rec)
	if resp.Outcome != dispatch.Accepted || len(resp.Emails) != 1 || resp.Emails[0].Route != "welcome" {
		t.Fatalf("unexpected response %+v", resp)
	}

	d, err := f.queue.Receive(context.Background())
	if err != nil || d == nil {
		t.Fatalf("job not enqueued: %v", err)
	}
	if d.Job.ID != resp.Emails[0].ID || d.Job.TenantID != "yner" || d.Job.Request.Template.Locale != "fr" {
		t.Fatalf("unexpected job %+v", d.Job)
	}

	// The broker redelivers: same e-mail, nothing new queued.
	rec = f.do("POST", "/v1/events", ynerKey, structuredEvent, "Content-Type", "application/cloudevents+json")
	again := decode[EventResponse](t, rec)
	if rec.Code != http.StatusAccepted || !again.Emails[0].Replayed || again.Emails[0].ID != resp.Emails[0].ID {
		t.Fatalf("redelivery: %d %+v", rec.Code, again)
	}
}

func TestEventBinaryMode(t *testing.T) {
	f := newFixtureWithRoutes(t, eventRoutes)
	rec := f.do("POST", "/v1/events", ynerKey, `{"email":"jane@example.com"}`,
		"Ce-Specversion", "1.0", "Ce-Id", "evt-2", "Ce-Source", "/users", "Ce-Type", "com.yner.user.created")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
}

func TestEventResponses(t *testing.T) {
	f := newFixtureWithRoutes(t, eventRoutes)
	ce := "application/cloudevents+json"
	tests := []struct {
		name, key, body, contentType string
		want                         int
		wantInBody                   string
	}{
		{"unauthenticated", "", structuredEvent, ce, http.StatusUnauthorized, ""},
		{"routes are scoped to the tenant", otherKey, structuredEvent, ce, http.StatusAccepted, `"outcome":"ignored"`},
		{"unrouted type", ynerKey, strings.Replace(structuredEvent, "user.created", "user.deleted", 1), ce, http.StatusAccepted, `"emails":[]`},
		{"plain json", ynerKey, `{"email":"a@b.fr"}`, "application/json", http.StatusUnsupportedMediaType, "not-a-cloudevent"},
		{"batch", ynerKey, `[]`, "application/cloudevents-batch+json", http.StatusUnsupportedMediaType, "batch"},
		{"malformed", ynerKey, `{"specversion":"1.0"}`, ce, http.StatusBadRequest, "malformed-cloudevent"},
		{"too large", ynerKey, strings.Repeat("a", 65<<10), ce, http.StatusRequestEntityTooLarge, ""},
		{"rejected and kept", ynerKey, strings.Replace(structuredEvent, "jane@example.com", "nope", 1), ce, http.StatusUnprocessableEntity, `"dead_letter_id":"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.do("POST", "/v1/events", tt.key, tt.body, "Content-Type", tt.contentType)
			if rec.Code != tt.want || !strings.Contains(rec.Body.String(), tt.wantInBody) {
				t.Fatalf("status = %d, body = %s; want %d containing %q", rec.Code, rec.Body, tt.want, tt.wantInBody)
			}
		})
	}
}

func TestEventEndpointDisabledWithoutRoutes(t *testing.T) {
	f := newFixture(t)
	rec := f.do("POST", "/v1/events", ynerKey, structuredEvent, "Content-Type", "application/cloudevents+json")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestEventRetryOnBackendFailure(t *testing.T) {
	f := newFixtureWithRoutes(t, eventRoutes)
	f.mr.Close()
	rec := f.do("POST", "/v1/events", ynerKey, structuredEvent, "Content-Type", "application/cloudevents+json")
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("status = %d, headers = %v", rec.Code, rec.Header())
	}
}

func TestEventTenantRateLimit(t *testing.T) {
	f := newFixtureWithRoutes(t, eventRoutes+`
limits:
  tenants:
    yner: { emails_per_second: 0.001, burst: 1 }
`)
	ce := "application/cloudevents+json"
	if rec := f.do("POST", "/v1/events", ynerKey, structuredEvent, "Content-Type", ce); rec.Code != http.StatusAccepted {
		t.Fatalf("first event: %d %s", rec.Code, rec.Body)
	}
	rec := f.do("POST", "/v1/events", ynerKey, strings.Replace(structuredEvent, "evt-1", "evt-2", 1), "Content-Type", ce)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("second event: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
}

func TestScopedKeys(t *testing.T) {
	f := newFixtureWithRoutes(t, eventRoutes)
	ce := "application/cloudevents+json"
	// eventsKey only grants the events scope (see newFixtureWithRoutes).
	if rec := f.do("POST", "/v1/events", eventsKey, structuredEvent, "Content-Type", ce); rec.Code != http.StatusAccepted {
		t.Fatalf("events key on /v1/events: %d %s", rec.Code, rec.Body)
	}
	rec := f.do("POST", "/v1/emails", eventsKey, validBody)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "insufficient-scope") {
		t.Fatalf("events key on /v1/emails: %d %s", rec.Code, rec.Body)
	}
	// Reading the templates is allowed with any scope: routes choose them too.
	if rec := f.do("GET", "/v1/templates", eventsKey, ""); rec.Code != http.StatusOK {
		t.Fatalf("events key on /v1/templates: %d %s", rec.Code, rec.Body)
	}
}
