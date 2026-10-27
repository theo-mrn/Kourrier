package submit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/theo-mrn/kourrier/internal/idempotency"
	"github.com/theo-mrn/kourrier/internal/mail"
	"github.com/theo-mrn/kourrier/internal/observability"
	"github.com/theo-mrn/kourrier/internal/queue"
	"github.com/theo-mrn/kourrier/internal/status"
	"github.com/theo-mrn/kourrier/internal/templates"
	"github.com/theo-mrn/kourrier/internal/tenant"
)

var shop = &tenant.Tenant{
	ID:             "shop",
	DefaultSender:  mail.Address{Email: "no-reply@shop.fr"},
	AllowedSenders: []string{"no-reply@shop.fr"},
}

func newService(t *testing.T) (*Service, *queue.RedisStreams, *status.Store) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	q, err := queue.NewRedisStreams(context.Background(), rdb, queue.RedisConfig{Consumer: "test", Block: 10 * time.Millisecond}, logger)
	if err != nil {
		t.Fatal(err)
	}
	statuses := status.NewStore(rdb, "kourrier", time.Hour)
	return New(Deps{
		Queue: q, Idempotency: idempotency.NewStore(rdb, "kourrier", time.Hour), Statuses: statuses,
		Metrics: observability.NewMetrics("test"), Logger: logger,
	}), q, statuses
}

func request(to string) mail.Request {
	return mail.Request{To: []mail.Address{{Email: to}}, Template: &mail.TemplateRef{Name: "welcome"}}
}

func TestSubmitQueuesWithStatus(t *testing.T) {
	s, q, statuses := newService(t)
	res, err := s.Submit(context.Background(), &Request{Tenant: shop, Mail: request(" Jane@Example.com "), Origin: OriginAPI})
	if err != nil {
		t.Fatal(err)
	}
	st, err := statuses.Get(context.Background(), res.JobID)
	if err != nil || st == nil || st.State != status.Queued || st.TenantID != "shop" {
		t.Fatalf("status = %+v, %v", st, err)
	}
	d, err := q.Receive(context.Background())
	if err != nil || d == nil {
		t.Fatalf("receive: %v", err)
	}
	if d.Job.Request.From.Email != "no-reply@shop.fr" || d.Job.Request.To[0].Email != "jane@example.com" {
		t.Fatalf("defaults and normalisation not applied: %+v", d.Job.Request)
	}
}

func TestSubmitRejections(t *testing.T) {
	s, _, _ := newService(t)
	var ve *mail.ValidationError
	if _, err := s.Submit(context.Background(), &Request{Tenant: shop, Mail: request("nope"), Origin: OriginAPI}); !errors.As(err, &ve) {
		t.Fatalf("err = %v, want a validation error", err)
	}
	r := request("a@b.fr")
	r.From = mail.Address{Email: "ceo@shop.fr"}
	if _, err := s.Submit(context.Background(), &Request{Tenant: shop, Mail: r, Origin: OriginAPI}); !errors.Is(err, ErrSenderNotAllowed) {
		t.Fatalf("err = %v, want ErrSenderNotAllowed", err)
	}
}

func TestIdempotencyScopes(t *testing.T) {
	s, _, _ := newService(t)
	ctx := context.Background()
	api, err := s.Submit(ctx, &Request{Tenant: shop, Mail: request("a@b.fr"), Origin: OriginAPI, IdempotencyKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	// The same key coming from an event is another namespace.
	ev, err := s.Submit(ctx, &Request{Tenant: shop, Mail: request("a@b.fr"), Origin: OriginEvent, IdempotencyKey: "k", Fingerprint: "evt"})
	if err != nil || ev.Replayed || ev.JobID == api.JobID {
		t.Fatalf("event submission collided with the API key: %+v, %v", ev, err)
	}
	// An explicit fingerprint wins over the content: a different e-mail for
	// the same event is still a replay, not a conflict.
	again, err := s.Submit(ctx, &Request{Tenant: shop, Mail: request("other@b.fr"), Origin: OriginEvent, IdempotencyKey: "k", Fingerprint: "evt"})
	if err != nil || !again.Replayed || again.JobID != ev.JobID {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	// Without an explicit fingerprint, a different body is a conflict.
	if _, err := s.Submit(ctx, &Request{Tenant: shop, Mail: request("other@b.fr"), Origin: OriginAPI, IdempotencyKey: "k"}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("err = %v, want ErrIdempotencyConflict", err)
	}
}

// checkerFunc adapts a function to TemplateChecker.
type checkerFunc func(tenant, name string) error

func (f checkerFunc) Check(_ context.Context, tenant, name, _ string) error { return f(tenant, name) }

func TestSubmitChecksTemplate(t *testing.T) {
	s, q, _ := newService(t)
	ctx := context.Background()
	s.templates = checkerFunc(func(tenant, name string) error {
		switch name {
		case "welcome":
			return nil
		case "broken":
			return fmt.Errorf("%w: parse subject", templates.ErrInvalid)
		case "slow":
			return errors.New("storage timeout")
		}
		return fmt.Errorf("%w: %s/%s", templates.ErrNotFound, tenant, name)
	})
	submitTemplate := func(name string) error {
		r := request("a@b.fr")
		r.Template.Name = name
		_, err := s.Submit(ctx, &Request{Tenant: shop, Mail: r, Origin: OriginAPI})
		return err
	}

	if err := submitTemplate("missing"); !errors.Is(err, templates.ErrNotFound) {
		t.Fatalf("err = %v, want templates.ErrNotFound", err)
	}
	if err := submitTemplate("broken"); !errors.Is(err, templates.ErrInvalid) {
		t.Fatalf("err = %v, want templates.ErrInvalid", err)
	}
	// Rejected requests are never queued.
	if d, err := q.Receive(ctx); err != nil || d != nil {
		t.Fatalf("a rejected request was queued: %+v, %v", d, err)
	}
	// A storage failure must not stop e-mails from being queued.
	if err := submitTemplate("slow"); err != nil {
		t.Fatalf("storage failure rejected the request: %v", err)
	}
	if err := submitTemplate("welcome"); err != nil {
		t.Fatal(err)
	}
}
