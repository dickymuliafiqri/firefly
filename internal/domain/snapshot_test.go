package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func testSnapshot() *CatalogSnapshot {
	up := &Upstream{Name: "openai-main", Protocol: ProtocolOpenAI, BaseURL: "https://x/v1", CredentialRef: "OPENAI_KEY"}
	return NewCatalogSnapshot(
		1,
		map[string]*Upstream{"openai-main": up},
		[]string{"openai-main"},
		map[string]*ModelEntry{
			"gpt-4o-mini": {PublicName: "gpt-4o-mini", Upstream: "openai-main", UpstreamModel: "gpt-4o-mini", Enabled: true},
			"off":         {PublicName: "off", Upstream: "openai-main", UpstreamModel: "x", Enabled: false},
		},
		[]string{"gpt-4o-mini"},
		map[string]*Tenant{
			"h1": {KeyHash: "h1", Name: "alpha", Status: TenantStatusActive, AllowedModels: []string{"gpt-4o-mini"}},
			"h2": {KeyHash: "h2", Name: "internal", Status: TenantStatusActive, AllowedModels: []string{"*"}},
		},
		[]string{"h1", "h2"},
	)
}

func TestResolveTarget(t *testing.T) {
	s := testSnapshot()
	alpha, _ := s.TenantByHash("h1")
	tgt, err := s.ResolveTarget(alpha, "gpt-4o-mini")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tgt.UpstreamModel != "gpt-4o-mini" || tgt.CredentialRef != "OPENAI_KEY" || tgt.ResolvedModel != "gpt-4o-mini" {
		t.Fatalf("bad target: %+v", tgt)
	}
}

func TestResolveTargetForbidden(t *testing.T) {
	s := testSnapshot()
	alpha, _ := s.TenantByHash("h1")
	// alpha is not allowed gpt-4o (which isn't even in the catalog here).
	_, err := s.ResolveTarget(alpha, "gpt-4o-mini-2")
	if err == nil {
		t.Fatal("expected not-found error")
	}
	re, ok := err.(*ResolveError)
	if !ok || re.Kind != ResolveModelNotFound {
		t.Fatalf("want ResolveModelNotFound, got %T %v", err, err)
	}
}

func TestResolveDisabledModel(t *testing.T) {
	s := testSnapshot()
	internal, _ := s.TenantByHash("h2")
	_, err := s.ResolveTarget(internal, "off")
	if err == nil {
		t.Fatal("disabled model must not resolve")
	}
	if re, ok := err.(*ResolveError); !ok || re.Kind != ResolveModelNotFound {
		t.Fatalf("want ResolveModelNotFound, got %v", err)
	}
}

func TestResolveForbiddenByPolicy(t *testing.T) {
	s := testSnapshot()
	// Give alpha access to a real model then remove it via a narrower tenant.
	restricted := &Tenant{KeyHash: "h3", Name: "beta", Status: TenantStatusActive, AllowedModels: []string{"other"}}
	_, err := s.ResolveTarget(restricted, "gpt-4o-mini")
	if err == nil {
		t.Fatal("expected forbidden")
	}
	if re, ok := err.(*ResolveError); !ok || re.Kind != ResolveModelForbidden {
		t.Fatalf("want ResolveModelForbidden, got %v", err)
	}
}

func TestAllowsModelWildcard(t *testing.T) {
	tt := Tenant{AllowedModels: []string{"*"}}
	if !tt.AllowsModel("anything") {
		t.Fatal("wildcard should allow all")
	}
}

func TestKeySlot_AvailabilityAndCooldown(t *testing.T) {
	slot := &KeySlot{
		Ref:           "KEY_A",
		Secret:        "secret-a",
		RPS:           10,
		MaxConcurrent: 2,
	}

	nowNano := int64(1000000000)
	if !slot.IsAvailable(nowNano) {
		t.Fatal("new slot should be available")
	}
	if slot.IsInCooldown(nowNano) {
		t.Fatal("new slot should not be in cooldown")
	}

	// Inflight saturation
	slot.Inflight.Store(2)
	if slot.IsAvailable(nowNano) {
		t.Fatal("slot with saturated inflight should not be available")
	}
	slot.Inflight.Store(1)
	if !slot.IsAvailable(nowNano) {
		t.Fatal("slot with available inflight should be available")
	}

	// Cooldown
	slot.CooldownUntil.Store(nowNano + 5000)
	if !slot.IsInCooldown(nowNano) {
		t.Fatal("slot should report in cooldown")
	}
	if slot.IsAvailable(nowNano) {
		t.Fatal("slot in cooldown should not be available")
	}
	// Past cooldown
	if slot.IsInCooldown(nowNano + 6000) {
		t.Fatal("slot past cooldown should not report in cooldown")
	}

	// Revoked
	slot.CooldownUntil.Store(0)
	slot.Revoked.Store(true)
	if slot.IsAvailable(nowNano) {
		t.Fatal("revoked slot must not be available")
	}
}

func TestKeyRing_ConstructorAndHelpers(t *testing.T) {
	s1 := &KeySlot{Ref: "K1"}
	s2 := &KeySlot{Ref: "K2"}

	// Default strategy
	kr := NewKeyRing("", []*KeySlot{s1, s2})
	if kr.Strategy != KeyStrategyRoundRobin {
		t.Fatalf("expected round_robin, got %s", kr.Strategy)
	}
	if kr.SlotCount() != 2 {
		t.Fatalf("expected 2 slots, got %d", kr.SlotCount())
	}
	if kr.PrimarySlot() != s1 {
		t.Fatalf("primary slot mismatch: got %+v, want %+v", kr.PrimarySlot(), s1)
	}

	// Nil safety
	var nilRing *KeyRing
	if nilRing.SlotCount() != 0 {
		t.Fatal("nil ring slot count should be 0")
	}
	if nilRing.PrimarySlot() != nil {
		t.Fatal("nil ring primary slot should be nil")
	}
}

