package upstream

import (
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

type staticSnapshotProvider struct{ snap *domain.CatalogSnapshot }

func (s staticSnapshotProvider) Current() *domain.CatalogSnapshot { return s.snap }

func TestReleaseUpstreamKeyPenalties(t *testing.T) {
	now := time.Now().UnixNano()
	warped := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{{Ref: "k1"}})
	warped.MarkCooldown("k1", 5*time.Minute, now)
	warped.SlotByRef("k1").ConsecutiveErrors.Store(3)

	other := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{{Ref: "k2"}})
	other.MarkCooldown("k2", 5*time.Minute, now)
	other.SlotByRef("k2").ConsecutiveErrors.Store(2)
	other.MarkRevoked("k2")

	snap := domain.NewCatalogSnapshot(1,
		map[string]*domain.Upstream{
			"warped":    {Name: "warped", KeyRing: warped},
			"untouched": {Name: "untouched", KeyRing: other},
		},
		[]string{"warped", "untouched"},
		nil, nil, nil, nil)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	provider := staticSnapshotProvider{snap: snap}

	cooldowns, counters := ReleaseUpstreamKeyPenalties(provider, "warped", logger)
	if cooldowns != 1 || counters != 1 {
		t.Fatalf("ReleaseUpstreamKeyPenalties() = (%d, %d), want (1, 1)", cooldowns, counters)
	}
	if got := warped.SlotByRef("k1").CooldownUntil.Load(); got != 0 {
		t.Errorf("target slot cooldown = %d, want 0", got)
	}
	if got := warped.SlotByRef("k1").ConsecutiveErrors.Load(); got != 0 {
		t.Errorf("target slot consecutive errors = %d, want 0", got)
	}
	if got := other.SlotByRef("k2").CooldownUntil.Load(); got == 0 {
		t.Error("an unrelated upstream's cooldown was cleared")
	}
	if got := other.SlotByRef("k2").ConsecutiveErrors.Load(); got != 2 {
		t.Errorf("an unrelated upstream's error counter = %d, want 2", got)
	}
	if !other.SlotByRef("k2").Revoked.Load() {
		t.Error("revocation was cleared; only penalties may be released")
	}

	if c, r := ReleaseUpstreamKeyPenalties(provider, "warped", logger); c != 0 || r != 0 {
		t.Errorf("second call = (%d, %d), want (0, 0)", c, r)
	}

	for _, tc := range []struct {
		name     string
		provider SnapshotProvider
		upstream string
	}{
		{"unknown upstream", provider, "missing"},
		{"nil provider", nil, "warped"},
		{"empty upstream name", provider, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if c, r := ReleaseUpstreamKeyPenalties(tc.provider, tc.upstream, logger); c != 0 || r != 0 {
				t.Errorf("ReleaseUpstreamKeyPenalties() = (%d, %d), want (0, 0)", c, r)
			}
		})
	}
}

// TestReleaseUpstreamKeyPenaltiesDisarmsThreshold is the regression guard for the
// finding that motivated the counter reset: with key_error_threshold = 3, the
// 429s of an IP-bound storm charge the slot's counter, so without the release the
// next storm reaches the threshold and deactivates a key whose only problem was
// the egress IP that the rotation already replaced. Residual limit, deliberately
// not addressed here: a single pre-rotation burst of threshold-many 429s still
// fires the action before any release can run — the reset stops accumulation
// ACROSS rotations.
func TestReleaseUpstreamKeyPenaltiesDisarmsThreshold(t *testing.T) {
	notifier := &mockKeyNotifier{}
	slot := &domain.KeySlot{Ref: "k1", APIKeyID: 42}
	ring := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{slot})
	u := &domain.Upstream{
		Name:              "opencode-free",
		KeyRing:           ring,
		KeyErrorThreshold: 3,
		KeyErrorAction:    "deactivate",
	}
	snap := domain.NewCatalogSnapshot(1,
		map[string]*domain.Upstream{"opencode-free": u},
		[]string{"opencode-free"},
		nil, nil, nil, nil)
	provider := staticSnapshotProvider{snap: snap}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Storm 1 charges two 429s; the rotation those 429s triggered completes and
	// releases the penalties.
	for i := 0; i < 2; i++ {
		HandleKeyOutcome(u, slot, http.StatusTooManyRequests, "", notifier, logger)
	}
	if got := slot.ConsecutiveErrors.Load(); got != 2 {
		t.Fatalf("consecutive errors after two 429s = %d, want 2", got)
	}
	if c, r := ReleaseUpstreamKeyPenalties(provider, "opencode-free", logger); c != 1 || r != 1 {
		t.Fatalf("ReleaseUpstreamKeyPenalties() = (%d, %d), want (1, 1)", c, r)
	}

	// Storm 2 starts from zero: the first 429 of the new era is error number one,
	// not the threshold. Without the release the counter would be at 3 and the
	// deactivate action would fire against a healthy key.
	action, _ := HandleKeyOutcome(u, slot, http.StatusTooManyRequests, "", notifier, logger)
	if action || slot.Revoked.Load() {
		t.Fatalf("healthy key was actioned by the storm after a rotation: action=%v revoked=%v", action, slot.Revoked.Load())
	}
	if got := slot.ConsecutiveErrors.Load(); got != 1 {
		t.Errorf("consecutive errors after the post-rotation 429 = %d, want 1", got)
	}
	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.actions) != 0 {
		t.Errorf("no key action may fire for a post-rotation storm, got %v", notifier.actions)
	}
}
