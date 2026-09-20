package upstream

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

func TestRetryPolicyDefaults(t *testing.T) {
	p := RetryPolicy{}.normalized()
	if p.MaxAttempts != 1 {
		t.Fatalf("MaxAttempts = %d, want 1", p.MaxAttempts)
	}
	if p.Jitter == nil {
		t.Fatal("Jitter must default to a non-nil source")
	}
}

func TestRetryPolicyBackoffGrowsAndCaps(t *testing.T) {
	p := RetryPolicy{
		MaxAttempts: 10,
		BaseBackoff: 100 * time.Millisecond,
		MaxBackoff:  400 * time.Millisecond,
		Jitter:      func(time.Duration) time.Duration { return 0 }, // deterministic
	}
	// backoff(n) = base for n=2, doubling until the cap.
	got := []time.Duration{p.backoff(2), p.backoff(3), p.backoff(4), p.backoff(5)}
	want := []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond, 200 * time.Millisecond}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("backoff[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestRetryPolicyWaitHonorsCancel(t *testing.T) {
	p := RetryPolicy{BaseBackoff: time.Hour, MaxBackoff: time.Hour, Jitter: func(time.Duration) time.Duration { return 0 }}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if p.Wait(ctx, 2) {
		t.Fatal("Wait should return false when ctx is cancelled")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Wait blocked for %v despite cancellation", elapsed)
	}
}

func TestAttemptsReflectsPolicy(t *testing.T) {
	if got := (RetryPolicy{MaxAttempts: 5}).Attempts(); got != 5 {
		t.Fatalf("Attempts() = %d, want 5", got)
	}
	if got := (RetryPolicy{}).Attempts(); got != 1 {
		t.Fatalf("Attempts() default = %d, want 1", got)
	}
}

func TestPoolReturnsStableClient(t *testing.T) {
	pool := NewPool()
	u := &domain.Upstream{Name: "u1", MaxIdleConnsPerHost: 10, MaxConnsPerHost: 20, TimeoutMs: 1000, IdleTimeoutMs: 1000}
	c1 := pool.Client(u)
	c2 := pool.Client(u)
	if c1 != c2 {
		t.Fatal("pool must return the same client for the same upstream")
	}
	// A different upstream gets a different client.
	u2 := &domain.Upstream{Name: "u2"}
	if pool.Client(u2) == c1 {
		t.Fatal("distinct upstreams must not share a client")
	}
}

func TestPoolNilUpstreamIsSafe(t *testing.T) {
	pool := NewPool()
	if pool.Client(nil) == nil {
		t.Fatal("nil upstream should return a usable default client")
	}
}

type mockWarpRotator struct {
	rotatedUpstream string
	called          bool
}

func (m *mockWarpRotator) RotateAsync(upstreamName string) {
	m.rotatedUpstream = upstreamName
	m.called = true
}

func TestPool_EgressTransport_WarpAndProxy(t *testing.T) {
	t.Parallel()

	pool := NewPool()
	// 1. Direct mode
	uDirect := &domain.Upstream{Name: "direct", EgressMode: "direct"}
	cDirect := pool.Client(uDirect)
	if cDirect == nil || cDirect.Transport == nil {
		t.Fatal("expected valid client for direct egress")
	}

	// 2. Warp mode
	uWarp := &domain.Upstream{Name: "warp", EgressMode: "warp"}
	cWarp := pool.Client(uWarp)
	if cWarp == nil || cWarp.Transport == nil {
		t.Fatal("expected valid client for warp egress")
	}

	// 3. Proxy mode
	uProxy := &domain.Upstream{Name: "proxy", EgressMode: "proxy", ProxyURL: "socks5://127.0.0.1:1080"}
	cProxy := pool.Client(uProxy)
	if cProxy == nil || cProxy.Transport == nil {
		t.Fatal("expected valid client for proxy egress")
	}
}