func TestResolveTarget_WithKeySlotAndFallback(t *testing.T) {
	s1 := &KeySlot{Ref: "PRIMARY_KEY", Secret: "sk-p"}
	s2 := &KeySlot{Ref: "TENANT_OVERRIDE_KEY", Secret: "sk-o"}
	up := &Upstream{
		Name:          "u1",
		Protocol:      ProtocolOpenAI,
		BaseURL:       "https://api.openai.com/v1",
		CredentialRef: "PRIMARY_KEY",
		KeyRing:       NewKeyRing(KeyStrategyRoundRobin, []*KeySlot{s1, s2}),
	}
	upFallback := &Upstream{
		Name:          "u2",
		Protocol:      ProtocolOpenAI,
		BaseURL:       "https://backup.openai.com/v1",
		CredentialRef: "FALLBACK_KEY",
	}

	snap := NewCatalogSnapshot(
		1,
		map[string]*Upstream{"u1": up, "u2": upFallback},
		[]string{"u1", "u2"},
		map[string]*ModelEntry{
			"gpt-4": {
				PublicName:    "gpt-4",
				Upstream:      "u1",
				UpstreamModel: "gpt-4",
				Enabled:       true,
			},
		},
		[]string{"gpt-4"},
		map[string]*Tenant{
			"h_default": {
				KeyHash:       "h_default",
				Name:          "standard",
				Status:        TenantStatusActive,
				AllowedModels: []string{"*"},
			},
			"h_override": {
				KeyHash:       "h_override",
				Name:          "enterprise",
				Status:        TenantStatusActive,
				AllowedModels: []string{"*"},
				CredentialRef: "TENANT_OVERRIDE_KEY",
			},
		},
		[]string{"h_default", "h_override"},
	)

	// Default tenant uses round-robin key selection across the ring. The ring's
	// starting slot is seeded from a process-wide rotation counter (so rotation
	// survives snapshot rebuilds), so the selected slot may be either s1 or s2.
	// Assert only that the resolved KeySlot is one of the ring slots and that
	// CredentialRef is consistent with the chosen slot.
	stdTenant, _ := snap.TenantByHash("h_default")
	tgt, err := snap.ResolveTarget(stdTenant, "gpt-4")
	if err != nil {
		t.Fatalf("resolve target: %v", err)
	}
	if tgt.KeySlot != s1 && tgt.KeySlot != s2 {
		t.Fatalf("target KeySlot is not a ring slot: got %+v", tgt.KeySlot)
	}
	if tgt.CredentialRef != tgt.KeySlot.Ref {
		t.Fatalf("target CredentialRef %q inconsistent with selected slot ref %q", tgt.CredentialRef, tgt.KeySlot.Ref)
	}

	// Override tenant matches s2
	ovTenant, _ := snap.TenantByHash("h_override")
	tgt2, err := snap.ResolveTarget(ovTenant, "gpt-4")
	if err != nil {
		t.Fatalf("resolve target override: %v", err)
	}
	if tgt2.CredentialRef != "TENANT_OVERRIDE_KEY" {
		t.Fatalf("target credential ref = %s, want TENANT_OVERRIDE_KEY", tgt2.CredentialRef)
	}
	if tgt2.KeySlot != s2 {
		t.Fatalf("target KeySlot mismatch: got %+v, want %+v", tgt2.KeySlot, s2)
	}

}

func TestResolveTargetWithBreakerFallback(t *testing.T) {
	k1 := &KeySlot{Ref: "K1", Secret: "s1"}
	k2 := &KeySlot{Ref: "K2", Secret: "s2"}
	ring1 := NewKeyRing(KeyStrategyRoundRobin, []*KeySlot{k1})
	ring2 := NewKeyRing(KeyStrategyRoundRobin, []*KeySlot{k2})

	u1 := &Upstream{Name: "u-primary", Protocol: ProtocolOpenAI, BaseURL: "https://p/v1", KeyRing: ring1}
	u2 := &Upstream{Name: "u-fallback", Protocol: ProtocolOpenAI, BaseURL: "https://f/v1", KeyRing: ring2}

	snap := NewCatalogSnapshot(
		1,
		map[string]*Upstream{"u-primary": u1, "u-fallback": u2},
		[]string{"u-primary", "u-fallback"},
		map[string]*ModelEntry{
			"gpt-4-primary": {
				PublicName:    "gpt-4-primary",
				Upstream:      "u-primary",
				UpstreamModel: "gpt-4-up",
				Enabled:       true,
			},
			"gpt-4-fallback": {
				PublicName:    "gpt-4-fallback",
				Upstream:      "u-fallback",
				UpstreamModel: "gpt-4-up",
				Enabled:       true,
			},
		},
		[]string{"gpt-4-primary", "gpt-4-fallback"},
		map[string]*Tenant{
			"h": {KeyHash: "h", Name: "t1", Status: TenantStatusActive, AllowedModels: []string{"*"}},
		},
		[]string{"h"},
		WithCombos(
			map[string]*Combo{
				"gpt-4": {
					Name:     "gpt-4",
					Strategy: RoutingStrategyFailover,
					Models:   []string{"gpt-4-primary", "gpt-4-fallback"},
					Enabled:  true,
				},
			},
			[]string{"gpt-4"},
		),
	)
	tenant, _ := snap.TenantByHash("h")

	// Case 1: Both upstreams healthy -> selects primary
	tgt, fb, err := snap.ResolveTargetWithBreaker(tenant, "gpt-4", func(string) bool { return true })
	if err != nil || fb || tgt.Upstream.Name != "u-primary" {
		t.Fatalf("expected primary upstream, got fb=%v tgt=%+v err=%v", fb, tgt, err)
	}

	// Case 2: Primary breaker open -> selects fallback
	tgt, fb, err = snap.ResolveTargetWithBreaker(tenant, "gpt-4", func(name string) bool {
		return name != "u-primary" // u-primary open
	})
	if err != nil || !fb || tgt.Upstream.Name != "u-fallback" {
		t.Fatalf("expected fallback upstream when primary breaker open, got fb=%v tgt=%+v err=%v", fb, tgt, err)
	}

	// Case 3: Primary keys in cooldown -> selects fallback
	k1.CooldownUntil.Store(time.Now().Add(time.Minute).UnixNano())
	tgt, fb, err = snap.ResolveTargetWithBreaker(tenant, "gpt-4", func(string) bool { return true })
	if err != nil || !fb || tgt.Upstream.Name != "u-fallback" {
		t.Fatalf("expected fallback upstream when primary keys exhausted, got fb=%v tgt=%+v err=%v", fb, tgt, err)
	}

	// Case 4: Both upstreams unavailable
	tgt, fb, err = snap.ResolveTargetWithBreaker(tenant, "gpt-4", func(string) bool { return false })
	if err == nil {
		t.Fatalf("expected error when all upstreams unavailable, got tgt=%+v fb=%v", tgt, fb)
	}
}

