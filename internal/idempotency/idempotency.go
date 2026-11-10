// Package idempotency makes POST /v1/emails safe to retry (ADR 007): a client
// that resends a request with the same Idempotency-Key gets the original
// response instead of a duplicate e-mail.
package idempotency

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
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

// Store describes how idempotency records are kept in Redis: key layout
// (in the queue's hash tag) and retention.
type Store struct {
	prefix string
	ttl    time.Duration
}

// NewStore creates a Store. Keys are namespaced per tenant.
func NewStore(prefix string, ttl time.Duration) *Store {
	return &Store{prefix: "{" + prefix + "}:idem:", ttl: ttl}
}

// Prepare returns the Redis key, value and TTL that record rec for key
// within scope (a tenant ID, or a tenant-derived namespace). The record is
// written by queue.RedisStreams.EnqueueAtomic, together with the job, so that
// a record never exists without its job.
func (s *Store) Prepare(scope, key string, rec Record) (redisKey string, value []byte, ttl time.Duration, err error) {
	value, err = json.Marshal(rec)
	if err != nil {
		return "", nil, 0, err
	}
	return s.redisKey(scope, key), value, s.ttl, nil
}

// Decode parses a stored record.
func Decode(value []byte) (*Record, error) {
	var rec Record
	if err := json.Unmarshal(value, &rec); err != nil {
		return nil, fmt.Errorf("idempotency: decode: %w", err)
	}
	return &rec, nil
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
