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
	// An expired deadline is released but must not be reported as a live cooldown.
	ring.MarkCooldown("c", time.Minute, now-2*time.Minute.Nanoseconds())

	if got := ring.ClearCooldowns(now); got != 2 {
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
	if got := ring.ClearCooldowns(now); got != 0 {
		t.Errorf("second ClearCooldowns() = %d, want 0", got)
	}
}

func TestKeyRing_ClearCooldownsNilSafe(t *testing.T) {
	var ring *KeyRing
	if got := ring.ClearCooldowns(time.Now().UnixNano()); got != 0 {
		t.Errorf("nil ring ClearCooldowns() = %d, want 0", got)
	}
}

func TestKeyRing_ResetConsecutiveErrors(t *testing.T) {
	now := time.Now().UnixNano()
	ring := NewKeyRing(KeyStrategyRoundRobin, []*KeySlot{{Ref: "a"}, {Ref: "b"}, {Ref: "c"}})

	ring.SlotByRef("a").ConsecutiveErrors.Store(3)
	ring.SlotByRef("c").ConsecutiveErrors.Store(1)
	ring.MarkCooldown("c", time.Minute, now)
	ring.MarkRevoked("c")

	if got := ring.ResetConsecutiveErrors(); got != 2 {
		t.Fatalf("ResetConsecutiveErrors() = %d, want 2", got)
	}
	for _, slot := range ring.Slots {
		if got := slot.ConsecutiveErrors.Load(); got != 0 {
			t.Errorf("slot %q consecutive errors = %d, want 0", slot.Ref, got)
		}
	}
	if ring.SlotByRef("c").CooldownUntil.Load() == 0 {
		t.Error("ResetConsecutiveErrors cleared a cooldown; that state is separate")
	}
	if !ring.SlotByRef("c").Revoked.Load() {
		t.Error("ResetConsecutiveErrors cleared a revocation; a fired action is not undone")
	}
	if got := ring.ResetConsecutiveErrors(); got != 0 {
		t.Errorf("second ResetConsecutiveErrors() = %d, want 0", got)
	}
}

func TestKeyRing_ResetConsecutiveErrorsNilSafe(t *testing.T) {
	var ring *KeyRing
	if got := ring.ResetConsecutiveErrors(); got != 0 {
		t.Errorf("nil ring ResetConsecutiveErrors() = %d, want 0", got)
	}
}
