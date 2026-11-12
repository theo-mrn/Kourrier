// Package deadevent keeps the events Kourrier could not fully turn into
// e-mails, so that they can be inspected and replayed once the cause is fixed
// (a route corrected, a template added). Without it, an event rejected
// because of a route bug would be lost: the broker considers it settled.
//
// Entries live in a capped Redis stream, {prefix}:events:dead, next to the
// e-mail dead-letter queue. They contain event payloads, hence personal data:
// the stream is capped and entries are deleted once replayed.
package deadevent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Reason tells why an event was dead-lettered. It is a metric label.
type Reason string

const (
	// Rejected: every matching route failed permanently.
	Rejected Reason = "rejected"
	// Partial: some routes produced their e-mail, others failed; only the
	// failed routes are replayed.
	Partial Reason = "partial"
	// Unrouted: no route matched, on a source configured to keep such
	// events (on_unrouted: dead_letter).
	Unrouted Reason = "unrouted"
	// Invalid: the payload is not a valid CloudEvent. It is kept for
	// inspection but cannot be replayed.
	Invalid Reason = "invalid"
)

// maxDetail bounds the stored error description.
const maxDetail = 1024

// Entry is a dead-lettered event.
type Entry struct {
	// ID is the stream entry ID, set by List.
	ID     string
	Tenant string
	// Source is the transport the event came from ("http" or a NATS source).
	Source string
	Reason Reason
	Detail string
	// Routes lists the routes to replay; empty means every matching route.
	Routes []string
	// Event is the event in structured JSON format, or the raw payload
	// (possibly empty) when Reason is Invalid.
	Event    []byte
	FailedAt time.Time
}

// Replayable reports whether the entry holds a usable event.
func (e *Entry) Replayable() bool { return e.Reason != Invalid && len(e.Event) > 0 }

// Store is the Redis-backed dead-letter stream for events.
type Store struct {
	rdb    redis.UniversalClient
	key    string
	maxLen int64
	// waitReplicas mirrors KOURRIER_REDIS_WAIT_REPLICAS: an entry is only
	// reported as stored once replicated, since the broker then forgets the
	// event.
	waitReplicas int
	waitTimeout  time.Duration
}

// WithReplication makes Add wait for n replicas (Redis WAIT), up to timeout.
func (s *Store) WithReplication(n int, timeout time.Duration) *Store {
	s.waitReplicas, s.waitTimeout = n, timeout
	return s
}

// NewStore creates a Store. maxLen caps the stream, approximately.
func NewStore(rdb redis.UniversalClient, prefix string, maxLen int64) *Store {
	if maxLen <= 0 {
		maxLen = 10000
	}
	return &Store{rdb: rdb, key: "{" + prefix + "}:events:dead", maxLen: maxLen}
}

// Add appends e and returns its entry ID.
func (s *Store) Add(ctx context.Context, e Entry) (string, error) {
	detail := e.Detail
	if len(detail) > maxDetail {
		detail = strings.ToValidUTF8(detail[:maxDetail], "")
	}
	if e.FailedAt.IsZero() {
		e.FailedAt = time.Now().UTC()
	}
	id, err := s.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: s.key,
		MaxLen: s.maxLen,
		Approx: true,
		Values: []any{
			"tenant", e.Tenant, "source", e.Source, "reason", string(e.Reason), "detail", detail,
			"routes", strings.Join(e.Routes, ","), "event", e.Event,
			"failed_at", e.FailedAt.UTC().Format(time.RFC3339Nano),
		},
	}).Result()
	if err != nil {
		return "", fmt.Errorf("deadevent: add: %w", err)
	}
	if s.waitReplicas > 0 {
		n, err := s.rdb.Do(ctx, "WAIT", s.waitReplicas, s.waitTimeout.Milliseconds()).Int64()
		if err != nil {
			return "", fmt.Errorf("deadevent: wait replicas: %w", err)
		}
		if n < int64(s.waitReplicas) {
			return "", fmt.Errorf("deadevent: entry %s acknowledged by %d of %d replica(s)", id, n, s.waitReplicas)
		}
	}
	return id, nil
}

// List returns up to count entries, oldest first.
func (s *Store) List(ctx context.Context, count int64) ([]Entry, error) {
	return s.ListAfter(ctx, "", count)
}

// ListAfter returns up to count entries following the entry ID after (all
// entries when after is empty), oldest first.
func (s *Store) ListAfter(ctx context.Context, after string, count int64) ([]Entry, error) {
	start := "-"
	if after != "" {
		start = "(" + after
	}
	msgs, err := s.rdb.XRangeN(ctx, s.key, start, "+", count).Result()
	if err != nil {
		return nil, fmt.Errorf("deadevent: list: %w", err)
	}
	out := make([]Entry, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, decode(m))
	}
	return out, nil
}

// Get returns the entries with the given IDs that still exist, in the order
// of ids.
func (s *Store) Get(ctx context.Context, ids ...string) ([]Entry, error) {
	cmds := make([]*redis.XMessageSliceCmd, len(ids))
	_, err := s.rdb.Pipelined(ctx, func(p redis.Pipeliner) error {
		for i, id := range ids {
			cmds[i] = p.XRange(ctx, s.key, id, id)
		}
		return nil
	})
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("deadevent: get: %w", err)
	}
	out := make([]Entry, 0, len(ids))
	for _, c := range cmds {
		for _, m := range c.Val() {
			out = append(out, decode(m))
		}
	}
	return out, nil
}

func decode(m redis.XMessage) Entry {
	str := func(k string) string { v, _ := m.Values[k].(string); return v }
	e := Entry{
		ID: m.ID, Tenant: str("tenant"), Source: str("source"), Reason: Reason(str("reason")),
		Detail: str("detail"), Event: []byte(str("event")),
	}
	if r := str("routes"); r != "" {
		e.Routes = strings.Split(r, ",")
	}
	e.FailedAt, _ = time.Parse(time.RFC3339Nano, str("failed_at"))
	return e
}

// Delete removes entries, typically after a successful replay.
func (s *Store) Delete(ctx context.Context, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	if err := s.rdb.XDel(ctx, s.key, ids...).Err(); err != nil {
		return fmt.Errorf("deadevent: delete: %w", err)
	}
	return nil
}

// Len returns the number of entries.
func (s *Store) Len(ctx context.Context) (int64, error) {
	n, err := s.rdb.XLen(ctx, s.key).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return 0, fmt.Errorf("deadevent: len: %w", err)
	}
	return n, nil
}
