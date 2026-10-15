// Package templates loads e-mail templates from the object storage and renders
// them (ADR 003).
//
// Storage layout, for a tenant "yner" and a template "welcome":
//
//	yner/_layout.html                 optional, shared HTML layout
//	yner/welcome/fr/subject.txt       required, text/template
//	yner/welcome/fr/body.html         html/template (html and/or txt required)
//	yner/welcome/fr/body.txt          text/template
//
// The requested locale is tried first, then the default locale. Compiled
// templates are cached for a configurable TTL, so edits in the bucket are
// picked up without a redeploy.
package templates

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"path"
	"strings"
	"sync"
	texttemplate "text/template"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/theo-mrn/kourrier/internal/mail"
	"github.com/theo-mrn/kourrier/internal/observability"
	"github.com/theo-mrn/kourrier/internal/retry"
	"github.com/theo-mrn/kourrier/internal/storage"
)

// ErrNotFound is returned (wrapped in a permanent error) when no locale of the
// requested template exists.
var ErrNotFound = errors.New("template not found")

const (
	fileSubject = "subject.txt"
	fileHTML    = "body.html"
	fileText    = "body.txt"
	fileLayout  = "_layout.html"
)

// Rendered is the output of a template rendering.
type Rendered struct {
	Subject string
	HTML    string
	Text    string
	Locale  string
}

type compiled struct {
	locale  string
	subject *texttemplate.Template
	html    *htmltemplate.Template
	text    *texttemplate.Template
}

type cacheEntry struct {
	tpl     *compiled
	expires time.Time
}

// Renderer renders templates from a storage.Provider.
type Renderer struct {
	store         storage.Provider
	ttl           time.Duration
	defaultLocale string
	metrics       *observability.Metrics

	mu    sync.RWMutex
	cache map[string]cacheEntry
	group singleflight.Group
	now   func() time.Time
}

// Options configures a Renderer.
type Options struct {
	// CacheTTL is how long a compiled template is reused before being
	// reloaded from storage. Zero disables caching.
	CacheTTL      time.Duration
	DefaultLocale string
	Metrics       *observability.Metrics
}

// NewRenderer creates a Renderer.
func NewRenderer(store storage.Provider, opts Options) *Renderer {
	return &Renderer{
		store:         store,
		ttl:           opts.CacheTTL,
		defaultLocale: opts.DefaultLocale,
		metrics:       opts.Metrics,
		cache:         make(map[string]cacheEntry),
		now:           time.Now,
	}
}

// Render renders the template ref for the given tenant.
func (r *Renderer) Render(ctx context.Context, tenant string, ref mail.TemplateRef) (*Rendered, error) {
	start := time.Now()
	out, err := r.render(ctx, tenant, ref)
	if r.metrics != nil {
		outcome := "success"
		if err != nil {
			outcome = "error"
		}
		r.metrics.TemplateRender.WithLabelValues(outcome).Observe(time.Since(start).Seconds())
	}
	return out, err
}

func (r *Renderer) render(ctx context.Context, tenant string, ref mail.TemplateRef) (*Rendered, error) {
	if !mail.NamePattern.MatchString(tenant) || !mail.NamePattern.MatchString(ref.Name) {
		return nil, retry.Permanent(fmt.Errorf("invalid template reference %q/%q", tenant, ref.Name))
	}
	tpl, err := r.load(ctx, tenant, ref.Name, ref.Locale)
	if err != nil {
		return nil, err
	}

	data := ref.Data
	if data == nil {
		data = map[string]any{}
	}
	out := &Rendered{Locale: tpl.locale}

	var buf bytes.Buffer
	if err := tpl.subject.Execute(&buf, data); err != nil {
		return nil, retry.Permanent(fmt.Errorf("render subject: %w", err))
	}
	// A subject is a single header line: fold any line break coming from the
	// data to prevent header injection.
	out.Subject = strings.Join(strings.Fields(buf.String()), " ")
	if out.Subject == "" {
		return nil, retry.Permanent(errors.New("rendered subject is empty"))
	}

	if tpl.html != nil {
		buf.Reset()
		if err := tpl.html.Execute(&buf, data); err != nil {
			return nil, retry.Permanent(fmt.Errorf("render %s: %w", fileHTML, err))
		}
		out.HTML = buf.String()
	}
	if tpl.text != nil {
		buf.Reset()
		if err := tpl.text.Execute(&buf, data); err != nil {
			return nil, retry.Permanent(fmt.Errorf("render %s: %w", fileText, err))
		}
		out.Text = buf.String()
	}
	return out, nil
}

