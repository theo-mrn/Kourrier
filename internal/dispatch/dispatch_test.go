package dispatch

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/theo-mrn/kourrier/internal/cloudevent"
	"github.com/theo-mrn/kourrier/internal/idempotency"
	"github.com/theo-mrn/kourrier/internal/mail"
	"github.com/theo-mrn/kourrier/internal/observability"
	"github.com/theo-mrn/kourrier/internal/queue"
	"github.com/theo-mrn/kourrier/internal/routing"
	"github.com/theo-mrn/kourrier/internal/status"
	"github.com/theo-mrn/kourrier/internal/submit"
	"github.com/theo-mrn/kourrier/internal/tenant"
)

const routes = `
apiVersion: kourrier/v1alpha1
routes:
  - name: welcome
    tenant: shop
    match: { types: [user.created] }
    email: { template: welcome, to: data.email }
  - name: notify-admin
    tenant: shop
    match: { types: [user.created] }
    email: { template: admin, to: '"admin@shop.fr"' }
`

type fixture struct {
	d      *Dispatcher
	q      *queue.RedisStreams
	mr     *miniredis.Miniredis
	tenant *tenant.Tenant
	gate   *fakeGate
}

type fakeGate struct{ over bool }

func (g *fakeGate) Overloaded() bool { return g.over }