// quotaComboSnapshot builds a two-member failover combo whose members run on
// separate OAuth credentials, mirroring a real Antigravity setup where one
// account is out of allowance and another is not.
func quotaComboSnapshot() (*CatalogSnapshot, *Tenant) {
	u1 := &Upstream{Name: "ag-1", Protocol: ProtocolOpenAI, BaseURL: "https://a/v1", CredentialRef: "oauth:ag-1"}
	u2 := &Upstream{Name: "ag-2", Protocol: ProtocolOpenAI, BaseURL: "https://b/v1", CredentialRef: "oauth:ag-2"}
	snap := NewCatalogSnapshot(
		1,
		map[string]*Upstream{"ag-1": u1, "ag-2": u2},
		[]string{"ag-1", "ag-2"},
		map[string]*ModelEntry{
			"flash-a": {PublicName: "flash-a", Upstream: "ag-1", UpstreamModel: "gemini-3.8-flash", Enabled: true},
			"flash-b": {PublicName: "flash-b", Upstream: "ag-2", UpstreamModel: "gemini-3.8-flash", Enabled: true},
		},
		[]string{"flash-a", "flash-b"},
		map[string]*Tenant{
			"h": {KeyHash: "h", Name: "t1", Status: TenantStatusActive, AllowedModels: []string{"*"}},
		},
		[]string{"h"},
		WithCombos(
			map[string]*Combo{
				"fast": {Name: "fast", Strategy: RoutingStrategyFailover, Models: []string{"flash-a", "flash-b"}, Enabled: true},
			},
			[]string{"fast"},
		),
	)
	tenant, _ := snap.TenantByHash("h")
	return snap, tenant
}

