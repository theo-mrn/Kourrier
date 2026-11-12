// Package activity keeps a capped feed of the latest e-mail status changes
// (ADR 016), so that operators can browse recent e-mails: a status alone can
// only be read by its ID.
//
// The feed is observability, not a source of truth: it is written on a best
// effort basis after the status itself, and only holds the latest entries.
// Recipients are masked as in the logs.
package activity

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/theo-mrn/kourrier/internal/status"
)

// maxErrorLen bounds the error kept in an entry: provider errors can be long
// and the feed is capped in entries, not in bytes.
const maxErrorLen = 500

// Entry is a status change.
type Entry struct {
	// ID is the stream entry ID, set by List. It doubles as a cursor.
	ID       string       `json:"id"`
	EmailID  string       `json:"email_id"`
	Tenant   string       `json:"tenant"`
	State    status.State `json:"state"`
	Template string       `json:"template,omitempty"`
	// To holds the masked recipients (j***@example.com).
	To []string `json:"to,omitempty"`
	// Origin is "api" or "event" for queued e-mails.
	Origin  string    `json:"origin,omitempty"`
	Attempt int       `json:"attempt,omitempty"`
	Error   string    `json:"error,omitempty"`
	At      time.Time `json:"at"`
}

// Store is the Redis stream holding the feed.
type Store struct {
	rdb    redis.UniversalClient
	key    string
	maxLen int64
}

// NewStore creates a Store keeping about maxLen entries.
func NewStore(rdb redis.UniversalClient, prefix string, maxLen int64) *Store {
	return &Store{rdb: rdb, key: "{" + prefix + "}:activity", maxLen: maxLen}
}

// Prepare returns the stream key, the fields of e and the maximum length of
// the stream, for writers that must append e atomically with other keys
// (queue.RedisStreams.EnqueueAtomic): a "queued" entry is then never missing,
// nor written after the "sent" of a fast worker.
func (s *Store) Prepare(e Entry) (key string, fields []string, maxLen int64) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	if len(e.Error) > maxErrorLen {
		e.Error = e.Error[:maxErrorLen] + "…"
	}
	fields = []string{"email", e.EmailID, "tenant", e.Tenant, "state", string(e.State)}
	for _, kv := range [][2]string{
		{"template", e.Template}, {"to", strings.Join(e.To, ",")}, {"origin", e.Origin}, {"error", e.Error},
	} {
		if kv[1] != "" {
			fields = append(fields, kv[0], kv[1])
		}
	}
	if e.Attempt > 0 {
		fields = append(fields, "attempt", strconv.Itoa(e.Attempt))
	}
	fields = append(fields, "at", e.At.UTC().Format(time.RFC3339Nano))
	return s.key, fields, s.maxLen
}

// Record appends e to the feed. The stream is trimmed approximately, which
// keeps XADD O(1).
func (s *Store) Record(ctx context.Context, e Entry) error {
	key, fields, maxLen := s.Prepare(e)
	err := s.rdb.XAdd(ctx, &redis.XAddArgs{Stream: key, MaxLen: maxLen, Approx: true, Values: fields}).Err()
	if err != nil {
		return fmt.Errorf("activity: record: %w", err)
	}
	return nil
}

// Query filters the feed. Zero values match everything.
type Query struct {
	Tenant  string
	State   status.State
	EmailID string
	// Before is a cursor: only entries older than this entry ID are returned.
	Before string
	// Limit is the maximum number of entries (default 50, at most 500).
	Limit int
}

// Page is a page of entries, newest first.
type Page struct {
	Entries []Entry `json:"entries"`
	// Next is the cursor of the following page, empty on the last one.
	Next string `json:"next,omitempty"`
}

const (
	defaultLimit = 50
	maxLimit     = 500
	// scanBatch is the number of entries read per round trip while filtering.
	scanBatch = 200
)

// List returns the entries matching q, newest first. Filters are applied
// while scanning the stream, which is bounded by its maximum length.
func (s *Store) List(ctx context.Context, q Query) (Page, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	limit = min(limit, maxLimit)

	page := Page{Entries: []Entry{}}
	end := "+"
	if q.Before != "" {
		end = "(" + q.Before
	}
	for {
		msgs, err := s.rdb.XRevRangeN(ctx, s.key, end, "-", scanBatch).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return Page{}, fmt.Errorf("activity: list: %w", err)
		}
		for i, m := range msgs {
			e := decode(m)
			if !q.matches(e) {
				continue
			}
			page.Entries = append(page.Entries, e)
			if len(page.Entries) == limit {
				// More entries may follow: this one, or a later one in the
				// batch, is not the oldest of the stream.
				if i < len(msgs)-1 || len(msgs) == scanBatch {
					page.Next = e.ID
				}
				return page, nil
			}
		}
		if len(msgs) < scanBatch {
			return page, nil
		}
		end = "(" + msgs[len(msgs)-1].ID
	}
}

func (q Query) matches(e Entry) bool {
	return (q.Tenant == "" || e.Tenant == q.Tenant) &&
		(q.State == "" || e.State == q.State) &&
		(q.EmailID == "" || e.EmailID == q.EmailID)
}

