package upstream

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
)

type mockKeyNotifier struct {
	mu      sync.Mutex
	actions []ports.KeyAction
	refs    []string
	ids     []int64
}

func (m *mockKeyNotifier) NotifyKeyAction(action ports.KeyAction, upstreamName, ref string, keyID int64, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.actions = append(m.actions, action)
	m.refs = append(m.refs, ref)
	m.ids = append(m.ids, keyID)
}

func TestHandleKeyOutcome_SuccessResetsCounter(t *testing.T) {
	slot := &domain.KeySlot{Ref: "k1", APIKeyID: 10}
	slot.ConsecutiveErrors.Store(5)

	u := &domain.Upstream{
		Name:              "test-up",
		KeyErrorThreshold: 3,
		KeyErrorAction:    "deactivate",
	}

	action, failover := HandleKeyOutcome(u, slot, http.StatusOK, "", nil, nil)
	if action || failover {
		t.Errorf("expected no action and no failover on 200 OK, got action=%v failover=%v", action, failover)
	}
	if slot.ConsecutiveErrors.Load() != 0 {
		t.Errorf("expected consecutive errors reset to 0, got %d", slot.ConsecutiveErrors.Load())
	}
}

// TestHandleKeyOutcome_NonKeyErrorsResetCounter is the core of the simplified
// policy: the counter tracks ONLY consecutive credential errors (429/401/402/
// 403). Any other outcome — success, client cancel (499), transport drop
// (status 0), or a 5xx host error — resets the counter so transient 429s can
// never accumulate to the delete/deactivate threshold on a healthy key.
func TestHandleKeyOutcome_NonKeyErrorsResetCounter(t *testing.T) {
	for _, status := range []int{0, 200, 404, 499, 500, 502, 503} {
		slot := &domain.KeySlot{Ref: "k1", APIKeyID: 10}
		slot.ConsecutiveErrors.Store(2)
		u := &domain.Upstream{Name: "up", KeyErrorThreshold: 3, KeyErrorAction: "delete"}

		action, failover := HandleKeyOutcome(u, slot, status, "", nil, nil)
		if action || failover {
			t.Errorf("status %d: expected no action/failover, got action=%v failover=%v", status, action, failover)
		}
		if got := slot.ConsecutiveErrors.Load(); got != 0 {
			t.Errorf("status %d: expected counter reset to 0, got %d", status, got)
		}
		if slot.Revoked.Load() {
			t.Errorf("status %d: key must not be revoked on a non-key-error", status)
		}
	}
}

// TestHandleKeyOutcome_TransientRateLimitsBetweenSuccessNeverAccumulate models
// the real coding-agent traffic that triggered the account-deletion bug: many
// requests where an occasional genuine 429 is interleaved with successes and
// client cancels. The counter must never climb past 1 because every non-429
// resets it, so the delete threshold is never reached.
func TestHandleKeyOutcome_TransientRateLimitsBetweenSuccessNeverAccumulate(t *testing.T) {
	notifier := &mockKeyNotifier{}
	slot := &domain.KeySlot{Ref: "k1", APIKeyID: 10}
	u := &domain.Upstream{Name: "up", KeyErrorThreshold: 3, KeyErrorAction: "delete"}

	// Interleave 429s with successes/cancels many times.
	sequence := []int{429, 200, 429, 499, 429, 500, 429, 200, 429, 499, 429}
	for _, status := range sequence {
		HandleKeyOutcome(u, slot, status, "", notifier, nil)
		if got := slot.ConsecutiveErrors.Load(); got > 1 {
			t.Fatalf("counter climbed to %d despite non-429 resets between rate limits", got)
		}
	}
	if slot.Revoked.Load() {
		t.Error("healthy key with only transient interleaved 429s must never be revoked")
	}
	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.actions) != 0 {
		t.Errorf("no key action should fire for interleaved transient 429s, got %v", notifier.actions)
	}
}