func newFixture(t *testing.T) *fixture {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { rdb.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	metrics := observability.NewMetrics("test")

	shop := &tenant.Tenant{
		ID:             "shop",
		APIKeys:        []tenant.APIKey{{SHA256: tenant.HashKey("kr_shop")}},
		DefaultSender:  mail.Address{Email: "no-reply@shop.fr"},
		AllowedSenders: []string{"no-reply@shop.fr"},
	}
	reg, err := tenant.NewRegistry(shop)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := routing.Parse([]byte(routes), reg)
	if err != nil {
		t.Fatal(err)
	}
	q, err := queue.NewRedisStreams(context.Background(), rdb, queue.RedisConfig{Consumer: "test", Block: 10 * time.Millisecond}, logger)
	if err != nil {
		t.Fatal(err)
	}
	gate := &fakeGate{}
	d := New(Deps{
		Router: cfg.Router,
		Submitter: submit.New(submit.Deps{
			Queue:       q,
			Idempotency: idempotency.NewStore("kourrier", time.Hour),
			Statuses:    status.NewStore(rdb, "kourrier", time.Hour),
			Metrics:     metrics,
			Logger:      logger,
		}),
		Gate:    gate,
		Metrics: metrics,
		Logger:  logger,
	})
	return &fixture{d: d, q: q, mr: mr, tenant: shop, gate: gate}
}

func ev(t *testing.T, typ, source, id, data string) *cloudevent.Event {
	t.Helper()
	e, err := cloudevent.ParseStructured([]byte(`{"specversion":"1.0","id":"` + id + `","source":"` + source +
		`","type":"` + typ + `","data":` + data + `}`))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (f *fixture) queued(t *testing.T) int64 {
	t.Helper()
	s, err := f.q.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s.Ready
}

func TestDispatchIsIdempotent(t *testing.T) {
	f := newFixture(t)
	e := ev(t, "user.created", "/users", "1", `{"email":"jane@example.com"}`)

	first := f.d.Dispatch(context.Background(), "test", f.tenant, e)
	if first.Outcome != Accepted || len(first.Emails) != 2 || len(first.Errors) != 0 {
		t.Fatalf("unexpected report %+v", first)
	}
	// Redelivery of the same event: no new e-mail.
	again := f.d.Dispatch(context.Background(), "test", f.tenant, e)
	if again.Outcome != Accepted || !again.Emails[0].Replayed || again.Emails[0].ID != first.Emails[0].ID {
		t.Fatalf("redelivery was not deduplicated: %+v", again)
	}
	if n := f.queued(t); n != 2 {
		t.Fatalf("%d jobs queued, want 2", n)
	}

	// Same id from another source is another event.
	other := f.d.Dispatch(context.Background(), "test", f.tenant, ev(t, "user.created", "/users/v2", "1", `{"email":"jane@example.com"}`))
	if other.Outcome != Accepted || other.Emails[0].Replayed {
		t.Fatalf("event from another source was deduplicated: %+v", other)
	}

	d := receive(t, f.q)
	if d.Job.Request.Tags[routing.TagRoute] == "" || d.Job.Request.From.Email != "no-reply@shop.fr" {
		t.Fatalf("unexpected job %+v", d.Job.Request)
	}
}

func receive(t *testing.T, q *queue.RedisStreams) *queue.Delivery {
	t.Helper()
	d, err := q.Receive(context.Background())
	if err != nil || d == nil {
		t.Fatalf("receive: %v", err)
	}
	return d
}

func TestDispatchOutcomes(t *testing.T) {
	f := newFixture(t)

	if rep := f.d.Dispatch(context.Background(), "test", f.tenant, ev(t, "user.deleted", "/users", "1", `{}`)); rep.Outcome != Ignored {
		t.Fatalf("unrouted event: %+v", rep)
	}

	// The welcome route builds an invalid recipient, the admin route works.
	partial := f.d.Dispatch(context.Background(), "test", f.tenant, ev(t, "user.created", "/users", "2", `{"email":"not-an-address"}`))
	if partial.Outcome != Accepted || len(partial.Emails) != 1 || len(partial.Errors) != 1 || partial.Errors[0].Route != "welcome" {
		t.Fatalf("partial failure: %+v", partial)
	}

	// A payload that no route can use.
	rejected := f.d.Dispatch(context.Background(), "test", f.tenant, ev(t, "user.created", "/users", "3", `"a string"`))
	if rejected.Outcome != Rejected || len(rejected.Errors) != 2 {
		t.Fatalf("unexpected report %+v", rejected)
	}
}

func TestDispatchAllRoutesRejected(t *testing.T) {
	f := newFixture(t)
	reg, _ := tenant.NewRegistry(f.tenant)
	cfg, err := routing.Parse([]byte(`
apiVersion: kourrier/v1alpha1
routes:
  - name: welcome
    tenant: shop
    match: { types: [user.created] }
    email: { template: welcome, to: data.email }
`), reg)
	if err != nil {
		t.Fatal(err)
	}
	f.d.router = cfg.Router
	rep := f.d.Dispatch(context.Background(), "test", f.tenant, ev(t, "user.created", "/users", "1", `{}`))
	if rep.Outcome != Rejected || len(rep.Errors) != 1 {
		t.Fatalf("unexpected report %+v", rep)
	}
}

func TestDispatchRetriesTransientFailures(t *testing.T) {
	f := newFixture(t)
	f.mr.Close()
	rep := f.d.Dispatch(context.Background(), "test", f.tenant, ev(t, "user.created", "/users", "1", `{"email":"jane@example.com"}`))
	if rep.Outcome != Retry || !errors.Is(rep.Err, submit.ErrUnavailable) {
		t.Fatalf("unexpected report %+v", rep)
	}
}

func TestDispatchBackpressure(t *testing.T) {
	f := newFixture(t)
	f.gate.over = true
	rep := f.d.Dispatch(context.Background(), "test", f.tenant, ev(t, "user.created", "/users", "1", `{"email":"jane@example.com"}`))
	if rep.Outcome != Retry || !errors.Is(rep.Err, ErrBackpressure) {
		t.Fatalf("unexpected report %+v", rep)
	}
	if n := f.queued(t); n != 0 {
		t.Fatalf("%d jobs queued while overloaded", n)
	}
	// Events without routes are still drained.
	if rep := f.d.Dispatch(context.Background(), "test", f.tenant, ev(t, "user.deleted", "/users", "2", `{}`)); rep.Outcome != Ignored {
		t.Fatalf("unrouted event under backpressure: %+v", rep)
	}
}

func TestDepthGate(t *testing.T) {
	stats := queue.Stats{Ready: 5, Delayed: 6}
	var fail bool
	g := NewDepthGate(func(context.Context) (queue.Stats, error) {
		if fail {
			return queue.Stats{}, errors.New("redis down")
		}
		return stats, nil
	}, 10, time.Hour, observability.NewMetrics("test"), slog.New(slog.NewTextHandler(io.Discard, nil)))

	g.check(context.Background())
	if !g.Overloaded() {
		t.Fatal("11 jobs > 10 should overload the gate")
	}
	fail = true
	g.check(context.Background())
	if !g.Overloaded() {
		t.Fatal("a stats error must keep the previous state")
	}
	fail, stats = false, queue.Stats{Ready: 10, Pending: 100}
	g.check(context.Background())
	if g.Overloaded() {
		t.Fatal("pending jobs are in flight and must not count")
	}
}

func TestIdempotencyKeyHasNoCollision(t *testing.T) {
	a := idempotencyKey("r", &cloudevent.Event{Source: "a|1:b", ID: "c"})
	b := idempotencyKey("r", &cloudevent.Event{Source: "a", ID: "1:b|c"})
	if a == b {
		t.Fatalf("keys collide: %q", a)
	}
}