// TestResolveTarget_CandidateVetoFailsOver pins the account-level veto: a member
// whose credential has no allowance is skipped and the combo serves the request
// from the member that does, exactly as it does for a breaker-open upstream.
func TestResolveTarget_CandidateVetoFailsOver(t *testing.T) {
	snap, tenant := quotaComboSnapshot()

	tgt, fb, err := snap.ResolveTargetWithCandidates(tenant, "fast", nil, func(c TargetCandidate) bool {
		return c.CredentialRef != "oauth:ag-1" // ag-1 is out of allowance
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tgt.Upstream.Name != "ag-2" {
		t.Fatalf("expected failover to ag-2, got %s", tgt.Upstream.Name)
	}
	if !fb {
		t.Fatal("a vetoed primary must be reported as a fallback, not a silent swap")
	}
}

// TestResolveTarget_CandidateVetoAllExhausted pins the failure kind: when quota
// is the only reason nothing resolved, the error says so, so the transport can
// answer an honest 429 instead of a misleading "no upstream available".
func TestResolveTarget_CandidateVetoAllExhausted(t *testing.T) {
	snap, tenant := quotaComboSnapshot()

	_, _, err := snap.ResolveTargetWithCandidates(tenant, "fast", nil, func(TargetCandidate) bool {
		return false
	})
	if !errors.Is(err, ErrProviderQuotaExhausted) {
		t.Fatalf("want ErrProviderQuotaExhausted, got %T %v", err, err)
	}

	// A breaker filter is a different failure and must keep its own kind: the
	// veto only decides the candidate, never the classification.
	_, _, err = snap.ResolveTargetWithCandidates(tenant, "fast", func(string) bool { return false },
		func(TargetCandidate) bool { return true })
	if errors.Is(err, ErrProviderQuotaExhausted) {
		t.Fatal("a breaker rejection must not be reported as quota exhaustion")
	}
	var re *ResolveError
	if !errors.As(err, &re) || re.Kind != ResolveUpstreamUnavailable {
		t.Fatalf("want ResolveUpstreamUnavailable, got %T %v", err, err)
	}
}

// TestResolveTargetWithBreaker_IgnoresCandidateVeto pins backward compatibility:
// the old entry point keeps its exact previous behavior, since callers that do
// not know about quota must never be vetoed.
func TestResolveTargetWithBreaker_IgnoresCandidateVeto(t *testing.T) {
	snap, tenant := quotaComboSnapshot()
	tgt, fb, err := snap.ResolveTargetWithBreaker(tenant, "fast", nil)
	if err != nil || fb || tgt.Upstream.Name != "ag-1" {
		t.Fatalf("expected the primary, got fb=%v tgt=%+v err=%v", fb, tgt, err)
	}
}

func TestKeySlotSecretMasking(t *testing.T) {
	secret := "sk-plaintext-super-sensitive-token-12345"
	slot := &KeySlot{
		Ref:           "OPENAI_API_KEY_PRIMARY",
		Secret:        secret,
		RPS:           50.0,
		MaxConcurrent: 10,
	}

	// 1. %v formatting
	vStr := fmt.Sprintf("%v", slot)
	if strings.Contains(vStr, secret) {
		t.Fatalf("%%v leaked secret plaintext: %s", vStr)
	}
	if !strings.Contains(vStr, "OPENAI_API_KEY_PRIMARY") {
		t.Fatalf("%%v missing ref name: %s", vStr)
	}

	// 2. %+v formatting
	plusVStr := fmt.Sprintf("%+v", slot)
	if strings.Contains(plusVStr, secret) {
		t.Fatalf("%%+v leaked secret plaintext: %s", plusVStr)
	}
	if !strings.Contains(plusVStr, "[REDACTED]") {
		t.Fatalf("%%+v missing [REDACTED] mask: %s", plusVStr)
	}

	// 3. %#v formatting
	hashVStr := fmt.Sprintf("%#v", slot)
	if strings.Contains(hashVStr, secret) {
		t.Fatalf("%%#v leaked secret plaintext: %s", hashVStr)
	}

	// 4. json.Marshal
	b, err := json.Marshal(slot)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	jsonStr := string(b)
	if strings.Contains(jsonStr, secret) {
		t.Fatalf("json.Marshal leaked secret: %s", jsonStr)
	}
	if !strings.Contains(jsonStr, "OPENAI_API_KEY_PRIMARY") {
		t.Fatalf("json.Marshal missing ref name: %s", jsonStr)
	}
}

func TestResolveTargetWithDisabledUpstream(t *testing.T) {
	uPrimary := &Upstream{Name: "u-primary", Protocol: ProtocolOpenAI, BaseURL: "https://p/v1", CredentialRef: "KEY1", Disabled: true}
	uFallback := &Upstream{Name: "u-fallback", Protocol: ProtocolOpenAI, BaseURL: "https://f/v1", CredentialRef: "KEY2", Disabled: false}

	snap := NewCatalogSnapshot(
		1,
		map[string]*Upstream{"u-primary": uPrimary, "u-fallback": uFallback},
		[]string{"u-primary", "u-fallback"},
		map[string]*ModelEntry{
			"m-primary": {
				PublicName:    "m-primary",
				Upstream:      "u-primary",
				UpstreamModel: "gpt-test-priv",
				Enabled:       true,
			},
			"m-fallback": {
				PublicName:    "m-fallback",
				Upstream:      "u-fallback",
				UpstreamModel: "gpt-test-priv",
				Enabled:       true,
			},
		},
		[]string{"m-primary", "m-fallback"},
		map[string]*Tenant{
			"h": {KeyHash: "h", Name: "t1", Status: TenantStatusActive, AllowedModels: []string{"*"}},
		},
		[]string{"h"},
		WithCombos(
			map[string]*Combo{
				"gpt-test": {
					Name:     "gpt-test",
					Strategy: RoutingStrategyFailover,
					Models:   []string{"m-primary", "m-fallback"},
					Enabled:  true,
				},
			},
			[]string{"gpt-test"},
		),
	)
	tenant, _ := snap.TenantByHash("h")

	// Primary is disabled -> should route to fallback
	tgt, fb, err := snap.ResolveTargetWithBreaker(tenant, "gpt-test", nil)
	if err != nil {
		t.Fatalf("expected fallback target, got err: %v", err)
	}
	if !fb || tgt.Upstream.Name != "u-fallback" {
		t.Fatalf("expected fallback u-fallback, got fb=%v tgt=%+v", fb, tgt)
	}

	// Disable fallback as well -> should return ResolveUpstreamUnavailable
	uFallback.Disabled = true
	_, _, err = snap.ResolveTargetWithBreaker(tenant, "gpt-test", nil)
	if err == nil {
		t.Fatalf("expected error when all upstreams disabled")
	}
	re, ok := err.(*ResolveError)
	if !ok || re.Kind != ResolveUpstreamUnavailable {
		t.Fatalf("expected ResolveUpstreamUnavailable, got %v (%T)", err, err)
	}
}

func TestResolveTarget_RoundRobin(t *testing.T) {
	u1 := &Upstream{Name: "u1", Protocol: ProtocolOpenAI, BaseURL: "https://u1/v1", CredentialRef: "K1"}
	u2 := &Upstream{Name: "u2", Protocol: ProtocolOpenAI, BaseURL: "https://u2/v1", CredentialRef: "K2"}
	u3 := &Upstream{Name: "u3", Protocol: ProtocolOpenAI, BaseURL: "https://u3/v1", CredentialRef: "K3"}

	snap := NewCatalogSnapshot(
		1,
		map[string]*Upstream{"u1": u1, "u2": u2, "u3": u3},
		[]string{"u1", "u2", "u3"},
		map[string]*ModelEntry{
			"m1": {PublicName: "m1", Upstream: "u1", UpstreamModel: "u1-model", Enabled: true},
			"m2": {PublicName: "m2", Upstream: "u2", UpstreamModel: "u2-model", Enabled: true},
			"m3": {PublicName: "m3", Upstream: "u3", UpstreamModel: "u3-model", Enabled: true},
		},
		[]string{"m1", "m2", "m3"},
		map[string]*Tenant{
			"t": {KeyHash: "t", Name: "tenant", Status: TenantStatusActive, AllowedModels: []string{"*"}},
		},
		[]string{"t"},
		WithCombos(
			map[string]*Combo{
				"gpt-multi": {
					Name:     "gpt-multi",
					Strategy: RoutingStrategyRoundRobin,
					Models:   []string{"m1", "m2", "m3"},
					Enabled:  true,
				},
			},
			[]string{"gpt-multi"},
		),
	)
	tenant, _ := snap.TenantByHash("t")

	// Call 6 times: should cycle round-robin across candidates [m1, m2, m3]
	// Combo round-robin cycles across candidates [u1, u2, u3]. The starting
	// offset is seeded from a process-wide rotation counter (so rotation
	// survives snapshot rebuilds), so the absolute first upstream is not fixed
	// across runs. Assert the rotation order relative to the first result, and
	// that fallback-used is reported whenever the chosen member is not the
	// combo's primary model (m1 -> u1).
	cycle := []string{"u1", "u2", "u3"}
	idxOf := map[string]int{"u1": 0, "u2": 1, "u3": 2}

	firstTgt, _, err := snap.ResolveTargetWithBreaker(tenant, "gpt-multi", nil)
	if err != nil {
		t.Fatalf("initial resolve failed: %v", err)
	}
	startIdx := idxOf[firstTgt.Upstream.Name]

	// Re-check the first result plus 5 more, verifying strict rotation.
	checkCall := func(i int, tgt *Target, fb bool) {
		expectedUpstream := cycle[(startIdx+i)%len(cycle)]
		if tgt.Upstream.Name != expectedUpstream {
			t.Fatalf("call %d: got upstream %q, want %q", i, tgt.Upstream.Name, expectedUpstream)
		}
		expectedModel := expectedUpstream + "-model"
		if tgt.UpstreamModel != expectedModel {
			t.Fatalf("call %d: got upstreamModel %q, want %q", i, tgt.UpstreamModel, expectedModel)
		}
		expectedResolvedModel := "m" + expectedUpstream[1:]
		if tgt.ResolvedModel != expectedResolvedModel {
			t.Fatalf("call %d: got resolvedModel %q, want %q", i, tgt.ResolvedModel, expectedResolvedModel)
		}
		expectedFB := (expectedUpstream != "u1")
		if fb != expectedFB {
			t.Fatalf("call %d: got fallbackUsed=%v, want %v", i, fb, expectedFB)
		}
	}

	checkCall(0, firstTgt, firstTgt.Upstream.Name != "u1")
	for i := 1; i < 6; i++ {
		tgt, fb, err := snap.ResolveTargetWithBreaker(tenant, "gpt-multi", nil)
		if err != nil {
			t.Fatalf("call %d: unexpected error %v", i, err)
		}
		checkCall(i, tgt, fb)
	}

	// Breaker test: if u2 breaker is open (canUse returns false), it skips u2
	canUse := func(name string) bool {
		return name != "u2"
	}
	// Next cursor call should hit u1, then u3 (skipping u2), then u1
	tgt1, _, _ := snap.ResolveTargetWithBreaker(tenant, "gpt-multi", canUse)
	tgt2, _, _ := snap.ResolveTargetWithBreaker(tenant, "gpt-multi", canUse)
	if tgt1.Upstream.Name == "u2" || tgt2.Upstream.Name == "u2" {
		t.Fatalf("expected u2 to be skipped by circuit breaker, got tgt1=%s tgt2=%s",
			tgt1.Upstream.Name, tgt2.Upstream.Name)
	}
}

func TestResolveTarget_LeastInflight(t *testing.T) {
	u1 := &Upstream{Name: "u1", Protocol: ProtocolOpenAI, BaseURL: "https://u1/v1", CredentialRef: "K1"}
	u2 := &Upstream{Name: "u2", Protocol: ProtocolOpenAI, BaseURL: "https://u2/v1", CredentialRef: "K2"}
	u3 := &Upstream{Name: "u3", Protocol: ProtocolOpenAI, BaseURL: "https://u3/v1", CredentialRef: "K3"}

	snap := NewCatalogSnapshot(
		1,
		map[string]*Upstream{"u1": u1, "u2": u2, "u3": u3},
		[]string{"u1", "u2", "u3"},
		map[string]*ModelEntry{
			"m1": {PublicName: "m1", Upstream: "u1", UpstreamModel: "gpt-lb-priv", Enabled: true},
			"m2": {PublicName: "m2", Upstream: "u2", UpstreamModel: "gpt-lb-priv", Enabled: true},
			"m3": {PublicName: "m3", Upstream: "u3", UpstreamModel: "gpt-lb-priv", Enabled: true},
		},
		[]string{"m1", "m2", "m3"},
		map[string]*Tenant{
			"t": {KeyHash: "t", Name: "tenant", Status: TenantStatusActive, AllowedModels: []string{"*"}},
		},
		[]string{"t"},
		WithCombos(
			map[string]*Combo{
				"gpt-lb": {
					Name:     "gpt-lb",
					Strategy: RoutingStrategyLeastInflight,
					Models:   []string{"m1", "m2", "m3"},
					Enabled:  true,
				},
			},
			[]string{"gpt-lb"},
		),
	)
	tenant, _ := snap.TenantByHash("t")

	// 1. All at 0 inflight -> picks first candidate (u1 via m1)
	tgt, _, err := snap.ResolveTargetWithBreaker(tenant, "gpt-lb", nil)
	if err != nil || tgt.Upstream.Name != "u1" {
		t.Fatalf("expected u1, got tgt=%+v err=%v", tgt, err)
	}

	// 2. Set u1 inflight high -> should pick u2 (lowest inflight)
	u1.Inflight.Store(10)
	u2.Inflight.Store(2)
	u3.Inflight.Store(5)

	tgt, fb, err := snap.ResolveTargetWithBreaker(tenant, "gpt-lb", nil)
	if err != nil || tgt.Upstream.Name != "u2" {
		t.Fatalf("expected u2 (inflight 2), got tgt=%+v err=%v", tgt, err)
	}
	if !fb {
		t.Fatalf("expected fallbackUsed=true for u2")
	}

	// 3. Set u2 inflight higher than u3 -> should pick u3
	u2.Inflight.Store(8)
	tgt, _, err = snap.ResolveTargetWithBreaker(tenant, "gpt-lb", nil)
	if err != nil || tgt.Upstream.Name != "u3" {
		t.Fatalf("expected u3 (inflight 5), got tgt=%+v err=%v", tgt, err)
	}

	// 4. Circuit breaker skip: u3 is open, u2 is 8, u1 is 10 -> should pick u2
	canUse := func(name string) bool {
		return name != "u3"
	}
	tgt, _, err = snap.ResolveTargetWithBreaker(tenant, "gpt-lb", canUse)
	if err != nil || tgt.Upstream.Name != "u2" {
		t.Fatalf("expected u2 when u3 breaker open, got tgt=%+v err=%v", tgt, err)
	}
}

func TestTenant_IsExpired(t *testing.T) {
	nowMs := time.Now().UnixMilli()
	nowSec := time.Now().Unix()

	// 1. Unlimited (expires_at = 0)
	tUnlimited := &Tenant{ExpiresAt: 0}
	if tUnlimited.IsExpired(nowMs) {
		t.Errorf("expected tenant with ExpiresAt=0 to not be expired")
	}

	// 2. Future expiration in seconds (e.g. 10 digits)
	tFutureSec := &Tenant{ExpiresAt: nowSec + 3600}
	if tFutureSec.IsExpired(nowMs) {
		t.Errorf("expected tenant with future seconds ExpiresAt to not be expired")
	}

	// 3. Past expiration in seconds
	tPastSec := &Tenant{ExpiresAt: nowSec - 3600}
	if !tPastSec.IsExpired(nowMs) {
		t.Errorf("expected tenant with past seconds ExpiresAt to be expired")
	}

	// 4. Future expiration in milliseconds (e.g. 13 digits)
	tFutureMs := &Tenant{ExpiresAt: nowMs + 3600000}
	if tFutureMs.IsExpired(nowMs) {
		t.Errorf("expected tenant with future milliseconds ExpiresAt to not be expired")
	}

	// 5. Past expiration in milliseconds
	tPastMs := &Tenant{ExpiresAt: nowMs - 3600000}
	if !tPastMs.IsExpired(nowMs) {
		t.Errorf("expected tenant with past milliseconds ExpiresAt to be expired")
	}
}

// nestedComboSnapshot builds a two-level combo graph:
//
//	outer (failover) -> [inner-a, m3]
//	inner-a (failover) -> [m1, inner-b]
//	inner-b (failover) -> [m2]
//
// so resolution must descend two nested levels before reaching a concrete model.
func nestedComboSnapshot() (*CatalogSnapshot, *Tenant) {
	upstreams := map[string]*Upstream{
		"u1": {Name: "u1", Protocol: ProtocolOpenAI, BaseURL: "https://u1/v1", CredentialRef: "K1"},
		"u2": {Name: "u2", Protocol: ProtocolOpenAI, BaseURL: "https://u2/v1", CredentialRef: "K2"},
		"u3": {Name: "u3", Protocol: ProtocolOpenAI, BaseURL: "https://u3/v1", CredentialRef: "K3"},
	}
	models := map[string]*ModelEntry{
		"m1": {PublicName: "m1", Upstream: "u1", UpstreamModel: "m1-real", Enabled: true},
		"m2": {PublicName: "m2", Upstream: "u2", UpstreamModel: "m2-real", Enabled: true},
		"m3": {PublicName: "m3", Upstream: "u3", UpstreamModel: "m3-real", Enabled: true},
	}
	snap := NewCatalogSnapshot(
		1,
		upstreams,
		[]string{"u1", "u2", "u3"},
		models,
		[]string{"m1", "m2", "m3"},
		map[string]*Tenant{
			"h": {KeyHash: "h", Name: "t1", Status: TenantStatusActive, AllowedModels: []string{"*"}},
		},
		[]string{"h"},
		WithCombos(
			map[string]*Combo{
				"inner-b": {Name: "inner-b", Strategy: RoutingStrategyFailover, Models: []string{"m2"}, Enabled: true},
				"inner-a": {Name: "inner-a", Strategy: RoutingStrategyFailover, Models: []string{"m1", "inner-b"}, Enabled: true},
				"outer":   {Name: "outer", Strategy: RoutingStrategyFailover, Models: []string{"inner-a", "m3"}, Enabled: true},
			},
			[]string{"inner-a", "inner-b", "outer"},
		),
	)
	tenant, _ := snap.TenantByHash("h")
	return snap, tenant
}

// TestResolveTarget_NestedComboFallsThroughLevels pins that a nested combo is
// expanded recursively: an unavailable m1 makes the inner combo fail over to
// m2, and only when the whole inner combo is exhausted does the outer combo
// reach its own second member.
func TestResolveTarget_NestedComboFallsThroughLevels(t *testing.T) {
	snap, tenant := nestedComboSnapshot()

	// Case 1: everything healthy -> deepest-first primary is m1.
	tgt, fb, err := snap.ResolveTargetWithBreaker(tenant, "outer", func(string) bool { return true })
	if err != nil || fb || tgt.UpstreamModel != "m1-real" {
		t.Fatalf("healthy graph: got fb=%v tgt=%+v err=%v, want m1-real", fb, tgt, err)
	}

	// Case 2: u1 open -> inner-a falls over to inner-b -> m2.
	tgt, fb, err = snap.ResolveTargetWithBreaker(tenant, "outer", func(name string) bool {
		return name != "u1"
	})
	if err != nil || tgt.UpstreamModel != "m2-real" {
		t.Fatalf("u1 open: got fb=%v tgt=%+v err=%v, want m2-real", fb, tgt, err)
	}

	// Case 3: inner combo entirely unavailable -> outer falls over to m3.
	tgt, fb, err = snap.ResolveTargetWithBreaker(tenant, "outer", func(name string) bool {
		return name == "u3"
	})
	if err != nil || tgt.UpstreamModel != "m3-real" {
		t.Fatalf("inner unavailable: got fb=%v tgt=%+v err=%v, want m3-real", fb, tgt, err)
	}

	// Case 4: nothing reachable -> resolver error, not a nil target.
	if _, _, err = snap.ResolveTargetWithBreaker(tenant, "outer", func(string) bool { return false }); err == nil {
		t.Fatal("expected error when every leaf is unavailable")
	}
}

// TestResolveTarget_NestedComboRoundRobin pins that the nested level keeps its
// own strategy: the outer failover always picks inner-a, whose round_robin
// cursor alternates between its two members across calls.
func TestResolveTarget_NestedComboRoundRobin(t *testing.T) {
	upstreams := map[string]*Upstream{
		"u1": {Name: "u1", Protocol: ProtocolOpenAI, BaseURL: "https://u1/v1", CredentialRef: "K1"},
		"u2": {Name: "u2", Protocol: ProtocolOpenAI, BaseURL: "https://u2/v1", CredentialRef: "K2"},
	}
	snap := NewCatalogSnapshot(
		1,
		upstreams,
		[]string{"u1", "u2"},
		map[string]*ModelEntry{
			"m1": {PublicName: "m1", Upstream: "u1", UpstreamModel: "m1-real", Enabled: true},
			"m2": {PublicName: "m2", Upstream: "u2", UpstreamModel: "m2-real", Enabled: true},
		},
		[]string{"m1", "m2"},
		map[string]*Tenant{
			"h": {KeyHash: "h", Name: "t1", Status: TenantStatusActive, AllowedModels: []string{"*"}},
		},
		[]string{"h"},
		WithCombos(
			map[string]*Combo{
				"rr":    {Name: "rr", Strategy: RoutingStrategyRoundRobin, Models: []string{"m1", "m2"}, Enabled: true},
				"outer": {Name: "outer", Strategy: RoutingStrategyFailover, Models: []string{"rr"}, Enabled: true},
			},
			[]string{"outer", "rr"},
		),
	)
	tenant, _ := snap.TenantByHash("h")

	seen := map[string]int{}
	for i := 0; i < 10; i++ {
		tgt, _, err := snap.ResolveTargetWithBreaker(tenant, "outer", func(string) bool { return true })
		if err != nil {
			t.Fatalf("iteration %d: unexpected error: %v", i, err)
		}
		seen[tgt.UpstreamModel]++
	}
	if seen["m1-real"] == 0 || seen["m2-real"] == 0 {
		t.Fatalf("nested round_robin did not alternate: %v", seen)
	}
}

// TestResolveTarget_NestedComboDisabledMember pins that a disabled nested combo
// is skipped instead of being treated as an unknown model.
func TestResolveTarget_NestedComboDisabledMember(t *testing.T) {
	upstreams := map[string]*Upstream{
		"u1": {Name: "u1", Protocol: ProtocolOpenAI, BaseURL: "https://u1/v1", CredentialRef: "K1"},
		"u2": {Name: "u2", Protocol: ProtocolOpenAI, BaseURL: "https://u2/v1", CredentialRef: "K2"},
	}
	snap := NewCatalogSnapshot(
		1,
		upstreams,
		[]string{"u1", "u2"},
		map[string]*ModelEntry{
			"m1": {PublicName: "m1", Upstream: "u1", UpstreamModel: "m1-real", Enabled: true},
			"m2": {PublicName: "m2", Upstream: "u2", UpstreamModel: "m2-real", Enabled: true},
		},
		[]string{"m1", "m2"},
		map[string]*Tenant{
			"h": {KeyHash: "h", Name: "t1", Status: TenantStatusActive, AllowedModels: []string{"*"}},
		},
		[]string{"h"},
		WithCombos(
			map[string]*Combo{
				"off":   {Name: "off", Strategy: RoutingStrategyFailover, Models: []string{"m1"}, Enabled: false},
				"outer": {Name: "outer", Strategy: RoutingStrategyFailover, Models: []string{"off", "m2"}, Enabled: true},
			},
			[]string{"off", "outer"},
		),
	)
	tenant, _ := snap.TenantByHash("h")

	tgt, _, err := snap.ResolveTargetWithBreaker(tenant, "outer", func(string) bool { return true })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tgt.UpstreamModel != "m2-real" {
		t.Fatalf("disabled nested combo must be skipped, got %q", tgt.UpstreamModel)
	}
}

// TestResolveTarget_NestedComboCycleIsGuarded pins the runtime guard: a cycle
// that bypasses config validation (hand-built snapshot) must terminate with an
// error instead of recursing until the stack dies.
func TestResolveTarget_NestedComboCycleIsGuarded(t *testing.T) {
	up := &Upstream{Name: "u1", Protocol: ProtocolOpenAI, BaseURL: "https://u1/v1", CredentialRef: "K1"}
	snap := NewCatalogSnapshot(
		1,
		map[string]*Upstream{"u1": up},
		[]string{"u1"},
		map[string]*ModelEntry{
			"m1": {PublicName: "m1", Upstream: "u1", UpstreamModel: "m1-real", Enabled: true},
		},
		[]string{"m1"},
		map[string]*Tenant{
			"h": {KeyHash: "h", Name: "t1", Status: TenantStatusActive, AllowedModels: []string{"*"}},
		},
		[]string{"h"},
		WithCombos(
			map[string]*Combo{
				"a": {Name: "a", Strategy: RoutingStrategyFailover, Models: []string{"b"}, Enabled: true},
				"b": {Name: "b", Strategy: RoutingStrategyFailover, Models: []string{"a"}, Enabled: true},
			},
			[]string{"a", "b"},
		),
	)
	tenant, _ := snap.TenantByHash("h")

	done := make(chan error, 1)
	go func() {
		_, _, err := snap.ResolveTargetWithBreaker(tenant, "a", func(string) bool { return true })
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cyclic combo graph must fail resolution")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cyclic combo graph did not terminate: recursion guard is missing")
	}
}

// TestResolveMemberCandidates pins the flattening helper used by context-length
// reporting and trace candidate capture: nested combos are expanded to concrete
// models, duplicates collapse, and a cycle cannot loop.
func TestResolveMemberCandidates(t *testing.T) {
	snap, _ := nestedComboSnapshot()
	got := snap.ResolveMemberCandidates("outer")
	want := []string{"m1", "m2", "m3"}
	if len(got) != len(want) {
		t.Fatalf("ResolveMemberCandidates(outer) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ResolveMemberCandidates(outer) = %v, want %v", got, want)
		}
	}
	if members := snap.ResolveMemberCandidates("m1"); len(members) != 1 || members[0] != "m1" {
		t.Fatalf("concrete model must expand to itself, got %v", members)
	}
}

