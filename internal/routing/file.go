package routing

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"cel.dev/cel-go/cel"
	"go.yaml.in/yaml/v3"

	"github.com/theo-mrn/kourrier/internal/mail"
	"github.com/theo-mrn/kourrier/internal/tenant"
)

// APIVersion is the schema version of the routes file. It is bumped on any
// incompatible change so that an old binary refuses a newer file.
const APIVersion = "kourrier/v1alpha1"

// Limits on the routes file.
const (
	maxRoutes      = 1000
	maxTypeBytes   = 256
	maxRouteTags   = 8 // mail.MaxTags minus the tags added by Kourrier
	defaultAckWait = 30 * time.Second
	// minAckWait stays above the time Kourrier allows itself to process one
	// event, so that an event is not redelivered while it is being handled.
	minAckWait = 15 * time.Second
)

// natsName is a safe subset of the names accepted by NATS for streams and
// consumers (no whitespace, '.', '*', '>' or path separators).
var natsName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Config is the content of the routes file: the event sources to consume and
// the routes that turn their events into e-mails.
type Config struct {
	Router *Router
	NATS   []NATSSource
	Limits Limits
}

// Limit caps the rate at which a tenant's events may create e-mails. It
// keeps one tenant from filling the shared queue and starving the others.
type Limit struct {
	EmailsPerSecond float64 `yaml:"emails_per_second"`
	Burst           int     `yaml:"burst"`
}

// Limits holds the default limit and per-tenant overrides. A zero Limit
// means unlimited.
type Limits struct {
	Default Limit            `yaml:"default"`
	Tenants map[string]Limit `yaml:"tenants"`
}

// For returns the limit that applies to tenant.
func (l Limits) For(tenant string) Limit {
	if t, ok := l.Tenants[tenant]; ok {
		return t
	}
	return l.Default
}

// NATSSource binds a JetStream durable consumer to a tenant: every event read
// from it is routed with the routes of that tenant only (ADR 014).
type NATSSource struct {
	// Name identifies the source in logs and metrics.
	Name   string
	Tenant string
	Stream string
	// Consumer is the durable consumer name.
	Consumer       string
	FilterSubjects []string
	// Bind makes Kourrier use an existing consumer instead of creating or
	// updating it, for setups where consumers are managed declaratively
	// (NACK, Terraform) and Kourrier has no permission to create them.
	Bind bool
	// OnUnrouted is "ignore" (default) or "dead_letter": keep events no route
	// matches, to catch a typo in an event type or a missing route.
	OnUnrouted string
	// DeliverPolicy is "new" (default: only events published after the
	// consumer is created) or "all" (replay what the stream retains).
	DeliverPolicy string
	AckWait       time.Duration
	MaxDeliver    int
	MaxAckPending int
}

// Policies for unrouted events of a NATS source.
const (
	UnroutedIgnore     = "ignore"
	UnroutedDeadLetter = "dead_letter"
)

// Deliver policies of a NATS source.
const (
	DeliverNew = "new"
	DeliverAll = "all"
)

// Tenants looks tenants up by ID. *tenant.Registry implements it.
type Tenants interface {
	Get(id string) (*tenant.Tenant, bool)
}

type fileSchema struct {
	APIVersion string `yaml:"apiVersion"`
	Sources    struct {
		NATS []natsSchema `yaml:"nats"`
	} `yaml:"sources"`
	Routes []routeSchema `yaml:"routes"`
	Limits Limits        `yaml:"limits"`
}

// natsSchema mirrors NATSSource field for field (it converts to it) and only
// adds the YAML names.
type natsSchema struct {
	Name           string        `yaml:"name"`
	Tenant         string        `yaml:"tenant"`
	Stream         string        `yaml:"stream"`
	Consumer       string        `yaml:"consumer"`
	FilterSubjects []string      `yaml:"filter_subjects"`
	Bind           bool          `yaml:"bind"`
	OnUnrouted     string        `yaml:"on_unrouted"`
	DeliverPolicy  string        `yaml:"deliver_policy"`
	AckWait        time.Duration `yaml:"ack_wait"`
	MaxDeliver     int           `yaml:"max_deliver"`
	MaxAckPending  int           `yaml:"max_ack_pending"`
}

type routeSchema struct {
	Name   string `yaml:"name"`
	Tenant string `yaml:"tenant"`
	Match  struct {
		Types  []string `yaml:"types"`
		Source string   `yaml:"source"`
		When   string   `yaml:"when"`
	} `yaml:"match"`
	Email struct {
		Template string            `yaml:"template"`
		To       string            `yaml:"to"`
		Locale   string            `yaml:"locale"`
		Data     string            `yaml:"data"`
		From     mail.Address      `yaml:"from"`
		Tags     map[string]string `yaml:"tags"`
	} `yaml:"email"`
}

