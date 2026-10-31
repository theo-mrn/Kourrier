// Package routing maps CloudEvents to e-mail requests with declarative rules
// (ADR 012, ADR 013).
//
// A route belongs to one tenant and matches events by type, optionally by
// source and by a CEL predicate. When it matches, CEL expressions extract the
// recipients, the locale and the template data from the event:
//
//	routes:
//	  - name: welcome
//	    tenant: yner
//	    match:
//	      types: [com.yner.user.signed_up]
//	      when: data.user.email_verified
//	    email:
//	      template: welcome
//	      to: data.user.email
//	      locale: data.user.locale
//
// Routes are compiled once at startup: an invalid expression prevents the
// service from starting instead of failing on the first event.
package routing

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/theo-mrn/kourrier/internal/cloudevent"
	"github.com/theo-mrn/kourrier/internal/mail"
)

// TagRoute is the provider tag carrying the name of the route that produced
// an e-mail, next to kourrier_tenant and kourrier_template.
const TagRoute = "kourrier_route"

// ErrEvaluation wraps every failure to evaluate a route against an event. It
// is permanent: the same event will always fail the same way.
var ErrEvaluation = errors.New("route evaluation failed")

// Route is a compiled routing rule.
type Route struct {
	Name     string
	Tenant   string
	Template string

	types  []string
	source string
	when   *expr
	to     *expr
	locale *expr
	data   *expr
	from   mail.Address
	tags   map[string]string
}

// Router finds the routes of a tenant that match an event.
type Router struct {
	routes []*Route
	// index: tenant -> event type -> routes, in declaration order.
	index map[string]map[string][]*Route
}

func newRouter(routes []*Route) *Router {
	r := &Router{routes: routes, index: make(map[string]map[string][]*Route)}
	for _, rt := range routes {
		byType := r.index[rt.Tenant]
		if byType == nil {
			byType = make(map[string][]*Route)
			r.index[rt.Tenant] = byType
		}
		for _, typ := range rt.types {
			byType[typ] = append(byType[typ], rt)
		}
	}
	return r
}

// Routes returns every route, in declaration order.
func (r *Router) Routes() []*Route { return slices.Clone(r.routes) }

// HasRoutes reports whether tenant has at least one route.
func (r *Router) HasRoutes(tenant string) bool { return len(r.index[tenant]) > 0 }

// Result is the outcome of one matching route.
type Result struct {
	Route *Route
	// Request is the e-mail to submit; nil when Err is set.
	Request *mail.Request
	// Err wraps ErrEvaluation when the route matched the event type but could
	// not be evaluated (missing field, wrong type...).
	Err error
}

// Route evaluates the routes of tenant against e. Routes whose predicate is
// false are left out; an empty result means no route matched. The returned
// error is only set when the event payload itself cannot be decoded.
func (r *Router) Route(tenant string, e *cloudevent.Event) ([]Result, error) {
	candidates := r.index[tenant][e.Type]
	if len(candidates) == 0 {
		return nil, nil
	}
	data, err := e.DecodeData()
	if err != nil {
		return nil, err
	}
	vars := map[string]any{"event": e.Attributes(), "data": data}

	var results []Result
	for _, rt := range candidates {
		if rt.source != "" && rt.source != e.Source {
			continue
		}
		req, matched, err := rt.evaluate(vars, data)
		switch {
		case err != nil:
			results = append(results, Result{Route: rt, Err: fmt.Errorf("%w: route %q: %w", ErrEvaluation, rt.Name, err)})
		case matched:
			results = append(results, Result{Route: rt, Request: req})
		}
	}
	return results, nil
}

func (rt *Route) evaluate(vars map[string]any, data any) (*mail.Request, bool, error) {
	if rt.when != nil {
		v, err := rt.when.eval(vars)
		if err != nil {
			return nil, false, fmt.Errorf("match.when: %w", err)
		}
		ok, isBool := v.(bool)
		if !isBool {
			return nil, false, fmt.Errorf("match.when must return a bool, got %T", v)
		}
		if !ok {
			return nil, false, nil
		}
	}

	v, err := rt.to.eval(vars)
	if err != nil {
		return nil, false, fmt.Errorf("email.to: %w", err)
	}
	to, err := recipients(v)
	if err != nil {
		return nil, false, fmt.Errorf("email.to: %w", err)
	}

	ref := &mail.TemplateRef{Name: rt.Template}
	if rt.locale != nil {
		v, err := rt.locale.eval(vars)
		if err != nil {
			return nil, false, fmt.Errorf("email.locale: %w", err)
		}
		switch l := v.(type) {
		case nil: // fall back to the default locale
		case string:
			ref.Locale = l
		default:
			return nil, false, fmt.Errorf("email.locale must return a string, got %T", v)
		}
	}

	tplData := data
	if rt.data != nil {
		if tplData, err = rt.data.eval(vars); err != nil {
			return nil, false, fmt.Errorf("email.data: %w", err)
		}
	}
	switch d := tplData.(type) {
	case map[string]any:
		ref.Data = d
	case nil:
	default:
		return nil, false, fmt.Errorf("template data must be an object, got %T (set email.data)", tplData)
	}

	tags := maps.Clone(rt.tags)
	if tags == nil {
		tags = make(map[string]string, 1)
	}
	tags[TagRoute] = rt.Name
	return &mail.Request{From: rt.from, To: to, Template: ref, Tags: tags}, true, nil
}

// recipients accepts the shapes an expression may return for email.to: an
// address string, an object {email, name}, or a list of either.
func recipients(v any) ([]mail.Address, error) {
	switch x := v.(type) {
	case []any:
		out := make([]mail.Address, 0, len(x))
		for i, item := range x {
			a, err := address(item)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			out = append(out, a)
		}
		if len(out) == 0 {
			return nil, errors.New("no recipient")
		}
		return out, nil
	default:
		a, err := address(v)
		if err != nil {
			return nil, err
		}
		return []mail.Address{a}, nil
	}
}

func address(v any) (mail.Address, error) {
	switch x := v.(type) {
	case string:
		return mail.Address{Email: x}, nil
	case map[string]any:
		email, ok := x["email"].(string)
		if !ok {
			return mail.Address{}, errors.New(`recipient object needs a string "email" field`)
		}
		name, _ := x["name"].(string)
		return mail.Address{Email: email, Name: name}, nil
	default:
		return mail.Address{}, fmt.Errorf("expected an address string or an {email, name} object, got %T", v)
	}
}
