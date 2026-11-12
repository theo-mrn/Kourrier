// Package ratelimit throttles calls to the e-mail provider (ADR 009) so that
// the sending quota (e.g. 14 e-mails/s on a fresh SES account) is respected.
package ratelimit

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/time/rate"
)

// Limiter blocks until the caller is allowed to send one e-mail.
type Limiter interface {
	Wait(ctx context.Context) error
}

// Unlimited never blocks.
type Unlimited struct{}

func (Unlimited) Wait(context.Context) error { return nil }

// NewLocal returns an in-process token bucket. The limit applies per pod.
func NewLocal(perSecond float64, burst int) Limiter {
	return rate.NewLimiter(rate.Limit(perSecond), max(burst, 1))
}

// gcraScript implements the Generic Cell Rate Algorithm. It stores a single
// value per key (the theoretical arrival time, in microseconds) and returns 0
// when the request is allowed, or the number of microseconds to wait. Time is
// taken from the Redis server so that pods with skewed clocks agree.
var gcraScript = redis.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000000 + tonumber(t[2])
local interval = tonumber(ARGV[1])
local tolerance = tonumber(ARGV[2])
local tat = tonumber(redis.call('GET', KEYS[1]))
if not tat or tat < now then tat = now end
local wait = tat - tolerance - now
if wait > 0 then return wait end
local new_tat = tat + interval
redis.call('SET', KEYS[1], string.format('%.0f', new_tat), 'PX', math.ceil((new_tat - now) / 1000) + 1000)
return 0
`)

// Redis is a distributed limiter shared by every pod using the same key.
type Redis struct {
	rdb       redis.UniversalClient
	key       string
	interval  int64 // microseconds between two e-mails
	tolerance int64 // microseconds of burst allowance
}

// NewRedis returns a limiter allowing perSecond e-mails per second across the
// whole deployment, with bursts of up to burst e-mails.
func NewRedis(rdb redis.UniversalClient, prefix string, perSecond float64, burst int) *Redis {
	interval := int64(math.Round(1e6 / perSecond))
	return &Redis{
		rdb:       rdb,
		key:       "{" + prefix + "}:ratelimit",
		interval:  interval,
		tolerance: interval * int64(max(burst, 1)-1),
	}
}

func (l *Redis) Wait(ctx context.Context) error {
	for {
		wait, err := gcraScript.Run(ctx, l.rdb, []string{l.key}, l.interval, l.tolerance).Int64()
		if err != nil {
			return fmt.Errorf("ratelimit: %w", err)
		}
		if wait <= 0 {
			return nil
		}
		timer := time.NewTimer(time.Duration(wait) * time.Microsecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Keyed is a distributed GCRA limiter with one bucket per key (e.g. one per
// tenant), sharing the script of Redis. Unlike Redis.Wait it never blocks:
// it reports how long the caller should wait, so that a broker can redeliver
// later instead of a goroutine sleeping.
type Keyed struct {
	rdb    redis.UniversalClient
	prefix string
}

// NewKeyed creates a Keyed limiter. Keys live in the queue's hash tag.
func NewKeyed(rdb redis.UniversalClient, prefix string) *Keyed {
	return &Keyed{rdb: rdb, prefix: "{" + prefix + "}:ratelimit:"}
}

// Allow takes one token from key's bucket. It returns 0 when allowed, or how
// long to wait before the next token is available.
func (k *Keyed) Allow(ctx context.Context, key string, perSecond float64, burst int) (time.Duration, error) {
	interval := int64(math.Round(1e6 / perSecond))
	tolerance := interval * int64(max(burst, 1)-1)
	wait, err := gcraScript.Run(ctx, k.rdb, []string{k.prefix + key}, interval, tolerance).Int64()
	if err != nil {
		return 0, fmt.Errorf("ratelimit: %w", err)
	}
	return time.Duration(max(wait, 0)) * time.Microsecond, nil
}
