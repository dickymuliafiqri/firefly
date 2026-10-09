package warp

import (
	"sync/atomic"
	"testing"
	"time"
)

// limitFor marks a session as egress-limited until now+d. It mirrors what
// RotateEgress writes so tests can drive the picker's skip logic directly.
func limitFor(s *Session, d time.Duration) {
	s.limitedUntil.Store(time.Now().Add(d).UnixNano())
}

// A 429 on one egress IP must not take the whole pool down with it: pick() has
// to keep handing out the other live sessions.
func TestPick_SkipsLimitedSession(t *testing.T) {
	mgr := NewManager(quietLogger(), "")
	defer mgr.Close()

	a := &Session{PublicIP: "203.0.113.1", CreatedAt: time.Now()}
	b := &Session{PublicIP: "203.0.113.2", CreatedAt: time.Now().Add(time.Second)}
	c := &Session{PublicIP: "203.0.113.3", CreatedAt: time.Now().Add(2 * time.Second)}
	seedActive(mgr, a, b, c)

	limitFor(b, time.Minute)

	for i := 0; i < 30; i++ {
		if got := mgr.pick(); got == b {
			t.Fatalf("pick() handed out an egress-limited session on iteration %d", i)
		}
	}
}

// Every slot limited must not deadlock the pool. A request served from a
// limited IP is strictly better than one that is dropped, and the next attempt
// still advances toward a slot whose penalty has expired.
func TestPick_FallsBackWhenEverySessionLimited(t *testing.T) {
	mgr := NewManager(quietLogger(), "")
	defer mgr.Close()

	a := &Session{PublicIP: "203.0.113.1", CreatedAt: time.Now()}
	b := &Session{PublicIP: "203.0.113.2", CreatedAt: time.Now().Add(time.Second)}
	seedActive(mgr, a, b)

	limitFor(a, time.Minute)
	limitFor(b, time.Minute)

	for i := 0; i < 10; i++ {
		if got := mgr.pick(); got == nil {
			t.Fatalf("pick() returned nil on iteration %d; an all-limited pool must still serve", i)
		}
	}
}

// The penalty is a timestamp, not a permanent removal: once it expires the
// session rejoins rotation with no reaper involved.
func TestPick_LimitedSessionRejoinsAfterExpiry(t *testing.T) {
	mgr := NewManager(quietLogger(), "")
	defer mgr.Close()

	a := &Session{PublicIP: "203.0.113.1", CreatedAt: time.Now()}
	seedActive(mgr, a)

	limitFor(a, 120*time.Millisecond)
	for i := 0; i < 5; i++ {
		if mgr.pick() == nil {
			t.Fatal("a single-session pool must fall back to its only slot")
		}
	}

	waitFor(t, 2*time.Second, func() bool { return !a.limited(time.Now().UnixNano()) },
		"the egress penalty should expire on its own")
}

// RotateEgress penalizes the session that served the last dial and reports that
// a different live session is available to retry on.
func TestRotateEgress_PenalizesLastPickedAndReportsAlternate(t *testing.T) {
	mgr := NewManager(quietLogger(), "")
	defer mgr.Close()

	a := &Session{PublicIP: "203.0.113.1", CreatedAt: time.Now()}
	b := &Session{PublicIP: "203.0.113.2", CreatedAt: time.Now().Add(time.Second)}
	seedActive(mgr, a, b)

	// Simulate a dial that landed on b, then a 429 from that egress.
	mgr.lastPicked.Store(b)
	if !mgr.RotateEgress() {
		t.Fatal("RotateEgress must report an alternate when another live session exists")
	}
	if !b.limited(time.Now().UnixNano()) {
		t.Error("RotateEgress must penalize the session that served the limited request")
	}
	if a.limited(time.Now().UnixNano()) {
		t.Error("RotateEgress must not penalize the healthy alternate")
	}

	// The next pick must land on the healthy session, not the penalized one.
	if got := mgr.pick(); got != a {
		t.Fatalf("pick() after RotateEgress returned %v, want the unpenalized session a", got)
	}
}

