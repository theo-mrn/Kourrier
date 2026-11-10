package natsjs

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/theo-mrn/kourrier/internal/cloudevent"
	"github.com/theo-mrn/kourrier/internal/dispatch"
	"github.com/theo-mrn/kourrier/internal/mail"
	"github.com/theo-mrn/kourrier/internal/observability"
	"github.com/theo-mrn/kourrier/internal/routing"
	"github.com/theo-mrn/kourrier/internal/tenant"
)

func runServer(t *testing.T) *server.Server {
	t.Helper()
	s, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go s.Start()
	if !s.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats server not ready")
	}
	t.Cleanup(s.Shutdown)
	return s
}

// fakeDispatcher records events and answers with the outcomes of script, in
// order; the last outcome repeats.
type fakeDispatcher struct {
	mu     sync.Mutex
	script []dispatch.Outcome
	events []*cloudevent.Event
	seen   chan struct{}
}

func (f *fakeDispatcher) Dispatch(_ context.Context, source string, tn *tenant.Tenant, e *cloudevent.Event) dispatch.Report {
	f.mu.Lock()
	defer f.mu.Unlock()
	if source != "orders" || tn.ID != "shop" {
		panic("unexpected source or tenant")
	}
	f.events = append(f.events, e)
	out := f.script[min(len(f.events), len(f.script))-1]
	f.seen <- struct{}{}
	return dispatch.Report{Outcome: out}
}

