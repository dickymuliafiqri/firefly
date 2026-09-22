package turso

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	_ "turso.tech/database/tursogo"
)

func setupTestDB(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("turso", ":memory:")
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx := context.Background()
	if err := MigrateSchema(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}

	store := &Store{
		db: db,
	}

	t.Cleanup(func() {
		_ = db.Close()
	})

	return store, db
}

func TestMigrateSchema(t *testing.T) {
	store, _ := setupTestDB(t)
	rev, err := store.GetCatalogRevision(context.Background())
	if err != nil {
		t.Fatalf("get catalog revision: %v", err)
	}
	if rev < 1 {
		t.Fatalf("expected catalog revision >= 1, got %d", rev)
	}
}

func TestMigrateSchema_ExistingOldTenantsTable(t *testing.T) {
	db, err := sql.Open("turso", ":memory:")
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx := context.Background()

	// Simulate old schema without api_key column
	_, err = db.ExecContext(ctx, `
		CREATE TABLE tenants (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			name           VARCHAR(128) NOT NULL,
			key_hash       VARCHAR(128) NOT NULL UNIQUE,
			key_hint       VARCHAR(32) NOT NULL,
			status         VARCHAR(20) NOT NULL DEFAULT 'active',
			rps            REAL,
			burst          INTEGER,
			max_concurrent INTEGER,
			allowed_models TEXT,
			metadata       TEXT,
			version        INTEGER NOT NULL DEFAULT 1,
			created_at     BIGINT NOT NULL,
			updated_at     BIGINT NOT NULL
		);
	`)
	if err != nil {
		t.Fatalf("setup old tenants table: %v", err)
	}

	// Now run MigrateSchema
	if err := MigrateSchema(ctx, db); err != nil {
		t.Fatalf("MigrateSchema failed on existing old tenants table: %v", err)
	}
}

func TestStore_OCC_Upstream(t *testing.T) {
	ctx := context.Background()
	store, _ := setupTestDB(t)

	// 1. Insert new upstream
	u := &UpstreamRecord{
		Name:        "openai-prod",
		Protocol:    "openai",
		BaseURL:     "https://api.openai.com",
		KeyStrategy: "round_robin",
		Enabled:     true,
	}
	if err := store.SaveUpstream(ctx, u); err != nil {
		t.Fatalf("insert upstream: %v", err)
	}
	if u.ID <= 0 || u.Version != 1 {
		t.Fatalf("expected ID > 0 and version 1, got id=%d version=%d", u.ID, u.Version)
	}

	// 2. Successful update with valid version
	u.BaseURL = "https://api.openai.com/v1"
	if err := store.SaveUpstream(ctx, u); err != nil {
		t.Fatalf("update upstream: %v", err)
	}
	if u.Version != 2 {
		t.Fatalf("expected version 2 after update, got %d", u.Version)
	}

	// 3. Stale update must trigger ErrConflict (OCC)
	stale := &UpstreamRecord{
		ID:          u.ID,
		Name:        "openai-prod",
		Protocol:    "openai",
		BaseURL:     "https://stale-url.com",
		KeyStrategy: "round_robin",
		Version:     1, // Stale version! Current is 2
		Enabled:     true,
	}
	err := store.SaveUpstream(ctx, stale)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on stale version update, got %v", err)
	}

	// 4. Delete
	if err := store.DeleteUpstream(ctx, u.ID); err != nil {
		t.Fatalf("delete upstream: %v", err)
	}
	// Double delete must return ErrNotFound
	if err := store.DeleteUpstream(ctx, u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound on duplicate delete, got %v", err)
	}
}

func TestStore_OCC_Model(t *testing.T) {
	ctx := context.Background()
	store, _ := setupTestDB(t)

	// Create parent upstream first
	u := &UpstreamRecord{
		Name:        "anthropic-prod",
		Protocol:    "anthropic",
		BaseURL:     "https://api.anthropic.com",
		KeyStrategy: "round_robin",
		Enabled:     true,
	}
	if err := store.SaveUpstream(ctx, u); err != nil {
		t.Fatalf("save upstream: %v", err)
	}

	m := &ModelRecord{
		PublicName:    "claude-3-5-sonnet",
		UpstreamID:    u.ID,
		UpstreamModel: "claude-3-5-sonnet-20241022",
		Enabled:       true,
		Capabilities:  domain.Capabilities{Stream: true, Tools: true},
	}
	if err := store.SaveModel(ctx, m); err != nil {
		t.Fatalf("insert model: %v", err)
	}
	if m.ID <= 0 || m.Version != 1 {
		t.Fatalf("expected id > 0 and version 1, got id=%d version=%d", m.ID, m.Version)
	}

	// Update
	m.MaxContext = 200000
	if err := store.SaveModel(ctx, m); err != nil {
		t.Fatalf("update model: %v", err)
	}
	if m.Version != 2 {
		t.Fatalf("expected version 2, got %d", m.Version)
	}

	// Stale conflict
	stale := &ModelRecord{
		ID:            m.ID,
		PublicName:    m.PublicName,
		UpstreamID:    u.ID,
		UpstreamModel: "stale-model",
		Version:       1,
	}
	if err := store.SaveModel(ctx, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}

	// Delete
	if err := store.DeleteModel(ctx, m.ID); err != nil {
		t.Fatalf("delete model: %v", err)
	}
}

