package idempotency

import (
	"strings"
	"testing"
	"time"
)

func TestPrepareAndDecode(t *testing.T) {
	s := NewStore("test", time.Hour)
	rec := Record{JobID: "job-1", Fingerprint: "fp"}
	key, value, ttl, err := s.Prepare("yner", "key-1", rec)
	if err != nil || ttl != time.Hour {
		t.Fatalf("Prepare() = %q, %v, %v", key, ttl, err)
	}
	if !strings.HasPrefix(key, "{test}:idem:yner:") || strings.Contains(key, "key-1") {
		t.Fatalf("key %q must be in the queue hash tag, scoped and hashed", key)
	}
	if other, _, _, _ := s.Prepare("other", "key-1", rec); other == key {
		t.Fatal("idempotency keys must not leak across scopes")
	}
	got, err := Decode(value)
	if err != nil || *got != rec {
		t.Fatalf("Decode() = %+v, %v", got, err)
	}
	if _, err := Decode([]byte("{")); err == nil {
		t.Fatal("Decode must reject invalid records")
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
