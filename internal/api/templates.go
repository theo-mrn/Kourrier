package api

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/theo-mrn/kourrier/internal/mail"
	"github.com/theo-mrn/kourrier/internal/templates"
)

// Templates gives the API read access to the templates of a tenant (ADR 011).
type Templates interface {
	Check(ctx context.Context, tenant, name, locale string) error
	List(ctx context.Context, tenant string) ([]templates.Info, error)
	Render(ctx context.Context, tenant string, ref mail.TemplateRef) (*templates.Rendered, error)
}

// templateCheckTimeout bounds the storage round-trip added to POST /v1/emails
// when the template is not cached yet.
const templateCheckTimeout = 2 * time.Second

// checkTemplate rejects a request whose template does not exist or does not
// compile. When the storage cannot be read, the request is accepted anyway:
// the worker retries, and a storage outage must not stop e-mails from being
// queued.
func (s *Server) checkTemplate(ctx context.Context, tenant string, ref *mail.TemplateRef) *Problem {
	ctx, cancel := context.WithTimeout(ctx, templateCheckTimeout)
	defer cancel()
	err := s.templates.Check(ctx, tenant, ref.Name, ref.Locale)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, templates.ErrNotFound):
		return templateNotFound(ref.Name, http.StatusUnprocessableEntity)
	case errors.Is(err, templates.ErrInvalid):
		return templateInvalid(err)
	default:
		s.logger.WarnContext(ctx, "template not checked: storage unavailable",
			"tenant", tenant, "template", ref.Name, "error", err)
		return nil
	}
}

// TemplatesResponse is returned by GET /v1/templates.
type TemplatesResponse struct {
	Templates []templates.Info `json:"templates"`
}

func (s *Server) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	infos, err := s.templates.List(r.Context(), tenantFrom(r.Context()).ID)
	if err != nil {
		s.unavailable(w, r, "list templates", err)
		return
	}
	writeJSON(w, http.StatusOK, TemplatesResponse{Templates: infos})
}

// PreviewRequest is the body of POST /v1/templates/{name}/preview.
type PreviewRequest struct {
	Locale string         `json:"locale,omitempty"`
	Data   map[string]any `json:"data,omitempty"`
}

// PreviewResponse is a rendered template. Locale is the locale actually used,
// which differs from the requested one when the default locale was used.
type PreviewResponse struct {
	Locale  string `json:"locale"`
	Subject string `json:"subject"`
	HTML    string `json:"html,omitempty"`
	Text    string `json:"text,omitempty"`
}

func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !mail.NamePattern.MatchString(name) {
		writeProblemDetails(w, r, templateNotFound(name, http.StatusNotFound))
		return
	}
	var req PreviewRequest
	if p := s.decode(w, r, &req); p != nil {
		writeProblemDetails(w, r, p)
		return
	}
	if req.Locale != "" && !mail.LocalePattern.MatchString(req.Locale) {
		writeProblemDetails(w, r, &Problem{
			Type: problemBase + "validation", Title: "Invalid request", Status: http.StatusUnprocessableEntity,
			Errors: []mail.FieldError{{Field: "locale", Message: "must be a language tag such as \"fr\" or \"en-GB\""}},
		})
		return
	}

	out, err := s.templates.Render(r.Context(), tenantFrom(r.Context()).ID,
		mail.TemplateRef{Name: name, Locale: req.Locale, Data: req.Data})
	switch {
	case errors.Is(err, templates.ErrNotFound):
		writeProblemDetails(w, r, templateNotFound(name, http.StatusNotFound))
	case errors.Is(err, templates.ErrInvalid):
		writeProblemDetails(w, r, templateInvalid(err))
	case err != nil:
		s.unavailable(w, r, "render template", err)
	case out.HTML != "" && strings.Contains(r.Header.Get("Accept"), "text/html"):
		// Raw HTML, to open the preview in a browser. The sandbox CSP keeps
		// any script in the template from running on the API's origin.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "sandbox")
		w.Header().Set("X-Kourrier-Subject", mime.QEncoding.Encode("utf-8", out.Subject))
		_, _ = io.WriteString(w, out.HTML)
	default:
		writeJSON(w, http.StatusOK, PreviewResponse{Locale: out.Locale, Subject: out.Subject, HTML: out.HTML, Text: out.Text})
	}
}

// templateNotFound is a 422 when the template is a field of the request
// (POST /v1/emails) and a 404 when it is the resource itself (preview).
func templateNotFound(name string, status int) *Problem {
	return &Problem{
		Type: problemBase + "template-not-found", Title: "Unknown template", Status: status,
		Detail: "template " + name + " does not exist for this API key (see GET /v1/templates)",
	}
}

// templateInvalid exposes the parse or execution error: the template belongs
// to the caller's tenant, the message tells them which file to fix.
func templateInvalid(err error) *Problem {
	return &Problem{
		Type: problemBase + "template-invalid", Title: "Invalid template", Status: http.StatusUnprocessableEntity,
		Detail: err.Error(),
	}
}
