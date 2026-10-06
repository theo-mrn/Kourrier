// Package console implements a development driver that prints e-mails to the
// terminal instead of sending them.
package console

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/theo-mrn/kourrier/internal/mail"
	"github.com/theo-mrn/kourrier/internal/provider"
)

// Provider writes a readable dump of each e-mail to an io.Writer.
type Provider struct {
	mu sync.Mutex
	w  io.Writer
}

// New creates a console driver writing to w (typically os.Stdout).
func New(w io.Writer) *Provider {
	return &Provider{w: w}
}

func (p *Provider) Name() string { return "console" }

func (p *Provider) Send(_ context.Context, msg *mail.Message) (provider.Result, error) {
	var b strings.Builder
	rule := strings.Repeat("─", 72)
	fmt.Fprintf(&b, "\n┌%s\n", rule)
	fmt.Fprintf(&b, "│ 📧 Kourrier — e-mail %s (not sent: console driver)\n", msg.ID)
	fmt.Fprintf(&b, "├%s\n", rule)
	fmt.Fprintf(&b, "│ From:     %s\n", msg.From)
	writeList(&b, "To", msg.To)
	writeList(&b, "Cc", msg.Cc)
	writeList(&b, "Bcc", msg.Bcc)
	writeList(&b, "Reply-To", msg.ReplyTo)
	fmt.Fprintf(&b, "│ Subject:  %s\n", msg.Subject)
	for k, v := range msg.Headers {
		fmt.Fprintf(&b, "│ %s: %s\n", k, v)
	}
	if msg.Text != "" {
		fmt.Fprintf(&b, "├─ text/plain %s\n", strings.Repeat("─", 59))
		writeBody(&b, msg.Text)
	}
	if msg.HTML != "" {
		fmt.Fprintf(&b, "├─ text/html %s\n", strings.Repeat("─", 60))
		writeBody(&b, msg.HTML)
	}
	fmt.Fprintf(&b, "└%s\n", rule)

	p.mu.Lock()
	defer p.mu.Unlock()
	if _, err := io.WriteString(p.w, b.String()); err != nil {
		return provider.Result{}, fmt.Errorf("console: %w", err)
	}
	return provider.Result{MessageID: msg.ID}, nil
}

func writeList(b *strings.Builder, label string, list []mail.Address) {
	if len(list) == 0 {
		return
	}
	parts := make([]string, len(list))
	for i, a := range list {
		parts[i] = a.String()
	}
	fmt.Fprintf(b, "│ %-9s %s\n", label+":", strings.Join(parts, ", "))
}

func writeBody(b *strings.Builder, body string) {
	for line := range strings.SplitSeq(strings.TrimRight(body, "\n"), "\n") {
		fmt.Fprintf(b, "│ %s\n", line)
	}
}
