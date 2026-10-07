// Package smtp implements an SMTP driver, usable with any relay or with a
// local catcher such as Mailpit or MailHog.
package smtp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	gomail "github.com/wneessen/go-mail"

	"github.com/theo-mrn/kourrier/internal/mail"
	"github.com/theo-mrn/kourrier/internal/provider"
	"github.com/theo-mrn/kourrier/internal/retry"
)

// TLS modes.
const (
	TLSNone          = "none"          // plain text, for local catchers only
	TLSOpportunistic = "opportunistic" // STARTTLS when offered
	TLSMandatory     = "mandatory"     // STARTTLS required
	TLSImplicit      = "implicit"      // TLS from the first byte (SMTPS, port 465)
)

// Config configures the SMTP driver.
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	TLS      string
	Timeout  time.Duration
	// HeloDomain is the domain used to build Message-IDs. Defaults to the
	// host name of the relay.
	HeloDomain string
}

// Provider sends e-mails over SMTP. A new connection is opened per message,
// which keeps the driver safe for concurrent use by the worker pool.
type Provider struct {
	cfg  Config
	opts []gomail.Option
}

// New validates cfg and creates the driver.
func New(cfg Config) (*Provider, error) {
	if cfg.Host == "" {
		return nil, errors.New("smtp: host is required")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 15 * time.Second
	}
	opts := []gomail.Option{gomail.WithTimeout(cfg.Timeout)}
	if cfg.Port != 0 {
		opts = append(opts, gomail.WithPort(cfg.Port))
	}
	switch cfg.TLS {
	case TLSNone, "":
		opts = append(opts, gomail.WithTLSPolicy(gomail.NoTLS))
	case TLSOpportunistic:
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSOpportunistic))
	case TLSMandatory:
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSMandatory))
	case TLSImplicit:
		opts = append(opts, gomail.WithSSL())
	default:
		return nil, fmt.Errorf("smtp: unknown TLS mode %q", cfg.TLS)
	}
	if cfg.Username != "" {
		opts = append(opts,
			gomail.WithSMTPAuth(gomail.SMTPAuthAutoDiscover),
			gomail.WithUsername(cfg.Username),
			gomail.WithPassword(cfg.Password),
		)
	}
	if cfg.HeloDomain == "" {
		cfg.HeloDomain = cfg.Host
	}
	return &Provider{cfg: cfg, opts: opts}, nil
}

func (p *Provider) Name() string { return "smtp" }

func (p *Provider) Send(ctx context.Context, msg *mail.Message) (provider.Result, error) {
	m, err := p.build(msg)
	if err != nil {
		return provider.Result{}, retry.Permanent(fmt.Errorf("smtp: build message: %w", err))
	}
	client, err := gomail.NewClient(p.cfg.Host, p.opts...)
	if err != nil {
		return provider.Result{}, fmt.Errorf("smtp: new client: %w", err)
	}
	if err := client.DialAndSendWithContext(ctx, m); err != nil {
		return provider.Result{}, classify(err)
	}
	return provider.Result{MessageID: msg.ID + "@" + p.cfg.HeloDomain}, nil
}

func (p *Provider) build(msg *mail.Message) (*gomail.Msg, error) {
	m := gomail.NewMsg()
	if err := m.FromFormat(msg.From.Name, msg.From.Email); err != nil {
		return nil, err
	}
	for _, list := range []struct {
		add  func(name, addr string) error
		addr []mail.Address
	}{{m.AddToFormat, msg.To}, {m.AddCcFormat, msg.Cc}, {m.AddBccFormat, msg.Bcc}} {
		for _, a := range list.addr {
			if err := list.add(a.Name, a.Email); err != nil {
				return nil, err
			}
		}
	}
	if len(msg.ReplyTo) > 0 {
		replyTo := make([]string, len(msg.ReplyTo))
		for i, a := range msg.ReplyTo {
			replyTo[i] = a.String()
		}
		if err := m.SetAddrHeader(gomail.HeaderReplyTo, replyTo...); err != nil {
			return nil, err
		}
	}
	m.Subject(msg.Subject)
	for k, v := range msg.Headers {
		m.SetGenHeader(gomail.Header(k), v)
	}
	m.SetMessageIDWithValue(msg.ID + "@" + p.cfg.HeloDomain)
	m.SetDate()

	switch {
	case msg.Text != "" && msg.HTML != "":
		m.SetBodyString(gomail.TypeTextPlain, msg.Text)
		m.AddAlternativeString(gomail.TypeTextHTML, msg.HTML)
	case msg.HTML != "":
		m.SetBodyString(gomail.TypeTextHTML, msg.HTML)
	default:
		m.SetBodyString(gomail.TypeTextPlain, msg.Text)
	}
	return m, nil
}

// classify marks 5xx SMTP replies as permanent; 4xx replies and network
// errors are transient.
func classify(err error) error {
	var sendErr *gomail.SendError
	if errors.As(err, &sendErr) && !sendErr.IsTemp() && !isNetworkError(err) {
		return retry.Permanent(fmt.Errorf("smtp: %w", err))
	}
	return fmt.Errorf("smtp: %w", err)
}

func isNetworkError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr)
}
