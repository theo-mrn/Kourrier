package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestSendRejectsUnknownOrInvalidTemplates(t *testing.T) {
	f := newFixture(t)
	tests := map[string]struct {
		template string
		kind     string
	}{
		"unknown template":             {`{"name":"welcom"}`, "template-not-found"},
		"template of another tenant":   {`{"name":"nothing-here"}`, "template-not-found"},
		"template that does not parse": {`{"name":"broken","locale":"fr"}`, "template-invalid"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			rec := f.do("POST", "/v1/emails", ynerKey, `{"to":[{"email":"a@b.fr"}],"template":`+tt.template+`}`)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
			}
			if p := decode[Problem](t, rec); !strings.HasSuffix(p.Type, "#"+tt.kind) || p.Detail == "" {
				t.Fatalf("unexpected problem %+v", p)
			}
		})
	}
	if s, _ := f.queue.Stats(context.Background()); s.Ready != 0 {
		t.Fatalf("rejected requests must not be enqueued, stats = %+v", s)
	}
}

func TestSendAcceptsMissingLocaleWithDefault(t *testing.T) {
	f := newFixture(t)
	// "de" does not exist, the default locale "en" does: the worker will use it.
	rec := f.do("POST", "/v1/emails", ynerKey, `{"to":[{"email":"a@b.fr"}],"template":{"name":"welcome","locale":"de"}}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
}

func TestSendAcceptsWhenStorageIsDown(t *testing.T) {
	f := newFixtureWithStore(t, downStore{})
	rec := f.do("POST", "/v1/emails", ynerKey, validBody)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("a storage outage must not block sending, status = %d, body = %s", rec.Code, rec.Body)
	}
}

func TestListTemplates(t *testing.T) {
	f := newFixture(t)
	rec := f.do("GET", "/v1/templates", ynerKey, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	got := decode[TemplatesResponse](t, rec).Templates
	names := make([]string, len(got))
	for i, info := range got {
		names[i] = info.Name
	}
	if strings.Join(names, ",") != "broken,receipt,welcome" || strings.Join(got[2].Locales, ",") != "en,fr" {
		t.Fatalf("unexpected templates %+v", got)
	}

	other := decode[TemplatesResponse](t, f.do("GET", "/v1/templates", otherKey, "")).Templates
	if len(other) != 1 || other[0].Name != "welcome" || other[0].Locales[0] != "en" {
		t.Fatalf("tenants must only see their own templates, got %+v", other)
	}
	if rec := f.do("GET", "/v1/templates", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("listing requires an API key, status = %d", rec.Code)
	}
	if rec := newFixtureWithStore(t, downStore{}).do("GET", "/v1/templates", ynerKey, ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("storage outage: status = %d", rec.Code)
	}
}

func TestPreview(t *testing.T) {
	f := newFixture(t)
	rec := f.do("POST", "/v1/templates/welcome/preview", ynerKey, `{"locale":"fr","data":{"name":"<Jane>"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	got := decode[PreviewResponse](t, rec)
	want := PreviewResponse{Locale: "fr", Subject: "Bienvenue <Jane>", HTML: "<p>Bonjour &lt;Jane&gt;</p>", Text: "Bonjour <Jane>"}
	if got != want {
		t.Fatalf("preview = %+v, want %+v", got, want)
	}

	// Unknown locale: rendered with the default one, which the response tells.
	if got := decode[PreviewResponse](t, f.do("POST", "/v1/templates/welcome/preview", ynerKey, `{"locale":"de"}`)); got.Locale != "en" {
		t.Fatalf("fallback locale = %q", got.Locale)
	}
}

func TestPreviewAsHTML(t *testing.T) {
	f := newFixture(t)
	rec := f.do("POST", "/v1/templates/welcome/preview", ynerKey, `{"locale":"fr","data":{"name":"Jane"}}`, "Accept", "text/html")
	if rec.Code != http.StatusOK || rec.Body.String() != "<p>Bonjour Jane</p>" {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("content type = %q", ct)
	}
	if rec.Header().Get("Content-Security-Policy") != "sandbox" {
		t.Fatal("the HTML preview must be sandboxed")
	}
}

func TestPreviewErrors(t *testing.T) {
	f := newFixture(t)
	tests := map[string]struct {
		path, body string
		code       int
		kind       string
	}{
		"unknown template":        {"/v1/templates/nope/preview", `{}`, http.StatusNotFound, "template-not-found"},
		"invalid name":            {"/v1/templates/..%2Fx/preview", `{}`, http.StatusNotFound, "template-not-found"},
		"invalid locale":          {"/v1/templates/welcome/preview", `{"locale":"../x"}`, http.StatusUnprocessableEntity, "validation"},
		"unknown field":           {"/v1/templates/welcome/preview", `{"lang":"fr"}`, http.StatusBadRequest, "malformed"},
		"template does not parse": {"/v1/templates/broken/preview", `{"locale":"fr"}`, http.StatusUnprocessableEntity, "template-invalid"},
		"data does not fit":       {"/v1/templates/receipt/preview", `{"locale":"en"}`, http.StatusUnprocessableEntity, "template-invalid"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			rec := f.do("POST", tt.path, ynerKey, tt.body)
			if rec.Code != tt.code {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tt.code, rec.Body)
			}
			if p := decode[Problem](t, rec); !strings.HasSuffix(p.Type, "#"+tt.kind) {
				t.Fatalf("problem type = %s, want #%s", p.Type, tt.kind)
			}
		})
	}
	if rec := newFixtureWithStore(t, downStore{}).do("POST", "/v1/templates/welcome/preview", ynerKey, `{}`); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("storage outage: status = %d", rec.Code)
	}
}

// downStore simulates an unreachable object storage.
type downStore struct{}

var errDown = errors.New("connection refused")

func (downStore) Get(context.Context, string) ([]byte, error)    { return nil, errDown }
func (downStore) List(context.Context, string) ([]string, error) { return nil, errDown }
func (downStore) Ping(context.Context) error                     { return errDown }
