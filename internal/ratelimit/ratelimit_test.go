package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRedisLimiterAllowsBurstThenThrottles(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	// 20/s with a burst of 3: three immediate sends, then one every 50ms.
	l := NewRedis(rdb, "test", 20, 3)
	ctx := context.Background()

	start := time.Now()
	for range 3 {
		if err := l.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed > 30*time.Millisecond {
		t.Fatalf("burst should not wait, took %s", elapsed)
	}
	for range 4 {
		if err := l.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Fatalf("4 sends past the burst at 20/s should take >= 150ms, took %s", elapsed)
	}
}

func TestRedisLimiterIsSharedAcrossInstances(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	podA, podB := NewRedis(rdb, "test", 10, 1), NewRedis(rdb, "test", 10, 1)
	ctx := context.Background()
	if err := podA.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := podB.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
		t.Fatalf("pod B should wait for pod A's token, waited %s", elapsed)
	}
}

func TestRedisLimiterHonoursContext(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	l := NewRedis(rdb, "test", 0.1, 1) // one e-mail every 10s
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = l.Wait(ctx)
	if err := l.Wait(ctx); err == nil {
		t.Fatal("expected context deadline error")
	}
}
