package upstream

import (
	"errors"
	"testing"
	"time"
)

func TestBreakerTripsAfterThreshold(t *testing.T) {
	now := time.Unix(1000, 0)
	b := NewBreaker(BreakerConfig{
		FailureThreshold: 3,
		SuccessThreshold: 1,
		Cooldown:         time.Second,
		Now:              func() time.Time { return now },
	})

	for i := 0; i < 3; i++ {
		if err := b.Allow(); err != nil {
			t.Fatalf("attempt %d: Allow() = %v, want nil", i, err)
		}
		b.Report(false)
	}
	if got := b.State(); got != StateOpen {
		t.Fatalf("state = %v, want open", got)
	}
	if err := b.Allow(); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("Allow() while open = %v, want ErrCircuitOpen", err)
	}
}

func TestBreakerHalfOpenThenClose(t *testing.T) {
	now := time.Unix(1000, 0)
	b := NewBreaker(BreakerConfig{
		FailureThreshold: 1,
		SuccessThreshold: 2,
		Cooldown:         time.Second,
		Now:              func() time.Time { return now },
	})

	_ = b.Allow()
	b.Report(false)
	if b.State() != StateOpen {
		t.Fatal("want open")
	}

	// Advance past cooldown: a probe is admitted and transitions to half-open.
	now = now.Add(2 * time.Second)
	if err := b.Allow(); err != nil {
		t.Fatalf("probe Allow() = %v, want nil", err)
	}
	if b.State() != StateHalfOpen {
		t.Fatalf("state = %v, want half-open", b.State())
	}

	// One success is not enough (threshold 2).
	b.Report(true)
	if b.State() != StateHalfOpen {
		t.Fatalf("state after 1 success = %v, want half-open", b.State())
	}
	b.Report(true)
	if b.State() != StateClosed {
		t.Fatalf("state after 2 successes = %v, want closed", b.State())
	}
}

func TestBreakerHalfOpenFailureReopens(t *testing.T) {
	now := time.Unix(1000, 0)
	b := NewBreaker(BreakerConfig{FailureThreshold: 1, SuccessThreshold: 2, Cooldown: time.Second,
		Now: func() time.Time { return now }})

	_ = b.Allow()
	b.Report(false)
	now = now.Add(2 * time.Second)
	_ = b.Allow() // -> half-open
	if b.State() != StateHalfOpen {
		t.Fatal("want half-open")
	}
	b.Report(false) // probe fails
	if b.State() != StateOpen {
		t.Fatalf("state = %v, want open", b.State())
	}
}

func TestBreakerRegistryPerUpstream(t *testing.T) {
	r := NewBreakerRegistry(BreakerConfig{FailureThreshold: 2})
	a, b := r.For("a"), r.For("b")
	if a == b {
		t.Fatal("distinct upstreams share a breaker")
	}
	if r.For("a") != a {
		t.Fatal("For must be stable")
	}
	// Tripping a must not affect b.
	r.Report("a", false)
	r.Report("a", false)
	if a.State() != StateOpen {
		t.Fatal("a should be open")
	}
	if b.State() != StateClosed {
		t.Fatal("b should be unaffected")
	}
}

func TestBreakerForceStateAndRegistry(t *testing.T) {
	r := NewBreakerRegistry(BreakerConfig{})
	r.For("upstream-1")
	r.For("upstream-2")

	// Verify initial closed states
	if r.StateFor("upstream-1") != StateClosed {
		t.Fatalf("expected closed, got %v", r.StateFor("upstream-1"))
	}

	// Force state to OPEN
	r.SetState("upstream-1", StateOpen)
	if r.StateFor("upstream-1") != StateOpen {
		t.Fatalf("expected open, got %v", r.StateFor("upstream-1"))
	}
	if r.BreakerStateString("upstream-1") != "open" {
		t.Fatalf("expected 'open', got %s", r.BreakerStateString("upstream-1"))
	}

	// Force state back to CLOSED
	r.SetState("upstream-1", StateClosed)
	if r.StateFor("upstream-1") != StateClosed {
		t.Fatalf("expected closed, got %v", r.StateFor("upstream-1"))
	}

	// Force state to HALF-OPEN
	r.SetState("upstream-2", StateHalfOpen)
	all := r.AllStates()
	if all["upstream-2"] != StateHalfOpen || all["upstream-1"] != StateClosed {
		t.Fatalf("unexpected AllStates: %+v", all)
	}

	// Test ParseBreakerState
	if st, ok := ParseBreakerState("open"); !ok || st != StateOpen {
		t.Fatalf("expected open, got %v", st)
	}
	if st, ok := ParseBreakerState("CLOSED"); !ok || st != StateClosed {
		t.Fatalf("expected closed, got %v", st)
	}
	if st, ok := ParseBreakerState("half-open"); !ok || st != StateHalfOpen {
		t.Fatalf("expected half-open, got %v", st)
	}
	if _, ok := ParseBreakerState("invalid"); ok {
		t.Fatal("expected false for invalid state")
	}
}
