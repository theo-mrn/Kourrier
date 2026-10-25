package mail

import (
	"fmt"
	netmail "net/mail"
	"regexp"
	"strings"
)

// Limits enforced on incoming requests. MaxRecipients matches the AWS SES
// per-message limit so that a request accepted by the API is always sendable.
const (
	MaxRecipients   = 50
	MaxTags         = 10
	MaxHeaders      = 20
	MaxSubjectBytes = 998
)

var (
	// NamePattern is the format of template names, tenant IDs and locales
	// segments: it doubles as a path-traversal guard for the storage layer.
	NamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	// LocalePattern is the format of template locales: "fr", "en-GB"…
	LocalePattern = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})?$`)
	tagPattern    = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	headerPattern = regexp.MustCompile(`^X-[A-Za-z0-9-]{1,64}$`)
)

// allowedHeaders lists the non "X-" headers clients may set.
var allowedHeaders = map[string]bool{
	"List-Unsubscribe":      true,
	"List-Unsubscribe-Post": true,
	"List-Id":               true,
}

// FieldError describes why a single field of a request is invalid.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ValidationError aggregates every problem found in a request.
type ValidationError struct {
	Errors []FieldError
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Errors))
	for i, fe := range e.Errors {
		parts[i] = fe.Field + ": " + fe.Message
	}
	return "invalid request: " + strings.Join(parts, "; ")
}

func (e *ValidationError) add(field, format string, args ...any) {
	e.Errors = append(e.Errors, FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
}

// Normalize trims and lower-cases the address fields in place.
func (r *Request) Normalize() {
	norm := func(list []Address) {
		for i := range list {
			list[i].Email = strings.ToLower(strings.TrimSpace(list[i].Email))
			list[i].Name = strings.TrimSpace(list[i].Name)
		}
	}
	norm(r.To)
	norm(r.Cc)
	norm(r.Bcc)
	norm(r.ReplyTo)
	r.From.Email = strings.ToLower(strings.TrimSpace(r.From.Email))
	r.From.Name = strings.TrimSpace(r.From.Name)
}

// Validate checks the request and returns a *ValidationError listing every
// invalid field, or nil.
func (r *Request) Validate() error {
	v := &ValidationError{}

	validateAddress(v, "from", r.From)
	if len(r.To) == 0 {
		v.add("to", "at least one recipient is required")
	}
	if n := len(r.To) + len(r.Cc) + len(r.Bcc); n > MaxRecipients {
		v.add("to", "too many recipients (%d > %d)", n, MaxRecipients)
	}
	for field, list := range map[string][]Address{"to": r.To, "cc": r.Cc, "bcc": r.Bcc, "reply_to": r.ReplyTo} {
		for i, a := range list {
			validateAddress(v, fmt.Sprintf("%s[%d]", field, i), a)
		}
	}

	switch {
	case r.Template == nil && r.Content == nil:
		v.add("template", "either template or content is required")
	case r.Template != nil && r.Content != nil:
		v.add("template", "template and content are mutually exclusive")
	case r.Template != nil:
		if !NamePattern.MatchString(r.Template.Name) {
			v.add("template.name", "must match %s", NamePattern)
		}
		if r.Template.Locale != "" && !LocalePattern.MatchString(r.Template.Locale) {
			v.add("template.locale", "must be a language tag such as \"fr\" or \"en-GB\"")
		}
	case r.Content != nil:
		if strings.TrimSpace(r.Content.Subject) == "" {
			v.add("content.subject", "is required")
		}
		if len(r.Content.Subject) > MaxSubjectBytes || strings.ContainsAny(r.Content.Subject, "\r\n") {
			v.add("content.subject", "must be a single line of at most %d bytes", MaxSubjectBytes)
		}
		if r.Content.HTML == "" && r.Content.Text == "" {
			v.add("content", "html or text is required")
		}
	}

	if len(r.Headers) > MaxHeaders {
		v.add("headers", "at most %d headers are allowed", MaxHeaders)
	}
	for k, val := range r.Headers {
		if !headerPattern.MatchString(k) && !allowedHeaders[k] {
			v.add("headers."+k, "only X-* headers and List-* unsubscribe headers are allowed")
		}
		if strings.ContainsAny(val, "\r\n") {
			v.add("headers."+k, "must not contain line breaks")
		}
	}

	if len(r.Tags) > MaxTags {
		v.add("tags", "at most %d tags are allowed", MaxTags)
	}
	for k, val := range r.Tags {
		if !tagPattern.MatchString(k) || !tagPattern.MatchString(val) {
			v.add("tags."+k, "keys and values must match %s", tagPattern)
		}
	}

	if len(v.Errors) > 0 {
		return v
	}
	return nil
}

func validateAddress(v *ValidationError, field string, a Address) {
	if a.Email == "" {
		v.add(field+".email", "is required")
		return
	}
	parsed, err := netmail.ParseAddress(a.Email)
	if err != nil || parsed.Address != a.Email || !strings.Contains(a.Domain(), ".") {
		v.add(field+".email", "is not a valid e-mail address")
	}
	if strings.ContainsAny(a.Name, "\r\n") {
		v.add(field+".name", "must not contain line breaks")
	}
}
