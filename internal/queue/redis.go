package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisStreams implements Queue with Redis Streams and consumer groups,
// giving at-least-once delivery:
//
//   - {prefix}:stream   jobs ready to be processed (consumer group "workers")
//   - {prefix}:delayed  sorted set of jobs waiting for a retry, scored by due time
//   - {prefix}:dead     stream of jobs that failed permanently
//
// A job read by a consumer stays in the group's pending list until it is
// acknowledged. If the consumer dies, another one reclaims it with XAUTOCLAIM
// after VisibilityTimeout. Every multi-key operation runs in a Lua script or
// a MULTI block so that a job is never lost nor duplicated between keys. The
// prefix is wrapped in a hash tag so all keys live in the same Redis Cluster
// slot.
type RedisStreams struct {
	rdb      redis.UniversalClient
	logger   *slog.Logger
	cfg      RedisConfig
	stream   string
	delayed  string
	dead     string
	consumer string

	lastReclaim atomic.Int64
}

// RedisConfig configures RedisStreams.
type RedisConfig struct {
	// Prefix namespaces the Redis keys (default "kourrier").
	Prefix string
	// Group is the consumer group name (default "workers").
	Group string
	// Consumer identifies this process in the group, e.g. the pod name.
	Consumer string
	// Block is how long Receive waits for new jobs (default 2s).
	Block time.Duration
	// VisibilityTimeout is how long a job may stay unacknowledged before it is
	// handed to another consumer (default 5m). It must exceed the worst-case
	// processing time of a job.
	VisibilityTimeout time.Duration
	// ReclaimInterval is how often Receive looks for abandoned jobs (default 30s).
	ReclaimInterval time.Duration
	// DeadLetterMaxLen caps the dead-letter stream, approximately (default 10000).
	DeadLetterMaxLen int64
}

func (c *RedisConfig) defaults() {
	if c.Prefix == "" {
		c.Prefix = "kourrier"
	}
	if c.Group == "" {
		c.Group = "workers"
	}
	if c.Consumer == "" {
		c.Consumer = "kourrier"
	}
	if c.Block <= 0 {
		c.Block = 2 * time.Second
	}
	if c.VisibilityTimeout <= 0 {
		c.VisibilityTimeout = 5 * time.Minute
	}
	if c.ReclaimInterval <= 0 {
		c.ReclaimInterval = 30 * time.Second
	}
	if c.DeadLetterMaxLen <= 0 {
		c.DeadLetterMaxLen = 10000
	}
}

// NewRedisStreams creates the queue and its consumer group if needed.
func NewRedisStreams(ctx context.Context, rdb redis.UniversalClient, cfg RedisConfig, logger *slog.Logger) (*RedisStreams, error) {
	cfg.defaults()
	tag := "{" + cfg.Prefix + "}"
	q := &RedisStreams{
		rdb:      rdb,
		logger:   logger,
		cfg:      cfg,
		stream:   tag + ":stream",
		delayed:  tag + ":delayed",
		dead:     tag + ":dead",
		consumer: cfg.Consumer,
	}
	// Start the group at "0" rather than "$" so jobs enqueued before the
	// group existed are not skipped.
	err := rdb.XGroupCreateMkStream(ctx, q.stream, cfg.Group, "0").Err()
	if err != nil && !isBusyGroup(err) {
		return nil, fmt.Errorf("queue: create consumer group: %w", err)
	}
	return q, nil
}

const fieldJob = "job"

func (q *RedisStreams) Enqueue(ctx context.Context, job *Job) error {
	payload, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("queue: encode job: %w", err)
	}
	if err := q.rdb.XAdd(ctx, &redis.XAddArgs{Stream: q.stream, Values: []any{fieldJob, payload}}).Err(); err != nil {
		return fmt.Errorf("queue: enqueue: %w", err)
	}
	return nil
}

// Write is a key written atomically with a job by EnqueueAtomic.
type Write struct {
	Key   string
	Value []byte
	TTL   time.Duration
}

// enqueueAtomicScript writes the extra keys and adds the job in one step.
// With a guard (ARGV[2] = "1"), KEYS[2] is a SET NX: when it already exists,
// its value is returned and nothing is written.
var enqueueAtomicScript = redis.NewScript(`
if ARGV[2] == '1' then
  local prev = redis.call('GET', KEYS[2])
  if prev then return prev end
end
local argi = 3
for i = 2, #KEYS do
  redis.call('SET', KEYS[i], ARGV[argi], 'PX', ARGV[argi + 1])
  argi = argi + 2
end
redis.call('XADD', KEYS[1], '*', 'job', ARGV[1])
return false
`)

