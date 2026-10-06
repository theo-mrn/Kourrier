// Package memory implements an in-memory driver for automated tests.
package memory

import (
	"context"
	"sync"

	"github.com/theo-mrn/kourrier/internal/mail"
	"github.com/theo-mrn/kourrier/internal/provider"
)

// Provider records every message it is asked to send. Errors can be injected
// with FailNext.
type Provider struct {
	mu       sync.Mutex
	messages []mail.Message
	failures []error
}

// New creates an empty driver.
func New() *Provider { return &Provider{} }

func (p *Provider) Name() string { return "memory" }

func (p *Provider) Send(_ context.Context, msg *mail.Message) (provider.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.failures) > 0 {
		err := p.failures[0]
		p.failures = p.failures[1:]
		return provider.Result{}, err
	}
	p.messages = append(p.messages, *msg)
	return provider.Result{MessageID: "memory-" + msg.ID}, nil
}

// FailNext makes the next len(errs) calls to Send return errs, in order.
func (p *Provider) FailNext(errs ...error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failures = append(p.failures, errs...)
}

// Messages returns a copy of the messages sent so far.
func (p *Provider) Messages() []mail.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]mail.Message(nil), p.messages...)
}
