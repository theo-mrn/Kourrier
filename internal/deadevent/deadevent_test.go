package deadevent

import (
	"context"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestStore(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	s := NewStore(rdb, "kourrier", 100)
	ctx := context.Background()

	id, err := s.Add(ctx, Entry{
		Tenant: "shop", Source: "orders", Reason: Partial, Detail: strings.Repeat("x", 5000),
		Routes: []string{"a", "b"}, Event: []byte(`{"id":"1"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, Entry{Tenant: "shop", Source: "orders", Reason: Invalid, Event: []byte("garbage")}); err != nil {
		t.Fatal(err)
	}
	entries, err := s.List(ctx, 10)
	if err != nil || len(entries) != 2 {
		t.Fatalf("List() = %v, %v", entries, err)
	}
	e := entries[0]
	if e.ID != id || e.Reason != Partial || len(e.Routes) != 2 || e.Routes[1] != "b" || len(e.Detail) != maxDetail ||
		string(e.Event) != `{"id":"1"}` || e.FailedAt.IsZero() || !e.Replayable() {
		t.Fatalf("unexpected entry %+v", e)
	}
	if entries[1].Replayable() || entries[1].Routes != nil {
		t.Fatalf("invalid entries are not replayable: %+v", entries[1])
	}
	if err := s.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Len(ctx); err != nil || n != 1 {
		t.Fatalf("Len() = %d, %v", n, err)
	}
}

func TestStoreLookup(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	s := NewStore(rdb, "kourrier", 100)
	ctx := context.Background()

	var ids []string
	for _, tenant := range []string{"a", "b", "c"} {
		id, err := s.Add(ctx, Entry{Tenant: tenant, Source: "http", Reason: Rejected, Event: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	page, err := s.ListAfter(ctx, ids[0], 10)
	if err != nil || len(page) != 2 || page[0].Tenant != "b" {
		t.Fatalf("ListAfter() = %+v, %v", page, err)
	}
	got, err := s.Get(ctx, ids[2], "0-1", ids[0])
	if err != nil || len(got) != 2 || got[0].Tenant != "c" || got[1].Tenant != "a" {
		t.Fatalf("Get() = %+v, %v", got, err)
	}
}
