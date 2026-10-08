// Package ses implements the production e-mail driver on top of the AWS SES
// v2 API.
package ses

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/aws/smithy-go"

	"github.com/theo-mrn/kourrier/internal/mail"
	"github.com/theo-mrn/kourrier/internal/provider"
	"github.com/theo-mrn/kourrier/internal/retry"
)

const charset = "UTF-8"

// Config configures the SES driver. Credentials come from the default AWS
// chain (IRSA / EKS Pod Identity on Kubernetes, env vars locally).
type Config struct {
	Region string
	// ConfigurationSet routes SES events (bounces, complaints, deliveries) to
	// the destinations configured on the AWS side. Optional.
	ConfigurationSet string
}

// API is the subset of the SES client used by the driver.
type API interface {
	SendEmail(ctx context.Context, in *sesv2.SendEmailInput, opts ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error)
}

// Provider sends e-mails through AWS SES.
type Provider struct {
	api              API
	configurationSet string
}

// New creates an SES driver using the default AWS configuration.
func New(ctx context.Context, cfg Config) (*Provider, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("ses: load AWS config: %w", err)
	}
	return NewWithAPI(sesv2.NewFromConfig(awsCfg), cfg), nil
}

// NewWithAPI creates an SES driver on top of an existing client.
func NewWithAPI(api API, cfg Config) *Provider {
	return &Provider{api: api, configurationSet: cfg.ConfigurationSet}
}

func (p *Provider) Name() string { return "aws-ses" }

func (p *Provider) Send(ctx context.Context, msg *mail.Message) (provider.Result, error) {
	body := &types.Body{}
	if msg.HTML != "" {
		body.Html = &types.Content{Data: aws.String(msg.HTML), Charset: aws.String(charset)}
	}
	if msg.Text != "" {
		body.Text = &types.Content{Data: aws.String(msg.Text), Charset: aws.String(charset)}
	}

	headers := make([]types.MessageHeader, 0, len(msg.Headers))
	for k, v := range msg.Headers {
		headers = append(headers, types.MessageHeader{Name: aws.String(k), Value: aws.String(v)})
	}
	tags := make([]types.MessageTag, 0, len(msg.Tags))
	for k, v := range msg.Tags {
		tags = append(tags, types.MessageTag{Name: aws.String(k), Value: aws.String(v)})
	}

	in := &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(msg.From.String()),
		Destination: &types.Destination{
			ToAddresses:  addresses(msg.To),
			CcAddresses:  addresses(msg.Cc),
			BccAddresses: addresses(msg.Bcc),
		},
		ReplyToAddresses: addresses(msg.ReplyTo),
		Content: &types.EmailContent{Simple: &types.Message{
			Subject: &types.Content{Data: aws.String(msg.Subject), Charset: aws.String(charset)},
			Body:    body,
			Headers: headers,
		}},
		EmailTags: tags,
	}
	if p.configurationSet != "" {
		in.ConfigurationSetName = aws.String(p.configurationSet)
	}

	out, err := p.api.SendEmail(ctx, in)
	if err != nil {
		return provider.Result{}, classify(err)
	}
	return provider.Result{MessageID: aws.ToString(out.MessageId)}, nil
}

// classify marks errors that will fail again on retry as permanent. Throttling
// and paused/suspended sending stay transient: they are operational states
// that resolve without changing the message.
func classify(err error) error {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "MessageRejected", "MailFromDomainNotVerifiedException", "BadRequestException", "NotFoundException":
			return retry.Permanent(fmt.Errorf("ses: %w", err))
		}
	}
	return fmt.Errorf("ses: %w", err)
}

func addresses(list []mail.Address) []string {
	if len(list) == 0 {
		return nil
	}
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = a.String()
	}
	return out
}