// Invalidate drops every cached template.
func (r *Renderer) Invalidate() {
	r.mu.Lock()
	defer r.mu.Unlock()
	clear(r.cache)
}

func (r *Renderer) load(ctx context.Context, tenant, name, locale string) (*compiled, error) {
	key := tenant + "/" + name + "/" + locale
	if r.ttl > 0 {
		r.mu.RLock()
		entry, ok := r.cache[key]
		r.mu.RUnlock()
		if ok && r.now().Before(entry.expires) {
			r.observeCache("hit")
			return entry.tpl, nil
		}
	}
	r.observeCache("miss")

	// singleflight collapses concurrent loads of the same template into one
	// storage round-trip.
	v, err, _ := r.group.Do(key, func() (any, error) {
		tpl, err := r.fetch(ctx, tenant, name, locale)
		if err != nil {
			return nil, err
		}
		if r.ttl > 0 {
			r.mu.Lock()
			r.cache[key] = cacheEntry{tpl: tpl, expires: r.now().Add(r.ttl)}
			r.mu.Unlock()
		}
		return tpl, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*compiled), nil
}

func (r *Renderer) observeCache(result string) {
	if r.metrics != nil {
		r.metrics.TemplateCache.WithLabelValues(result).Inc()
	}
}

func (r *Renderer) fetch(ctx context.Context, tenant, name, locale string) (*compiled, error) {
	for _, loc := range r.candidateLocales(locale) {
		dir := path.Join(tenant, name, loc)
		subject, err := r.get(ctx, path.Join(dir, fileSubject))
		if errors.Is(err, storage.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return r.compile(ctx, tenant, dir, loc, subject)
	}
	return nil, retry.Permanent(fmt.Errorf("%w: %s/%s (locale %q, default %q)", ErrNotFound, tenant, name, locale, r.defaultLocale))
}

func (r *Renderer) compile(ctx context.Context, tenant, dir, locale string, subject []byte) (*compiled, error) {
	c := &compiled{locale: locale}
	var err error
	if c.subject, err = texttemplate.New(fileSubject).Funcs(funcs()).Parse(string(subject)); err != nil {
		return nil, retry.Permanent(fmt.Errorf("parse %s/%s: %w", dir, fileSubject, err))
	}

	htmlBody, err := r.getOptional(ctx, path.Join(dir, fileHTML))
	if err != nil {
		return nil, err
	}
	textBody, err := r.getOptional(ctx, path.Join(dir, fileText))
	if err != nil {
		return nil, err
	}
	if htmlBody == nil && textBody == nil {
		return nil, retry.Permanent(fmt.Errorf("%s: neither %s nor %s exists", dir, fileHTML, fileText))
	}

	if htmlBody != nil {
		c.html = htmltemplate.New(fileHTML).Funcs(funcs())
		layout, err := r.getOptional(ctx, path.Join(tenant, fileLayout))
		if err != nil {
			return nil, err
		}
		if layout != nil {
			if _, err := c.html.New("layout").Parse(string(layout)); err != nil {
				return nil, retry.Permanent(fmt.Errorf("parse %s/%s: %w", tenant, fileLayout, err))
			}
		}
		if _, err := c.html.Parse(string(htmlBody)); err != nil {
			return nil, retry.Permanent(fmt.Errorf("parse %s/%s: %w", dir, fileHTML, err))
		}
	}
	if textBody != nil {
		if c.text, err = texttemplate.New(fileText).Funcs(funcs()).Parse(string(textBody)); err != nil {
			return nil, retry.Permanent(fmt.Errorf("parse %s/%s: %w", dir, fileText, err))
		}
	}
	return c, nil
}

func (r *Renderer) candidateLocales(locale string) []string {
	var out []string
	for _, l := range []string{locale, r.defaultLocale} {
		if l != "" && (len(out) == 0 || out[0] != l) {
			out = append(out, l)
		}
	}
	return out
}

// get reads an object; storage errors other than ErrNotFound are transient.
func (r *Renderer) get(ctx context.Context, key string) ([]byte, error) {
	data, err := r.store.Get(ctx, key)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return nil, fmt.Errorf("load template %s: %w", key, err)
	}
	return data, err
}

func (r *Renderer) getOptional(ctx context.Context, key string) ([]byte, error) {
	data, err := r.get(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	return data, err
}
