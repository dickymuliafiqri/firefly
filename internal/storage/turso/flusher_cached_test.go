package turso

import (
	"context"
	"testing"
	"time"
)

// seedTenant inserts one tenant row and returns its api_key.
func seedTenant(t *testing.T, store *Store, apiKey string) {
	t.Helper()
	if err := store.SaveTenant(context.Background(), &TenantRecord{
		Name:    "acme",
		APIKey:  apiKey,
		Status:  "active",
		Version: 1,
	}); err != nil {
		t.Fatalf("SaveTenant: %v", err)
	}
}

// TestUsageFlusher_CachedTokensPersisted verifies the Task 6 persistence
// path: cached read/write counters queue alongside the billable total and
// land on the tenants row after one flush, aggregated across events.
func TestUsageFlusher_CachedTokensPersisted(t *testing.T) {
	store, db := setupTestDB(t)
	ctx := context.Background()
	seedTenant(t, store, "sk-gw-cached")

	flusher := NewUsageFlusher(store, nil, time.Second, nil)
	flusher.RecordTenantTokens("sk-gw-cached", 500, 320, 40)
	flusher.RecordTenantTokens("sk-gw-cached", 100, 0, 10)
	if err := flusher.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	var used, read, write int64
	if err := db.QueryRowContext(ctx,
		"SELECT used_tokens, cached_read_tokens, cached_write_tokens FROM tenants WHERE api_key = ?",
		"sk-gw-cached",
	).Scan(&used, &read, &write); err != nil {
		t.Fatalf("query tenant counters: %v", err)
	}
	if used != 600 {
		t.Errorf("used_tokens = %d, want 600", used)
	}
	if read != 320 {
		t.Errorf("cached_read_tokens = %d, want 320", read)
	}
	if write != 50 {
		t.Errorf("cached_write_tokens = %d, want 50", write)
	}
}

// TestUsageFlusher_MeteringDoesNotTripCatalogReload guards the syncer
// invariant: pure usage metering must never advance the api_keys
// MAX(updated_at) signal, otherwise active traffic would rebuild every
// KeyRing on every sync interval.
func TestUsageFlusher_MeteringDoesNotTripCatalogReload(t *testing.T) {
	store, db := setupTestDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx,
		"INSERT INTO providers (id, name, base_url, is_active, created_at, updated_at) VALUES (1, 'openai', 'https://api.openai.com', 1, 1, 1)",
	); err != nil {
		t.Fatalf("insert provider: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO api_keys (id, provider_id, api_key, status, is_active, last_used_at, total_requests, created_at, updated_at) VALUES (1, 1, 'sk-up', 'active', 1, 0, 0, 1, 1000)",
	); err != nil {
		t.Fatalf("insert api key: %v", err)
	}
	seedTenant(t, store, "sk-gw-cached")

	before, _, err := store.GetKeysState(ctx)
	if err != nil {
		t.Fatalf("GetKeysState before: %v", err)
	}
	if before != 1000 {
		t.Fatalf("seed updated_at = %d, want 1000", before)
	}

	flusher := NewUsageFlusher(store, nil, time.Second, nil)
	flusher.RecordTenantTokens("sk-gw-cached", 700, 512, 64)
	if err := flusher.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	after, _, err := store.GetKeysState(ctx)
	if err != nil {
		t.Fatalf("GetKeysState after: %v", err)
	}
	if after != before {
		t.Fatalf("metering advanced api_keys MAX(updated_at): %d -> %d (would force a catalog reload)", before, after)
	}

	// The tenant counters themselves must still have landed.
	var read, write int64
	if err := db.QueryRowContext(ctx,
		"SELECT cached_read_tokens, cached_write_tokens FROM tenants WHERE api_key = ?",
		"sk-gw-cached",
	).Scan(&read, &write); err != nil {
		t.Fatalf("query tenant counters: %v", err)
	}
	if read != 512 || write != 64 {
		t.Errorf("cached = %d/%d, want 512/64", read, write)
	}
}
