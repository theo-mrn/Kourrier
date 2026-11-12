// Package tenant manages the client applications allowed to use Kourrier
// (ADR 008): their API keys, the senders they may use and their template
// namespace.
//
// Tenants are declared in a YAML file, typically mounted from a Kubernetes
// Secret:
//
//	tenants:
//	  - id: yner
//	    api_keys:
//	      - sha256: 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
//	        scopes: [events]          # optional, default [emails, events]
//	    default_sender: { email: no-reply@yner.fr, name: Yner }
//	    allowed_senders: ["no-reply@yner.fr", "*@notifications.yner.fr"]
//
// Only SHA-256 digests of the keys are stored; `kourrier keygen` produces a
// key and its digest.
package tenant

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/theo-mrn/kourrier/internal/mail"
)

// KeyPrefix starts every generated API key, which makes leaked keys easy to
// spot with secret scanners.
const KeyPrefix = "kr_"

// Tenant is a client application.
type Tenant struct {
	ID             string       `yaml:"id"`
	APIKeys        []APIKey     `yaml:"api_keys"`
	DefaultSender  mail.Address `yaml:"default_sender"`
	AllowedSenders []string     `yaml:"allowed_senders"`
}

// Scopes limit what an API key may do (least privilege). A key given to an
// event producer only needs ScopeEvents: if it leaks, it cannot be used to
// send arbitrary e-mails. Reading e-mail statuses is allowed for any scope.
const (
	ScopeEmails = "emails" // POST /v1/emails
	ScopeEvents = "events" // POST /v1/events
)

// APIKey holds the hex-encoded SHA-256 digest of a key. A tenant may have
// several keys to allow zero-downtime rotation.
type APIKey struct {
	SHA256 string `yaml:"sha256"`
	// Scopes defaults to every scope when empty, for compatibility.
	Scopes []string `yaml:"scopes"`
}

// CanSendFrom reports whether the tenant may use email as sender. Patterns are
// either an exact address or "*@domain".
func (t *Tenant) CanSendFrom(email string) bool {
	email = strings.ToLower(email)
	_, domain, ok := strings.Cut(email, "@")
	if !ok {
		return false
	}
	for _, pattern := range t.AllowedSenders {
		pattern = strings.ToLower(pattern)
		if pattern == email || pattern == "*@"+domain {
			return true
		}
	}
	return false
}

// Registry indexes tenants by ID and by API key digest.
type Registry struct {
	byID  map[string]*Tenant
	byKey map[[sha256.Size]byte]keyEntry
}

type keyEntry struct {
	tenant *Tenant
	scopes map[string]bool
}

var (
	// ErrUnauthenticated means the key is missing or unknown.
	ErrUnauthenticated = errors.New("unknown API key")
	// ErrForbidden means the key is valid but lacks the required scope.
	ErrForbidden = errors.New("API key not allowed for this operation")
)

type file struct {
	Tenants []*Tenant `yaml:"tenants"`
}

// LoadFile reads and validates a tenants file.
func LoadFile(path string) (*Registry, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path comes from the operator configuration
	if err != nil {
		return nil, fmt.Errorf("tenant: read %q: %w", path, err)
	}
	return Parse(data)
}

// Parse validates YAML tenant definitions.
func Parse(data []byte) (*Registry, error) {
	var f file
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("tenant: parse: %w", err)
	}
	return NewRegistry(f.Tenants...)
}

// NewRegistry validates tenants and builds a Registry.
func NewRegistry(tenants ...*Tenant) (*Registry, error) {
	r := &Registry{
		byID:  make(map[string]*Tenant, len(tenants)),
		byKey: make(map[[sha256.Size]byte]keyEntry),
	}
	var errs []error
	for i, t := range tenants {
		if !mail.NamePattern.MatchString(t.ID) {
			errs = append(errs, fmt.Errorf("tenants[%d]: id %q must match %s", i, t.ID, mail.NamePattern))
			continue
		}
		if _, dup := r.byID[t.ID]; dup {
			errs = append(errs, fmt.Errorf("tenant %q: duplicate id", t.ID))
			continue
		}
		if len(t.APIKeys) == 0 {
			errs = append(errs, fmt.Errorf("tenant %q: at least one api key is required", t.ID))
		}
		if len(t.AllowedSenders) == 0 {
			errs = append(errs, fmt.Errorf("tenant %q: allowed_senders must not be empty", t.ID))
		}
		if t.DefaultSender.Email != "" && !t.CanSendFrom(t.DefaultSender.Email) {
			errs = append(errs, fmt.Errorf("tenant %q: default sender %q is not in allowed_senders", t.ID, t.DefaultSender.Email))
		}
		for j, k := range t.APIKeys {
			raw, err := hex.DecodeString(k.SHA256)
			if err != nil || len(raw) != sha256.Size {
				errs = append(errs, fmt.Errorf("tenant %q: api_keys[%d].sha256 must be 64 hex characters", t.ID, j))
				continue
			}
			digest := [sha256.Size]byte(raw)
			if other, dup := r.byKey[digest]; dup {
				errs = append(errs, fmt.Errorf("tenant %q: api key already used by tenant %q", t.ID, other.tenant.ID))
				continue
			}
			scopes := map[string]bool{}
			if len(k.Scopes) == 0 {
				scopes[ScopeEmails], scopes[ScopeEvents] = true, true
			}
			for _, sc := range k.Scopes {
				if sc != ScopeEmails && sc != ScopeEvents {
					errs = append(errs, fmt.Errorf("tenant %q: api_keys[%d]: unknown scope %q (emails, events)", t.ID, j, sc))
				}
				scopes[sc] = true
			}
			r.byKey[digest] = keyEntry{tenant: t, scopes: scopes}
		}
		r.byID[t.ID] = t
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("tenant: invalid configuration: %w", err)
	}
	return r, nil
}

// Authenticate returns the tenant owning key. Keys are compared through their
// SHA-256 digest, so lookup time does not leak information about valid keys.
func (r *Registry) Authenticate(key string) (*Tenant, bool) {
	t, err := r.Authorize(key, "")
	return t, err == nil
}

// Authorize authenticates key and checks that it grants scope (any scope
// when empty). It returns ErrUnauthenticated or ErrForbidden.
func (r *Registry) Authorize(key, scope string) (*Tenant, error) {
	if key == "" {
		return nil, ErrUnauthenticated
	}
	e, ok := r.byKey[sha256.Sum256([]byte(key))]
	if !ok {
		return nil, ErrUnauthenticated
	}
	if scope != "" && !e.scopes[scope] {
		return e.tenant, ErrForbidden
	}
	return e.tenant, nil
}

// Get returns a tenant by ID.
func (r *Registry) Get(id string) (*Tenant, bool) {
	t, ok := r.byID[id]
	return t, ok
}

// Len returns the number of tenants.
func (r *Registry) Len() int { return len(r.byID) }

// GenerateKey returns a new random API key and its hex SHA-256 digest.
func GenerateKey() (key, digest string, err error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", "", err
	}
	key = KeyPrefix + base64.RawURLEncoding.EncodeToString(buf[:])
	return key, HashKey(key), nil
}

// HashKey returns the hex SHA-256 digest of key.
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
