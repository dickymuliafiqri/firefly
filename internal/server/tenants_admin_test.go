package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/registry"
	"github.com/dickymuliafiqri/firefly/internal/security/auth"
	"github.com/dickymuliafiqri/firefly/internal/storage/turso"
	_ "turso.tech/database/tursogo"
)

// newTenantsTestServer builds a server backed by a temp config dir and a live
// registry, mirroring the persistence path production uses (files + hot-swap).
// No admin gate is configured, so handlers are reachable without a token.
func newTenantsTestServer(t *testing.T) (*Server, *registry.Registry, string) {
	t.Helper()
	tmpDir := t.TempDir()
	reg := registry.New()
	src := config.NewFileConfigSource(tmpDir)
	if _, err := reg.BuildAndStore(context.Background(), src, os.LookupEnv); err != nil {
		t.Fatalf("initial build and store failed: %v", err)
	}
	deps := RouterDeps{
		Snapshots:   reg,
		Registry:    reg,
		ConfigDir:   tmpDir,
		TenantStore: auth.NewStore(reg),
		Limiter:     limits.New(),
	}
	return New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil), reg, tmpDir
}

func doTenantsRequest(t *testing.T, s *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}

func decodeTenantsResponse(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal response: %v; body = %s", err, w.Body.String())
	}
	return out
}

func tenantNames(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	resp := decodeTenantsResponse(t, w)
	raw, ok := resp["tenants"].([]any)
	if !ok {
		t.Fatalf("response has no tenants list: %s", w.Body.String())
	}
	names := make([]string, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("tenant entry is not an object: %v", item)
		}
		names = append(names, m["name"].(string))
	}
	return names
}

func TestTenantsAdmin_RequiresAdmin(t *testing.T) {
	reg := registry.New()
	deps := RouterDeps{
		Snapshots:  reg,
		Registry:   reg,
		AdminToken: "top-secret-admin-token",
	}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	cases := []struct {
		name   string
		method string
		path   string
		body   any
		token  string // empty means no Authorization header at all
	}{
		{"list without token", http.MethodGet, "/api/tenants", nil, ""},
		{"create without token", http.MethodPost, "/api/tenants", map[string]any{"name": "acme"}, ""},
		{"get without token", http.MethodGet, "/api/tenants/acme", nil, ""},
		{"update without token", http.MethodPut, "/api/tenants/acme", map[string]any{"name": "acme"}, ""},
		{"delete without token", http.MethodDelete, "/api/tenants/acme", nil, ""},
		{"list with wrong token", http.MethodGet, "/api/tenants", nil, "wrong-token"},
		{"create with wrong token", http.MethodPost, "/api/tenants", map[string]any{"name": "acme"}, "wrong-token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := doTenantsRequest(t, s, tc.method, tc.path, tc.body)
			if tc.token != "" {
				// doTenantsRequest sends no Authorization header; resend with one.
				var reader io.Reader
				if tc.body != nil {
					raw, err := json.Marshal(tc.body)
					if err != nil {
						t.Fatalf("marshal body: %v", err)
					}
					reader = bytes.NewReader(raw)
				}
				req := httptest.NewRequest(tc.method, tc.path, reader)
				req.Header.Set("Authorization", "Bearer "+tc.token)
				w = httptest.NewRecorder()
				s.Handler().ServeHTTP(w, req)
			}
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body = %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "top-secret-admin-token") {
				t.Error("error response leaks the admin token")
			}
		})
	}
}