// excludingSnapshot builds c failover [a, b] where a is round_robin [a1, a2, a3]
// and b is round_robin [b1, b2]. That is exactly the shape the member-failover
// rule has to handle: one member per top-level entry, and the remaining members
// of a must survive for later requests.
func excludingSnapshot() (*CatalogSnapshot, *Tenant) {
	upstreams := make(map[string]*Upstream, 5)
	upstreamOrder := make([]string, 0, 5)
	models := make(map[string]*ModelEntry, 5)
	modelOrder := make([]string, 0, 5)
	for _, name := range []string{"a1", "a2", "a3", "b1", "b2"} {
		upstreams["u-"+name] = &Upstream{
			Name:          "u-" + name,
			Protocol:      ProtocolOpenAI,
			BaseURL:       "https://" + name + "/v1",
			CredentialRef: "K-" + name,
		}
		upstreamOrder = append(upstreamOrder, "u-"+name)
		models[name] = &ModelEntry{PublicName: name, Upstream: "u-" + name, UpstreamModel: name + "-real", Enabled: true}
		modelOrder = append(modelOrder, name)
	}
	snap := NewCatalogSnapshot(
		1,
		upstreams,
		upstreamOrder,
		models,
		modelOrder,
		map[string]*Tenant{
			"h": {KeyHash: "h", Name: "t1", Status: TenantStatusActive, AllowedModels: []string{"*"}},
		},
		[]string{"h"},
		WithCombos(
			map[string]*Combo{
				"a": {Name: "a", Strategy: RoutingStrategyRoundRobin, Models: []string{"a1", "a2", "a3"}, Enabled: true},
				"b": {Name: "b", Strategy: RoutingStrategyRoundRobin, Models: []string{"b1", "b2"}, Enabled: true},
				"c": {Name: "c", Strategy: RoutingStrategyFailover, Models: []string{"a", "b"}, Enabled: true},
			},
			[]string{"a", "b", "c"},
		),
	)
	tenant, _ := snap.TenantByHash("h")
	return snap, tenant
}

