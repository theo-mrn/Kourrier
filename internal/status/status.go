// Package status tracks the delivery state of each e-mail so that clients can
// query it (GET /v1/emails/{id}) and workers can skip jobs already sent.
package status

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// State of an e-mail.
type State string

const (
	Queued   State = "queued"   // accepted, waiting for a worker
	Retrying State = "retrying" // a transient error occurred, a retry is scheduled
	Sent     State = "sent"     // handed to the provider
	Failed   State = "failed"   // moved to the dead-letter queue
)

// Status is the delivery state of an e-mail.
type Status struct {
	ID                string     `json:"id"`
	TenantID          string     `json:"-"`
	State             State      `json:"status"`
	Attempts          int        `json:"attempts"`
	Provider          string     `json:"provider,omitempty"`
	ProviderMessageID string     `json:"provider_message_id,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	NextAttemptAt     *time.Time `json:"next_attempt_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// stored adds the fields hidden from the API to the persisted form.
type stored struct {
	Status
	TenantID string `json:"tenant_id"`
}

// Store persists statuses in Redis with a retention TTL.
type Store struct {
	rdb    redis.UniversalClient
	prefix string
	ttl    time.Duration
}

// NewStore creates a Store.
func NewStore(rdb redis.UniversalClient, prefix string, ttl time.Duration) *Store {
	return &Store{rdb: rdb, prefix: "{" + prefix + "}:status:", ttl: ttl}
}

// Prepare returns the Redis key, value and TTL of st, for writers that must
// store it atomically with other keys (queue.RedisStreams.EnqueueAtomic).
func (s *Store) Prepare(st *Status) (key string, value []byte, ttl time.Duration, err error) {
	if st.UpdatedAt.IsZero() {
		st.UpdatedAt = time.Now().UTC()
	}
	value, err = json.Marshal(stored{Status: *st, TenantID: st.TenantID})
	if err != nil {
		return "", nil, 0, err
	}
	return s.prefix + st.ID, value, s.ttl, nil
}

// Put writes s, replacing any previous status for the same ID.
func (s *Store) Put(ctx context.Context, st *Status) error {
	key, payload, ttl, err := s.Prepare(st)
	if err != nil {
		return err
	}
	if err := s.rdb.Set(ctx, key, payload, ttl).Err(); err != nil {
		return fmt.Errorf("status: put: %w", err)
	}
	return nil
}

// Get returns the status of id, or (nil, nil) if it is unknown or expired.
func (s *Store) Get(ctx context.Context, id string) (*Status, error) {
	raw, err := s.rdb.Get(ctx, s.prefix+id).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("status: get: %w", err)
	}
	var st stored
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("status: decode: %w", err)
	}
	st.Status.TenantID = st.TenantID
	return &st.Status, nil
}
