package upstream

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

// TestHandleKeyOutcome_AlwaysAvailableNeverClosesAKey pins the Layer 1 half of
// the AlwaysAvailable pin. The policy pipeline is otherwise the only thing that
// cooldowns or revokes a credential, and every action it can take (cooldown,
// deactivate, delete) ends in the same place for a pinned upstream: a request
// that used to succeed is refused locally. So a pinned upstream keeps the
// failover signal — try the next key — and nothing else.
func TestHandleKeyOutcome_AlwaysAvailableNeverClosesAKey(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden} {
		notifier := &mockKeyNotifier{}
		slot := &domain.KeySlot{Ref: "opencode-free-public", APIKeyID: 7, Secret: "public"}
		u := &domain.Upstream{
			Name:              "opencode-free",
			AlwaysAvailable:   true,
			KeyErrorThreshold: 1,
			KeyErrorAction:    "delete",
		}

		action, failover := HandleKeyOutcome(u, slot, status, "1", notifier, nil)
		if action {
			t.Errorf("status %d: a pinned upstream must never take a key action", status)
		}
		if !failover {
			t.Errorf("status %d: a credential error must stay failover-eligible", status)
		}
		if got := slot.ConsecutiveErrors.Load(); got != 0 {
			t.Errorf("status %d: counter must not advance, got %d", status, got)
		}
		if slot.Revoked.Load() {
			t.Errorf("status %d: a pinned upstream must never revoke the key", status)
		}
		if slot.IsInCooldown(time.Now().UnixNano()) {
			t.Errorf("status %d: a pinned upstream must never cooldown the key", status)
		}
		if len(notifier.actions) != 0 {
			t.Errorf("status %d: no key action may be reported, got %v", status, notifier.actions)
		}
	}
}

// TestHandleKeyOutcome_AlwaysAvailableStillResetsOnNonKeyOutcomes pins that the
// pin does not disturb the counter semantics AGENTS.md makes non-negotiable: a
// marker that never advances must still be cleared by success, a client cancel,
// a transport drop, or a host error.
func TestHandleKeyOutcome_AlwaysAvailableStillResetsOnNonKeyOutcomes(t *testing.T) {
	for _, status := range []int{0, 200, 499, 500, 502} {
		slot := &domain.KeySlot{Ref: "k", APIKeyID: 7}
		slot.ConsecutiveErrors.Store(4)
		u := &domain.Upstream{Name: "opencode-free", AlwaysAvailable: true, KeyErrorThreshold: 1, KeyErrorAction: "delete"}

		action, failover := HandleKeyOutcome(u, slot, status, "", nil, nil)
		if action || failover {
			t.Errorf("status %d: expected no action/failover, got action=%v failover=%v", status, action, failover)
		}
		if got := slot.ConsecutiveErrors.Load(); got != 0 {
			t.Errorf("status %d: expected counter reset, got %d", status, got)
		}
	}
}

// TestAllowUpstream_PinnedUpstreamBypassesOpenBreaker pins the Layer 2 half: a
// cooldown, a revocation, and an open breaker answer the same question — "should
// this credential be used again?" — and for a pinned upstream the gateway must
// answer yes. Report() still records the failure; only the veto is skipped.
func TestAllowUpstream_PinnedUpstreamBypassesOpenBreaker(t *testing.T) {
	reg := NewBreakerRegistry(BreakerConfig{FailureThreshold: 1, Cooldown: time.Hour})
	reg.Report("opencode-free", false)

	if err := reg.Allow("opencode-free"); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("sanity: breaker should be open, got %v", err)
	}
	if err := AllowUpstream(reg, &domain.Upstream{Name: "opencode-free", AlwaysAvailable: true}); err != nil {
		t.Fatalf("a pinned upstream must not be gated by the breaker, got %v", err)
	}
	// An unpinned upstream keeps the gate.
	if err := AllowUpstream(reg, &domain.Upstream{Name: "opencode-free"}); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("an unpinned upstream must stay gated, got %v", err)
	}
	// Fail-open on a missing dependency, never a panic: the guard also runs on
	// the health prober, where a nil breaker means "no breaker configured".
	if err := AllowUpstream(nil, &domain.Upstream{Name: "x"}); err != nil {
		t.Fatalf("nil gate must fail open, got %v", err)
	}
	if err := AllowUpstream(reg, nil); err != nil {
		t.Fatalf("nil upstream must fail open, got %v", err)
	}
}

// TestSelectKeyForUpstream_PinnedUpstreamSelectsDeadKey pins the shared selector
// both the resolver and the transport failover path use, so a retry after a 429
// does not silently fall back to strict selection and refuse the only key.
func TestSelectKeyForUpstream_PinnedUpstreamSelectsDeadKey(t *testing.T) {
	now := time.Now().UnixNano()

	slot := &domain.KeySlot{Ref: "opencode-free-public", Secret: "public"}
	slot.Revoked.Store(true)
	ring := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{slot})

	pinned := &domain.Upstream{Name: "opencode-free", AlwaysAvailable: true, KeyRing: ring}
	got, err := SelectKeyForUpstream(pinned, now)
	if err != nil || got != slot {
		t.Fatalf("pinned selector = %+v err=%v, want the revoked slot", got, err)
	}

	strict := &domain.Upstream{Name: "paid", KeyRing: ring}
	if _, err := SelectKeyForUpstream(strict, now); !errors.Is(err, domain.ErrAllKeysExhausted) {
		t.Fatalf("strict selector = %v, want ErrAllKeysExhausted", err)
	}

	// A pinned upstream with no ring at all is still exhaustion, not a panic.
	if _, err := SelectKeyForUpstream(&domain.Upstream{Name: "empty", AlwaysAvailable: true}, now); !errors.Is(err, domain.ErrAllKeysExhausted) {
		t.Fatalf("ringless pinned selector = %v, want ErrAllKeysExhausted", err)
	}
}