func TestHandleKeyOutcome_ThresholdDeactivate(t *testing.T) {
	notifier := &mockKeyNotifier{}
	slot := &domain.KeySlot{Ref: "k1", APIKeyID: 42}
	slot.ConsecutiveErrors.Store(2)

	u := &domain.Upstream{
		Name:              "test-up",
		KeyErrorThreshold: 3,
		KeyErrorAction:    "deactivate",
	}

	// 3rd consecutive error: 429
	action, failover := HandleKeyOutcome(u, slot, http.StatusTooManyRequests, "10", notifier, nil)
	if !action || !failover {
		t.Errorf("expected action=true and failover=true, got action=%v failover=%v", action, failover)
	}
	if !slot.Revoked.Load() {
		t.Errorf("expected slot to be revoked")
	}

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.actions) != 1 || notifier.actions[0] != ports.KeyActionDeactivate {
		t.Errorf("expected deactivate action notified, got %v", notifier.actions)
	}
	if len(notifier.ids) != 1 || notifier.ids[0] != 42 {
		t.Errorf("expected key ID 42, got %v", notifier.ids)
	}
}

func TestHandleKeyOutcome_ThresholdDelete(t *testing.T) {
	notifier := &mockKeyNotifier{}
	slot := &domain.KeySlot{Ref: "k-trial", APIKeyID: 99}
	slot.ConsecutiveErrors.Store(1)

	u := &domain.Upstream{
		Name:              "trial-up",
		KeyErrorThreshold: 2,
		KeyErrorAction:    "delete",
	}

	// 2nd consecutive error: 403 Forbidden
	action, failover := HandleKeyOutcome(u, slot, http.StatusForbidden, "", notifier, nil)
	if !action || !failover {
		t.Errorf("expected action=true and failover=true, got action=%v failover=%v", action, failover)
	}
	if !slot.Revoked.Load() {
		t.Errorf("expected slot to be revoked")
	}

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.actions) != 1 || notifier.actions[0] != ports.KeyActionDelete {
		t.Errorf("expected delete action notified, got %v", notifier.actions)
	}
}

func TestHandleKeyOutcome_ThresholdCooldown(t *testing.T) {
	slot := &domain.KeySlot{Ref: "k-cooldown", APIKeyID: 100}
	kr := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{slot})
	slot.ConsecutiveErrors.Store(2)

	u := &domain.Upstream{
		Name:                  "cooldown-up",
		KeyRing:               kr,
		KeyErrorThreshold:     3,
		KeyErrorAction:        "cooldown",
		KeyCooldownDurationMs: 60000,
	}

	action, failover := HandleKeyOutcome(u, slot, http.StatusTooManyRequests, "", nil, nil)
	if !action || !failover {
		t.Errorf("expected action=true and failover=true, got action=%v failover=%v", action, failover)
	}
	if slot.Revoked.Load() {
		t.Errorf("expected slot NOT to be revoked on cooldown action")
	}
	if !slot.IsInCooldown(time.Now().UnixNano()) {
		t.Errorf("expected slot to be in cooldown")
	}
	if slot.ConsecutiveErrors.Load() != 0 {
		t.Errorf("expected consecutive errors reset to 0 after cooldown action, got %d", slot.ConsecutiveErrors.Load())
	}
}

func TestHandleKeyOutcome_5xxIgnored(t *testing.T) {
	slot := &domain.KeySlot{Ref: "k1", APIKeyID: 1}
	u := &domain.Upstream{
		Name:              "test-up",
		KeyErrorThreshold: 1,
		KeyErrorAction:    "delete",
	}

	action, failover := HandleKeyOutcome(u, slot, http.StatusInternalServerError, "", nil, nil)
	if action || failover {
		t.Errorf("5xx must never trigger key threshold action, got action=%v failover=%v", action, failover)
	}
	if slot.ConsecutiveErrors.Load() != 0 {
		t.Errorf("5xx must not increment consecutive key error counter, got %d", slot.ConsecutiveErrors.Load())
	}
}