func TestStore_OCC_Tenant(t *testing.T) {
	ctx := context.Background()
	store, _ := setupTestDB(t)

	tr := &TenantRecord{
		Name:          "tenant-alpha",
		APIKey:        "sk-gw-test-1234567890",
		KeyHash:       "sha256:abc123def456",
		Status:        "active",
		MaxTokens:     100000,
		AllowedModels: []string{"*"},
	}
	if err := store.SaveTenant(ctx, tr); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	if tr.ID <= 0 || tr.Version != 1 {
		t.Fatalf("expected id > 0 and version 1, got id=%d version=%d", tr.ID, tr.Version)
	}

	// Update
	tr.MaxTokens = 200000
	if err := store.SaveTenant(ctx, tr); err != nil {
		t.Fatalf("update tenant: %v", err)
	}
	if tr.Version != 2 {
		t.Fatalf("expected version 2, got %d", tr.Version)
	}

	// Stale conflict
	stale := &TenantRecord{
		ID:        tr.ID,
		Name:      tr.Name,
		APIKey:    tr.APIKey,
		KeyHash:   tr.KeyHash,
		Version:   1, // stale version
		MaxTokens: 99999,
	}
	if err := store.SaveTenant(ctx, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}

	// Delete
	if err := store.DeleteTenant(ctx, tr.ID); err != nil {
		t.Fatalf("delete tenant: %v", err)
	}
	if err := store.DeleteTenant(ctx, tr.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound on duplicate delete, got %v", err)
	}
}

func TestStore_SaveAndLoadSettings(t *testing.T) {
	ctx := context.Background()
	store, db := setupTestDB(t)

	// Seed dummy provider & api_keys
	now := time.Now().UnixMilli()
	res, err := db.ExecContext(ctx, `
		INSERT INTO providers (name, base_url, is_active, created_at, updated_at)
		VALUES ('bai', 'https://api.bai.org', 1, ?, ?)
	`, now, now)
	if err != nil {
		t.Fatalf("insert provider: %v", err)
	}
	pID, _ := res.LastInsertId()

	_, err = db.ExecContext(ctx, `
		INSERT INTO api_keys (provider_id, api_key, status, is_active, last_used_at, total_requests, created_at, updated_at)
		VALUES (?, 'sk-test-key-12345', 'active', 1, 0, 0, ?, ?)
	`, pID, now, now)
	if err != nil {
		t.Fatalf("insert api key: %v", err)
	}

	isEn := true
	settings := config.SettingsDTO{
		Upstreams: []config.UpstreamDTO{
			{
				Name:        "bai-upstream",
				Protocol:    "openai",
				BaseURL:     "https://api.bai.org",
				KeyStrategy: "round_robin",
				Enabled:     &isEn,
				CredentialPool: []config.CredentialKeyDTO{
					{
						Ref:    "bai-key-1",
						Secret: "sk-test-key-12345",
					},
				},
			},
		},
		Models: []config.ModelDTO{
			{
				PublicName:    "gpt-4o",
				Upstream:      "bai-upstream",
				UpstreamModel: "gpt-4o",
				Enabled:       &isEn,
				Capabilities: &config.CapabilitiesDTO{
					Stream: true,
					Tools:  true,
				},
			},
		},
		Combos: []config.ComboDTO{
			{
				Name:     "gpt-smart",
				Strategy: "least_inflight",
				Models:   []string{"gpt-4o"},
				Enabled:  &isEn,
			},
		},
		Tenants: []config.TenantDTO{
			{
				Name:          "tenant-alpha",
				KeyHash:       "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				Status:        "active",
				AllowedModels: []string{"*"},
			},
		},
	}

	// 1. Save settings
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	// 2. Load settings
	loaded, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if len(loaded.Upstreams) != 1 || loaded.Upstreams[0].Name != "bai-upstream" {
		t.Fatalf("unexpected loaded upstreams: %+v", loaded.Upstreams)
	}
	if len(loaded.Models) != 1 || loaded.Models[0].PublicName != "gpt-4o" {
		t.Fatalf("unexpected loaded models: %+v", loaded.Models)
	}
	if len(loaded.Combos) != 1 || loaded.Combos[0].Name != "gpt-smart" {
		t.Fatalf("unexpected loaded combos: %+v", loaded.Combos)
	}
	if len(loaded.Tenants) != 1 || loaded.Tenants[0].Name != "tenant-alpha" {
		t.Fatalf("unexpected loaded tenants: %+v", loaded.Tenants)
	}

	// 3. Load Catalog Snapshot
	envLookup := func(k string) (string, bool) { return "", false }
	snap, warns, err := store.LoadCatalogSnapshot(ctx, envLookup)
	if err != nil {
		t.Fatalf("LoadCatalogSnapshot: %v", err)
	}
	if len(warns) > 0 {
		t.Logf("warnings: %v", warns)
	}
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}

	m, ok := snap.Model("gpt-4o")
	if !ok || m == nil {
		t.Fatalf("model gpt-4o not resolved in snapshot")
	}
	c, ok := snap.Combo("gpt-smart")
	if !ok || c == nil {
		t.Fatalf("combo gpt-smart not resolved in snapshot")
	}
	tenant, ok := snap.TenantByHash("sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	if !ok || tenant == nil {
		t.Fatalf("tenant not found in snapshot")
	}
}

func TestStore_CatalogRevision(t *testing.T) {
	ctx := context.Background()
	store, _ := setupTestDB(t)

	r1, err := store.GetCatalogRevision(ctx)
	if err != nil {
		t.Fatalf("GetCatalogRevision: %v", err)
	}
	r2, err := store.BumpCatalogRevision(ctx)
	if err != nil {
		t.Fatalf("BumpCatalogRevision: %v", err)
	}
	if r2 != r1+1 {
		t.Fatalf("expected bumped revision %d, got %d", r1+1, r2)
	}
}

