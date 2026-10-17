package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/theo-mrn/kourrier/internal/mail"
	"github.com/theo-mrn/kourrier/internal/observability"
	"github.com/theo-mrn/kourrier/internal/provider/memory"
	"github.com/theo-mrn/kourrier/internal/queue"
	"github.com/theo-mrn/kourrier/internal/retry"
	"github.com/theo-mrn/kourrier/internal/status"
	"github.com/theo-mrn/kourrier/internal/storage"
	"github.com/theo-mrn/kourrier/internal/templates"
)

type fixture struct {
	pool     *Pool
	queue    *queue.RedisStreams
	statuses *status.Store
	mailer   *memory.Provider
	metrics  *observability.Metrics
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	q, err := queue.NewRedisStreams(context.Background(), rdb, queue.RedisConfig{Consumer: "test", Block: 10 * time.Millisecond}, logger)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		queue:    q,
		statuses: status.NewStore(rdb, "kourrier", time.Hour),
		mailer:   memory.New(),
		metrics:  observability.NewMetrics("test"),
	}
	store := storage.NewMemory(map[string]string{
		"yner/welcome/en/subject.txt": "Welcome {{.name}}",
		"yner/welcome/en/body.html":   "<p>Hi {{.name}}</p>",
	})
	f.pool = New(Config{Concurrency: 2, Retry: retry.Policy{MaxAttempts: 3, Base: time.Second, Max: time.Minute}}, Deps{
		Queue:    q,
		Renderer: templates.NewRenderer(store, templates.Options{DefaultLocale: "en"}),
		Provider: f.mailer,
		Statuses: f.statuses,
		Metrics:  f.metrics,
		Logger:   logger,
	})
	return f
}

func (f *fixture) enqueue(t *testing.T, req mail.Request) *queue.Delivery {
	t.Helper()
	ctx := context.Background()
	job := &queue.Job{ID: "0192f1b4-6c1e-7a3b-8c4d-0123456789ab", TenantID: "yner", EnqueuedAt: time.Now(), Request: req}
	if err := f.queue.Enqueue(ctx, job); err != nil {
		t.Fatal(err)
	}
	d, err := f.queue.Receive(ctx)
	if err != nil || d == nil {
		t.Fatalf("Receive = %v, %v", d, err)
	}
	return d
}

func (f *fixture) status(t *testing.T) *status.Status {
	t.Helper()
	st, err := f.statuses.Get(context.Background(), "0192f1b4-6c1e-7a3b-8c4d-0123456789ab")
	if err != nil || st == nil {
		t.Fatalf("status = %v, %v", st, err)
	}
	return st
}

