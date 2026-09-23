package upstream

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

type staticSnapshotProvider struct{ snap *domain.CatalogSnapshot }

func (s staticSnapshotProvider) Current() *domain.CatalogSnapshot { return s.snap }

func TestClearUpstreamKeyCooldowns(t *testing.T) {
	now := time.Now().UnixNano()
	warped := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{{Ref: "k1"}})
	warped.MarkCooldown("k1", 5*time.Minute, now)
	other := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{{Ref: "k2"}})
	other.MarkCooldown("k2", 5*time.Minute, now)
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

	if got := ClearUpstreamKeyCooldowns(provider, "warped", logger); got != 1 {
		t.Fatalf("ClearUpstreamKeyCooldowns() = %d, want 1", got)
	}
	if got := warped.SlotByRef("k1").CooldownUntil.Load(); got != 0 {
		t.Errorf("target slot cooldown = %d, want 0", got)
	}
	if got := other.SlotByRef("k2").CooldownUntil.Load(); got == 0 {
		t.Error("an unrelated upstream's cooldown was cleared")
	}
	if !other.SlotByRef("k2").Revoked.Load() {
		t.Error("revocation was cleared; only cooldowns may be released")
	}

	if got := ClearUpstreamKeyCooldowns(provider, "missing", logger); got != 0 {
		t.Errorf("unknown upstream = %d, want 0", got)
	}
	if got := ClearUpstreamKeyCooldowns(nil, "warped", logger); got != 0 {
		t.Errorf("nil provider = %d, want 0", got)
	}
	if got := ClearUpstreamKeyCooldowns(provider, "", logger); got != 0 {
		t.Errorf("empty upstream name = %d, want 0", got)
	}
	if got := ClearUpstreamKeyCooldowns(provider, "warped", logger); got != 0 {
		t.Errorf("second call = %d, want 0", got)
	}
}