func TestProcessAttemptOutcome_WarpAutoRotateOn429(t *testing.T) {
	mockRot := &mockWarpRotator{}
	SetGlobalWarpRotator(mockRot)
	defer SetGlobalWarpRotator(nil)

	u := &domain.Upstream{
		Name:                "opencode-free",
		WarpAutoRotateOn429: true,
	}
	target := &domain.Target{
		Upstream: u,
	}

	outcome := AttemptOutcome{
		Status: http.StatusTooManyRequests,
	}

	decision := ProcessAttemptOutcome(u, target, outcome, nil, nil, nil, nil)
	if !decision.Relay {
		t.Errorf("expected decision Relay, got %+v", decision)
	}

	if !mockRot.called || mockRot.rotatedUpstream != "opencode-free" {
		t.Errorf("expected RotateAsync to be called for opencode-free, got called=%v upstream=%s",
			mockRot.called, mockRot.rotatedUpstream)
	}
}



// mockBreaker records the ok/failure signals reported to the circuit breaker.
type mockBreaker struct {
	reports []bool
}

func (m *mockBreaker) Report(_ string, ok bool) { m.reports = append(m.reports, ok) }

func (m *mockBreaker) tripped() bool {
	for _, ok := range m.reports {
		if !ok {
			return true
		}
	}
	return false
}

// TestProcessAttemptOutcome_ClientCancelNeverPenalizesKeyOrBreaker pins the fix
// for the account-deletion bug: a client disconnect (context.Canceled / HTTP
// 499-class) must never trip the breaker, never count as a key error, and must
// RESET the key's consecutive-error counter — regardless of the HTTP status the
// adapter stamped on the outcome (0 transport abort, 200 mid-stream abort, or a
// synthetic 5xx from a failed non-stream aggregation).
func TestProcessAttemptOutcome_ClientCancelNeverPenalizesKeyOrBreaker(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		committed bool
	}{
		{"transport abort before headers", 0, false},
		{"mid-stream abort after 200", http.StatusOK, true},
		{"synthetic 500 from failed aggregation", http.StatusInternalServerError, false},
		{"synthetic 502 from failed aggregation", http.StatusBadGateway, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			br := &mockBreaker{}
			notifier := &mockKeyNotifier{}
			slot := &domain.KeySlot{Ref: "k1", APIKeyID: 7}
			slot.ConsecutiveErrors.Store(2) // simulate prior transient errors

			u := &domain.Upstream{
				Name:              "up",
				KeyErrorThreshold: 3, // one more real error would delete/deactivate
				KeyErrorAction:    "delete",
			}
			target := &domain.Target{Upstream: u, KeySlot: slot}

			decision := ProcessAttemptOutcome(u, target, AttemptOutcome{
				Status:    tc.status,
				Err:       context.Canceled,
				Committed: tc.committed,
			}, br, nil, notifier, nil)

			if !decision.StopCommitted {
				t.Fatalf("expected StopCommitted for client cancel, got %+v", decision)
			}
			if br.tripped() {
				t.Errorf("client cancel must not trip the breaker, reports=%v", br.reports)
			}
			if got := slot.ConsecutiveErrors.Load(); got != 0 {
				t.Errorf("client cancel must reset consecutive errors, got %d", got)
			}
			if len(notifier.actions) != 0 {
				t.Errorf("client cancel must not trigger any key action, got %v", notifier.actions)
			}
		})
	}
}

// TestProcessAttemptOutcome_WrappedClientCancel ensures a cancel wrapped by an
// adapter's error type is still recognized via errors.Is and treated as benign.
func TestProcessAttemptOutcome_WrappedClientCancel(t *testing.T) {
	br := &mockBreaker{}
	slot := &domain.KeySlot{Ref: "k1", APIKeyID: 7}
	slot.ConsecutiveErrors.Store(1)
	u := &domain.Upstream{Name: "up", KeyErrorThreshold: 2, KeyErrorAction: "delete"}
	target := &domain.Target{Upstream: u, KeySlot: slot}

	wrapped := fmt.Errorf("relay aborted: %w", context.Canceled)
	decision := ProcessAttemptOutcome(u, target, AttemptOutcome{
		Status: http.StatusInternalServerError,
		Err:    wrapped,
	}, br, nil, nil, nil)

	if !decision.StopCommitted {
		t.Fatalf("expected StopCommitted for wrapped cancel, got %+v", decision)
	}
	if br.tripped() {
		t.Errorf("wrapped client cancel must not trip the breaker, reports=%v", br.reports)
	}
	if got := slot.ConsecutiveErrors.Load(); got != 0 {
		t.Errorf("wrapped client cancel must reset consecutive errors, got %d", got)
	}
}
