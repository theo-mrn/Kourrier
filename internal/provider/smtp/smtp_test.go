package smtp

import (
	"bytes"
	"strings"
	"testing"

	"github.com/theo-mrn/kourrier/internal/mail"
)

func TestBuildMessage(t *testing.T) {
	p, err := New(Config{Host: "localhost", Port: 1025})
	if err != nil {
		t.Fatal(err)
	}
	m, err := p.build(&mail.Message{
		ID:      "abc",
		From:    mail.Address{Email: "no-reply@yner.fr", Name: "Yner"},
		To:      []mail.Address{{Email: "jane@example.com"}},
		ReplyTo: []mail.Address{{Email: "support@yner.fr"}},
		Subject: "Bienvenue à vous",
		HTML:    "<p>Hi</p>",
		Text:    "Hi",
		Headers: map[string]string{"X-Campaign": "spring"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := m.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	raw := buf.String()
	for _, want := range []string{
		"<abc@localhost>", "Reply-To: <support@yner.fr>", "X-Campaign: spring",
		"multipart/alternative", "text/plain", "text/html", "=?UTF-8?",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("message missing %q:\n%s", want, raw)
		}
	}
}

func TestNewRejectsUnknownTLSMode(t *testing.T) {
	if _, err := New(Config{Host: "localhost", TLS: "maybe"}); err == nil {
		t.Fatal("expected an error")
	}
}
