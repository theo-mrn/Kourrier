// Package provider defines the EmailProvider abstraction (ADR 004). Drivers
// live in sub-packages and are selected at startup with
// KOURRIER_MAILER_PROVIDER:
//
//	aws-ses     production driver (AWS SES v2 API)
//	smtp        any SMTP relay, or Mailpit/MailHog in development
//	console     prints e-mails to the terminal (default, never sends anything)
//	filesystem  writes e-mails to a local directory for visual inspection
//
// Drivers return errors wrapped with retry.Permanent when retrying cannot
// succeed (rejected address, unverified sender...); any other error is
// considered transient and the job is retried with backoff.
package provider

import (
	"context"

	"github.com/theo-mrn/kourrier/internal/mail"
)

// Provider sends a rendered e-mail.
type Provider interface {
	// Name identifies the driver in logs and metrics.
	Name() string
	Send(ctx context.Context, msg *mail.Message) (Result, error)
}

// Result is returned by a successful Send.
type Result struct {
	// MessageID is the identifier assigned by the provider, if any.
	MessageID string
}
