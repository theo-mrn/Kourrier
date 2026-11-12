package queue

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

func TestEnqueueAtomic(t *testing.T) {
	mr, rdb := setup(t)
	q := newTestQueue(t, "c1", rdb)
	ctx := context.Background()
	guard := &Write{Key: "{kourrier}:idem:k", Value: []byte("record-1"), TTL: time.Hour}
	extra := Write{Key: "{kourrier}:status:1", Value: []byte("queued"), TTL: 2 * time.Hour}

	prev, err := q.EnqueueAtomic(ctx, job("1"), guard, extra)
	if err != nil || prev != nil {
		t.Fatalf("first EnqueueAtomic() = %q, %v", prev, err)
	}
	if v, _ := mr.Get(extra.Key); v != "queued" || mr.TTL(extra.Key) != 2*time.Hour || mr.TTL(guard.Key) != time.Hour {
		t.Fatalf("companion keys not written with their TTL: %q", v)
	}

	// Same guard: the existing record is returned and nothing is written.
	prev, err = q.EnqueueAtomic(ctx, job("2"), &Write{Key: guard.Key, Value: []byte("record-2"), TTL: time.Hour},
		Write{Key: "{kourrier}:status:2", Value: []byte("queued"), TTL: time.Hour})
	if err != nil || string(prev) != "record-1" {
		t.Fatalf("guarded EnqueueAtomic() = %q, %v", prev, err)
	}
	if mr.Exists("{kourrier}:status:2") {
		t.Fatal("a guarded no-op must not write companion keys")
	}
	if s := stats(t, q); s.Ready != 1 {
		t.Fatalf("ready = %d, want 1", s.Ready)
	}

	// Without guard the job is always enqueued.
	if _, err := q.EnqueueAtomic(ctx, job("3"), nil); err != nil {
		t.Fatal(err)
	}
	if s := stats(t, q); s.Ready != 2 {
		t.Fatalf("ready = %d, want 2", s.Ready)
	}
	if _, err := q.EnqueueAtomic(ctx, job("4"), nil, Write{Key: "{kourrier}:x", Value: []byte("v")}); err == nil {
		t.Fatal("a key without TTL must be refused")
	}
}

func TestEnqueueAtomicAppendsToStream(t *testing.T) {
	mr, rdb := setup(t)
	q := newTestQueue(t, "c1", rdb)
	ctx := context.Background()
	feed := func(id string) Write {
		return Write{Key: "{kourrier}:feed", Fields: []string{"email", id, "state", "queued"}, MaxLen: 100}
	}
	guard := &Write{Key: "{kourrier}:idem:k", Value: []byte("record"), TTL: time.Hour}

	if _, err := q.EnqueueAtomic(ctx, job("1"), guard, feed("1")); err != nil {
		t.Fatal(err)
	}
	// A guarded no-op appends nothing either.
	if _, err := q.EnqueueAtomic(ctx, job("2"), guard, feed("2")); err != nil {
		t.Fatal(err)
	}
	entries, err := mr.Stream("{kourrier}:feed")
	if err != nil || len(entries) != 1 {
		t.Fatalf("feed = %+v, %v", entries, err)
	}
	if got := entries[0].Values; len(got) != 4 || got[1] != "1" || got[3] != "queued" {
		t.Fatalf("entry = %v", got)
	}

	for _, bad := range []Write{
		{Key: "{kourrier}:feed", Fields: []string{"odd"}, MaxLen: 100},
		{Key: "{kourrier}:feed", Fields: []string{"k", "v"}},
	} {
		if _, err := q.EnqueueAtomic(ctx, job("3"), nil, bad); err == nil {
			t.Fatalf("invalid stream write %+v accepted", bad)
		}
	}
}

func TestEnqueueAtomicWaitsForReplicas(t *testing.T) {
	_, rdb := setup(t)
	q, err := NewRedisStreams(context.Background(), rdb, RedisConfig{
		Consumer: "c", WaitReplicas: 1, WaitTimeout: 10 * time.Millisecond,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	// miniredis has no replica: WAIT acknowledges 0 of 1.
	_, err = q.EnqueueAtomic(context.Background(), job("1"), nil)
	if !errors.Is(err, ErrNotReplicated) {
		t.Fatalf("err = %v, want ErrNotReplicated", err)
	}
}

func TestDeadLetterLookupAndDelete(t *testing.T) {
	_, rdb := setup(t)
	q := newTestQueue(t, "c1", rdb)
	ctx := context.Background()
	for _, id := range []string{"j1", "j2", "j3"} {
		_ = q.Enqueue(ctx, job(id))
		if err := q.DeadLetter(ctx, receive(t, q), "boom "+id); err != nil {
			t.Fatal(err)
		}
	}
	all, err := q.ListDead(ctx, 10)
	if err != nil || len(all) != 3 {
		t.Fatalf("ListDead = %+v, %v", all, err)
	}
	page, err := q.ListDeadAfter(ctx, all[0].ID, 10)
	if err != nil || len(page) != 2 || page[0].Job.ID != "j2" {
		t.Fatalf("ListDeadAfter = %+v, %v", page, err)
	}

	got, err := q.DeadJobs(ctx, all[2].ID, "0-1", all[0].ID)
	if err != nil || len(got) != 2 || got[0].Job.ID != "j3" || got[1].Job.ID != "j1" {
		t.Fatalf("DeadJobs = %+v, %v", got, err)
	}
	if n, err := q.DeleteDead(ctx, all[1].ID, "0-1"); err != nil || n != 1 {
		t.Fatalf("DeleteDead = %d, %v", n, err)
	}
	if s := stats(t, q); s.Dead != 2 {
		t.Fatalf("dead = %d, want 2", s.Dead)
	}
}
