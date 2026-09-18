package domain

import (
	"encoding/json"
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
	if tgt.UpstreamModel != "gpt-4o-mini" || tgt.CredentialRef != "OPENAI_KEY" {
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

	// Default tenant uses primary key slot
	stdTenant, _ := snap.TenantByHash("h_default")
	tgt, err := snap.ResolveTarget(stdTenant, "gpt-4")
	if err != nil {
		t.Fatalf("resolve target: %v", err)
	}
	if tgt.CredentialRef != "PRIMARY_KEY" {
		t.Fatalf("target credential ref = %s, want PRIMARY_KEY", tgt.CredentialRef)
	}
	if tgt.KeySlot != s1 {
		t.Fatalf("target KeySlot mismatch: got %+v, want %+v", tgt.KeySlot, s1)
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
	expectedSequence := []string{"u1", "u2", "u3", "u1", "u2", "u3"}
	for i, expectedUpstream := range expectedSequence {
		tgt, fb, err := snap.ResolveTargetWithBreaker(tenant, "gpt-multi", nil)
		if err != nil {
			t.Fatalf("call %d: unexpected error %v", i, err)
		}
		if tgt.Upstream.Name != expectedUpstream {
			t.Fatalf("call %d: got upstream %q, want %q", i, tgt.Upstream.Name, expectedUpstream)
		}
		expectedModel := expectedUpstream + "-model"
		if tgt.UpstreamModel != expectedModel {
			t.Fatalf("call %d: got upstreamModel %q, want %q", i, tgt.UpstreamModel, expectedModel)
		}
		expectedFB := (expectedUpstream != "u1")
		if fb != expectedFB {
			t.Fatalf("call %d: got fallbackUsed=%v, want %v", i, fb, expectedFB)
		}
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

