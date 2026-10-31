package routing

import (
	"errors"
	"strings"
	"testing"

	"github.com/theo-mrn/kourrier/internal/cloudevent"
	"github.com/theo-mrn/kourrier/internal/mail"
	"github.com/theo-mrn/kourrier/internal/tenant"
)

func testTenants(t *testing.T) *tenant.Registry {
	t.Helper()
	r, err := tenant.NewRegistry(
		&tenant.Tenant{
			ID:             "shop",
			APIKeys:        []tenant.APIKey{{SHA256: tenant.HashKey("kr_shop")}},
			DefaultSender:  mail.Address{Email: "no-reply@shop.fr"},
			AllowedSenders: []string{"no-reply@shop.fr", "*@news.shop.fr"},
		},
		&tenant.Tenant{
			ID:             "nosender",
			APIKeys:        []tenant.APIKey{{SHA256: tenant.HashKey("kr_nosender")}},
			AllowedSenders: []string{"*@nosender.fr"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

const validFile = `
apiVersion: kourrier/v1alpha1
sources:
  nats:
    - name: shop-orders
      tenant: shop
      stream: ORDERS
      consumer: kourrier
      filter_subjects: ["orders.>"]
routes:
  - name: order-paid
    tenant: shop
    match:
      types: [com.shop.order.paid, com.shop.order.paid.v2]
      source: /shop/orders
      when: data.total > 0 && !data.customer.unsubscribed
    email:
      template: order-receipt
      to: '{"email": data.customer.email, "name": data.customer.first_name + " " + data.customer.last_name}'
      locale: data.customer.locale
      data: '{"order_id": event.subject, "total": data.total, "lines": data.lines.map(l, l.label)}'
      from: { email: billing@news.shop.fr, name: Shop }
      tags: { flow: billing }
  - name: order-paid-audit
    tenant: shop
    match:
      types: [com.shop.order.paid]
    email:
      template: audit
      to: '["audit@shop.fr", {"email": "finance@shop.fr"}]'
`

func mustParse(t *testing.T, src string) *Config {
	t.Helper()
	cfg, err := Parse([]byte(src), testTenants(t))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func event(t *testing.T, typ, source, data string) *cloudevent.Event {
	t.Helper()
	e, err := cloudevent.ParseStructured([]byte(`{"specversion":"1.0","id":"evt-1","source":"` + source +
		`","type":"` + typ + `","subject":"order-42","data":` + data + `}`))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

const paidData = `{"total": 42, "customer": {"email": "jane@example.com", "first_name": "Jane", "last_name": "Doe",
  "locale": "fr", "unsubscribed": false}, "lines": [{"label": "Book"}, {"label": "Pen"}]}`

func TestRouteBuildsRequests(t *testing.T) {
	cfg := mustParse(t, validFile)
	if len(cfg.NATS) != 1 || cfg.NATS[0].AckWait == 0 || cfg.NATS[0].Tenant != "shop" {
		t.Fatalf("unexpected sources %+v", cfg.NATS)
	}

	results, err := cfg.Router.Route("shop", event(t, "com.shop.order.paid", "/shop/orders", paidData))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2: %+v", len(results), results)
	}
	r := results[0]
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	req := r.Request
	if req.From.Email != "billing@news.shop.fr" || len(req.To) != 1 || req.To[0] != (mail.Address{Email: "jane@example.com", Name: "Jane Doe"}) {
		t.Fatalf("unexpected addresses: %+v", req)
	}
	if req.Template.Name != "order-receipt" || req.Template.Locale != "fr" {
		t.Fatalf("unexpected template %+v", req.Template)
	}
	d := req.Template.Data
	if d["order_id"] != "order-42" || d["total"] != int64(42) {
		t.Fatalf("unexpected data %#v", d)
	}
	if lines, _ := d["lines"].([]any); len(lines) != 2 || lines[0] != "Book" {
		t.Fatalf("unexpected lines %#v", d["lines"])
	}
	if req.Tags["flow"] != "billing" || req.Tags[TagRoute] != "order-paid" {
		t.Fatalf("unexpected tags %v", req.Tags)
	}

	audit := results[1].Request
	if audit == nil || len(audit.To) != 2 || audit.To[1].Email != "finance@shop.fr" || audit.From.Email != "" {
		t.Fatalf("unexpected audit request %+v (err %v)", audit, results[1].Err)
	}
	// Without email.data, the event payload is the template data.
	if audit.Template.Data["total"] != int64(42) {
		t.Fatalf("default data not applied: %#v", audit.Template.Data)
	}
}

func TestRouteFiltering(t *testing.T) {
	cfg := mustParse(t, validFile)
	tests := []struct {
		name, tenant, typ, source, data string
		want                            int
	}{
		{"other tenant", "nosender", "com.shop.order.paid", "/shop/orders", paidData, 0},
		{"unknown type", "shop", "com.shop.order.refunded", "/shop/orders", paidData, 0},
		{"other source skips the first route", "shop", "com.shop.order.paid", "/elsewhere", paidData, 1},
		{"predicate false", "shop", "com.shop.order.paid", "/shop/orders", strings.Replace(paidData, `"total": 42`, `"total": 0`, 1), 1},
		{"second type", "shop", "com.shop.order.paid.v2", "/shop/orders", paidData, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := cfg.Router.Route(tt.tenant, event(t, tt.typ, tt.source, tt.data))
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != tt.want {
				t.Fatalf("got %d results, want %d", len(results), tt.want)
			}
		})
	}
}

func TestRouteEvaluationErrors(t *testing.T) {
	cfg := mustParse(t, `
apiVersion: kourrier/v1alpha1
routes:
  - name: r
    tenant: shop
    match: { types: [t] }
    email: { template: x, to: data.email, locale: data.locale }
`)
	tests := []struct{ name, data, want string }{
		{"missing field", `{"locale": "fr"}`, "email.to"},
		{"no data", `null`, "email.to"},
		{"wrong recipient type", `{"email": 42}`, "email.to"},
		{"wrong locale type", `{"email": "a@b.fr", "locale": 1}`, "email.locale"},
		{"non-object default data", `["a@b.fr"]`, "email.to"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := cfg.Router.Route("shop", event(t, "t", "/s", tt.data))
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || !errors.Is(results[0].Err, ErrEvaluation) || !strings.Contains(results[0].Err.Error(), tt.want) {
				t.Fatalf("results = %+v, want an evaluation error on %s", results, tt.want)
			}
		})
	}
}

func TestDefaultDataMustBeAnObject(t *testing.T) {
	cfg := mustParse(t, `
apiVersion: kourrier/v1alpha1
routes:
  - name: r
    tenant: shop
    match: { types: [t] }
    email: { template: x, to: '"a@b.fr"' }
`)
	results, _ := cfg.Router.Route("shop", event(t, "t", "/s", `[1, 2]`))
	if len(results) != 1 || results[0].Err == nil || !strings.Contains(results[0].Err.Error(), "set email.data") {
		t.Fatalf("results = %+v", results)
	}
	results, _ = cfg.Router.Route("shop", event(t, "t", "/s", `null`))
	if len(results) != 1 || results[0].Err != nil || results[0].Request.Template.Data != nil {
		t.Fatalf("an event without data should render with no data: %+v", results)
	}
}

func TestOptionalFields(t *testing.T) {
	cfg := mustParse(t, `
apiVersion: kourrier/v1alpha1
routes:
  - name: r
    tenant: shop
    match: { types: [t] }
    email:
      template: x
      to: data.email
      locale: data.?locale
      data: '{"name": data.?name.orValue("there")}'
`)
	results, _ := cfg.Router.Route("shop", event(t, "t", "/s", `{"email": "a@b.fr"}`))
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("results = %+v", results)
	}
	ref := results[0].Request.Template
	if ref.Locale != "" || ref.Data["name"] != "there" {
		t.Fatalf("optional values not applied: %+v", ref)
	}
	results, _ = cfg.Router.Route("shop", event(t, "t", "/s", `{"email": "a@b.fr", "locale": "de"}`))
	if results[0].Request.Template.Locale != "de" {
		t.Fatalf("present optional value ignored: %+v", results[0].Request.Template)
	}
}

func TestCostLimit(t *testing.T) {
	cfg := mustParse(t, `
apiVersion: kourrier/v1alpha1
routes:
  - name: r
    tenant: shop
    match:
      types: [t]
      when: data.items.all(a, data.items.all(b, data.items.all(c, a + b + c >= 0)))
    email: { template: x, to: '"a@b.fr"' }
`)
	items := "[" + strings.Repeat("1,", 200) + "1]"
	results, err := cfg.Router.Route("shop", event(t, "t", "/s", `{"items": `+items+`}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Err == nil || !strings.Contains(results[0].Err.Error(), "cost") {
		t.Fatalf("expected the cost limit to stop the evaluation, got %+v", results)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{"no version", `routes: []`, "apiVersion"},
		{"unknown field", "apiVersion: kourrier/v1alpha1\nroutez: []", "routez"},
		{"bad route", `
apiVersion: kourrier/v1alpha1
routes:
  - name: Bad Name
    tenant: ghost
    match: { types: [] }
    email: { template: "../etc", tags: { kourrier_x: y, "bad key": v } }
`, "name: must match|unknown tenant|at least one event type|template: must match|email.to: is required|reserved|keys and values"},
		{"bad expressions", `
apiVersion: kourrier/v1alpha1
routes:
  - name: r
    tenant: shop
    match: { types: [t], when: '"yes"' }
    email: { template: x, to: 'data.', locale: '42', data: '[1]' }
`, "match.when: expression returns string|email.to: ERROR|email.locale: expression returns int|email.data: expression returns list"},
		{"sender", `
apiVersion: kourrier/v1alpha1
routes:
  - name: r
    tenant: shop
    match: { types: [t] }
    email: { template: x, to: data.email, from: { email: ceo@shop.fr } }
  - name: r
    tenant: nosender
    match: { types: [t] }
    email: { template: x, to: data.email }
`, "not in the allowed_senders|duplicate route name|has no default_sender"},
		{"bad source", `
apiVersion: kourrier/v1alpha1
sources:
  nats:
    - { name: s, tenant: shop, stream: "a.b", consumer: "", ack_wait: 10ms, bind: true, max_deliver: 3 }
`, "stream: must match|consumer: must match|ack_wait|managed outside"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.src), testTenants(t))
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, want := range strings.Split(tt.want, "|") {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error does not mention %q:\n%v", want, err)
				}
			}
		})
	}
}

func TestParseWithoutTenants(t *testing.T) {
	// Offline linting (kourrier routes check) cannot resolve tenants.
	if _, err := Parse([]byte(validFile), nil); err != nil {
		t.Fatal(err)
	}
}

// BenchmarkRoute measures the CPU cost of routing one event through a route
// with a predicate, a recipient object, a locale and computed template data.
func BenchmarkRoute(b *testing.B) {
	r, err := tenant.NewRegistry(&tenant.Tenant{
		ID: "shop", APIKeys: []tenant.APIKey{{SHA256: tenant.HashKey("k")}},
		DefaultSender: mail.Address{Email: "no-reply@shop.fr"}, AllowedSenders: []string{"no-reply@shop.fr", "*@news.shop.fr"},
	})
	if err != nil {
		b.Fatal(err)
	}
	cfg, err := Parse([]byte(validFile), r)
	if err != nil {
		b.Fatal(err)
	}
	e, err := cloudevent.ParseStructured([]byte(`{"specversion":"1.0","id":"evt-1","source":"/shop/orders",
		"type":"com.shop.order.paid","subject":"order-42","data":` + paidData + `}`))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := cfg.Router.Route("shop", e); err != nil {
			b.Fatal(err)
		}
	}
}