// LoadFile reads and compiles a routes file. tenants may be nil, in which
// case references to tenants and senders are not checked (offline linting).
func LoadFile(path string, tenants Tenants) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path comes from the operator configuration
	if err != nil {
		return nil, fmt.Errorf("routing: read %q: %w", path, err)
	}
	return Parse(data, tenants)
}

// Parse compiles a routes file. Every problem is reported, not only the
// first one, so that a CI check gives the full picture at once.
func Parse(data []byte, tenants Tenants) (*Config, error) {
	var f fileSchema
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("routing: parse: %w", err)
	}
	if f.APIVersion != APIVersion {
		return nil, fmt.Errorf("routing: apiVersion must be %q (got %q)", APIVersion, f.APIVersion)
	}

	env, err := newEnv()
	if err != nil {
		return nil, fmt.Errorf("routing: cel environment: %w", err)
	}
	v := &validator{tenants: tenants}
	cfg := &Config{}
	sourceNames := make(map[string]bool, len(f.Sources.NATS))
	consumers := make(map[string]string, len(f.Sources.NATS))
	for i, s := range f.Sources.NATS {
		field := fmt.Sprintf("sources.nats[%d]", i)
		// Two sources sharing a durable consumer would split its messages
		// between two tenants at random.
		if sourceNames[s.Name] {
			v.errorf(field+".name", "duplicate source name %q", s.Name)
		}
		sourceNames[s.Name] = true
		if other, dup := consumers[s.Stream+"/"+s.Consumer]; dup {
			v.errorf(field, "stream %q and consumer %q are already used by source %q", s.Stream, s.Consumer, other)
		}
		consumers[s.Stream+"/"+s.Consumer] = s.Name
		if src, ok := v.natsSource(field, s); ok {
			cfg.NATS = append(cfg.NATS, src)
		}
	}
	if len(f.Routes) > maxRoutes {
		v.errorf("routes", "at most %d routes are allowed", maxRoutes)
	}
	routes := make([]*Route, 0, len(f.Routes))
	names := make(map[string]bool, len(f.Routes))
	for i, s := range f.Routes {
		field := fmt.Sprintf("routes[%d]", i)
		if s.Name != "" {
			field = fmt.Sprintf("routes[%s]", s.Name)
			if names[s.Name] {
				v.errorf(field, "duplicate route name")
			}
			names[s.Name] = true
		}
		if rt := v.route(env, field, s); rt != nil {
			routes = append(routes, rt)
		}
	}
	v.limit("limits.default", f.Limits.Default)
	for id, l := range f.Limits.Tenants {
		v.tenant("limits.tenants["+id+"]", id)
		v.limit("limits.tenants["+id+"]", l)
	}
	cfg.Limits = f.Limits
	if err := errors.Join(v.errs...); err != nil {
		return nil, fmt.Errorf("routing: invalid configuration:\n%w", err)
	}
	cfg.Router = newRouter(routes)
	return cfg, nil
}

type validator struct {
	tenants Tenants
	errs    []error
}

func (v *validator) errorf(field, format string, args ...any) {
	v.errs = append(v.errs, fmt.Errorf("  %s: %s", field, fmt.Sprintf(format, args...)))
}

// tenant validates a tenant reference and returns the tenant when known.
func (v *validator) tenant(field, id string) *tenant.Tenant {
	if !mail.NamePattern.MatchString(id) {
		v.errorf(field+".tenant", "must match %s", mail.NamePattern)
		return nil
	}
	if v.tenants == nil {
		return nil
	}
	t, ok := v.tenants.Get(id)
	if !ok {
		v.errorf(field+".tenant", "unknown tenant %q", id)
		return nil
	}
	return t
}

func (v *validator) limit(field string, l Limit) {
	if l.EmailsPerSecond < 0 || l.Burst < 0 {
		v.errorf(field, "emails_per_second and burst must be >= 0")
	}
	if l.EmailsPerSecond == 0 && l.Burst != 0 {
		v.errorf(field, "burst needs emails_per_second")
	}
}

