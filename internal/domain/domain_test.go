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

func TestKeyRing_RemoveSlot(t *testing.T) {
	k1 := &KeySlot{Ref: "k1"}
	k2 := &KeySlot{Ref: "k2"}
	k3 := &KeySlot{Ref: "k3"}
	ring := NewKeyRing(KeyStrategyRoundRobin, []*KeySlot{k1, k2, k3})

	if ring.SlotCount() != 3 {
		t.Fatalf("expected 3 slots, got %d", ring.SlotCount())
	}

	// Remove middle slot
	if !ring.RemoveSlot("k2") {
		t.Fatal("expected RemoveSlot(k2) to return true")
	}

	if ring.SlotCount() != 2 {
		t.Fatalf("expected 2 slots after removal, got %d", ring.SlotCount())
	}
	if ring.SlotByRef("k2") != nil {
		t.Error("expected SlotByRef(k2) to be nil after removal")
	}
	if ring.SlotByRef("k1") != k1 || ring.SlotByRef("k3") != k3 {
		t.Error("k1 and k3 should still be in the ring")
	}

	all := ring.AllSlots()
	if len(all) != 2 || all[0].Ref != "k1" || all[1].Ref != "k3" {
		t.Fatalf("AllSlots mismatch: %+v", all)
	}

	// SelectKey should only select k1 and k3
	for i := 0; i < 10; i++ {
		slot, err := ring.SelectKey(time.Now().UnixNano())
		if err != nil {
			t.Fatalf("unexpected SelectKey error: %v", err)
		}
		if slot.Ref == "k2" {
			t.Fatal("SelectKey selected removed slot k2")
		}
	}

	// Removing nonexistent ref returns false
	if ring.RemoveSlot("nonexistent") {
		t.Error("expected RemoveSlot(nonexistent) to return false")
	}
	if ring.RemoveSlot("") {
		t.Error("expected RemoveSlot(\"\") to return false")
	}

	// Remove remaining slots
	if !ring.RemoveSlot("k1") || !ring.RemoveSlot("k3") {
		t.Fatal("expected removing k1 and k3 to succeed")
	}
	if ring.SlotCount() != 0 {
		t.Fatalf("expected 0 slots, got %d", ring.SlotCount())
	}
	if ring.PrimarySlot() != nil {
		t.Error("expected PrimarySlot() to be nil on empty ring")
	}
	_, err := ring.SelectKey(time.Now().UnixNano())
	if err != ErrAllKeysExhausted {
		t.Fatalf("expected ErrAllKeysExhausted on empty ring, got %v", err)
	}
}

func TestKeyRing_RemoveSlotNilSafe(t *testing.T) {
	var ring *KeyRing
	if ring.RemoveSlot("k1") {
		t.Error("nil ring RemoveSlot should return false")
	}
	if ring.AllSlots() != nil {
		t.Error("nil ring AllSlots should return nil")
	}
}