func TestTenantsAdmin_CRUDRoundTrip(t *testing.T) {
	s, reg, tmpDir := newTenantsTestServer(t)

	// Create: key material is generated when omitted.
	createBody := map[string]any{
		"name":           "acme",
		"max_tokens":     1000,
		"allowed_models": []string{"*"},
		"metadata":       map[string]string{"plan": "pro"},
	}
	w := doTenantsRequest(t, s, http.MethodPost, "/api/tenants", createBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST status = %d, want 201; body = %s", w.Code, w.Body.String())
	}
	created := decodeTenantsResponse(t, w)["tenant"].(map[string]any)
	apiKey, ok := created["api_key"].(string)
	if !ok || !strings.HasPrefix(apiKey, auth.KeyPrefix) {
		t.Fatalf("generated api_key missing or malformed: %v", created["api_key"])
	}
	keyHash, ok := created["key_hash"].(string)
	if !ok || !strings.HasPrefix(keyHash, "sha256:") {
		t.Fatalf("derived key_hash missing or malformed: %v", created["key_hash"])
	}

	// The change must be live in the routing snapshot immediately.
	snap := reg.Current()
	if snap == nil {
		t.Fatal("snapshot missing after create")
	}
	found := false
	for _, k := range snap.TenantKeys() {
		tenant, ok := snap.TenantByKey(k)
		if ok && tenant.Name == "acme" {
			found = true
			if tenant.KeyHash != keyHash {
				t.Errorf("snapshot key_hash = %q, want %q", tenant.KeyHash, keyHash)
			}
		}
	}
	if !found {
		t.Error("created tenant not present in snapshot")
	}

	// ... and persisted to tenants.json on disk.
	disk, err := os.ReadFile(tmpDir + "/" + config.FileNameTenants)
	if err != nil {
		t.Fatalf("read tenants.json: %v", err)
	}
	if !strings.Contains(string(disk), `"acme"`) {
		t.Errorf("tenants.json does not contain the created tenant: %s", disk)
	}
	// The tenant CRUD save path is a second catalog writer: tenants.json holds raw
	// gateway keys, so it must land owner-only.
	t.Run("tenants.json lands owner-only", func(t *testing.T) {
		skipUnlessPosixModes(t)
		diskInfo, err := os.Stat(tmpDir + "/" + config.FileNameTenants)
		if err != nil {
			t.Fatalf("stat tenants.json: %v", err)
		}
		if got := diskInfo.Mode().Perm(); got != config.SecretFileMode {
			t.Errorf("tenants.json mode = %o, want %o", got, config.SecretFileMode)
		}
	})

	// List contains exactly the new tenant.
	w = doTenantsRequest(t, s, http.MethodGet, "/api/tenants", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET list status = %d, want 200", w.Code)
	}
	if names := tenantNames(t, w); len(names) != 1 || names[0] != "acme" {
		t.Fatalf("list = %v, want [acme]", names)
	}

	// Get by name.
	w = doTenantsRequest(t, s, http.MethodGet, "/api/tenants/acme", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET one status = %d, want 200; body = %s", w.Code, w.Body.String())
	}

	// Update: quota changes, masked credential placeholder keeps the key.
	masked := apiKey[:3] + "..." + apiKey[len(apiKey)-4:]
	w = doTenantsRequest(t, s, http.MethodPut, "/api/tenants/acme", map[string]any{
		"name":       "acme",
		"max_tokens": 5000,
		"api_key":    masked,
		// used_tokens is metered state and must be ignored even if supplied.
		"used_tokens": 999999,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	updated := decodeTenantsResponse(t, w)["tenant"].(map[string]any)
	if updated["max_tokens"].(float64) != 5000 {
		t.Errorf("max_tokens = %v, want 5000", updated["max_tokens"])
	}
	if updated["api_key"].(string) != apiKey {
		t.Errorf("masked placeholder rotated the credential: got %v, want original key", updated["api_key"])
	}
	if updated["used_tokens"].(float64) != 0 {
		t.Errorf("used_tokens = %v, want 0 (request value must be ignored)", updated["used_tokens"])
	}

	// Rotation with an explicit new plaintext key.
	newKey := auth.KeyPrefix + "0123456789abcdef0123456789abcdef"
	w = doTenantsRequest(t, s, http.MethodPut, "/api/tenants/acme", map[string]any{
		"name":    "acme",
		"api_key": newKey,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT rotate status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	rotated := decodeTenantsResponse(t, w)["tenant"].(map[string]any)
	if rotated["api_key"].(string) != newKey {
		t.Errorf("rotation did not apply: api_key = %v", rotated["api_key"])
	}
	if want := auth.HashKey(newKey); rotated["key_hash"].(string) != want {
		t.Errorf("key_hash = %v, want %v after rotation", rotated["key_hash"], want)
	}

	// Delete removes it from list, snapshot, and disk.
	w = doTenantsRequest(t, s, http.MethodDelete, "/api/tenants/acme", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if got := decodeTenantsResponse(t, w)["status"]; got != "ok" {
		t.Errorf("delete status field = %v, want ok", got)
	}

	w = doTenantsRequest(t, s, http.MethodGet, "/api/tenants/acme", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET after delete status = %d, want 404", w.Code)
	}
	w = doTenantsRequest(t, s, http.MethodGet, "/api/tenants", nil)
	if names := tenantNames(t, w); len(names) != 0 {
		t.Fatalf("list after delete = %v, want empty", names)
	}
	snap = reg.Current()
	for _, k := range snap.TenantKeys() {
		if tenant, ok := snap.TenantByKey(k); ok && tenant.Name == "acme" {
			t.Error("deleted tenant still present in snapshot")
		}
	}
	disk, err = os.ReadFile(tmpDir + "/" + config.FileNameTenants)
	if err != nil {
		t.Fatalf("read tenants.json after delete: %v", err)
	}
	if strings.Contains(string(disk), `"acme"`) {
		t.Errorf("tenants.json still contains the deleted tenant: %s", disk)
	}
}

func TestTenantsAdmin_ValidationAndConflict(t *testing.T) {
	s, _, _ := newTenantsTestServer(t)

	// Duplicate name → 409.
	w := doTenantsRequest(t, s, http.MethodPost, "/api/tenants", map[string]any{"name": "acme"})
	if w.Code != http.StatusCreated {
		t.Fatalf("seed create status = %d, want 201; body = %s", w.Code, w.Body.String())
	}
	w = doTenantsRequest(t, s, http.MethodPost, "/api/tenants", map[string]any{"name": "acme"})
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate create status = %d, want 409; body = %s", w.Code, w.Body.String())
	}

	// Unknown allowed model → 400 from catalog validation.
	w = doTenantsRequest(t, s, http.MethodPost, "/api/tenants", map[string]any{
		"name":           "bogus",
		"allowed_models": []string{"no-such-model"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown model status = %d, want 400; body = %s", w.Code, w.Body.String())
	}

	// Missing name → 400.
	w = doTenantsRequest(t, s, http.MethodPost, "/api/tenants", map[string]any{"max_tokens": 10})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("nameless create status = %d, want 400; body = %s", w.Code, w.Body.String())
	}

	// Body/path name mismatch on update → 400.
	w = doTenantsRequest(t, s, http.MethodPut, "/api/tenants/acme", map[string]any{"name": "other"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("name mismatch status = %d, want 400; body = %s", w.Code, w.Body.String())
	}

	// Malformed JSON → 400 (not 500).
	req := httptest.NewRequest(http.MethodPost, "/api/tenants", strings.NewReader("{not json"))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed JSON status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}

func TestTenantsAdmin_NotFound(t *testing.T) {
	s, _, _ := newTenantsTestServer(t)

	w := doTenantsRequest(t, s, http.MethodGet, "/api/tenants/ghost", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("GET missing status = %d, want 404", w.Code)
	}
	w = doTenantsRequest(t, s, http.MethodPut, "/api/tenants/ghost", map[string]any{"name": "ghost"})
	if w.Code != http.StatusNotFound {
		t.Errorf("PUT missing status = %d, want 404", w.Code)
	}
	w = doTenantsRequest(t, s, http.MethodDelete, "/api/tenants/ghost", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("DELETE missing status = %d, want 404", w.Code)
	}
}

func TestTenantsAdmin_PayloadTooLarge(t *testing.T) {
	s, _, _ := newTenantsTestServer(t)

	// One byte past the gateway limit, streamed as NULs so the test stays
	// cheap. The handler must answer 413, distinguishable from a JSON 400.
	oversize := io.LimitReader(zeroReader{}, maxRequestBodyBytes+1)
	req := httptest.NewRequest(http.MethodPost, "/api/tenants", oversize)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized create status = %d, want 413; body = %s", w.Code, w.Body.String())
	}

	oversize = io.LimitReader(zeroReader{}, maxRequestBodyBytes+1)
	req = httptest.NewRequest(http.MethodPut, "/api/tenants/acme", oversize)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized update status = %d, want 413; body = %s", w.Code, w.Body.String())
	}
}

func TestTenantsAdmin_CRUDRoundTrip_TursoBacked(t *testing.T) {
	db, err := sql.Open("turso", ":memory:")
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	if err := turso.MigrateSchema(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}

	tStore := turso.NewStoreWithDB(db)
	reg := registry.New()

	deps := RouterDeps{
		Snapshots:   reg,
		Registry:    reg,
		TursoStore:  tStore,
		TenantStore: auth.NewStore(reg),
		Limiter:     limits.New(),
	}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, ctx, nil)

	// 1. Create: key material is generated when omitted, row is inserted into database
	createBody := map[string]any{
		"name":           "db-tenant",
		"max_tokens":     2000,
		"allowed_models": []string{"*"},
	}
	w := doTenantsRequest(t, s, http.MethodPost, "/api/tenants", createBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST status = %d, want 201; body = %s", w.Code, w.Body.String())
	}
	created := decodeTenantsResponse(t, w)["tenant"].(map[string]any)
	if created["name"] != "db-tenant" {
		t.Fatalf("created name = %v, want db-tenant", created["name"])
	}
	apiKey := created["api_key"].(string)

	// Direct DB verification: row must be in the tenants table in the database
	var dbName, dbStatus, dbKey string
	var dbMaxTokens int64
	err = db.QueryRowContext(ctx, "SELECT name, status, api_key, max_tokens FROM tenants WHERE name = ?", "db-tenant").
		Scan(&dbName, &dbStatus, &dbKey, &dbMaxTokens)
	if err != nil {
		t.Fatalf("query db tenant: %v", err)
	}
	if dbName != "db-tenant" || dbStatus != "active" || dbKey != apiKey || dbMaxTokens != 2000 {
		t.Errorf("db row mismatch: name=%s status=%s key=%s maxTokens=%d", dbName, dbStatus, dbKey, dbMaxTokens)
	}

	// 2. List via API
	w = doTenantsRequest(t, s, http.MethodGet, "/api/tenants", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET list status = %d, want 200", w.Code)
	}
	if names := tenantNames(t, w); len(names) != 1 || names[0] != "db-tenant" {
		t.Fatalf("list = %v, want [db-tenant]", names)
	}

	// 3. Get via API
	w = doTenantsRequest(t, s, http.MethodGet, "/api/tenants/db-tenant", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET one status = %d, want 200", w.Code)
	}
	got := decodeTenantsResponse(t, w)
	if got["name"] != "db-tenant" || got["api_key"] != apiKey {
		t.Errorf("GET one mismatch: %v", got)
	}

	// 4. Update via API
	w = doTenantsRequest(t, s, http.MethodPut, "/api/tenants/db-tenant", map[string]any{
		"name":       "db-tenant",
		"max_tokens": 8000,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	// Direct DB check for updated max_tokens in the tenants table
	err = db.QueryRowContext(ctx, "SELECT max_tokens FROM tenants WHERE name = ?", "db-tenant").Scan(&dbMaxTokens)
	if err != nil {
		t.Fatalf("query db max_tokens after update: %v", err)
	}
	if dbMaxTokens != 8000 {
		t.Errorf("db max_tokens = %d, want 8000", dbMaxTokens)
	}

	// 5. Delete via API
	w = doTenantsRequest(t, s, http.MethodDelete, "/api/tenants/db-tenant", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, want 200; body = %s", w.Code, w.Body.String())
	}

	// Direct DB check: row must be deleted from database
	var count int
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM tenants WHERE name = ?", "db-tenant").Scan(&count)
	if count != 0 {
		t.Errorf("tenants table still contains deleted tenant: count = %d", count)
	}

	// GET after delete returns 404
	w = doTenantsRequest(t, s, http.MethodGet, "/api/tenants/db-tenant", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET after delete status = %d, want 404", w.Code)
	}
}
