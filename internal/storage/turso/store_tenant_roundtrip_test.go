package turso

import (
	"context"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/config"
)

// TestSaveSettings_TenantEditRoundTrip reproduces the dashboard flow that made an
// edited tenant disappear: GET /api/settings populates the form, the operator
// changes a field, and the page re-sends the whole settings payload with
// manage_tenants=true (the store is then allowed to delete tenants "absent" from
// the payload). The tenant must survive, exactly once, with its credential
// untouched.
func TestSaveSettings_TenantEditRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, _ := setupTestDB(t)

	const rawKey = "sk-gw-tenant-roundtrip-0123456789"
	rps := 10.0
	seed := config.SettingsDTO{
		ManageTenants: true,
		Tenants: []config.TenantDTO{{
			Name:          "acme",
			APIKey:        rawKey,
			Status:        "active",
			AllowedModels: []string{"*"},
			RateLimit:     &config.RateLimitDTO{RPS: &rps},
		}},
	}
	if err := store.SaveSettings(ctx, seed); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	// Step 1: what the dashboard receives from GET /api/settings.
	loaded, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	idx := -1
	for i, tn := range loaded.Tenants {
		if tn.Name == "acme" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("seeded tenant missing from the loaded snapshot: %+v", loaded.Tenants)
	}
	t.Logf("loaded tenant: api_key=%q key_hash=%q status=%q used=%d",
		loaded.Tenants[idx].APIKey, loaded.Tenants[idx].KeyHash,
		loaded.Tenants[idx].Status, loaded.Tenants[idx].UsedTokens)

	// Step 2: the edit, expressed the way the page expresses it: a fresh entry
	// rebuilt from the form fields (which do not carry key_hash/used_tokens)
	// replacing the existing one in the list.
	newRps := 42.0
	entry := config.TenantDTO{
		Name:          loaded.Tenants[idx].Name,
		APIKey:        loaded.Tenants[idx].APIKey,
		Status:        loaded.Tenants[idx].Status,
		AllowedModels: loaded.Tenants[idx].AllowedModels,
		RateLimit:     &config.RateLimitDTO{RPS: &newRps},
	}
	others := make([]config.TenantDTO, 0, len(loaded.Tenants))
	for i, tn := range loaded.Tenants {
		if i != idx {
			others = append(others, tn)
		}
	}
	edited := config.SettingsDTO{
		Tenants:       append(others, entry),
		ManageTenants: true,
	}
	if err := store.SaveSettings(ctx, edited); err != nil {
		t.Fatalf("edit save: %v", err)
	}

	// Step 3: the tenant must still be there, once, with the same credential.
	after, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("reload settings: %v", err)
	}
	count := 0
	for _, tn := range after.Tenants {
		if tn.Name != "acme" {
			continue
		}
		count++
		if tn.APIKey != rawKey {
			t.Errorf("tenant api_key changed to %q, want %q", tn.APIKey, rawKey)
		}
	}
	if count != 1 {
		t.Fatalf("tenant %q present %d times after an edit, want exactly 1 (tenants now: %+v)",
			"acme", count, after.Tenants)
	}
}

// TestSaveSettings_TenantEditKeepsSiblings proves the authoritative sweep only
// removes what the payload actually dropped: editing one tenant must leave the
// others — and their credentials — untouched.
func TestSaveSettings_TenantEditKeepsSiblings(t *testing.T) {
	ctx := context.Background()
	store, _ := setupTestDB(t)

	rps := 5.0
	seed := config.SettingsDTO{
		ManageTenants: true,
		Tenants: []config.TenantDTO{
			{Name: "alpha", APIKey: "sk-gw-alpha-0123456789", Status: "active", RateLimit: &config.RateLimitDTO{RPS: &rps}},
			{Name: "beta", APIKey: "sk-gw-beta-9876543210", Status: "active", RateLimit: &config.RateLimitDTO{RPS: &rps}},
		},
	}
	if err := store.SaveSettings(ctx, seed); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	loaded, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if len(loaded.Tenants) != 2 {
		t.Fatalf("expected 2 seeded tenants, got %d", len(loaded.Tenants))
	}

	// Edit beta only, rebuilding the entry the way the dashboard form does.
	newRps := 77.0
	edited := config.SettingsDTO{ManageTenants: true}
	for _, tn := range loaded.Tenants {
		if tn.Name == "beta" {
			edited.Tenants = append(edited.Tenants, config.TenantDTO{
				Name:      tn.Name,
				APIKey:    tn.APIKey,
				Status:    "suspended",
				RateLimit: &config.RateLimitDTO{RPS: &newRps},
			})
			continue
		}
		edited.Tenants = append(edited.Tenants, tn)
	}
	if err := store.SaveSettings(ctx, edited); err != nil {
		t.Fatalf("edit save: %v", err)
	}

	after, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("reload settings: %v", err)
	}
	seen := map[string]string{}
	for _, tn := range after.Tenants {
		seen[tn.Name] = tn.APIKey
	}
	if len(seen) != 2 {
		t.Fatalf("expected both tenants to survive, got %+v", after.Tenants)
	}
	if seen["alpha"] != "sk-gw-alpha-0123456789" || seen["beta"] != "sk-gw-beta-9876543210" {
		t.Fatalf("tenant credentials changed: %+v", seen)
	}
}

// TestSaveSettings_TenantRemovalStillHonored proves the fix did not disarm the
// authoritative delete: a payload that genuinely omits a tenant still removes it.
func TestSaveSettings_TenantRemovalStillHonored(t *testing.T) {
	ctx := context.Background()
	store, _ := setupTestDB(t)

	rps := 5.0
	seed := config.SettingsDTO{
		ManageTenants: true,
		Tenants: []config.TenantDTO{
			{Name: "alpha", APIKey: "sk-gw-alpha-0123456789", Status: "active", RateLimit: &config.RateLimitDTO{RPS: &rps}},
			{Name: "beta", APIKey: "sk-gw-beta-9876543210", Status: "active", RateLimit: &config.RateLimitDTO{RPS: &rps}},
		},
	}
	if err := store.SaveSettings(ctx, seed); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	// The dashboard "delete tenant" flow: everything except the removed tenant.
	loaded, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	remaining := config.SettingsDTO{ManageTenants: true}
	for _, tn := range loaded.Tenants {
		if tn.Name == "beta" {
			continue
		}
		remaining.Tenants = append(remaining.Tenants, tn)
	}
	if err := store.SaveSettings(ctx, remaining); err != nil {
		t.Fatalf("delete save: %v", err)
	}

	after, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("reload settings: %v", err)
	}
	if len(after.Tenants) != 1 || after.Tenants[0].Name != "alpha" {
		t.Fatalf("expected only alpha to remain, got %+v", after.Tenants)
	}
}
