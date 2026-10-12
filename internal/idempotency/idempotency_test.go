package idempotency

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestReserve(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	s := NewStore(rdb, "test", time.Hour)
	ctx := context.Background()

	first := Record{JobID: "job-1", Fingerprint: "fp"}
	existing, err := s.Reserve(ctx, "yner", "key-1", first)
	if err != nil || existing != nil {
		t.Fatalf("first Reserve = %v, %v", existing, err)
	}
	existing, err = s.Reserve(ctx, "yner", "key-1", Record{JobID: "job-2", Fingerprint: "fp"})
	if err != nil || existing == nil || *existing != first {
		t.Fatalf("second Reserve should return the original record, got %v, %v", existing, err)
	}
	// Keys are scoped per tenant.
	if existing, _ := s.Reserve(ctx, "other", "key-1", first); existing != nil {
		t.Fatal("idempotency keys must not leak across tenants")
	}
	if err := s.Release(ctx, "yner", "key-1"); err != nil {
		t.Fatal(err)
	}
	if existing, _ := s.Reserve(ctx, "yner", "key-1", first); existing != nil {
		t.Fatal("released key should be reservable again")
	}

	mr.FastForward(2 * time.Hour)
	if existing, _ := s.Reserve(ctx, "yner", "key-1", first); existing != nil {
		t.Fatal("expired key should be reservable again")
	}
}

func TestFingerprintIsOrderIndependent(t *testing.T) {
	a, _ := Fingerprint(map[string]any{"a": 1, "b": map[string]any{"x": 1, "y": 2}})
	b, _ := Fingerprint(map[string]any{"b": map[string]any{"y": 2, "x": 1}, "a": 1})
	if a != b {
		t.Fatal("fingerprint depends on map ordering")
	}
}

func TestValidKey(t *testing.T) {
	for key, want := range map[string]bool{
		"":                       false,
		"abc-123":                true,
		"with space":             false,
		"émoji":                  false,
		strings.Repeat("a", 255): true,
		strings.Repeat("a", 256): false,
	} {
		if ValidKey(key) != want {
			t.Errorf("ValidKey(%q) = %v", key, !want)
		}
	}
}
