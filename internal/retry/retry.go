// Package retry classifies delivery errors and computes retry delays.
package retry

import (
	"errors"
	"math/rand/v2"
	"time"
)

type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// Permanent marks err as non-retryable: the job goes straight to the
// dead-letter queue. A nil err stays nil.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

// IsPermanent reports whether err, or any error it wraps, was marked with
// Permanent.
func IsPermanent(err error) bool {
	var pe *permanentError
	return errors.As(err, &pe)
}

// Policy is an exponential backoff policy with full jitter.
type Policy struct {
	MaxAttempts int
	Base        time.Duration
	Max         time.Duration
}

// Exhausted reports whether a job that has already been attempted `attempts`
// times must stop being retried.
func (p Policy) Exhausted(attempts int) bool {
	return attempts >= p.MaxAttempts
}

// Backoff returns the delay before the next attempt, given the number of
// attempts already made (>= 1). The delay grows as Base*2^(attempts-1), is
// capped at Max, and is jittered between 50% and 100% of that value so that
// retries of a burst of failures don't hit the provider in lockstep.
func (p Policy) Backoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := p.Base
	for i := 1; i < attempts && d < p.Max; i++ {
		d *= 2
	}
	d = min(d, p.Max)
	half := d / 2
	if half <= 0 {
		return d
	}
	return half + rand.N(half+1) //nolint:gosec // jitter does not need a CSPRNG
}
