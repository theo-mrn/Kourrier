// Package idempotency makes POST /v1/emails safe to retry (ADR 007): a client
// that resends a request with the same Idempotency-Key gets the original
// response instead of a duplicate e-mail.
package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// MaxKeyLength bounds the Idempotency-Key header.
const MaxKeyLength = 255

// Record is what is remembered for a key.
type Record struct {
	JobID string `json:"job_id"`
	// Fingerprint is a digest of the request body, used to reject a key
	// reused with a different payload.
	Fingerprint string `json:"fingerprint"`
}

// Store keeps idempotency records in Redis for a fixed TTL.
type Store struct {
	rdb    redis.UniversalClient
	prefix string
	ttl    time.Duration
}

// NewStore creates a Store. Keys are namespaced per tenant.
func NewStore(rdb redis.UniversalClient, prefix string, ttl time.Duration) *Store {
	return &Store{rdb: rdb, prefix: "{" + prefix + "}:idem:", ttl: ttl}
}

// Reserve atomically claims key for rec within scope (a tenant ID, or a
// tenant-derived namespace). If the key was already claimed, the
// existing record is returned and nothing is written.
func (s *Store) Reserve(ctx context.Context, scope, key string, rec Record) (*Record, error) {
	payload, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	redisKey := s.redisKey(scope, key)
	// SET NX GET returns the previous value when the key exists (Redis 7+).
	prev, err := s.rdb.SetArgs(ctx, redisKey, payload, redis.SetArgs{Mode: "NX", Get: true, TTL: s.ttl}).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("idempotency: reserve: %w", err)
	}
	var existing Record
	if err := json.Unmarshal([]byte(prev), &existing); err != nil {
		return nil, fmt.Errorf("idempotency: decode: %w", err)
	}
	return &existing, nil
}

// Release forgets key, so the request can be retried after a failure that
// happened before the job was enqueued.
func (s *Store) Release(ctx context.Context, scope, key string) error {
	return s.rdb.Del(ctx, s.redisKey(scope, key)).Err()
}

// The client key is hashed so that arbitrary header values make safe,
// bounded Redis keys.
func (s *Store) redisKey(scope, key string) string {
	sum := sha256.Sum256([]byte(key))
	return s.prefix + scope + ":" + hex.EncodeToString(sum[:])
}

// Fingerprint returns a stable digest of v's JSON encoding. encoding/json
// sorts map keys, so semantically identical requests produce the same digest.
func Fingerprint(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// ValidKey reports whether key is an acceptable Idempotency-Key: 1 to 255
// printable ASCII characters.
func ValidKey(key string) bool {
	if len(key) == 0 || len(key) > MaxKeyLength {
		return false
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x21 || key[i] > 0x7e {
			return false
		}
	}
	return true
}
