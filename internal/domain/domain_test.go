package domain

import (
	"testing"
	"time"
)

func TestKeyRing_ClearCooldowns(t *testing.T) {
	now := time.Now().UnixNano()
	ring := NewKeyRing(KeyStrategyRoundRobin, []*KeySlot{{Ref: "a"}, {Ref: "b"}, {Ref: "c"}})

	ring.MarkCooldown("a", time.Minute, now)
	ring.MarkCooldown("b", time.Minute, now)
	ring.MarkRevoked("b")

	if got := ring.ClearCooldowns(); got != 2 {
		t.Fatalf("ClearCooldowns() = %d, want 2", got)
	}
	for _, slot := range ring.Slots {
		if got := slot.CooldownUntil.Load(); got != 0 {
			t.Errorf("slot %q cooldown = %d, want 0", slot.Ref, got)
		}
	}
	if !ring.SlotByRef("b").Revoked.Load() {
		t.Error("ClearCooldowns cleared a revocation; that state is not a cooldown")
	}
	if got := ring.ClearCooldowns(); got != 0 {
		t.Errorf("second ClearCooldowns() = %d, want 0", got)
	}
}

func TestKeyRing_ClearCooldownsNilSafe(t *testing.T) {
	var ring *KeyRing
	if got := ring.ClearCooldowns(); got != 0 {
		t.Errorf("nil ring ClearCooldowns() = %d, want 0", got)
	}
}
