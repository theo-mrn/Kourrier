// Package filesystem implements a development driver that writes each e-mail
// to a directory so it can be opened in a browser.
package filesystem

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/theo-mrn/kourrier/internal/mail"
	"github.com/theo-mrn/kourrier/internal/provider"
)

// Provider writes <timestamp>_<id>.html, .txt and .json files to a directory.
type Provider struct {
	dir string
}

// New creates the driver, creating dir if needed.
func New(dir string) (*Provider, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("filesystem: create %q: %w", dir, err)
	}
	return &Provider{dir: dir}, nil
}

func (p *Provider) Name() string { return "filesystem" }

type envelope struct {
	ID      string            `json:"id"`
	From    mail.Address      `json:"from"`
	To      []mail.Address    `json:"to"`
	Cc      []mail.Address    `json:"cc,omitempty"`
	Bcc     []mail.Address    `json:"bcc,omitempty"`
	ReplyTo []mail.Address    `json:"reply_to,omitempty"`
	Subject string            `json:"subject"`
	Headers map[string]string `json:"headers,omitempty"`
	Tags    map[string]string `json:"tags,omitempty"`
}

func (p *Provider) Send(_ context.Context, msg *mail.Message) (provider.Result, error) {
	base := filepath.Join(p.dir, time.Now().UTC().Format("20060102T150405")+"_"+msg.ID)

	meta, err := json.MarshalIndent(envelope{
		ID: msg.ID, From: msg.From, To: msg.To, Cc: msg.Cc, Bcc: msg.Bcc, ReplyTo: msg.ReplyTo,
		Subject: msg.Subject, Headers: msg.Headers, Tags: msg.Tags,
	}, "", "  ")
	if err != nil {
		return provider.Result{}, fmt.Errorf("filesystem: %w", err)
	}
	files := map[string][]byte{".json": meta}
	if msg.HTML != "" {
		files[".html"] = []byte(msg.HTML)
	}
	if msg.Text != "" {
		files[".txt"] = []byte(msg.Text)
	}
	for ext, data := range files {
		if err := os.WriteFile(base+ext, data, 0o600); err != nil {
			return provider.Result{}, fmt.Errorf("filesystem: %w", err)
		}
	}
	return provider.Result{MessageID: msg.ID}, nil
}