func (f *fixture) stats(t *testing.T) queue.Stats {
	t.Helper()
	s, err := f.queue.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func templateRequest() mail.Request {
	return mail.Request{
		From:     mail.Address{Email: "no-reply@yner.fr"},
		To:       []mail.Address{{Email: "jane@example.com"}},
		Template: &mail.TemplateRef{Name: "welcome", Data: map[string]any{"name": "Jane"}},
		Tags:     map[string]string{"campaign": "onboarding"},
	}
}

func TestProcessSendsRenderedEmail(t *testing.T) {
	f := newFixture(t)
	f.pool.Process(context.Background(), f.enqueue(t, templateRequest()))

	msgs := f.mailer.Messages()
	if len(msgs) != 1 {
		t.Fatalf("sent %d messages", len(msgs))
	}
	m := msgs[0]
	if m.Subject != "Welcome Jane" || m.HTML != "<p>Hi Jane</p>" || m.To[0].Email != "jane@example.com" {
		t.Fatalf("unexpected message %+v", m)
	}
	if m.Tags["kourrier_tenant"] != "yner" || m.Tags["kourrier_template"] != "welcome" || m.Tags["campaign"] != "onboarding" {
		t.Fatalf("unexpected tags %v", m.Tags)
	}
	if st := f.status(t); st.State != status.Sent || st.Attempts != 1 || st.ProviderMessageID == "" {
		t.Fatalf("unexpected status %+v", st)
	}
	if s := f.stats(t); s != (queue.Stats{}) {
		t.Fatalf("job should be acked, stats = %+v", s)
	}
}

func TestProcessRetriesTransientErrors(t *testing.T) {
	f := newFixture(t)
	f.mailer.FailNext(errors.New("connection reset"))
	f.pool.Process(context.Background(), f.enqueue(t, templateRequest()))

	st := f.status(t)
	if st.State != status.Retrying || st.Attempts != 1 || st.NextAttemptAt == nil || st.LastError == "" {
		t.Fatalf("unexpected status %+v", st)
	}
	if s := f.stats(t); s.Delayed != 1 || s.Pending != 0 {
		t.Fatalf("job should be delayed, stats = %+v", s)
	}
}

func TestProcessDeadLettersPermanentErrors(t *testing.T) {
	f := newFixture(t)
	f.mailer.FailNext(retry.Permanent(errors.New("MessageRejected")))
	f.pool.Process(context.Background(), f.enqueue(t, templateRequest()))

	if st := f.status(t); st.State != status.Failed {
		t.Fatalf("unexpected status %+v", st)
	}
	if s := f.stats(t); s.Dead != 1 || s.Delayed != 0 {
		t.Fatalf("job should be dead-lettered, stats = %+v", s)
	}
}

func TestProcessDeadLettersMissingTemplate(t *testing.T) {
	f := newFixture(t)
	req := templateRequest()
	req.Template.Name = "missing"
	f.pool.Process(context.Background(), f.enqueue(t, req))

	if s := f.stats(t); s.Dead != 1 {
		t.Fatalf("missing template should not be retried, stats = %+v", s)
	}
	if len(f.mailer.Messages()) != 0 {
		t.Fatal("nothing should have been sent")
	}
}

func TestProcessGivesUpAfterMaxAttempts(t *testing.T) {
	f := newFixture(t)
	f.mailer.FailNext(errors.New("timeout"))
	d := f.enqueue(t, templateRequest())
	d.Job.Attempts = 2 // MaxAttempts is 3
	f.pool.Process(context.Background(), d)

	if st := f.status(t); st.State != status.Failed || st.Attempts != 3 {
		t.Fatalf("unexpected status %+v", st)
	}
	if s := f.stats(t); s.Dead != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestProcessSkipsJobsAlreadySent(t *testing.T) {
	f := newFixture(t)
	d := f.enqueue(t, templateRequest())
	if err := f.statuses.Put(context.Background(), &status.Status{ID: d.Job.ID, State: status.Sent}); err != nil {
		t.Fatal(err)
	}
	f.pool.Process(context.Background(), d)

	if len(f.mailer.Messages()) != 0 {
		t.Fatal("a job already sent must not be sent twice")
	}
	if s := f.stats(t); s != (queue.Stats{}) {
		t.Fatalf("redelivered job should be acked, stats = %+v", s)
	}
}

func TestProcessRequeuesOnShutdown(t *testing.T) {
	f := newFixture(t)
	f.pool.limiter = blockingLimiter{}
	ctx, cancel := context.WithCancel(context.Background())
	d := f.enqueue(t, templateRequest())
	cancel()
	f.pool.Process(ctx, d)

	if len(f.mailer.Messages()) != 0 {
		t.Fatal("nothing should be sent during shutdown")
	}
	if s := f.stats(t); s.Delayed != 1 || s.Pending != 0 {
		t.Fatalf("job should be handed back, stats = %+v", s)
	}
	if d.Job.Attempts != 0 {
		t.Fatal("a shutdown must not count as a failed attempt")
	}
}

func TestRunDrainsQueue(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for i := range 5 {
		job := &queue.Job{ID: string(rune('a' + i)), TenantID: "yner", EnqueuedAt: time.Now(), Request: templateRequest()}
		if err := f.queue.Enqueue(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { f.pool.Run(runCtx); close(done) }()

	deadline := time.Now().Add(5 * time.Second)
	for len(f.mailer.Messages()) < 5 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if n := len(f.mailer.Messages()); n != 5 {
		t.Fatalf("sent %d/5 messages", n)
	}
}

type blockingLimiter struct{}

func (blockingLimiter) Wait(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}
