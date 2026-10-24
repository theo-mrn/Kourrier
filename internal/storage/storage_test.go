package storage

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestMemory(t *testing.T) {
	m := NewMemory(map[string]string{"a/1": "x", "a/2": "y", "b/1": "z"})
	ctx := context.Background()

	if v, err := m.Get(ctx, "a/1"); err != nil || string(v) != "x" {
		t.Fatalf("Get = %q, %v", v, err)
	}
	if _, err := m.Get(ctx, "c"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing key: %v", err)
	}
	m.Put("a/0", "w")
	if keys, _ := m.List(ctx, "a/"); !slices.Equal(keys, []string{"a/0", "a/1", "a/2"}) {
		t.Fatalf("List = %v", keys)
	}
	if keys, _ := m.List(ctx, "nothing/"); len(keys) != 0 {
		t.Fatalf("List(unknown prefix) = %v", keys)
	}
}