// EnqueueAtomic adds job and writes the given keys atomically. When guard is
// not nil, the operation only happens if guard.Key does not exist yet;
// otherwise the current value of guard.Key is returned and nothing changes.
//
// Atomicity is what makes idempotency sound: a guard (idempotency record)
// can never exist without its job, whatever fails in between (lost reply,
// timeout, crash). Every key must share the queue's hash tag so that the
// script stays valid on Redis Cluster.
func (q *RedisStreams) EnqueueAtomic(ctx context.Context, job *Job, guard *Write, writes ...Write) ([]byte, error) {
	payload, err := json.Marshal(job)
	if err != nil {
		return nil, fmt.Errorf("queue: encode job: %w", err)
	}
	keys := []string{q.stream}
	args := []any{payload, "0"}
	all := writes
	if guard != nil {
		args[1] = "1"
		all = append([]Write{*guard}, writes...)
	}
	for _, w := range all {
		ttl := w.TTL.Milliseconds()
		if ttl <= 0 {
			return nil, fmt.Errorf("queue: key %q needs a positive TTL", w.Key)
		}
		keys = append(keys, w.Key)
		args = append(args, w.Value, ttl)
	}
	prev, err := enqueueAtomicScript.Run(ctx, q.rdb, keys, args...).Text()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("queue: enqueue: %w", err)
	}
	return []byte(prev), nil
}

func (q *RedisStreams) Receive(ctx context.Context) (*Delivery, error) {
	if d, err := q.reclaim(ctx); d != nil || err != nil {
		return d, err
	}
	res, err := q.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    q.cfg.Group,
		Consumer: q.consumer,
		Streams:  []string{q.stream, ">"},
		Count:    1,
		Block:    q.cfg.Block,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if isNoGroup(err) {
			// The stream was deleted (e.g. FLUSHDB): recreate the group.
			_ = q.rdb.XGroupCreateMkStream(ctx, q.stream, q.cfg.Group, "0").Err()
		}
		return nil, fmt.Errorf("queue: read: %w", err)
	}
	if len(res) == 0 || len(res[0].Messages) == 0 {
		return nil, nil
	}
	return q.decode(ctx, res[0].Messages[0])
}

// reclaim takes over a job abandoned by a dead consumer. It runs at most once
// per ReclaimInterval, except while it keeps finding jobs, so that the backlog
// of a crashed pod is drained quickly.
func (q *RedisStreams) reclaim(ctx context.Context) (*Delivery, error) {
	now := time.Now().UnixNano()
	last := q.lastReclaim.Load()
	if now-last < int64(q.cfg.ReclaimInterval) || !q.lastReclaim.CompareAndSwap(last, now) {
		return nil, nil
	}
	msgs, _, err := q.rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream:   q.stream,
		Group:    q.cfg.Group,
		Consumer: q.consumer,
		MinIdle:  q.cfg.VisibilityTimeout,
		Start:    "0-0",
		Count:    1,
	}).Result()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("queue: reclaim: %w", err)
	}
	if len(msgs) == 0 {
		return nil, nil
	}
	q.lastReclaim.Store(0) // look again on the next call
	q.logger.WarnContext(ctx, "reclaimed abandoned job", "message_id", msgs[0].ID)
	return q.decode(ctx, msgs[0])
}

// decode parses a stream entry. Undecodable entries cannot ever succeed and
// are moved to the dead-letter stream right away.
func (q *RedisStreams) decode(ctx context.Context, msg redis.XMessage) (*Delivery, error) {
	raw, _ := msg.Values[fieldJob].(string)
	var job Job
	if err := json.Unmarshal([]byte(raw), &job); err != nil || job.ID == "" {
		q.logger.ErrorContext(ctx, "dropping undecodable job to dead-letter queue", "message_id", msg.ID, "error", err)
		_ = q.deadLetterRaw(ctx, msg.ID, raw, "undecodable payload")
		return nil, nil
	}
	return &Delivery{Job: &job, ID: msg.ID}, nil
}

func (q *RedisStreams) Ack(ctx context.Context, d *Delivery) error {
	_, err := q.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.XAck(ctx, q.stream, q.cfg.Group, d.ID)
		p.XDel(ctx, q.stream, d.ID)
		return nil
	})
	if err != nil {
		return fmt.Errorf("queue: ack: %w", err)
	}
	return nil
}

// retryScript atomically schedules the job and removes it from the stream.
var retryScript = redis.NewScript(`
redis.call('ZADD', KEYS[2], ARGV[3], ARGV[4])
redis.call('XACK', KEYS[1], ARGV[1], ARGV[2])
redis.call('XDEL', KEYS[1], ARGV[2])
return 1
`)

func (q *RedisStreams) Retry(ctx context.Context, d *Delivery, at time.Time) error {
	payload, err := json.Marshal(d.Job)
	if err != nil {
		return fmt.Errorf("queue: encode job: %w", err)
	}
	err = retryScript.Run(ctx, q.rdb, []string{q.stream, q.delayed},
		q.cfg.Group, d.ID, at.UnixMilli(), payload).Err()
	if err != nil {
		return fmt.Errorf("queue: retry: %w", err)
	}
	return nil
}

func (q *RedisStreams) DeadLetter(ctx context.Context, d *Delivery, reason string) error {
	payload, err := json.Marshal(d.Job)
	if err != nil {
		return fmt.Errorf("queue: encode job: %w", err)
	}
	return q.deadLetterRaw(ctx, d.ID, string(payload), reason)
}