// A single-session pool has nowhere to retry: RotateEgress must say so, so the
// caller can hand the 429 back to the existing key cooldown/failover path
// instead of spinning.
func TestRotateEgress_NoAlternateReturnsFalse(t *testing.T) {
	mgr := NewManager(quietLogger(), "")
	defer mgr.Close()

	only := &Session{PublicIP: "203.0.113.9", CreatedAt: time.Now()}
	seedActive(mgr, only)
	mgr.lastPicked.Store(only)

	if mgr.RotateEgress() {
		t.Fatal("RotateEgress must report false when the pool holds a single session")
	}
	if !only.limited(time.Now().UnixNano()) {
		t.Error("RotateEgress should still penalize the IP that answered 429")
	}
}

// A pool whose every other slot is already limited has no healthy alternate
// either, even though it is not single-session.
func TestRotateEgress_AllOthersLimitedReturnsFalse(t *testing.T) {
	mgr := NewManager(quietLogger(), "")
	defer mgr.Close()

	a := &Session{PublicIP: "203.0.113.1", CreatedAt: time.Now()}
	b := &Session{PublicIP: "203.0.113.2", CreatedAt: time.Now().Add(time.Second)}
	seedActive(mgr, a, b)

	limitFor(a, time.Minute)
	mgr.lastPicked.Store(b)

	if mgr.RotateEgress() {
		t.Fatal("RotateEgress must report false when the only other session is already limited")
	}
}

// An empty pool has nothing to penalize and nothing to retry on.
func TestRotateEgress_EmptyPoolReturnsFalse(t *testing.T) {
	mgr := NewManager(quietLogger(), "")
	defer mgr.Close()

	if mgr.RotateEgress() {
		t.Fatal("RotateEgress must report false on an empty pool")
	}
}

// A closed manager must never resurrect sessions or report a usable alternate.
func TestRotateEgress_ClosedManagerReturnsFalse(t *testing.T) {
	mgr := NewManager(quietLogger(), "")
	a := &Session{PublicIP: "203.0.113.1", CreatedAt: time.Now()}
	seedActive(mgr, a)
	mgr.Close()

	if mgr.RotateEgress() {
		t.Fatal("RotateEgress must report false after Close")
	}
}

// RotateEgress drops the idle keep-alive sockets pooled on the penalized IP;
// without that the retry would reuse the same connection and the same address.
func TestRotateEgress_NotifiesRotationObserver(t *testing.T) {
	mgr := NewManager(quietLogger(), "")
	defer mgr.Close()

	a := &Session{PublicIP: "203.0.113.1", CreatedAt: time.Now()}
	b := &Session{PublicIP: "203.0.113.2", CreatedAt: time.Now().Add(time.Second)}
	seedActive(mgr, a, b)

	var calls atomic.Int64
	mgr.SetRotationObserver(func() { calls.Add(1) })
	mgr.lastPicked.Store(b)

	if !mgr.RotateEgress() {
		t.Fatal("expected an alternate session")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("rotation observer called %d times, want 1", got)
	}
}

// SetEgressLimit is the operator-facing knob; a non-positive value must restore
// the default rather than disabling the skip entirely.
func TestSetEgressLimit(t *testing.T) {
	mgr := NewManager(quietLogger(), "")
	defer mgr.Close()

	if mgr.egressLimit != DefaultEgressLimit {
		t.Fatalf("default egressLimit = %v, want %v", mgr.egressLimit, DefaultEgressLimit)
	}
	mgr.SetEgressLimit(5 * time.Second)
	if mgr.egressLimit != 5*time.Second {
		t.Fatalf("egressLimit = %v, want 5s", mgr.egressLimit)
	}
	mgr.SetEgressLimit(0)
	if mgr.egressLimit != DefaultEgressLimit {
		t.Fatalf("egressLimit after reset = %v, want the default", mgr.egressLimit)
	}
}
