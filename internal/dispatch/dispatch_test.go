package dispatch

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/theo-mrn/kourrier/internal/cloudevent"
	"github.com/theo-mrn/kourrier/internal/deadevent"
	"github.com/theo-mrn/kourrier/internal/idempotency"
	"github.com/theo-mrn/kourrier/internal/mail"
	"github.com/theo-mrn/kourrier/internal/observability"
	"github.com/theo-mrn/kourrier/internal/queue"
	"github.com/theo-mrn/kourrier/internal/ratelimit"
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
	rdb    *redis.Client
	dead   *deadevent.Store
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
	dead := deadevent.NewStore(rdb, "kourrier", 100)
	d := New(Deps{
		Router:             cfg.Router,
		DeadLetters:        dead,
		DeadLetterUnrouted: []string{"strict"},
		Limiter:            ratelimit.NewKeyed(rdb, "kourrier"),
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
	return &fixture{d: d, q: q, mr: mr, rdb: rdb, dead: dead, tenant: shop, gate: gate}
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

func (f *fixture) deadEntries(t *testing.T) []deadevent.Entry {
	t.Helper()
	entries, err := f.dead.List(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestDeadLetters(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Partial failure: only the failed route is kept for replay.
	partial := f.d.Dispatch(ctx, "test", f.tenant, ev(t, "user.created", "/users", "1", `{"email":"not-an-address"}`))
	if partial.Outcome != Accepted || partial.DeadLetterID == "" {
		t.Fatalf("partial: %+v", partial)
	}
	// Every route fails.
	rejected := f.d.Dispatch(ctx, "test", f.tenant, ev(t, "user.created", "/users", "2", `"a string"`))
	if rejected.Outcome != Rejected || rejected.DeadLetterID == "" {
		t.Fatalf("rejected: %+v", rejected)
	}
	// Unrouted events are kept on strict sources only.
	if rep := f.d.Dispatch(ctx, "test", f.tenant, ev(t, "user.typo", "/users", "3", `{}`)); rep.DeadLetterID != "" {
		t.Fatalf("unrouted event kept on a non-strict source: %+v", rep)
	}
	if rep := f.d.Dispatch(ctx, "strict", f.tenant, ev(t, "user.typo", "/users", "4", `{}`)); rep.Outcome != Ignored || rep.DeadLetterID == "" {
		t.Fatalf("unrouted event on a strict source: %+v", rep)
	}

	entries := f.deadEntries(t)
	if len(entries) != 3 {
		t.Fatalf("%d dead entries, want 3", len(entries))
	}
	if e := entries[0]; e.Reason != deadevent.Partial || len(e.Routes) != 1 || e.Routes[0] != "welcome" || e.Tenant != "shop" {
		t.Fatalf("partial entry: %+v", e)
	}
	if e := entries[1]; e.Reason != deadevent.Rejected || len(e.Routes) != 2 {
		t.Fatalf("rejected entry: %+v", e)
	}
	if e := entries[2]; e.Reason != deadevent.Unrouted || e.Routes != nil || !strings.Contains(e.Detail, "user.typo") {
		t.Fatalf("unrouted entry: %+v", e)
	}

	// Replaying the partial entry only runs the failed route, which still
	// fails: it is kept again, and the admin e-mail is not resent.
	stored, err := cloudevent.ParseStructured(entries[0].Event)
	if err != nil {
		t.Fatal(err)
	}
	before := f.queued(t)
	again := f.d.Replay(ctx, f.tenant, stored, entries[0].Routes)
	if again.Outcome != Rejected || len(again.Errors) != 1 || again.DeadLetterID == "" || f.queued(t) != before {
		t.Fatalf("replay: %+v (queued %d -> %d)", again, before, f.queued(t))
	}
}

func TestDeadLetterFailureRetries(t *testing.T) {
	f := newFixture(t)
	// The e-mail queue works but the dead-letter stream cannot be written:
	// the event must be redelivered, not silently dropped.
	if err := f.mr.Set("{kourrier}:events:dead", "not a stream"); err != nil {
		t.Fatal(err)
	}
	rep := f.d.Dispatch(context.Background(), "test", f.tenant, ev(t, "user.created", "/users", "1", `{"email":"not-an-address"}`))
	if rep.Outcome != Retry || !errors.Is(rep.Err, submit.ErrUnavailable) {
		t.Fatalf("unexpected report %+v", rep)
	}
}

func TestTenantRateLimit(t *testing.T) {
	f := newFixture(t)
	f.d.limits = routing.Limits{Tenants: map[string]routing.Limit{"shop": {EmailsPerSecond: 0.001, Burst: 3}}}
	ctx := context.Background()
	// Two e-mails per event, three tokens: the second event is throttled
	// after its first route.
	if rep := f.d.Dispatch(ctx, "test", f.tenant, ev(t, "user.created", "/users", "1", `{"email":"a@b.fr"}`)); rep.Outcome != Accepted {
		t.Fatalf("first event: %+v", rep)
	}
	rep := f.d.Dispatch(ctx, "test", f.tenant, ev(t, "user.created", "/users", "2", `{"email":"a@b.fr"}`))
	if rep.Outcome != Retry || !errors.Is(rep.Err, ErrTenantRateLimited) || rep.RetryAfter <= 0 || len(rep.Emails) != 1 {
		t.Fatalf("throttled event: %+v", rep)
	}
	// Other tenants keep their own budget.
	f.d.limits.Default = routing.Limit{}
	other := &tenant.Tenant{ID: "other", DefaultSender: mail.Address{Email: "a@o.fr"}, AllowedSenders: []string{"a@o.fr"}}
	if rep := f.d.Dispatch(ctx, "test", other, ev(t, "user.created", "/users", "3", `{}`)); rep.Outcome != Ignored {
		t.Fatalf("other tenant: %+v", rep)
	}
}