func (q *RedisStreams) deadLetterRaw(ctx context.Context, id, payload, reason string) error {
	_, err := q.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.XAdd(ctx, &redis.XAddArgs{
			Stream: q.dead,
			MaxLen: q.cfg.DeadLetterMaxLen,
			Approx: true,
			Values: []any{fieldJob, payload, "reason", reason, "failed_at", time.Now().UTC().Format(time.RFC3339)},
		})
		p.XAck(ctx, q.stream, q.cfg.Group, id)
		p.XDel(ctx, q.stream, id)
		return nil
	})
	if err != nil {
		return fmt.Errorf("queue: dead-letter: %w", err)
	}
	return nil
}

// promoteScript moves due jobs from the delayed set back to the stream.
var promoteScript = redis.NewScript(`
local items = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1], 'LIMIT', 0, ARGV[2])
for _, item in ipairs(items) do
  redis.call('XADD', KEYS[2], '*', 'job', item)
  redis.call('ZREM', KEYS[1], item)
end
return #items
`)

// PromoteDue moves up to limit jobs whose retry time has come back to the
// stream and returns how many were moved. It is safe to run concurrently from
// several pods.
func (q *RedisStreams) PromoteDue(ctx context.Context, limit int) (int, error) {
	n, err := promoteScript.Run(ctx, q.rdb, []string{q.delayed, q.stream}, time.Now().UnixMilli(), limit).Int()
	if err != nil {
		return 0, fmt.Errorf("queue: promote: %w", err)
	}
	return n, nil
}

// RunScheduler calls PromoteDue every interval until ctx is done.
func (q *RedisStreams) RunScheduler(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for {
				n, err := q.PromoteDue(ctx, 100)
				if err != nil {
					if ctx.Err() == nil {
						q.logger.ErrorContext(ctx, "retry scheduler failed", "error", err)
					}
					break
				}
				if n < 100 {
					break
				}
			}
		}
	}
}

func (q *RedisStreams) Stats(ctx context.Context) (Stats, error) {
	var (
		length, delayed, dead *redis.IntCmd
		pending               *redis.XPendingCmd
	)
	_, err := q.rdb.Pipelined(ctx, func(p redis.Pipeliner) error {
		length = p.XLen(ctx, q.stream)
		pending = p.XPending(ctx, q.stream, q.cfg.Group)
		delayed = p.ZCard(ctx, q.delayed)
		dead = p.XLen(ctx, q.dead)
		return nil
	})
	if err != nil && !errors.Is(err, redis.Nil) {
		return Stats{}, fmt.Errorf("queue: stats: %w", err)
	}
	s := Stats{Delayed: delayed.Val(), Dead: dead.Val()}
	if pending.Val() != nil {
		s.Pending = pending.Val().Count
	}
	// Acknowledged entries are deleted, so the stream only holds ready and
	// pending jobs.
	s.Ready = max(length.Val()-s.Pending, 0)
	return s, nil
}

// DeadJob is an entry of the dead-letter queue.
type DeadJob struct {
	ID       string
	Job      *Job
	Raw      string
	Reason   string
	FailedAt string
}

// ListDead returns up to count dead-letter entries, oldest first.
func (q *RedisStreams) ListDead(ctx context.Context, count int64) ([]DeadJob, error) {
	msgs, err := q.rdb.XRangeN(ctx, q.dead, "-", "+", count).Result()
	if err != nil {
		return nil, fmt.Errorf("queue: list dead: %w", err)
	}
	out := make([]DeadJob, 0, len(msgs))
	for _, m := range msgs {
		dj := DeadJob{ID: m.ID}
		dj.Raw, _ = m.Values[fieldJob].(string)
		dj.Reason, _ = m.Values["reason"].(string)
		dj.FailedAt, _ = m.Values["failed_at"].(string)
		var job Job
		if json.Unmarshal([]byte(dj.Raw), &job) == nil {
			dj.Job = &job
		}
		out = append(out, dj)
	}
	return out, nil
}

// replayScript moves one dead-letter entry back to the stream.
var replayScript = redis.NewScript(`
local entries = redis.call('XRANGE', KEYS[1], ARGV[1], ARGV[1])
if #entries == 0 then return 0 end
redis.call('XADD', KEYS[2], '*', 'job', ARGV[2])
redis.call('XDEL', KEYS[1], ARGV[1])
return 1
`)

// ReplayDead re-enqueues the given dead-letter entries with a fresh attempt
// counter and returns how many were replayed.
func (q *RedisStreams) ReplayDead(ctx context.Context, entries []DeadJob) (int, error) {
	replayed := 0
	for _, dj := range entries {
		if dj.Job == nil {
			continue
		}
		job := *dj.Job
		job.Attempts = 0
		payload, err := json.Marshal(&job)
		if err != nil {
			return replayed, err
		}
		n, err := replayScript.Run(ctx, q.rdb, []string{q.dead, q.stream}, dj.ID, payload).Int()
		if err != nil {
			return replayed, fmt.Errorf("queue: replay %s: %w", dj.ID, err)
		}
		replayed += n
	}
	return replayed, nil
}

func isBusyGroup(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "BUSYGROUP")
}

func isNoGroup(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "NOGROUP")
}
