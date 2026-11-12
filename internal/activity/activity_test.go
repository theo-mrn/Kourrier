package activity

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/theo-mrn/kourrier/internal/status"
)

func newStore(t *testing.T, maxLen int64) *Store {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	return NewStore(rdb, "kourrier", maxLen)
}

func TestRecordAndList(t *testing.T) {
	s := newStore(t, 1000)
	ctx := context.Background()
	long := strings.Repeat("x", 2*maxErrorLen)
	entries := []Entry{
		{EmailID: "e1", Tenant: "shop", State: status.Queued, Template: "welcome", To: []string{"j***@example.com"}, Origin: "api"},
		{EmailID: "e1", Tenant: "shop", State: status.Retrying, Attempt: 1, Error: long},
		{EmailID: "e2", Tenant: "blog", State: status.Queued, Origin: "event"},
		{EmailID: "e1", Tenant: "shop", State: status.Sent, Attempt: 2},
	}
	for _, e := range entries {
		if err := s.Record(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	page, err := s.List(ctx, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 4 || page.Next != "" {
		t.Fatalf("page = %+v", page)
	}
	newest, oldest := page.Entries[0], page.Entries[3]
	if newest.State != status.Sent || newest.Attempt != 2 || newest.At.IsZero() || newest.ID == "" {
		t.Fatalf("newest = %+v", newest)
	}
	if oldest.Template != "welcome" || oldest.Origin != "api" || len(oldest.To) != 1 || oldest.To[0] != "j***@example.com" {
		t.Fatalf("oldest = %+v", oldest)
	}
	if got := page.Entries[2].Error; len(got) > maxErrorLen+len("…") {
		t.Fatalf("error not truncated: %d bytes", len(got))
	}

	for _, tc := range []struct {
		q    Query
		want int
	}{
		{Query{Tenant: "shop"}, 3},
		{Query{State: status.Queued}, 2},
		{Query{EmailID: "e1", State: status.Sent}, 1},
		{Query{Tenant: "nobody"}, 0},
	} {
		page, err := s.List(ctx, tc.q)
		if err != nil || len(page.Entries) != tc.want {
			t.Errorf("List(%+v) = %d entries, %v; want %d", tc.q, len(page.Entries), err, tc.want)
		}
	}
}

func TestListPaginatesAcrossBatches(t *testing.T) {
	s := newStore(t, 10000)
	ctx := context.Background()
	const total = 2*scanBatch + 30
	for i := range total {
		tenant := "a"
		if i%2 == 1 {
			tenant = "b"
		}
		if err := s.Record(ctx, Entry{EmailID: fmt.Sprint(i), Tenant: tenant, State: status.Sent}); err != nil {
			t.Fatal(err)
		}
	}

	// Filtered pages must cross scan batches and never repeat an entry.
	seen := map[string]bool{}
	q := Query{Tenant: "b", Limit: 70}
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("pagination does not end")
		}
		page, err := s.List(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Entries {
			if e.Tenant != "b" || seen[e.EmailID] {
				t.Fatalf("unexpected entry %+v", e)
			}
			seen[e.EmailID] = true
		}
		if page.Next == "" {
			break
		}
		q.Before = page.Next
	}
	if len(seen) != total/2 {
		t.Fatalf("saw %d entries, want %d", len(seen), total/2)
	}
}

func TestListCapsTheLimit(t *testing.T) {
	s := newStore(t, 10000)
	ctx := context.Background()
	for i := range maxLimit + 10 {
		if err := s.Record(ctx, Entry{EmailID: fmt.Sprint(i), Tenant: "a", State: status.Sent}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.List(ctx, Query{Limit: 10 * maxLimit})
	if err != nil || len(page.Entries) != maxLimit || page.Next == "" {
		t.Fatalf("len = %d, next = %q, err = %v", len(page.Entries), page.Next, err)
	}
}