func decode(m redis.XMessage) Entry {
	str := func(k string) string { v, _ := m.Values[k].(string); return v }
	e := Entry{
		ID: m.ID, EmailID: str("email"), Tenant: str("tenant"), State: status.State(str("state")),
		Template: str("template"), Origin: str("origin"), Error: str("error"),
	}
	if to := str("to"); to != "" {
		e.To = strings.Split(to, ",")
	}
	e.Attempt, _ = strconv.Atoi(str("attempt"))
	e.At, _ = time.Parse(time.RFC3339Nano, str("at"))
	return e
}

// Stats summarises the feed over a time window, for the console dashboard.
type Stats struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	Step int64     `json:"step_seconds"`
	// Complete is false when the feed does not reach back to From (it is
	// capped in entries): the figures then cover a shorter window.
	Complete bool        `json:"complete"`
	Totals   Totals      `json:"totals"`
	Buckets  []Bucket    `json:"buckets"`
	Latency  *Latency    `json:"latency"`
	Template []Breakdown `json:"templates"`
	Tenants  []Breakdown `json:"tenants"`
}

// Totals counts the status changes of the window.
type Totals struct {
	Queued  int `json:"queued"`
	Sent    int `json:"sent"`
	Failed  int `json:"failed"`
	Retries int `json:"retries"`
}

// Bucket counts the emails sent and failed during one step of the window.
type Bucket struct {
	Start  time.Time `json:"start"`
	Sent   int       `json:"sent"`
	Failed int       `json:"failed"`
}

// Latency is the delivery time (queued to sent) of the emails queued and sent
// within the window, in milliseconds.
type Latency struct {
	P50 int64 `json:"p50_ms"`
	P95 int64 `json:"p95_ms"`
}

// Breakdown counts sent and failed emails per template or tenant.
type Breakdown struct {
	Name   string `json:"name"`
	Sent   int    `json:"sent"`
	Failed int    `json:"failed"`
}

// maxBreakdown bounds the template and tenant breakdowns.
const maxBreakdown = 6

// Stats reads the entries recorded between from and to and aggregates them in
// buckets of step. Stream IDs are millisecond timestamps, so the window is
// read directly, in batches.
func (s *Store) Stats(ctx context.Context, from, to time.Time, step time.Duration) (Stats, error) {
	if step <= 0 || !from.Before(to) {
		return Stats{}, errors.New("activity: invalid stats window")
	}
	n := int(to.Sub(from) / step)
	st := Stats{From: from, To: to, Step: int64(step / time.Second), Buckets: make([]Bucket, n)}
	for i := range st.Buckets {
		st.Buckets[i].Start = from.Add(time.Duration(i) * step)
	}

	oldest, err := s.rdb.XRangeN(ctx, s.key, "-", "+", 1).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return Stats{}, fmt.Errorf("activity: stats: %w", err)
	}
	st.Complete = len(oldest) == 1 && !decode(oldest[0]).At.After(from)

	queuedAt := map[string]time.Time{}
	var latencies []int64
	templates, tenants := map[string]*Breakdown{}, map[string]*Breakdown{}
	count := func(m map[string]*Breakdown, name string, sent bool) {
		if name == "" {
			name = "inline"
		}
		b := m[name]
		if b == nil {
			b = &Breakdown{Name: name}
			m[name] = b
		}
		if sent {
			b.Sent++
		} else {
			b.Failed++
		}
	}

	start := strconv.FormatInt(from.UnixMilli(), 10)
	end := strconv.FormatInt(to.UnixMilli(), 10)
	for {
		msgs, err := s.rdb.XRangeN(ctx, s.key, start, end, 1000).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return Stats{}, fmt.Errorf("activity: stats: %w", err)
		}
		for _, m := range msgs {
			e := decode(m)
			i := int(e.At.Sub(from) / step)
			switch e.State {
			case status.Queued:
				st.Totals.Queued++
				queuedAt[e.EmailID] = e.At
			case status.Retrying:
				st.Totals.Retries++
			case status.Sent, status.Failed:
				sent := e.State == status.Sent
				if sent {
					st.Totals.Sent++
					if q, ok := queuedAt[e.EmailID]; ok {
						latencies = append(latencies, e.At.Sub(q).Milliseconds())
					}
				} else {
					st.Totals.Failed++
				}
				if i >= 0 && i < n {
					if sent {
						st.Buckets[i].Sent++
					} else {
						st.Buckets[i].Failed++
					}
				}
				count(templates, e.Template, sent)
				count(tenants, e.Tenant, sent)
			}
		}
		if len(msgs) < 1000 {
			break
		}
		start = "(" + msgs[len(msgs)-1].ID
	}

	if len(latencies) > 0 {
		slices.Sort(latencies)
		pick := func(q float64) int64 { return latencies[int(q*float64(len(latencies)-1))] }
		st.Latency = &Latency{P50: pick(0.5), P95: pick(0.95)}
	}
	st.Template, st.Tenants = top(templates), top(tenants)
	return st, nil
}

// top returns the busiest entries of m, by total emails.
func top(m map[string]*Breakdown) []Breakdown {
	out := make([]Breakdown, 0, len(m))
	for _, b := range m {
		out = append(out, *b)
	}
	slices.SortFunc(out, func(a, b Breakdown) int {
		if d := (b.Sent + b.Failed) - (a.Sent + a.Failed); d != 0 {
			return d
		}
		return strings.Compare(a.Name, b.Name)
	})
	if len(out) > maxBreakdown {
		out = out[:maxBreakdown]
	}
	return out
}