// TestResolveTargetExcluding_SkipsExcludedTopMember pins that an excluded member
// is jumped over entirely: excluding a moves the request to b.
func TestResolveTargetExcluding_SkipsExcludedTopMember(t *testing.T) {
	snap, tenant := excludingSnapshot()

	tgt, _, err := snap.ResolveTargetExcluding(tenant, "c", map[string]bool{"a": true}, nil, nil)
	if err != nil {
		t.Fatalf("exclude a: unexpected error: %v", err)
	}
	if tgt.ComboMember != "b" {
		t.Fatalf("exclude a: got member %q, want b", tgt.ComboMember)
	}
	if tgt.ResolvedModel != "b1" && tgt.ResolvedModel != "b2" {
		t.Fatalf("exclude a: got model %q, want a b member", tgt.ResolvedModel)
	}
}

// TestResolveTargetExcluding_DoesNotConsumeNestedMembers is the regression guard
// for the whole feature: once a is excluded, repeated resolutions must never
// reach a2 or a3. A failed request is attributed to the combo, not to the leaf
// that happened to run, so the nested combo keeps its own rotation.
func TestResolveTargetExcluding_DoesNotConsumeNestedMembers(t *testing.T) {
	snap, tenant := excludingSnapshot()

	for i := 0; i < 20; i++ {
		tgt, _, err := snap.ResolveTargetExcluding(tenant, "c", map[string]bool{"a": true}, nil, nil)
		if err != nil {
			t.Fatalf("iteration %d: unexpected error: %v", i, err)
		}
		if tgt.ResolvedModel == "a1" || tgt.ResolvedModel == "a2" || tgt.ResolvedModel == "a3" {
			t.Fatalf("iteration %d: excluded combo a still supplied %q", i, tgt.ResolvedModel)
		}
	}

	// Without the exclusion the outer failover always prefers a, and a's own
	// round-robin cursor is what rotates inside it.
	for i := 0; i < 3; i++ {
		tgt, _, err := snap.ResolveTargetExcluding(tenant, "c", nil, nil, nil)
		if err != nil {
			t.Fatalf("iteration %d: unexpected error: %v", i, err)
		}
		if tgt.ComboMember != "a" {
			t.Fatalf("iteration %d: got member %q, want a", i, tgt.ComboMember)
		}
	}
}