func TestOAuthStore(t *testing.T) {
	_, db := setupTestDB(t)
	s := &OAuthStore{db: db}
	ctx := context.Background()

	conn := &domain.OAuthConnection{
		ID:       "test-cline-user-1",
		Provider: "cline",
		Email:    "test@example.com",
		Token: domain.OAuthToken{
			AccessToken:  "access-123",
			RefreshToken: "refresh-456",
			ExpiresAt:    time.Now().Add(1 * time.Hour),
		},
		ProviderSpecificData: map[string]string{
			"project_id": "proj-xyz",
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	// 1. Save
	if err := s.Save(ctx, conn); err != nil {
		t.Fatalf("save oauth conn: %v", err)
	}

	// 2. Get
	fetched, err := s.Get(ctx, conn.ID)
	if err != nil {
		t.Fatalf("get oauth conn: %v", err)
	}
	if fetched.Email != "test@example.com" || fetched.Token.AccessToken != "access-123" {
		t.Fatalf("unexpected fetched oauth conn: %+v", fetched)
	}
	if fetched.ProviderSpecificData["project_id"] != "proj-xyz" {
		t.Fatalf("unexpected provider specific data: %+v", fetched.ProviderSpecificData)
	}

	// 3. List
	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("list oauth conns: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 connection, got %d", len(list))
	}

	// 4. Delete
	if err := s.Delete(ctx, conn.ID); err != nil {
		t.Fatalf("delete oauth conn: %v", err)
	}
	_, err = s.Get(ctx, conn.ID)
	if err == nil {
		t.Fatal("expected error getting deleted connection, got nil")
	}
}

func TestUsageFlusher(t *testing.T) {
	ctx := context.Background()
	store, db := setupTestDB(t)

	// Insert test api_key with id = 42
	now := time.Now().UnixMilli()
	_, err := db.ExecContext(ctx, `
		INSERT INTO api_keys (id, provider_id, api_key, status, is_active, last_used_at, total_requests, created_at, updated_at)
		VALUES (42, 1, 'sk-test-key-42', 'active', 1, 0, 0, ?, ?)
	`, now, now)
	if err != nil {
		t.Fatalf("insert test key: %v", err)
	}

	flusher := NewUsageFlusher(store, nil, 1*time.Hour, nil)

	// Record 5 requests for key-42
	flusher.Record("myupstream-key-42", "tenant-test", "gpt-4o", 5)

	// Flush
	if err := flusher.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// Verify total_requests in database
	var totalRequests int64
	err = db.QueryRowContext(ctx, "SELECT total_requests FROM api_keys WHERE id = 42").Scan(&totalRequests)
	if err != nil {
		t.Fatalf("query total_requests: %v", err)
	}
	if totalRequests != 5 {
		t.Fatalf("expected total_requests=5, got %d", totalRequests)
	}

	// Mark revoked
	flusher.MarkRevoked("myupstream-key-42")
	if err := flusher.Flush(ctx); err != nil {
		t.Fatalf("flush revoked: %v", err)
	}

	var status string
	var isActive int
	err = db.QueryRowContext(ctx, "SELECT status, is_active FROM api_keys WHERE id = 42").Scan(&status, &isActive)
	if err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "revoked" || isActive != 0 {
		t.Fatalf("expected revoked and is_active=0, got status=%s is_active=%d", status, isActive)
	}
}

func TestUsageFlusher_KeyActions(t *testing.T) {
	ctx := context.Background()
	store, db := setupTestDB(t)

	now := time.Now().UnixMilli()
	_, err := db.ExecContext(ctx, "INSERT INTO providers (id, name, base_url, is_active, created_at, updated_at) VALUES (1, 'prov', 'https://api.test.com', 1, ?, ?)", now, now)
	if err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO api_keys (id, provider_id, api_key, status, is_active, last_used_at, total_requests, created_at, updated_at)
		VALUES (101, 1, 'sk-test-101', 'active', 1, 0, 0, ?, ?),
		       (102, 1, 'sk-test-102', 'active', 1, 0, 0, ?, ?)
	`, now, now, now, now)
	if err != nil {
		t.Fatalf("seed keys: %v", err)
	}

	flusher := NewUsageFlusher(store, nil, 100*time.Millisecond, nil)

	// Action 1: Deactivate key 101
	flusher.NotifyKeyAction(ports.KeyActionDeactivate, "test-up", "test-up-key-101", 101, "rate limited")
	// Action 2: Delete key 102
	flusher.NotifyKeyAction(ports.KeyActionDelete, "test-up", "test-up-key-102", 102, "quota expired")

	if err := flusher.Flush(ctx); err != nil {
		t.Fatalf("flush actions: %v", err)
	}

	// Verify key 101 is deactivated
	var status101 string
	var isActive101 int
	err = db.QueryRowContext(ctx, "SELECT status, is_active FROM api_keys WHERE id = 101").Scan(&status101, &isActive101)
	if err != nil {
		t.Fatalf("query key 101: %v", err)
	}
	if status101 != "deactivated" || isActive101 != 0 {
		t.Errorf("expected deactivated, got status=%s isActive=%d", status101, isActive101)
	}

	// Verify key 102 is deleted
	var count102 int
	err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM api_keys WHERE id = 102").Scan(&count102)
	if err != nil {
		t.Fatalf("query count 102: %v", err)
	}
	if count102 != 0 {
		t.Errorf("expected key 102 to be deleted, found count=%d", count102)
	}
}

// TestSaveSettings_DoesNotCascadeDeleteModelsOnPartialPayload verifies that a
// stale/partial save (one that omits an upstream still referenced by surviving
// models, or that carries an empty Models list) never wipes existing models.
// This guards against the "delete one model deletes all models" data-loss bug.
func TestSaveSettings_DoesNotCascadeDeleteModelsOnPartialPayload(t *testing.T) {
	ctx := context.Background()
	store, _ := setupTestDB(t)

	isEn := true
	baseline := config.SettingsDTO{
		Upstreams: []config.UpstreamDTO{
			{
				Name:        "atria",
				Protocol:    "openai",
				BaseURL:     "https://api.atria.example",
				KeyStrategy: "round_robin",
				Enabled:     &isEn,
				CredentialPool: []config.CredentialKeyDTO{
					{Ref: "atria-key-1", Secret: "sk-atria-secret-123456"},
				},
			},
		},
		Models: []config.ModelDTO{
			{PublicName: "atria-a", Upstream: "atria", UpstreamModel: "atria", Enabled: &isEn},
			{PublicName: "atria-b", Upstream: "atria", UpstreamModel: "atria", Enabled: &isEn},
			{PublicName: "atria-c", Upstream: "atria", UpstreamModel: "atria", Enabled: &isEn},
		},
	}
	if err := store.SaveSettings(ctx, baseline); err != nil {
		t.Fatalf("baseline save: %v", err)
	}

	// Case 1: a save whose upstream list is missing "atria" (as could happen
	// when the store is momentarily incomplete) but whose models still reference
	// it. Models must NOT be deleted.
	partial := config.SettingsDTO{
		Upstreams: []config.UpstreamDTO{}, // empty/partial upstream list
		Models: []config.ModelDTO{
			{PublicName: "atria-a", Upstream: "atria", UpstreamModel: "atria", Enabled: &isEn},
			{PublicName: "atria-b", Upstream: "atria", UpstreamModel: "atria", Enabled: &isEn},
			{PublicName: "atria-c", Upstream: "atria", UpstreamModel: "atria", Enabled: &isEn},
		},
	}
	if err := store.SaveSettings(ctx, partial); err != nil {
		t.Fatalf("partial save: %v", err)
	}

	got, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("load after partial: %v", err)
	}
	if len(got.Models) != 3 {
		t.Fatalf("after partial save: got %d models, want 3 (no cascade delete)", len(got.Models))
	}
	if len(got.Upstreams) != 1 || got.Upstreams[0].Name != "atria" {
		t.Fatalf("after partial save: upstream must survive, got %+v", got.Upstreams)
	}

	// Case 2: an empty Models list must not wipe all models.
	emptyModels := config.SettingsDTO{
		Upstreams: baseline.Upstreams,
		Models:    []config.ModelDTO{},
	}
	if err := store.SaveSettings(ctx, emptyModels); err != nil {
		t.Fatalf("empty-models save: %v", err)
	}
	got2, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("load after empty-models: %v", err)
	}
	if len(got2.Models) != 3 {
		t.Fatalf("after empty-models save: got %d models, want 3 (empty list ignored)", len(got2.Models))
	}
}

// TestSaveSettings_DeletesSingleModelWhenAuthoritative verifies the normal case
// still works: an authoritative payload that drops exactly one model removes
// only that model.
func TestSaveSettings_DeletesSingleModelWhenAuthoritative(t *testing.T) {
	ctx := context.Background()
	store, _ := setupTestDB(t)

	isEn := true
	baseline := config.SettingsDTO{
		Upstreams: []config.UpstreamDTO{
			{
				Name:        "atria",
				Protocol:    "openai",
				BaseURL:     "https://api.atria.example",
				KeyStrategy: "round_robin",
				Enabled:     &isEn,
				CredentialPool: []config.CredentialKeyDTO{
					{Ref: "atria-key-1", Secret: "sk-atria-secret-123456"},
				},
			},
		},
		Models: []config.ModelDTO{
			{PublicName: "atria-a", Upstream: "atria", UpstreamModel: "atria", Enabled: &isEn},
			{PublicName: "atria-b", Upstream: "atria", UpstreamModel: "atria", Enabled: &isEn},
		},
	}
	if err := store.SaveSettings(ctx, baseline); err != nil {
		t.Fatalf("baseline save: %v", err)
	}

	// Authoritative save: keep the upstream, drop model atria-b only.
	next := config.SettingsDTO{
		Upstreams: baseline.Upstreams,
		Models: []config.ModelDTO{
			{PublicName: "atria-a", Upstream: "atria", UpstreamModel: "atria", Enabled: &isEn},
		},
	}
	if err := store.SaveSettings(ctx, next); err != nil {
		t.Fatalf("delete-one save: %v", err)
	}
	got, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Models) != 1 || got.Models[0].PublicName != "atria-a" {
		t.Fatalf("expected only atria-a to remain, got %+v", got.Models)
	}
}

// TestSaveSettings_ManageModelsDeletesLastModel verifies that an authoritative
// payload (ManageModels=true) with an empty Models list deletes the remaining
// models — the intended "delete the last model" flow — while a non-authoritative
// empty payload leaves them intact.
func TestSaveSettings_ManageModelsDeletesLastModel(t *testing.T) {
	ctx := context.Background()
	store, _ := setupTestDB(t)

	isEn := true
	baseline := config.SettingsDTO{
		Upstreams: []config.UpstreamDTO{
			{
				Name:        "atria",
				Protocol:    "openai",
				BaseURL:     "https://api.atria.example",
				KeyStrategy: "round_robin",
				Enabled:     &isEn,
				CredentialPool: []config.CredentialKeyDTO{
					{Ref: "atria-key-1", Secret: "sk-atria-secret-123456"},
				},
			},
		},
		Models: []config.ModelDTO{
			{PublicName: "only-model", Upstream: "atria", UpstreamModel: "atria", Enabled: &isEn},
		},
	}
	if err := store.SaveSettings(ctx, baseline); err != nil {
		t.Fatalf("baseline save: %v", err)
	}

	// Non-authoritative empty payload must NOT delete the model.
	if err := store.SaveSettings(ctx, config.SettingsDTO{
		Upstreams: baseline.Upstreams,
		Models:    []config.ModelDTO{},
	}); err != nil {
		t.Fatalf("non-authoritative empty save: %v", err)
	}
	got, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("load after non-authoritative: %v", err)
	}
	if len(got.Models) != 1 {
		t.Fatalf("non-authoritative empty payload must not delete models, got %d", len(got.Models))
	}

	// Authoritative empty payload SHOULD delete the last model.
	if err := store.SaveSettings(ctx, config.SettingsDTO{
		Upstreams:    baseline.Upstreams,
		Models:       []config.ModelDTO{},
		ManageModels: true,
	}); err != nil {
		t.Fatalf("authoritative empty save: %v", err)
	}
	got2, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("load after authoritative: %v", err)
	}
	if len(got2.Models) != 0 {
		t.Fatalf("authoritative empty payload must delete the last model, got %d", len(got2.Models))
	}
	// The upstream itself must survive (we only managed models).
	if len(got2.Upstreams) != 1 {
		t.Fatalf("upstream must survive model deletion, got %d", len(got2.Upstreams))
	}
}

// TestSaveSettings_ManageUpstreamsDeletesLastUpstream verifies that an
// authoritative payload (ManageUpstreams=true) with an empty Upstreams list
// deletes the remaining upstream — the intended "delete the last upstream" flow
// — while a non-authoritative empty payload leaves it intact.
func TestSaveSettings_ManageUpstreamsDeletesLastUpstream(t *testing.T) {
	ctx := context.Background()
	store, _ := setupTestDB(t)

	isEn := true
	baseline := config.SettingsDTO{
		Upstreams: []config.UpstreamDTO{
			{
				Name:        "atria",
				Protocol:    "openai",
				BaseURL:     "https://api.atria.example",
				KeyStrategy: "round_robin",
				Enabled:     &isEn,
				CredentialPool: []config.CredentialKeyDTO{
					{Ref: "atria-key-1", Secret: "sk-atria-secret-123456"},
				},
			},
		},
		// No models reference the upstream, so it can be deleted.
		Models: []config.ModelDTO{},
	}
	if err := store.SaveSettings(ctx, baseline); err != nil {
		t.Fatalf("baseline save: %v", err)
	}

	// Non-authoritative empty payload must NOT delete the upstream.
	if err := store.SaveSettings(ctx, config.SettingsDTO{
		Upstreams: []config.UpstreamDTO{},
		Models:    []config.ModelDTO{},
	}); err != nil {
		t.Fatalf("non-authoritative empty save: %v", err)
	}
	got, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("load after non-authoritative: %v", err)
	}
	if len(got.Upstreams) != 1 {
		t.Fatalf("non-authoritative empty payload must not delete upstreams, got %d", len(got.Upstreams))
	}

	// Authoritative empty payload SHOULD delete the last upstream.
	if err := store.SaveSettings(ctx, config.SettingsDTO{
		Upstreams:       []config.UpstreamDTO{},
		Models:          []config.ModelDTO{},
		ManageUpstreams: true,
	}); err != nil {
		t.Fatalf("authoritative empty save: %v", err)
	}
	got2, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("load after authoritative: %v", err)
	}
	if len(got2.Upstreams) != 0 {
		t.Fatalf("authoritative empty payload must delete the last upstream, got %d", len(got2.Upstreams))
	}
}

// TestSaveSettings_ManageCombosAndTenantsDeleteLast verifies that authoritative
// payloads with empty Combos/Tenants lists delete the remaining rows only when
// the corresponding manage flag is set, and never on a bare empty payload.
func TestSaveSettings_ManageCombosAndTenantsDeleteLast(t *testing.T) {
	ctx := context.Background()
	store, _ := setupTestDB(t)

	isEn := true
	baseline := config.SettingsDTO{
		Upstreams: []config.UpstreamDTO{
			{
				Name:        "atria",
				Protocol:    "openai",
				BaseURL:     "https://api.atria.example",
				KeyStrategy: "round_robin",
				Enabled:     &isEn,
				CredentialPool: []config.CredentialKeyDTO{
					{Ref: "atria-key-1", Secret: "sk-atria-secret-123456"},
				},
			},
		},
		Models: []config.ModelDTO{
			{PublicName: "atria-a", Upstream: "atria", UpstreamModel: "atria", Enabled: &isEn},
		},
		Combos: []config.ComboDTO{
			{Name: "combo-a", Strategy: "least_inflight", Models: []string{"atria-a"}, Enabled: &isEn},
		},
		Tenants: []config.TenantDTO{
			{
				Name:          "tenant-alpha",
				KeyHash:       "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
				Status:        "active",
				AllowedModels: []string{"*"},
			},
		},
	}
	if err := store.SaveSettings(ctx, baseline); err != nil {
		t.Fatalf("baseline save: %v", err)
	}

	// Bare empty payload: neither combos nor tenants are deleted.
	if err := store.SaveSettings(ctx, config.SettingsDTO{
		Upstreams: baseline.Upstreams,
		Models:    baseline.Models,
		Combos:    []config.ComboDTO{},
		Tenants:   []config.TenantDTO{},
	}); err != nil {
		t.Fatalf("bare empty save: %v", err)
	}
	got, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("load after bare: %v", err)
	}
	if len(got.Combos) != 1 || len(got.Tenants) != 1 {
		t.Fatalf("bare empty payload must not delete combos/tenants, got combos=%d tenants=%d", len(got.Combos), len(got.Tenants))
	}

	// Authoritative empty payload: combos and tenants are deleted.
	if err := store.SaveSettings(ctx, config.SettingsDTO{
		Upstreams:     baseline.Upstreams,
		Models:        baseline.Models,
		Combos:        []config.ComboDTO{},
		Tenants:       []config.TenantDTO{},
		ManageCombos:  true,
		ManageTenants: true,
	}); err != nil {
		t.Fatalf("authoritative empty save: %v", err)
	}
	got2, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("load after authoritative: %v", err)
	}
	if len(got2.Combos) != 0 {
		t.Fatalf("ManageCombos must delete the last combo, got %d", len(got2.Combos))
	}
	if len(got2.Tenants) != 0 {
		t.Fatalf("ManageTenants must delete the last tenant, got %d", len(got2.Tenants))
	}
}

func TestStore_StaleTransactionRecovery(t *testing.T) {
	ctx := context.Background()
	store, db := setupTestDB(t)

	// Intentionally simulate a dangling/stale transaction left uncommitted on the connection
	_, err := db.ExecContext(ctx, "BEGIN")
	if err != nil {
		t.Fatalf("setup raw BEGIN: %v", err)
	}

	// BeginTx should intercept "cannot start a transaction within a transaction",
	// rollback the stale transaction, and successfully open a new transaction.
	tx, err := store.BeginTx(ctx)
	if err != nil {
		t.Fatalf("expected BeginTx to recover from stale transaction, got: %v", err)
	}

	// The recovered transaction must be valid and operational
	_, err = tx.ExecContext(ctx, "UPDATE catalog_revisions SET revision = revision + 1 WHERE id = 1")
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("exec on recovered tx: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit recovered tx: %v", err)
	}
}

func TestStore_SaveSettingsStaleTransactionRecovery(t *testing.T) {
	ctx := context.Background()
	store, db := setupTestDB(t)

	// Intentionally leave connection in a transaction state
	_, err := db.ExecContext(ctx, "BEGIN")
	if err != nil {
		t.Fatalf("setup raw BEGIN: %v", err)
	}

	// SaveSettings should transparently recover and save successfully
	err = store.SaveSettings(ctx, config.SettingsDTO{
		Upstreams: []config.UpstreamDTO{
			{
				Name:     "upstream-recovery",
				Protocol: "openai",
				BaseURL:  "https://api.openai.com",
			},
		},
	})
	if err != nil {
		t.Fatalf("expected SaveSettings to recover from stale transaction, got: %v", err)
	}

	got, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if len(got.Upstreams) != 1 || got.Upstreams[0].Name != "upstream-recovery" {
		t.Fatalf("unexpected upstreams loaded: %+v", got.Upstreams)
	}
}

func TestStore_ConcurrentOperations(t *testing.T) {
	ctx := context.Background()
	store, _ := setupTestDB(t)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				_, _ = store.GetCatalogRevision(ctx)
				_, _ = store.LoadSettings(ctx)
				_ = store.SaveSettings(ctx, config.SettingsDTO{
					Upstreams: []config.UpstreamDTO{
						{
							Name:     fmt.Sprintf("upstream-%d-%d", idx, j),
							Protocol: "openai",
							BaseURL:  "https://api.openai.com",
						},
					},
				})
			}
		}(i)
	}
	wg.Wait()
}

// TestLoadCatalogSnapshot_CarriesTokenSaver guards the database reload path:
// Token Saver is stored in the settings blob, so a snapshot rebuilt from the
// database must carry it. Without this, every sync published a snapshot whose
// TokenSaver was the disabled default and the configured compression silently
// stopped applying to chat completions.
func TestLoadCatalogSnapshot_CarriesTokenSaver(t *testing.T) {
	ctx := context.Background()
	store, _ := setupTestDB(t)

	isEn := true
	maxTool := 4321
	ctxThreshold := 9999
	settings := config.SettingsDTO{
		Upstreams: []config.UpstreamDTO{
			{
				Name:        "openai",
				Protocol:    "openai",
				BaseURL:     "https://api.openai.com/v1",
				KeyStrategy: "round_robin",
				Enabled:     &isEn,
				CredentialPool: []config.CredentialKeyDTO{
					{Ref: "openai-cred-1", Secret: "sk-valid-secret"},
				},
			},
		},
		Models: []config.ModelDTO{
			{
				PublicName:    "gpt-4o",
				Upstream:      "openai",
				UpstreamModel: "gpt-4o",
				Enabled:       &isEn,
			},
		},
		TokenSaver: &config.TokenSaverDTO{
			Enabled:            true,
			CompressToolOutput: true,
			MaxToolOutputChars: &maxTool,
			ContextThreshold:   &ctxThreshold,
		},
	}
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	snap, _, err := store.LoadCatalogSnapshot(ctx, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("LoadCatalogSnapshot: %v", err)
	}

	ts := snap.TokenSaver()
	if !ts.Enabled || !ts.CompressToolOutput {
		t.Fatalf("TokenSaver not carried into the snapshot: %+v", ts)
	}
	if ts.MaxToolOutputChars != maxTool || ts.ContextThreshold != ctxThreshold {
		t.Fatalf("TokenSaver thresholds lost: max=%d threshold=%d, want %d/%d",
			ts.MaxToolOutputChars, ts.ContextThreshold, maxTool, ctxThreshold)
	}
}

// TestLoadCatalogSnapshot_SkipsEmptySecretCredential guards the DB-load
// regression: an upstream_credentials row whose secret has not (yet)
// replicated (NULL secret, no joined api_key) must be skipped rather than
// producing a KeySlot with an empty secret. A single valid credential on the
// same upstream must still load, and the snapshot build must succeed (not fall
// into zero-config mode).
func TestLoadCatalogSnapshot_SkipsEmptySecretCredential(t *testing.T) {
	ctx := context.Background()
	store, db := setupTestDB(t)

	isEn := true
	settings := config.SettingsDTO{
		Upstreams: []config.UpstreamDTO{
			{
				Name:        "openai",
				Protocol:    "openai",
				BaseURL:     "https://api.openai.com/v1",
				KeyStrategy: "round_robin",
				Enabled:     &isEn,
				CredentialPool: []config.CredentialKeyDTO{
					{Ref: "openai-cred-1", Secret: "sk-valid-secret"},
				},
			},
		},
		Models: []config.ModelDTO{
			{
				PublicName:    "gpt-4o",
				Upstream:      "openai",
				UpstreamModel: "gpt-4o",
				Enabled:       &isEn,
				Capabilities:  &config.CapabilitiesDTO{Stream: true},
			},
		},
	}
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	// Resolve the upstream id and inject a second credential with a NULL
	// secret and no api_key_id (simulating a harvester row that has not yet
	// replicated into the local Turso replica).
	var upID int64
	if err := db.QueryRowContext(ctx, "SELECT id FROM upstreams WHERE name = 'openai'").Scan(&upID); err != nil {
		t.Fatalf("resolve upstream id: %v", err)
	}
	now := time.Now().UnixMilli()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO upstream_credentials (upstream_id, ref, secret, status, is_active, created_at, updated_at)
		VALUES (?, 'openai-cred-2', NULL, 'active', 1, ?, ?)
	`, upID, now, now); err != nil {
		t.Fatalf("insert empty-secret credential: %v", err)
	}

	envLookup := func(k string) (string, bool) { return "", false }
	snap, _, err := store.LoadCatalogSnapshot(ctx, envLookup)
	if err != nil {
		t.Fatalf("LoadCatalogSnapshot must not fail with an empty-secret credential present: %v", err)
	}
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}

	up, ok := snap.Upstream("openai")
	if !ok || up == nil || up.KeyRing == nil {
		t.Fatalf("upstream openai not resolved with a key ring")
	}
	if got := up.KeyRing.SlotCount(); got != 1 {
		t.Fatalf("expected exactly 1 key slot (empty-secret credential skipped), got %d", got)
	}
	if sec := up.KeyRing.Slots[0].Secret; sec != "sk-valid-secret" {
		t.Fatalf("expected the valid secret to remain, got %q", sec)
	}
}

