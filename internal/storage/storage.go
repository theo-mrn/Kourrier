// Package storage abstracts the object storage holding e-mail templates
// (ADR 003). Implementations live in sub-packages: s3store (AWS S3, MinIO and
// any S3-compatible service) and fsstore (local directory, for development).
package storage

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
)

// ErrNotFound is returned by Get when the object does not exist.
var ErrNotFound = errors.New("storage: object not found")

// MaxObjectSize bounds the size of a template file read from storage.
const MaxObjectSize = 1 << 20

// MaxListedKeys bounds the number of keys returned by List.
const MaxListedKeys = 10_000

// Provider reads objects by key. Keys use "/" as separator.
type Provider interface {
	Get(ctx context.Context, key string) ([]byte, error)
	// List returns the keys of every object under prefix ("tenant/"), sorted.
	// An empty or missing prefix is not an error.
	List(ctx context.Context, prefix string) ([]string, error)
	// Ping checks that the storage is reachable.
	Ping(ctx context.Context) error
}

// Memory is an in-memory Provider, handy for tests.
type Memory struct {
	mu      sync.RWMutex
	objects map[string][]byte
}

// NewMemory returns a Memory provider seeded with objects.
func NewMemory(objects map[string]string) *Memory {
	m := &Memory{objects: make(map[string][]byte, len(objects))}
	for k, v := range objects {
		m.objects[k] = []byte(v)
	}
	return m
}

// Put stores an object.
func (m *Memory) Put(key, value string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = []byte(value)
}

func (m *Memory) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.objects[key]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), v...), nil
}

func (m *Memory) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var keys []string
	for k := range m.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys, nil
}

func (m *Memory) Ping(context.Context) error { return nil }
