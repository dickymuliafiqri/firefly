package domain

import (
	"errors"
	"testing"
	"time"
)

// TestKeyRing_SelectKeyLenient_IgnoresRevokedAndCooldown pins the selection an
// upstream with the AlwaysAvailable pin uses. For such an upstream neither a
// cooldown nor a revocation describes the credential — an OpenCode Free key is
// the literal string "public" and its allowance follows the caller's egress IP,
// so a 429 describes the network and a 401 a stale shared key, and dropping the
// slot only turned a provider hiccup into a local "all credentials exhausted".
func TestKeyRing_SelectKeyLenient_IgnoresRevokedAndCooldown(t *testing.T) {
	now := time.Now().UnixNano()

	cooling := &KeySlot{Ref: "k1"}
	coolingRing := NewKeyRing(KeyStrategyRoundRobin, []*KeySlot{cooling})
	coolingRing.MarkCooldown(cooling.Ref, 30*time.Second, now)

	if got, err := coolingRing.SelectKey(now); err == nil || got != nil {
		t.Fatalf("strict selection must skip a cooling key, got %+v err=%v", got, err)
	}
	if got, err := coolingRing.SelectKeyLenient(now); err != nil || got != cooling {
		t.Fatalf("lenient selection must keep a cooling key selectable, got %+v err=%v", got, err)
	}

	revoked := &KeySlot{Ref: "k2"}
	revoked.Revoked.Store(true)
	revokedRing := NewKeyRing(KeyStrategyRoundRobin, []*KeySlot{revoked})

	if got, err := revokedRing.SelectKey(now); err == nil || got != nil {
		t.Fatalf("strict selection must skip a revoked key, got %+v err=%v", got, err)
	}
	if got, err := revokedRing.SelectKeyLenient(now); err != nil || got != revoked {
		t.Fatalf("lenient selection must keep a revoked key selectable, got %+v err=%v", got, err)
	}
}

// TestKeyRing_SelectKeyLenient_HonorsConcurrency pins the boundary of the pin:
// AlwaysAvailable overrides the outcomes we attribute to the provider, never the
// capacity of the account itself. A saturated slot stays unselectable so the
// Layer 3 gate can still answer an honest 429 instead of the gateway forwarding
// past the operator's own concurrency ceiling.
func TestKeyRing_SelectKeyLenient_HonorsConcurrency(t *testing.T) {
	now := time.Now().UnixNano()
	saturated := &KeySlot{Ref: "k1", MaxConcurrent: 1}
	saturated.Inflight.Store(1)
	ring := NewKeyRing(KeyStrategyRoundRobin, []*KeySlot{saturated})

	if got, err := ring.SelectKeyLenient(now); !errors.Is(err, ErrAllKeysExhausted) || got != nil {
		t.Fatalf("a saturated slot must stay unselectable even when lenient, got %+v err=%v", got, err)
	}
	if saturated.IsAvailableLenient(now) {
		t.Fatal("IsAvailableLenient must still honor MaxConcurrent")
	}

	// An empty ring is exhaustion in both modes, not a nil dereference.
	empty := NewKeyRing(KeyStrategyRoundRobin, nil)
	if _, err := empty.SelectKeyLenient(now); !errors.Is(err, ErrAllKeysExhausted) {
		t.Fatalf("empty lenient ring = %v, want ErrAllKeysExhausted", err)
	}
}

// TestResolveTarget_AlwaysAvailableIgnoresDeadKey pins the resolver half of the
// reported bug: before this, a single revoked/cooldown slot made
// ResolveTargetWithCandidates return ErrAllKeysExhausted and the transport
// answered the client with "all upstream credentials exhausted; retry shortly",
// even though the upstream (and its key) was perfectly usable. With the pin set
// the same snapshot resolves to the same slot.
func TestResolveTarget_AlwaysAvailableIgnoresDeadKey(t *testing.T) {
	now := time.Now().UnixNano()

	keyless := &Upstream{
		Name:            "opencode-free",
		Protocol:        ProtocolOpenCode,
		BaseURL:         "https://opencode.ai/zen/v1",
		AlwaysAvailable: true,
		KeyRing:         NewKeyRing(KeyStrategyRoundRobin, []*KeySlot{{Ref: "opencode-free-public", Secret: "public"}}),
	}
	strict := &Upstream{
		Name:     "paid",
		Protocol: ProtocolOpenAI,
		BaseURL:  "https://api.openai.com/v1",
		KeyRing:  NewKeyRing(KeyStrategyRoundRobin, []*KeySlot{{Ref: "paid-key"}}),
	}
	for _, up := range []*Upstream{keyless, strict} {
		up.KeyRing.MarkCooldown(up.KeyRing.AllSlots()[0].Ref, 30*time.Second, now)
		up.KeyRing.MarkRevoked(up.KeyRing.AllSlots()[0].Ref)
	}

	snap := NewCatalogSnapshot(
		1,
		map[string]*Upstream{"opencode-free": keyless, "paid": strict},
		[]string{"opencode-free", "paid"},
		map[string]*ModelEntry{
			"free-model": {PublicName: "free-model", Upstream: "opencode-free", UpstreamModel: "mimo", Enabled: true},
			"paid-model": {PublicName: "paid-model", Upstream: "paid", UpstreamModel: "gpt-4o", Enabled: true},
		},
		[]string{"free-model", "paid-model"},
		map[string]*Tenant{
			"h": {KeyHash: "h", Name: "t", Status: TenantStatusActive, AllowedModels: []string{"*"}},
		},
		[]string{"h"},
	)
	tenant, _ := snap.TenantByHash("h")

	tgt, _, err := snap.ResolveTargetWithCandidates(tenant, "free-model", nil, nil)
	if err != nil {
		t.Fatalf("pinned upstream must resolve despite a dead key, got %v", err)
	}
	if tgt.KeySlot == nil || tgt.KeySlot.Ref != "opencode-free-public" {
		t.Fatalf("expected the pinned free-tier slot, got %+v", tgt.KeySlot)
	}

	// The strict upstream keeps its previous behavior: exhaustion is reported so
	// the client can be told to retry rather than have a revoked key used.
	if _, _, err := snap.ResolveTargetWithCandidates(tenant, "paid-model", nil, nil); !errors.Is(err, ErrAllKeysExhausted) {
		t.Fatalf("strict upstream = %v, want ErrAllKeysExhausted", err)
	}
}
