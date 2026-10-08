package ses

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/smithy-go"

	"github.com/theo-mrn/kourrier/internal/mail"
	"github.com/theo-mrn/kourrier/internal/retry"
)

type fakeAPI struct {
	in  *sesv2.SendEmailInput
	err error
}

func (f *fakeAPI) SendEmail(_ context.Context, in *sesv2.SendEmailInput, _ ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
	f.in = in
	if f.err != nil {
		return nil, f.err
	}
	return &sesv2.SendEmailOutput{MessageId: aws.String("ses-123")}, nil
}

func TestSendMapsMessage(t *testing.T) {
	api := &fakeAPI{}
	p := NewWithAPI(api, Config{ConfigurationSet: "kourrier"})
	res, err := p.Send(context.Background(), &mail.Message{
		From:    mail.Address{Email: "no-reply@yner.fr", Name: "Yner"},
		To:      []mail.Address{{Email: "jane@example.com"}},
		Bcc:     []mail.Address{{Email: "audit@yner.fr"}},
		Subject: "Hello", HTML: "<p>Hi</p>", Text: "Hi",
		Headers: map[string]string{"X-Campaign": "spring"},
		Tags:    map[string]string{"kourrier_tenant": "yner"},
	})
	if err != nil || res.MessageID != "ses-123" {
		t.Fatalf("Send = %+v, %v", res, err)
	}
	in := api.in
	if aws.ToString(in.FromEmailAddress) != `"Yner" <no-reply@yner.fr>` {
		t.Errorf("from = %q", aws.ToString(in.FromEmailAddress))
	}
	if in.Destination.ToAddresses[0] != "jane@example.com" || in.Destination.BccAddresses[0] != "audit@yner.fr" {
		t.Errorf("destination = %+v", in.Destination)
	}
	msg := in.Content.Simple
	if aws.ToString(msg.Subject.Data) != "Hello" || aws.ToString(msg.Body.Html.Data) != "<p>Hi</p>" || aws.ToString(msg.Body.Text.Data) != "Hi" {
		t.Errorf("content = %+v", msg)
	}
	if len(msg.Headers) != 1 || len(in.EmailTags) != 1 || aws.ToString(in.ConfigurationSetName) != "kourrier" {
		t.Errorf("headers/tags/configuration set not mapped: %+v", in)
	}
}

func TestErrorClassification(t *testing.T) {
	for code, permanent := range map[string]bool{
		"MessageRejected":                    true,
		"MailFromDomainNotVerifiedException": true,
		"TooManyRequestsException":           false,
		"SendingPausedException":             false,
		"InternalFailure":                    false,
	} {
		api := &fakeAPI{err: &smithy.GenericAPIError{Code: code}}
		_, err := NewWithAPI(api, Config{}).Send(context.Background(), &mail.Message{})
		if retry.IsPermanent(err) != permanent {
			t.Errorf("%s: permanent = %v, want %v", code, !permanent, permanent)
		}
	}
	_, err := NewWithAPI(&fakeAPI{err: errors.New("dial tcp: timeout")}, Config{}).Send(context.Background(), &mail.Message{})
	if retry.IsPermanent(err) {
		t.Error("network errors must be transient")
	}
}
