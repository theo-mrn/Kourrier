package console

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/theo-mrn/kourrier/internal/mail"
)

func TestSendPrintsMessage(t *testing.T) {
	var buf bytes.Buffer
	_, err := New(&buf).Send(context.Background(), &mail.Message{
		ID:      "abc",
		From:    mail.Address{Email: "no-reply@yner.fr"},
		To:      []mail.Address{{Email: "jane@example.com", Name: "Jane"}},
		Subject: "Hello",
		Text:    "line 1\nline 2",
	})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !utf8.ValidString(out) {
		t.Fatalf("output is not valid UTF-8:\n%s", out)
	}
	for _, want := range []string{"abc", "no-reply@yner.fr", `"Jane" <jane@example.com>`, "Subject:  Hello", "│ line 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}
