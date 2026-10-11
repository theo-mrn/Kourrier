package queue

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/theo-mrn/kourrier/internal/mail"
)

func newTestQueue(t *testing.T, consumer string, rdb *redis.Client) *RedisStreams {
	t.Helper()
	q, err := NewRedisStreams(context.Background(), rdb, RedisConfig{
		Consumer:          consumer,
		Block:             10 * time.Millisecond,
		VisibilityTimeout: time.Minute,
		ReclaimInterval:   time.Nanosecond,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func setup(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	return mr, rdb
}

func job(id string) *Job {
	return &Job{ID: id, TenantID: "yner", EnqueuedAt: time.Now().UTC(), Request: mail.Request{
		From: mail.Address{Email: "a@yner.fr"}, To: []mail.Address{{Email: "b@example.com"}},
	}}
}

func receive(t *testing.T, q *RedisStreams) *Delivery {
	t.Helper()
	d, err := q.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func stats(t *testing.T, q *RedisStreams) Stats {
	t.Helper()
	s, err := q.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestEnqueueReceiveAck(t *testing.T) {
	_, rdb := setup(t)
	q := newTestQueue(t, "c1", rdb)
	ctx := context.Background()

	if d := receive(t, q); d != nil {
		t.Fatalf("empty queue returned %+v", d)
	}
	if err := q.Enqueue(ctx, job("j1")); err != nil {
		t.Fatal(err)
	}
	if s := stats(t, q); s.Ready != 1 {
		t.Fatalf("stats = %+v", s)
	}
	d := receive(t, q)
	if d == nil || d.Job.ID != "j1" || d.Job.Request.To[0].Email != "b@example.com" {
		t.Fatalf("unexpected delivery %+v", d)
	}
	if s := stats(t, q); s.Pending != 1 || s.Ready != 0 {
		t.Fatalf("stats = %+v", s)
	}
	if err := q.Ack(ctx, d); err != nil {
		t.Fatal(err)
	}
	if s := stats(t, q); s != (Stats{}) {
		t.Fatalf("queue should be empty after ack, stats = %+v", s)
	}
}

func TestRetryIsDelayedThenPromoted(t *testing.T) {
	_, rdb := setup(t)
	q := newTestQueue(t, "c1", rdb)
	ctx := context.Background()
	_ = q.Enqueue(ctx, job("j1"))

	d := receive(t, q)
	d.Job.Attempts = 1
	if err := q.Retry(ctx, d, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s := stats(t, q); s.Delayed != 1 || s.Pending != 0 || s.Ready != 0 {
		t.Fatalf("stats = %+v", s)
	}
	if n, _ := q.PromoteDue(ctx, 10); n != 0 {
		t.Fatalf("job promoted before its due time")
	}

	// Make it due now.
	rdb.ZAdd(ctx, q.delayed, redis.Z{Score: 0, Member: rdb.ZRange(ctx, q.delayed, 0, 0).Val()[0]})
	if n, err := q.PromoteDue(ctx, 10); err != nil || n != 1 {
		t.Fatalf("PromoteDue = %d, %v", n, err)
	}
	d = receive(t, q)
	if d == nil || d.Job.ID != "j1" || d.Job.Attempts != 1 {
		t.Fatalf("retried job not redelivered with its attempt count: %+v", d)
	}
}

func TestDeadLetterAndReplay(t *testing.T) {
	_, rdb := setup(t)
	q := newTestQueue(t, "c1", rdb)
	ctx := context.Background()
	_ = q.Enqueue(ctx, job("j1"))

	d := receive(t, q)
	d.Job.Attempts = 8
	if err := q.DeadLetter(ctx, d, "boom"); err != nil {
		t.Fatal(err)
	}
	if s := stats(t, q); s.Dead != 1 || s.Pending != 0 {
		t.Fatalf("stats = %+v", s)
	}
	dead, err := q.ListDead(ctx, 10)
	if err != nil || len(dead) != 1 || dead[0].Reason != "boom" || dead[0].Job.ID != "j1" {
		t.Fatalf("ListDead = %+v, %v", dead, err)
	}
	if n, err := q.ReplayDead(ctx, dead); err != nil || n != 1 {
		t.Fatalf("ReplayDead = %d, %v", n, err)
	}
	d = receive(t, q)
	if d == nil || d.Job.ID != "j1" || d.Job.Attempts != 0 {
		t.Fatalf("replayed job should restart with 0 attempts: %+v", d)
	}
	if s := stats(t, q); s.Dead != 0 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestAbandonedJobIsReclaimed(t *testing.T) {
	mr, rdb := setup(t)
	crashed := newTestQueue(t, "crashed-pod", rdb)
	healthy := newTestQueue(t, "healthy-pod", rdb)
	ctx := context.Background()
	_ = crashed.Enqueue(ctx, job("j1"))

	if d := receive(t, crashed); d == nil {
		t.Fatal("expected a delivery")
	}
	// The consumer dies without acking. Before the visibility timeout, the
	// job is not handed to anyone else.
	if d := receive(t, healthy); d != nil {
		t.Fatalf("job reclaimed too early: %+v", d)
	}
	mr.SetTime(time.Now().Add(2 * time.Minute))
	d := receive(t, healthy)
	if d == nil || d.Job.ID != "j1" {
		t.Fatalf("abandoned job not reclaimed: %+v", d)
	}
	if err := healthy.Ack(ctx, d); err != nil {
		t.Fatal(err)
	}
}

func TestUndecodablePayloadGoesToDeadLetter(t *testing.T) {
	_, rdb := setup(t)
	q := newTestQueue(t, "c1", rdb)
	ctx := context.Background()
	rdb.XAdd(ctx, &redis.XAddArgs{Stream: q.stream, Values: []any{"job", "{not json"}})

	if d := receive(t, q); d != nil {
		t.Fatalf("garbage should not be delivered: %+v", d)
	}
	if s := stats(t, q); s.Dead != 1 || s.Pending != 0 {
		t.Fatalf("stats = %+v", s)
	}
}
