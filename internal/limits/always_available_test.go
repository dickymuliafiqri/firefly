package limits

import (
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

// TestAcquireKeySlotLenient_BypassesCooldownAndRevocation pins the Layer 3 gate
// for an upstream with the AlwaysAvailable pin. The gate used to re-check the
// slot's own state and answer ErrRateLimited for a cooling or revoked key, which
// meant the resolver could hand the request a key the gate then refused — the
// request never reached the provider at all. Concurrency is still enforced.
func TestAcquireKeySlotLenient_BypassesCooldownAndRevocation(t *testing.T) {
	l := New()
	now := time.Now().UnixNano()

	cooling := &domain.KeySlot{Ref: "opencode-free-public", Secret: "public"}
	cooling.CooldownUntil.Store(now + int64(30*time.Second))

	if _, err := l.AcquireKeySlot(cooling); err == nil {
		t.Fatal("the strict gate must refuse a cooling key")
	}
	release, err := l.AcquireKeySlotLenient(cooling)
	if err != nil {
		t.Fatalf("the lenient gate must admit a cooling key, got %v", err)
	}
	release()

	revoked := &domain.KeySlot{Ref: "k2"}
	revoked.Revoked.Store(true)
	if _, err := l.AcquireKeySlot(revoked); err == nil {
		t.Fatal("the strict gate must refuse a revoked key")
	}
	release, err = l.AcquireKeySlotLenient(revoked)
	if err != nil {
		t.Fatalf("the lenient gate must admit a revoked key, got %v", err)
	}
	release()
}

// TestAcquireKeySlotLenient_StillEnforcesConcurrency pins that the pin never
// lets the gateway exceed the account's own ceiling: the operator's
// max_concurrent is a limit on our traffic, not an outcome attributed to the
// provider.
func TestAcquireKeySlotLenient_StillEnforcesConcurrency(t *testing.T) {
	l := New()
	slot := &domain.KeySlot{Ref: "k", MaxConcurrent: 1}

	release, err := l.AcquireKeySlotLenient(slot)
	if err != nil {
		t.Fatalf("first lenient acquire: %v", err)
	}
	if _, err := l.AcquireKeySlotLenient(slot); err == nil {
		t.Fatal("a saturated slot must still be refused when lenient")
	}
	release()
	if release2, err := l.AcquireKeySlotLenient(slot); err != nil {
		t.Fatalf("acquire after release: %v", err)
	} else {
		release2()
	}
}