func (f *fakeDispatcher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

type fixture struct {
	js      jetstream.JetStream
	disp    *fakeDispatcher
	metrics *observability.Metrics
	stop    func()
}

func newFixture(t *testing.T, script ...dispatch.Outcome) *fixture {
	t.Helper()
	s := runServer(t)
	nc, err := nats.Connect(s.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{Name: "ORDERS", Subjects: []string{"orders.>"}}); err != nil {
		t.Fatal(err)
	}

	tenants, err := tenant.NewRegistry(&tenant.Tenant{
		ID: "shop", APIKeys: []tenant.APIKey{{SHA256: tenant.HashKey("kr_shop")}},
		DefaultSender: mail.Address{Email: "a@shop.fr"}, AllowedSenders: []string{"a@shop.fr"},
	})
	if err != nil {
		t.Fatal(err)
	}
	disp := &fakeDispatcher{script: script, seen: make(chan struct{}, 100)}
	metrics := observability.NewMetrics("test")
	c, err := New(Config{URL: s.ClientURL(), Name: "test"}, []routing.NATSSource{{
		Name: "orders", Tenant: "shop", Stream: "ORDERS", Consumer: "kourrier",
		FilterSubjects: []string{"orders.>"}, AckWait: 30 * time.Second,
	}}, Deps{
		Tenants: tenants, Dispatcher: disp, Metrics: metrics,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = c.Run(runCtx); close(done) }()
	stop := func() { cancel(); <-done }
	t.Cleanup(stop)
	waitConsumer(t, js, "ORDERS", "kourrier")
	return &fixture{js: js, disp: disp, metrics: metrics, stop: stop}
}

// waitConsumer waits for Kourrier to create its consumer: with the default
// "new" deliver policy, events published before that are not delivered.
func waitConsumer(t *testing.T, js jetstream.JetStream, stream, name string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := js.Consumer(context.Background(), stream, name); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("consumer not created")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (f *fixture) publish(t *testing.T, msg *nats.Msg) {
	t.Helper()
	if _, err := f.js.PublishMsg(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) waitDispatched(t *testing.T, n int) {
	t.Helper()
	for range n {
		select {
		case <-f.disp.seen:
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d event(s) dispatched, want %d", f.disp.count(), n)
		}
	}
}

// waitSettled waits until the consumer has no pending nor unacknowledged
// message.
func (f *fixture) waitSettled(t *testing.T) *jetstream.ConsumerInfo {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		cons, err := f.js.Consumer(context.Background(), "ORDERS", "kourrier")
		if err == nil {
			info, err := cons.Info(context.Background())
			if err == nil && info.NumAckPending == 0 && info.NumPending == 0 && info.Delivered.Consumer > 0 {
				return info
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("consumer did not settle")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

const structured = `{"specversion":"1.0","id":"evt-1","source":"/shop","type":"order.paid","data":{"email":"a@b.fr"}}`

func TestConsumesStructuredAndBinaryEvents(t *testing.T) {
	f := newFixture(t, dispatch.Accepted)

	f.publish(t, &nats.Msg{Subject: "orders.paid", Data: []byte(structured),
		Header: nats.Header{"Content-Type": []string{cloudevent.ContentTypeStructured}}})
	// No header: structured mode of the NATS binding 1.0 (outbox relays).
	f.publish(t, &nats.Msg{Subject: "orders.paid", Data: []byte(strings.Replace(structured, "evt-1", "evt-3", 1))})
	f.publish(t, &nats.Msg{Subject: "orders.paid", Data: []byte(`{"email":"a@b.fr"}`), Header: nats.Header{
		"ce-specversion": []string{"1.0"}, "ce-id": []string{"evt-2"}, "ce-source": []string{"/shop"},
		"ce-type": []string{"order.paid"}, "content-type": []string{"application/json"},
	}})
	f.waitDispatched(t, 3)
	info := f.waitSettled(t)
	if info.NumRedelivered != 0 {
		t.Fatalf("unexpected redeliveries: %+v", info)
	}
	if f.disp.events[0].ID != "evt-1" || f.disp.events[1].ID != "evt-3" || f.disp.events[2].ID != "evt-2" {
		t.Fatalf("unexpected events %+v", f.disp.events)
	}
	if info.Config.MaxDeliver != -1 || info.Config.AckPolicy != jetstream.AckExplicitPolicy ||
		info.Config.MaxAckPending != defaultMaxAckPending || info.Config.DeliverPolicy != jetstream.DeliverNewPolicy {
		t.Fatalf("unexpected consumer config %+v", info.Config)
	}
}

func TestTerminatesInvalidAndRejectedEvents(t *testing.T) {
	f := newFixture(t, dispatch.Rejected)

	// Not an event: terminated without reaching the dispatcher.
	f.publish(t, &nats.Msg{Subject: "orders.paid", Data: []byte("hello")})
	f.publish(t, &nats.Msg{Subject: "orders.paid", Data: []byte(structured),
		Header: nats.Header{"Content-Type": []string{cloudevent.ContentTypeStructured}}})
	f.waitDispatched(t, 1)
	info := f.waitSettled(t)
	if info.Delivered.Consumer != 2 || info.NumRedelivered != 0 || f.disp.count() != 1 {
		t.Fatalf("terminated messages must not be redelivered: %+v, dispatched %d", info, f.disp.count())
	}
}

func TestRedeliversOnRetry(t *testing.T) {
	f := newFixture(t, dispatch.Retry, dispatch.Accepted)
	f.publish(t, &nats.Msg{Subject: "orders.paid", Data: []byte(structured),
		Header: nats.Header{"Content-Type": []string{cloudevent.ContentTypeStructured}}})
	f.waitDispatched(t, 2) // the first Nak delays the redelivery by 1s
	info := f.waitSettled(t)
	if info.Delivered.Consumer != 2 {
		t.Fatalf("expected one redelivery: %+v", info)
	}
}

func TestDrainsOnShutdown(t *testing.T) {
	f := newFixture(t, dispatch.Accepted)
	f.publish(t, &nats.Msg{Subject: "orders.paid", Data: []byte(structured),
		Header: nats.Header{"Content-Type": []string{cloudevent.ContentTypeStructured}}})
	f.waitDispatched(t, 1)
	f.stop()
	f.waitSettled(t)
}

func (f *fixture) sourceUp() float64 {
	return testutil.ToFloat64(f.metrics.EventSourceUp.WithLabelValues("orders"))
}

func TestSourceUpAfterFirstConnection(t *testing.T) {
	f := newFixture(t, dispatch.Accepted)
	deadline := time.Now().Add(5 * time.Second)
	for f.sourceUp() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("kourrier_event_source_up never reached 1")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRebindsDeletedConsumer(t *testing.T) {
	f := newFixture(t, dispatch.Accepted)
	if err := f.js.DeleteConsumer(context.Background(), "ORDERS", "kourrier"); err != nil {
		t.Fatal(err)
	}
	// Kourrier notices the deletion and recreates its consumer; events
	// published afterwards are consumed again.
	deadline := time.Now().Add(15 * time.Second)
	for {
		f.publish(t, &nats.Msg{Subject: "orders.paid", Data: []byte(structured)})
		select {
		case <-f.disp.seen:
			return
		case <-time.After(500 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("consumer was not rebound after deletion")
		}
	}
}
