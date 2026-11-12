package console

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// MinTokenLength is the minimum length of the admin token.
	MinTokenLength = 32

	cookieName = "kourrier_console"
	// csrfHeader must be sent with every request that changes state. Browsers
	// only send custom headers on cross-origin requests after a CORS
	// preflight, which the console never allows.
	csrfHeader = "X-Kourrier-Console"
	sessionV   = "v1"
)

// LoadToken reads the admin token from path. Surrounding whitespace (the
// trailing newline of a Secret file) is ignored.
func LoadToken(path string) (string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path comes from the operator configuration
	if err != nil {
		return "", fmt.Errorf("console: read token: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if len(token) < MinTokenLength {
		return "", fmt.Errorf("console: the token in %s must be at least %d characters long", path, MinTokenLength)
	}
	return token, nil
}

// sessions issues and verifies stateless session cookies: an expiry date
// signed with HMAC-SHA256. The key is derived from the admin token, so every
// pod accepts the same cookies and rotating the token revokes every session.
type sessions struct {
	token  [sha256.Size]byte
	key    []byte
	ttl    time.Duration
	secure bool
	now    func() time.Time
}

func newSessions(token string, ttl time.Duration, secure bool) *sessions {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte("kourrier console session " + sessionV))
	return &sessions{token: sha256.Sum256([]byte(token)), key: mac.Sum(nil), ttl: ttl, secure: secure, now: time.Now}
}

// checkToken compares candidate with the admin token in constant time.
func (s *sessions) checkToken(candidate string) bool {
	sum := sha256.Sum256([]byte(candidate))
	return subtle.ConstantTimeCompare(sum[:], s.token[:]) == 1
}

func (s *sessions) sign(payload string) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// issue sets a new session cookie and returns its expiry.
func (s *sessions) issue(w http.ResponseWriter) time.Time {
	exp := s.now().Add(s.ttl).Truncate(time.Second)
	payload := sessionV + "." + strconv.FormatInt(exp.Unix(), 10)
	http.SetCookie(w, s.cookie(payload+"."+s.sign(payload), int(s.ttl.Seconds())))
	return exp
}

func (s *sessions) clear(w http.ResponseWriter) {
	http.SetCookie(w, s.cookie("", -1))
}

func (s *sessions) cookie(value string, maxAge int) *http.Cookie {
	// Secure is configurable for plain HTTP on a trusted network; HttpOnly and
	// SameSite=Strict are always set.
	return &http.Cookie{ //nolint:gosec // see above
		Name: cookieName, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteStrictMode,
	}
}

var errNoSession = errors.New("no valid session")

// verify returns the expiry of the session carried by r.
func (s *sessions) verify(r *http.Request) (time.Time, error) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return time.Time{}, errNoSession
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 3 || parts[0] != sessionV {
		return time.Time{}, errNoSession
	}
	payload := parts[0] + "." + parts[1]
	if !hmac.Equal([]byte(parts[2]), []byte(s.sign(payload))) {
		return time.Time{}, errNoSession
	}
	unix, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return time.Time{}, errNoSession
	}
	exp := time.Unix(unix, 0)
	if !s.now().Before(exp) {
		return time.Time{}, errNoSession
	}
	return exp, nil
}

// sameOrigin rejects requests that change state from another site: the CSRF
// header is required, the Origin (when sent) must be the console's host, and
// cross-site fetches are refused. SameSite=Strict cookies already block
// most of them; this is defence in depth.
func sameOrigin(r *http.Request) bool {
	if r.Header.Get(csrfHeader) == "" {
		return false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host {
			return false
		}
	}
	return true
}

// securityHeaders applies to every response. The preview of a template
// overrides the CSP and framing rules for its own response.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; "+
			"img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-src 'self'; "+
			"frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}
