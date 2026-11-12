package dispatch

import (
	"context"
	"errors"
	"testing"

	"github.com/theo-mrn/kourrier/internal/deadevent"
	"github.com/theo-mrn/kourrier/internal/tenant"
)

type tenants map[string]*tenant.Tenant

func (m tenants) Get(id string) (*tenant.Tenant, bool) { t, ok := m[id]; return t, ok }

func TestReplayDead(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	known := tenants{"shop": f.tenant}

	// A partial failure (the welcome route failed), an invalid payload and
	// an entry of a tenant that no longer exists.
	f.d.Dispatch(ctx, "test", f.tenant, ev(t, "user.created", "/users", "1", `{"email":"not-an-address"}`))
	add := func(e deadevent.Entry) {
		t.Helper()
		if _, err := f.dead.Add(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	add(deadevent.Entry{Tenant: "shop", Source: "orders", Reason: deadevent.Invalid, Event: []byte("garbage")})
	add(deadevent.Entry{Tenant: "gone", Source: "http", Reason: deadevent.Rejected, Event: f.deadEntries(t)[0].Event})
	entries := f.deadEntries(t)

	results, err := f.d.ReplayDead(ctx, f.dead, known, entries)
	if err != nil || len(results) != 3 {
		t.Fatalf("ReplayDead() = %+v, %v", results, err)
	}
	// The partial entry fails again: replayed, kept under a new entry.
	if r := results[0]; r.Outcome != Rejected || r.DeadLetterID == "" || r.Skipped != "" {
		t.Fatalf("partial: %+v", r)
	}
	if results[1].Skipped == "" || results[2].Skipped == "" {
		t.Fatalf("invalid and unknown-tenant entries must be skipped: %+v", results[1:])
	}
	left := f.deadEntries(t)
	if len(left) != 3 || left[0].ID != entries[1].ID || left[2].ID != results[0].DeadLetterID {
		t.Fatalf("replayed entry not deleted, or skipped ones not kept: %+v", left)
	}

	// A transient failure stops the replay and keeps the entry.
	f.gate.over = true
	results, err = f.d.ReplayDead(ctx, f.dead, known, f.deadEntries(t)[2:])
	if !errors.Is(err, ErrReplayInterrupted) || len(results) != 1 || results[0].Outcome != Retry {
		t.Fatalf("ReplayDead() under backpressure = %+v, %v", results, err)
	}
	if n, _ := f.dead.Len(ctx); n != 3 {
		t.Fatalf("%d entries left, want 3", n)
	}
}
