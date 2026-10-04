package id

import (
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	seen := make(map[string]bool)
	for range 1000 {
		v := New()
		if !Valid(v) || v[14] != '7' {
			t.Fatalf("%q is not a valid UUIDv7", v)
		}
		if seen[v] {
			t.Fatalf("duplicate id %q", v)
		}
		seen[v] = true
	}
	first := New()
	time.Sleep(2 * time.Millisecond)
	if second := New(); second <= first {
		t.Fatalf("ids are not time-ordered: %s then %s", first, second)
	}
}

func TestValid(t *testing.T) {
	for s, want := range map[string]bool{
		"0192f1b4-6c1e-7a3b-8c4d-0123456789ab": true,
		"0192F1B4-6C1E-7A3B-8C4D-0123456789AB": false,
		"0192f1b4-6c1e-7a3b-8c4d-0123456789a":  false,
		"0192f1b46c1e-7a3b-8c4d-0123456789ab-": false,
		"../../../../../../../../../etc/passw": false,
	} {
		if Valid(s) != want {
			t.Errorf("Valid(%q) = %v", s, !want)
		}
	}
}