// TestResolveTargetExcluding_DirectModelIgnoresExclude pins that the exclusion
// set is meaningless for a direct model request: the transport layer passes it
// on every attempt, including the first one of a non-combo request.
func TestResolveTargetExcluding_DirectModelIgnoresExclude(t *testing.T) {
	snap, tenant := excludingSnapshot()

	tgt, _, err := snap.ResolveTargetExcluding(tenant, "a1", map[string]bool{"a1": true}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tgt.ResolvedModel != "a1" {
		t.Fatalf("got %q, want a1: exclude must not affect a direct model", tgt.ResolvedModel)
	}
	if tgt.ComboMember != "" {
		t.Fatalf("direct model must have an empty ComboMember, got %q", tgt.ComboMember)
	}
}

// TestResolveTarget_ComboMemberIsTopLevelName pins the attribution rule: the
// stamp is the requested combo's own member, not the leaf that was reached
// inside a nested combo.
func TestResolveTarget_ComboMemberIsTopLevelName(t *testing.T) {
	snap, tenant := excludingSnapshot()

	tgt, _, err := snap.ResolveTargetExcluding(tenant, "c", nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tgt.ComboMember != "a" {
		t.Fatalf("got member %q, want a (the outer member, not %q)", tgt.ComboMember, tgt.ResolvedModel)
	}

	// A direct combo member is its own name; a leaf is not a combo member.
	tgt, _, err = snap.ResolveTargetExcluding(tenant, "a", nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tgt.ComboMember != tgt.ResolvedModel {
		t.Fatalf("direct member %q must be its own ComboMember, got %q", tgt.ResolvedModel, tgt.ComboMember)
	}
}

// TestResolveTargetExcluding_AllMembersExcluded pins that exhausting the budget
// is reported as an unavailable upstream, not as a not-found model and not as a
// nil target.
func TestResolveTargetExcluding_AllMembersExcluded(t *testing.T) {
	snap, tenant := excludingSnapshot()

	_, _, err := snap.ResolveTargetExcluding(
		tenant, "c", map[string]bool{"a": true, "b": true}, nil, nil)
	if err == nil {
		t.Fatal("expected an error when every member is excluded")
	}
	var re *ResolveError
	if !errors.As(err, &re) || re.Kind != ResolveUpstreamUnavailable {
		t.Fatalf("got %v, want ResolveUpstreamUnavailable", err)
	}
}

// TestResolveTargetExcluding_DeepGraphOnlyTopLevelHonorsExclude pins that the
// exclusion is scoped to the requested combo's direct members: excluding an
// inner name of a nested combo changes nothing, while excluding the nested combo
// itself jumps to the next direct member.
func TestResolveTargetExcluding_DeepGraphOnlyTopLevelHonorsExclude(t *testing.T) {
	snap := NewCatalogSnapshot(
		1,
		map[string]*Upstream{
			"ux": {Name: "ux", Protocol: ProtocolOpenAI, BaseURL: "https://x/v1", CredentialRef: "KX"},
			"uy": {Name: "uy", Protocol: ProtocolOpenAI, BaseURL: "https://y/v1", CredentialRef: "KY"},
			"uz": {Name: "uz", Protocol: ProtocolOpenAI, BaseURL: "https://z/v1", CredentialRef: "KZ"},
		},
		[]string{"ux", "uy", "uz"},
		map[string]*ModelEntry{
			"x": {PublicName: "x", Upstream: "ux", UpstreamModel: "x-real", Enabled: true},
			"y": {PublicName: "y", Upstream: "uy", UpstreamModel: "y-real", Enabled: true},
			"z": {PublicName: "z", Upstream: "uz", UpstreamModel: "z-real", Enabled: true},
		},
		[]string{"x", "y", "z"},
		map[string]*Tenant{
			"h": {KeyHash: "h", Name: "t1", Status: TenantStatusActive, AllowedModels: []string{"*"}},
		},
		[]string{"h"},
		WithCombos(
			map[string]*Combo{
				"inner": {Name: "inner", Strategy: RoutingStrategyFailover, Models: []string{"x", "y"}, Enabled: true},
				"mid":   {Name: "mid", Strategy: RoutingStrategyFailover, Models: []string{"inner"}, Enabled: true},
				"top":   {Name: "top", Strategy: RoutingStrategyFailover, Models: []string{"mid", "z"}, Enabled: true},
			},
			[]string{"inner", "mid", "top"},
		),
	)
	tenant, _ := snap.TenantByHash("h")

	// Excluding a leaf name that is not a direct member changes nothing.
	tgt, _, err := snap.ResolveTargetExcluding(tenant, "top", map[string]bool{"x": true}, nil, nil)
	if err != nil {
		t.Fatalf("exclude x: unexpected error: %v", err)
	}
	if tgt.ResolvedModel != "x" || tgt.ComboMember != "mid" {
		t.Fatalf("exclude x: got model %q member %q, want x under mid", tgt.ResolvedModel, tgt.ComboMember)
	}

	// Excluding the direct member jumps past the whole nested chain, even though
	// that chain still had y available.
	tgt, _, err = snap.ResolveTargetExcluding(tenant, "top", map[string]bool{"mid": true}, nil, nil)
	if err != nil {
		t.Fatalf("exclude mid: unexpected error: %v", err)
	}
	if tgt.ResolvedModel != "z" || tgt.ComboMember != "z" {
		t.Fatalf("exclude mid: got model %q member %q, want z", tgt.ResolvedModel, tgt.ComboMember)
	}
}
