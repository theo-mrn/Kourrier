package retry

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestPermanent(t *testing.T) {
	base := errors.New("rejected")
	err := fmt.Errorf("send: %w", Permanent(base))
	if !IsPermanent(err) || !errors.Is(err, base) {
		t.Fatal("wrapped permanent error not detected")
	}
	if IsPermanent(base) || Permanent(nil) != nil {
		t.Fatal("plain errors must be transient")
	}
}

func TestBackoff(t *testing.T) {
	p := Policy{MaxAttempts: 5, Base: 10 * time.Second, Max: time.Minute}
	for attempts, ceiling := range map[int]time.Duration{
		1: 10 * time.Second, 2: 20 * time.Second, 3: 40 * time.Second, 4: time.Minute, 30: time.Minute,
	} {
		for range 50 {
			d := p.Backoff(attempts)
			if d < ceiling/2 || d > ceiling {
				t.Fatalf("Backoff(%d) = %s, want in [%s, %s]", attempts, d, ceiling/2, ceiling)
			}
		}
	}
	if p.Exhausted(4) || !p.Exhausted(5) {
		t.Fatal("Exhausted is off by one")
	}
}
