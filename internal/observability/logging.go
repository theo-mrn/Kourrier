// Package observability holds the logging and Prometheus instrumentation
// shared by every component (ADR 005).
package observability

import (
	"io"
	"log/slog"
	"strings"

	"github.com/theo-mrn/kourrier/internal/mail"
)

// NewLogger builds a slog logger. format is "json" (default, for Loki) or
// "text" (for humans); level is debug|info|warn|error.
func NewLogger(w io.Writer, format, level string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}
	var h slog.Handler
	if strings.EqualFold(format, "text") {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(h)
}

func parseLevel(s string) slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return slog.LevelInfo
	}
	return l
}

// MaskEmail hides the local part of an e-mail address so that logs stay
// useful for debugging deliverability (the domain is kept) without storing
// personal data: "jane.doe@example.com" becomes "j***@example.com".
func MaskEmail(email string) string {
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" {
		return "***"
	}
	return local[:1] + "***@" + domain
}

// MaskAddresses applies MaskEmail to a list of addresses.
func MaskAddresses(list []mail.Address) []string {
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = MaskEmail(a.Email)
	}
	return out
}