func (v *validator) natsSource(field string, s natsSchema) (NATSSource, bool) {
	n := len(v.errs)
	if !mail.NamePattern.MatchString(s.Name) {
		v.errorf(field+".name", "must match %s", mail.NamePattern)
	}
	v.tenant(field, s.Tenant)
	if !natsName.MatchString(s.Stream) {
		v.errorf(field+".stream", "must match %s", natsName)
	}
	if !natsName.MatchString(s.Consumer) {
		v.errorf(field+".consumer", "must match %s", natsName)
	}
	for i, subj := range s.FilterSubjects {
		if subj == "" || strings.ContainsAny(subj, " \t\r\n") {
			v.errorf(fmt.Sprintf("%s.filter_subjects[%d]", field, i), "must be a non-empty subject without whitespace")
		}
	}
	if s.AckWait == 0 {
		s.AckWait = defaultAckWait
	}
	if s.OnUnrouted == "" {
		s.OnUnrouted = UnroutedIgnore
	}
	if s.OnUnrouted != UnroutedIgnore && s.OnUnrouted != UnroutedDeadLetter {
		v.errorf(field+".on_unrouted", "must be %q or %q", UnroutedIgnore, UnroutedDeadLetter)
	}
	if s.DeliverPolicy == "" {
		// Never replay a stream's history by default: that would send an
		// e-mail for every past event (e.g. welcome mails to every user).
		s.DeliverPolicy = DeliverNew
	}
	if s.DeliverPolicy != DeliverNew && s.DeliverPolicy != DeliverAll {
		v.errorf(field+".deliver_policy", "must be %q or %q", DeliverNew, DeliverAll)
	}
	if s.AckWait < minAckWait {
		v.errorf(field+".ack_wait", "must be at least %s", minAckWait)
	}
	if s.MaxDeliver < 0 || s.MaxAckPending < 0 {
		v.errorf(field, "max_deliver and max_ack_pending must be >= 0 (0 keeps the default)")
	}
	if s.Bind && (len(s.FilterSubjects) > 0 || s.MaxDeliver != 0 || s.MaxAckPending != 0 || s.DeliverPolicy != DeliverNew) {
		v.errorf(field, "filter_subjects, deliver_policy, max_deliver and max_ack_pending are managed outside Kourrier when bind is true")
	}
	return NATSSource(s), len(v.errs) == n
}

func (v *validator) route(env *cel.Env, field string, s routeSchema) *Route {
	n := len(v.errs)
	if !mail.NamePattern.MatchString(s.Name) {
		v.errorf(field+".name", "must match %s", mail.NamePattern)
	}
	t := v.tenant(field, s.Tenant)

	if len(s.Match.Types) == 0 {
		v.errorf(field+".match.types", "at least one event type is required")
	}
	for i, typ := range s.Match.Types {
		if typ == "" || len(typ) > maxTypeBytes {
			v.errorf(fmt.Sprintf("%s.match.types[%d]", field, i), "must be 1 to %d bytes", maxTypeBytes)
		}
	}

	if !mail.NamePattern.MatchString(s.Email.Template) {
		v.errorf(field+".email.template", "must match %s", mail.NamePattern)
	}
	if s.Email.From.Email != "" && t != nil && !t.CanSendFrom(s.Email.From.Email) {
		v.errorf(field+".email.from", "%q is not in the allowed_senders of tenant %q", s.Email.From.Email, s.Tenant)
	}
	if s.Email.From.Email == "" && t != nil && t.DefaultSender.Email == "" {
		v.errorf(field+".email.from", "required: tenant %q has no default_sender", s.Tenant)
	}
	if len(s.Email.Tags) > maxRouteTags {
		v.errorf(field+".email.tags", "at most %d tags are allowed", maxRouteTags)
	}
	for k, val := range s.Email.Tags {
		if strings.HasPrefix(k, "kourrier_") {
			v.errorf(field+".email.tags."+k, "the kourrier_ prefix is reserved")
		}
		if !mail.ValidTag(k, val) {
			v.errorf(field+".email.tags."+k, "keys and values must be 1-64 characters among [A-Za-z0-9_.-]")
		}
	}

	compileField := func(name, src string, required bool, want ...*cel.Type) *expr {
		if src == "" {
			if required {
				v.errorf(field+"."+name, "is required")
			}
			return nil
		}
		e, err := compile(env, src, want...)
		if err != nil {
			v.errorf(field+"."+name, "%v", err)
		}
		return e
	}
	rt := &Route{
		Name:     s.Name,
		Tenant:   s.Tenant,
		Template: s.Email.Template,
		types:    s.Match.Types,
		source:   s.Match.Source,
		when:     compileField("match.when", s.Match.When, false, cel.BoolType),
		to: compileField("email.to", s.Email.To, true,
			cel.StringType, cel.ListType(cel.DynType), cel.MapType(cel.StringType, cel.DynType)),
		locale: compileField("email.locale", s.Email.Locale, false, cel.StringType),
		data:   compileField("email.data", s.Email.Data, false, cel.MapType(cel.StringType, cel.DynType)),
		from:   s.Email.From,
		tags:   s.Email.Tags,
	}
	if len(v.errs) != n {
		return nil
	}
	return rt
}
