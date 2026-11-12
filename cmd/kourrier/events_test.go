package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/theo-mrn/kourrier/internal/deadevent"
)

func TestEventsListAndReplay(t *testing.T) {
	mr := miniredis.RunT(t)
	t.Setenv("KOURRIER_REDIS_ADDR", mr.Addr())
	t.Setenv("KOURRIER_TENANTS_FILE", "../../dev/tenants.yaml")
	t.Setenv("KOURRIER_EVENTS_ROUTES_FILE", "../../examples/routes.yaml")

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	dead := deadevent.NewStore(rdb, "kourrier", 100)
	event, err := os.ReadFile("../../examples/events/user-signed-up.json")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := dead.Add(ctx, deadevent.Entry{Tenant: "demo", Source: "demo-users", Reason: deadevent.Rejected,
		Detail: "template missing", Routes: []string{"welcome"}, Event: event}); err != nil {
		t.Fatal(err)
	}
	if _, err := dead.Add(ctx, deadevent.Entry{Tenant: "demo", Source: "demo-users", Reason: deadevent.Invalid, Event: []byte("??")}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := run([]string{"events", "list"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "template missing") || !strings.Contains(out.String(), "invalid") {
		t.Fatalf("list output:\n%s", out.String())
	}

	out.Reset()
	if err := run([]string{"events", "replay"}, &out); err != nil {
		t.Fatalf("replay: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "accepted, 1 e-mail(s)") || !strings.Contains(out.String(), "replayed 1 event(s), skipped 1") {
		t.Fatalf("replay output:\n%s", out.String())
	}
	if n, _ := dead.Len(ctx); n != 1 {
		t.Fatalf("%d entries left, want only the invalid one", n)
	}
	if n, _ := rdb.XLen(ctx, "{kourrier}:stream").Result(); n != 1 {
		t.Fatalf("%d jobs queued, want 1", n)
	}
}
