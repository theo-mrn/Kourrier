package templates

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/theo-mrn/kourrier/internal/mail"
	"github.com/theo-mrn/kourrier/internal/retry"
	"github.com/theo-mrn/kourrier/internal/storage"
)

func newStore() *storage.Memory {
	return storage.NewMemory(map[string]string{
		"yner/_layout.html":            `{{define "layout"}}<html><body>{{template "content" .}}<footer>© {{year}}</footer></body></html>{{end}}`,
		"yner/welcome/fr/subject.txt":  `Bienvenue {{.name}} !`,
		"yner/welcome/fr/body.html":    `{{template "layout" .}}{{define "content"}}<p>Bonjour {{.name}}</p>{{end}}`,
		"yner/welcome/fr/body.txt":     `Bonjour {{.name | default "à vous"}}`,
		"yner/welcome/en/subject.txt":  `Welcome {{.name}}!`,
		"yner/welcome/en/body.txt":     `Hello {{.name}}`,
		"yner/broken/en/subject.txt":   `{{.name`,
		"yner/no-body/en/subject.txt":  `Hi`,
		"other/welcome/en/subject.txt": `Other tenant`,
		"other/welcome/en/body.txt":    `Other tenant`,
	})
}

func TestRender(t *testing.T) {
	r := NewRenderer(newStore(), Options{CacheTTL: time.Minute, DefaultLocale: "en"})
	out, err := r.Render(context.Background(), "yner", mail.TemplateRef{
		Name: "welcome", Locale: "fr", Data: map[string]any{"name": "<Jane>"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Subject != "Bienvenue <Jane> !" {
		t.Errorf("subject = %q", out.Subject)
	}
	if !strings.Contains(out.HTML, "<p>Bonjour &lt;Jane&gt;</p>") || !strings.Contains(out.HTML, "<footer>") {
		t.Errorf("html should use the layout and escape data, got %q", out.HTML)
	}
	if out.Text != "Bonjour <Jane>" || out.Locale != "fr" {
		t.Errorf("text = %q, locale = %q", out.Text, out.Locale)
	}
}

func TestRenderFallsBackToDefaultLocale(t *testing.T) {
	r := NewRenderer(newStore(), Options{DefaultLocale: "en"})
	out, err := r.Render(context.Background(), "yner", mail.TemplateRef{Name: "welcome", Locale: "de", Data: map[string]any{"name": "Jane"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Locale != "en" || out.Subject != "Welcome Jane!" || out.HTML != "" {
		t.Errorf("unexpected fallback rendering: %+v", out)
	}
}

func TestRenderFoldsSubjectLineBreaks(t *testing.T) {
	r := NewRenderer(newStore(), Options{DefaultLocale: "en"})
	out, err := r.Render(context.Background(), "yner", mail.TemplateRef{
		Name: "welcome", Data: map[string]any{"name": "Jane\r\nBcc: evil@example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(out.Subject, "\r\n") {
		t.Fatalf("subject contains a line break: %q", out.Subject)
	}
}

func TestRenderErrorsArePermanent(t *testing.T) {
	r := NewRenderer(newStore(), Options{DefaultLocale: "en"})
	for _, ref := range []mail.TemplateRef{
		{Name: "missing"},
		{Name: "broken"},
		{Name: "no-body"},
		{Name: "../other"},
	} {
		_, err := r.Render(context.Background(), "yner", ref)
		if err == nil || !retry.IsPermanent(err) {
			t.Errorf("Render(%q) = %v, want a permanent error", ref.Name, err)
		}
	}
	_, err := r.Render(context.Background(), "yner", mail.TemplateRef{Name: "missing"})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("missing template error should wrap ErrNotFound, got %v", err)
	}
}

func TestStorageErrorsAreTransient(t *testing.T) {
	r := NewRenderer(failingStore{}, Options{DefaultLocale: "en"})
	_, err := r.Render(context.Background(), "yner", mail.TemplateRef{Name: "welcome"})
	if err == nil || retry.IsPermanent(err) {
		t.Fatalf("storage outage should be transient, got %v", err)
	}
}

func TestCacheTTL(t *testing.T) {
	store := newStore()
	r := NewRenderer(store, Options{CacheTTL: time.Minute, DefaultLocale: "en"})
	now := time.Now()
	r.now = func() time.Time { return now }
	ref := mail.TemplateRef{Name: "welcome", Locale: "en", Data: map[string]any{"name": "Jane"}}

	render := func() string {
		t.Helper()
		out, err := r.Render(context.Background(), "yner", ref)
		if err != nil {
			t.Fatal(err)
		}
		return out.Subject
	}
	if got := render(); got != "Welcome Jane!" {
		t.Fatalf("got %q", got)
	}
	store.Put("yner/welcome/en/subject.txt", "Hey {{.name}}")
	if got := render(); got != "Welcome Jane!" {
		t.Fatalf("cached template expected, got %q", got)
	}
	now = now.Add(2 * time.Minute)
	if got := render(); got != "Hey Jane" {
		t.Fatalf("template should be reloaded after the TTL, got %q", got)
	}
}

type failingStore struct{}

func (failingStore) Get(context.Context, string) ([]byte, error) {
	return nil, errors.New("connection refused")
}
func (failingStore) Ping(context.Context) error { return nil }
