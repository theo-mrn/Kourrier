package mail

import (
	"errors"
	"strings"
	"testing"
)

func validRequest() Request {
	return Request{
		From:     Address{Email: "no-reply@yner.fr", Name: "Yner"},
		To:       []Address{{Email: "jane@example.com"}},
		Template: &TemplateRef{Name: "welcome", Locale: "fr"},
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Request)
		field  string // expected invalid field, "" if valid
	}{
		{"valid template", func(*Request) {}, ""},
		{"valid content", func(r *Request) {
			r.Template = nil
			r.Content = &Content{Subject: "Hi", Text: "Hello"}
		}, ""},
		{"missing from", func(r *Request) { r.From = Address{} }, "from.email"},
		{"no recipients", func(r *Request) { r.To = nil }, "to"},
		{"bad recipient", func(r *Request) { r.To[0].Email = "not-an-email" }, "to[0].email"},
		{"recipient without tld", func(r *Request) { r.To[0].Email = "jane@localhost" }, "to[0].email"},
		{"display name in email field", func(r *Request) { r.To[0].Email = "Jane <jane@example.com>" }, "to[0].email"},
		{"too many recipients", func(r *Request) {
			for range MaxRecipients {
				r.Bcc = append(r.Bcc, Address{Email: "x@example.com"})
			}
		}, "to"},
		{"template and content", func(r *Request) { r.Content = &Content{Subject: "x", Text: "y"} }, "template"},
		{"neither template nor content", func(r *Request) { r.Template = nil }, "template"},
		{"path traversal in template name", func(r *Request) { r.Template.Name = "../secrets" }, "template.name"},
		{"bad locale", func(r *Request) { r.Template.Locale = "fr/../../x" }, "template.locale"},
		{"subject header injection", func(r *Request) {
			r.Template = nil
			r.Content = &Content{Subject: "Hi\r\nBcc: evil@example.com", Text: "x"}
		}, "content.subject"},
		{"content without body", func(r *Request) {
			r.Template = nil
			r.Content = &Content{Subject: "Hi"}
		}, "content"},
		{"forbidden header", func(r *Request) { r.Headers = map[string]string{"Bcc": "evil@example.com"} }, "headers.Bcc"},
		{"header injection", func(r *Request) { r.Headers = map[string]string{"X-Foo": "a\nBcc: b"} }, "headers.X-Foo"},
		{"allowed headers", func(r *Request) {
			r.Headers = map[string]string{"X-Campaign": "spring", "List-Unsubscribe": "<mailto:u@yner.fr>"}
		}, ""},
		{"bad tag", func(r *Request) { r.Tags = map[string]string{"campaign": "spring sale"} }, "tags.campaign"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validRequest()
			tt.mutate(&r)
			err := r.Validate()
			if tt.field == "" {
				if err != nil {
					t.Fatalf("expected valid request, got %v", err)
				}
				return
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("expected *ValidationError, got %v", err)
			}
			for _, fe := range ve.Errors {
				if fe.Field == tt.field {
					return
				}
			}
			t.Fatalf("expected error on field %q, got %v", tt.field, err)
		})
	}
}

func TestNormalize(t *testing.T) {
	r := Request{From: Address{Email: " No-Reply@Yner.FR "}, To: []Address{{Email: "Jane@Example.com", Name: " Jane "}}}
	r.Normalize()
	if r.From.Email != "no-reply@yner.fr" || r.To[0].Email != "jane@example.com" || r.To[0].Name != "Jane" {
		t.Fatalf("unexpected normalization: %+v", r)
	}
}

func TestAddressString(t *testing.T) {
	if got := (Address{Email: "a@b.fr"}).String(); got != "a@b.fr" {
		t.Errorf("got %q", got)
	}
	if got := (Address{Email: "a@b.fr", Name: "Théo"}).String(); !strings.HasPrefix(got, "=?utf-8?") {
		t.Errorf("non-ASCII display name should be RFC 2047 encoded, got %q", got)
	}
}