// TestLoadCatalogSnapshot_PoolsProviderKeysIntoBoundUpstream is the payoff test
// for Firefly owning providers/api_keys natively: an upstream whose provider_id
// is set receives that provider's routable keys as its credential pool during
// the snapshot build. This is what lets the dashboard bind credentials by
// provider id (upstreams.provider_id) instead of copying raw secrets into
// upstream_credentials, and it is the only path that turns a stored key into a
// routable slot for a provider-bound upstream.
func TestLoadCatalogSnapshot_PoolsProviderKeysIntoBoundUpstream(t *testing.T) {
	ctx := context.Background()
	store, db := setupTestDB(t)

	now := time.Now().UnixMilli()
	res, err := db.ExecContext(ctx, `
		INSERT INTO providers (name, base_url, description, is_active, created_at, updated_at)
		VALUES ('pool-prov', 'https://pool.example/v1', 'pooling fixture', 1, ?, ?)
	`, now, now)
	if err != nil {
		t.Fatalf("insert provider: %v", err)
	}
	providerID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("provider id: %v", err)
	}

	insertKey := func(id int64, secret, status string, active int, expiresAt any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO api_keys (
				id, provider_id, api_key, status, is_active, expires_at,
				last_used_at, total_requests, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, 0, 0, ?, ?)
		`, id, providerID, secret, status, active, expiresAt, now, now); err != nil {
			t.Fatalf("insert api_key %d: %v", id, err)
		}
	}
	insertKey(101, "sk-pool-routable", "active", 1, nil)
	insertKey(102, "sk-pool-expired", "active", 1, now-60_000)
	insertKey(103, "sk-pool-inactive", "active", 0, nil)
	insertKey(104, "sk-pool-unknown-status", "dead", 1, nil)
	insertKey(105, "sk-pool-valid-until-later", "active", 1, now+3_600_000)
	insertKey(106, "", "active", 1, nil)

	isEn := true
	settings := config.SettingsDTO{
		Upstreams: []config.UpstreamDTO{
			{
				Name:        "pool-upstream",
				Protocol:    "openai",
				BaseURL:     "https://pool.example/v1",
				KeyStrategy: "round_robin",
				Enabled:     &isEn,
				ProviderID:  &providerID,
			},
		},
		Models: []config.ModelDTO{
			{
				PublicName:    "pool-model",
				Upstream:      "pool-upstream",
				UpstreamModel: "pool-model",
				Enabled:       &isEn,
				Capabilities:  &config.CapabilitiesDTO{Stream: true},
			},
		},
	}
	if err := store.SaveSettings(ctx, settings); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	var ucCount int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM upstream_credentials").Scan(&ucCount); err != nil {
		t.Fatalf("count upstream_credentials: %v", err)
	}
	if ucCount != 0 {
		t.Fatalf("fixture must rely on the provider pool alone, found %d upstream_credentials rows", ucCount)
	}

	snap, _, err := store.LoadCatalogSnapshot(ctx, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("LoadCatalogSnapshot: %v", err)
	}
	up, ok := snap.Upstream("pool-upstream")
	if !ok || up == nil || up.KeyRing == nil {
		t.Fatal("upstream pool-upstream not resolved with a key ring")
	}

	// Only 101 and 105 are routable: 102 is expired, 103 is deactivated, 104
	// carries a non-routable status, and 106 has not replicated a secret yet.
	if got := up.KeyRing.SlotCount(); got != 2 {
		t.Fatalf("expected exactly 2 pooled slots, got %d", got)
	}
	type want struct{ ref, secret string }
	wants := []want{
		{"pool-upstream-key-101", "sk-pool-routable"},
		{"pool-upstream-key-105", "sk-pool-valid-until-later"},
	}
	for i, w := range wants {
		if got := up.KeyRing.Slots[i].Ref; got != w.ref {
			t.Errorf("slot %d ref = %q, want %q", i, got, w.ref)
		}
		if got := up.KeyRing.Slots[i].Secret; got != w.secret {
			t.Errorf("slot %d secret = %q, want %q", i, got, w.secret)
		}
	}

	// The pool must be queried per snapshot build, not cached: revoking the key
	// server-side has to disappear from the next snapshot without touching the
	// upstream row.
	if _, err := db.ExecContext(ctx, `UPDATE api_keys SET is_active = 0 WHERE id = 101`); err != nil {
		t.Fatalf("deactivate pooled key: %v", err)
	}
	snap2, _, err := store.LoadCatalogSnapshot(ctx, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("LoadCatalogSnapshot after deactivation: %v", err)
	}
	up2, ok := snap2.Upstream("pool-upstream")
	if !ok || up2 == nil || up2.KeyRing == nil {
		t.Fatal("upstream pool-upstream lost its key ring after deactivation")
	}
	if got := up2.KeyRing.SlotCount(); got != 1 {
		t.Fatalf("expected 1 slot after deactivating key 101, got %d", got)
	}
	if got := up2.KeyRing.Slots[0].Ref; got != "pool-upstream-key-105" {
		t.Fatalf("surviving slot ref = %q, want pool-upstream-key-105", got)
	}
}

// TestStore_SaveSettings_PrunesOrphanCredentials guards the credential-pool
// duplication regression: the pool carried by the payload is authoritative for
// its upstream, so rows whose ref is absent from it must be deleted. Older
// dashboard imports minted a brand-new ref family on every import, which never
// collided on UNIQUE(upstream_id, ref) and therefore accumulated unbounded.
// A payload without a credential pool must remain non-authoritative (no prune).
func TestStore_SaveSettings_PrunesOrphanCredentials(t *testing.T) {
	ctx := context.Background()
	store, db := setupTestDB(t)

	isEn := true
	settings := func(pool []config.CredentialKeyDTO) config.SettingsDTO {
		return config.SettingsDTO{
			Upstreams: []config.UpstreamDTO{
				{
					Name:           "dahl",
					Protocol:       "openai",
					BaseURL:        "https://api.dahl.ai/v1",
					KeyStrategy:    "round_robin",
					Enabled:        &isEn,
					CredentialPool: pool,
				},
				{
					Name:        "uno",
					Protocol:    "openai",
					BaseURL:     "https://api.uno.ai/v1",
					KeyStrategy: "round_robin",
					Enabled:     &isEn,
					CredentialPool: []config.CredentialKeyDTO{
						{Ref: "uno-key-7", Secret: "sk-uno-1"},
						{Ref: "uno-key-9", Secret: "sk-uno-2"},
					},
				},
			},
		}
	}
	upstreamID := func(name string) int64 {
		t.Helper()
		var id int64
		if err := db.QueryRowContext(ctx, "SELECT id FROM upstreams WHERE name = ?", name).Scan(&id); err != nil {
			t.Fatalf("resolve upstream %q: %v", name, err)
		}
		return id
	}
	countCreds := func(name string) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM upstream_credentials WHERE upstream_id = ?", upstreamID(name)).Scan(&n); err != nil {
			t.Fatalf("count credentials of %q: %v", name, err)
		}
		return n
	}

	dahlPool := []config.CredentialKeyDTO{
		{Ref: "dahl-key-101", Secret: "sk-a"},
		{Ref: "dahl-key-102", Secret: "sk-b"},
	}
	if err := store.SaveSettings(ctx, settings(dahlPool)); err != nil {
		t.Fatalf("initial SaveSettings: %v", err)
	}

	// Simulate a stale row left behind by an older import flow, plus a
	// credential whose secret has not replicated yet (must survive the prune).
	now := time.Now().UnixMilli()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO upstream_credentials (upstream_id, ref, secret, status, is_active, created_at, updated_at)
		VALUES (?, 'upstream-key-1', 'sk-a', 'active', 1, ?, ?)
	`, upstreamID("dahl"), now, now); err != nil {
		t.Fatalf("insert stale credential: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO upstream_credentials (upstream_id, ref, secret, status, is_active, created_at, updated_at)
		VALUES (?, 'dahl-key-999', NULL, 'active', 1, ?, ?)
	`, upstreamID("dahl"), now, now); err != nil {
		t.Fatalf("insert unreplicated credential: %v", err)
	}
	// A deactivated credential is a deliberate tombstone, invisible to the loaded
	// pool, and must survive a save that does not mention it.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO upstream_credentials (upstream_id, ref, secret, status, is_active, created_at, updated_at)
		VALUES (?, 'dahl-key-555', 'sk-dead', 'deactivated', 0, ?, ?)
	`, upstreamID("dahl"), now, now); err != nil {
		t.Fatalf("insert deactivated credential: %v", err)
	}
	if got := countCreds("dahl"); got != 5 {
		t.Fatalf("expected 5 credentials before prune, got %d", got)
	}

	if err := store.SaveSettings(ctx, settings(dahlPool)); err != nil {
		t.Fatalf("SaveSettings with authoritative pool: %v", err)
	}
	if got := countCreds("dahl"); got != 4 {
		t.Fatalf("expected 4 credentials after prune (stale row gone, unreplicated + deactivated kept), got %d", got)
	}
	var stale int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM upstream_credentials WHERE ref = 'upstream-key-1'").Scan(&stale); err != nil {
		t.Fatalf("query stale ref: %v", err)
	}
	if stale != 0 {
		t.Fatal("stale ref 'upstream-key-1' should have been pruned")
	}
	var unreplicated int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM upstream_credentials WHERE ref = 'dahl-key-999'").Scan(&unreplicated); err != nil {
		t.Fatalf("query unreplicated ref: %v", err)
	}
	if unreplicated != 1 {
		t.Fatal("credential without a replicated secret must not be pruned")
	}
	var deactivated int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM upstream_credentials WHERE ref = 'dahl-key-555'").Scan(&deactivated); err != nil {
		t.Fatalf("query deactivated ref: %v", err)
	}
	if deactivated != 1 {
		t.Fatal("deactivated credential tombstone must not be pruned")
	}
	if got := countCreds("uno"); got != 2 {
		t.Fatalf("prune must be scoped to the saved upstream; uno has %d credentials, want 2", got)
	}

	if err := store.SaveSettings(ctx, settings(nil)); err != nil {
		t.Fatalf("SaveSettings with empty pool: %v", err)
	}
	if got := countCreds("dahl"); got != 4 {
		t.Fatalf("empty pool must not prune; dahl has %d credentials, want 4", got)
	}
}
