package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/registry"
	_ "turso.tech/database/tursogo"
)

// providersEnv reuses the harvester harness: the same in-memory Turso store, a
// hand-bound syncer so reloads are deterministic, and the registry it swaps.
type providersEnv struct {
	*harvesterEnv
}

func newProvidersEnv(t *testing.T, mutate func(*RouterDeps)) *providersEnv {
	t.Helper()
	return &providersEnv{harvesterEnv: newHarvesterEnv(t, mutate)}
}

// do issues one JSON request through the real server handler.
func (e *providersEnv) do(method, path, token string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("marshal %s %s body: %v", method, path, err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(w, req)
	return w
}

// admin issues a credential-less request: the harness configures no admin gate,
// so authorizeAdmin allows it (the same posture as other bare unit servers).
func (e *providersEnv) admin(method, path string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(method, path, "", body)
}

func (e *providersEnv) decode(w *httptest.ResponseRecorder) map[string]any {
	e.t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		e.t.Fatalf("decode response: %v; body = %s", err, w.Body.String())
	}
	return out
}

func (e *providersEnv) createProvider(name, baseURL string) int64 {
	e.t.Helper()
	w := e.admin(http.MethodPost, "/api/providers", map[string]any{"name": name, "base_url": baseURL})
	if w.Code != http.StatusCreated {
		e.t.Fatalf("create provider %q: status = %d, want 201; body = %s", name, w.Code, w.Body.String())
	}
	resp := e.decode(w)
	if reloaded, _ := resp["reloaded"].(bool); !reloaded {
		e.t.Fatalf("create provider %q: reloaded = false, want true", name)
	}
	prov, ok := resp["provider"].(map[string]any)
	if !ok {
		e.t.Fatalf("create provider %q: response has no provider object: %s", name, w.Body.String())
	}
	id, ok := prov["id"].(float64)
	if !ok || id <= 0 {
		e.t.Fatalf("create provider %q: id = %v, want positive number", name, prov["id"])
	}
	return int64(id)
}

func (e *providersEnv) upsertKeys(providerID int64, body map[string]any) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.admin(http.MethodPost, fmt.Sprintf("/api/providers/%d/keys", providerID), body)
}

func (e *providersEnv) listKeys(providerID int64) ([]map[string]any, *httptest.ResponseRecorder) {
	e.t.Helper()
	w := e.admin(http.MethodGet, fmt.Sprintf("/api/providers/%d/keys", providerID), nil)
	if w.Code != http.StatusOK {
		e.t.Fatalf("list keys: status = %d; body = %s", w.Code, w.Body.String())
	}
	resp := e.decode(w)
	raw, ok := resp["keys"].([]any)
	if !ok {
		e.t.Fatalf("list keys: no keys array: %s", w.Body.String())
	}
	keys := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			e.t.Fatalf("list keys: entry is not an object: %v", item)
		}
		keys = append(keys, m)
	}
	return keys, w
}

func (e *providersEnv) providerIDByName(name string) int64 {
	e.t.Helper()
	var id int64
	if err := e.db.QueryRowContext(context.Background(), "SELECT id FROM providers WHERE name = ?", name).Scan(&id); err != nil {
		e.t.Fatalf("read provider %q id: %v", name, err)
	}
	return id
}

// insertUpstream creates a bare upstreams row owned by providerID, standing in
// for a settings-saved upstream whose credential pool is bound by provider_id.
func (e *providersEnv) insertUpstream(name string, providerID int64) int64 {
	e.t.Helper()
	now := time.Now().UnixMilli()
	res, err := e.db.ExecContext(context.Background(), `
		INSERT INTO upstreams (name, protocol, base_url, key_strategy, provider_id, created_at, updated_at)
		VALUES (?, 'openai', 'https://api.example.test', 'round_robin', ?, ?, ?)
	`, name, providerID, now, now)
	if err != nil {
		e.t.Fatalf("insert upstream %q: %v", name, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		e.t.Fatalf("read upstream %q id: %v", name, err)
	}
	return id
}

func (e *providersEnv) bindCredential(upstreamID, keyID int64, ref string) {
	e.t.Helper()
	now := time.Now().UnixMilli()
	e.exec(`
		INSERT INTO upstream_credentials (upstream_id, api_key_id, ref, status, is_active, created_at, updated_at)
		VALUES (?, ?, ?, 'active', 1, ?, ?)
	`, upstreamID, keyID, ref, now, now)
}

func (e *providersEnv) countCredentialsForKey(keyID int64) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM upstream_credentials WHERE api_key_id = ?", keyID).Scan(&n); err != nil {
		e.t.Fatalf("count credentials for key %d: %v", keyID, err)
	}
	return n
}

func (e *providersEnv) keyProviderID(keyID int64) int64 {
	e.t.Helper()
	var pid int64
	if err := e.db.QueryRowContext(context.Background(),
		"SELECT provider_id FROM api_keys WHERE id = ?", keyID).Scan(&pid); err != nil {
		e.t.Fatalf("read provider of key %d: %v", keyID, err)
	}
	return pid
}

func testKeyIDs(t *testing.T, w *httptest.ResponseRecorder) []int64 {
	t.Helper()
	var out struct {
		KeyIDs []int64 `json:"key_ids"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode key_ids: %v; body = %s", err, w.Body.String())
	}
	return out.KeyIDs
}

func TestProvidersAdmin_RequiresAdmin(t *testing.T) {
	const adminToken = "top-secret-providers-admin-token"
	env := newProvidersEnv(t, func(d *RouterDeps) { d.AdminToken = adminToken })
	before := env.revision()

	cases := []struct {
		name   string
		method string
		path   string
		body   any
		token  string // empty means no Authorization header at all
	}{
		{"list providers without token", http.MethodGet, "/api/providers", nil, ""},
		{"create provider without token", http.MethodPost, "/api/providers", map[string]any{"name": "acme", "base_url": "https://api.acme.test"}, ""},
		{"get provider without token", http.MethodGet, "/api/providers/1", nil, ""},
		{"update provider without token", http.MethodPut, "/api/providers/1", map[string]any{"base_url": "https://api.acme.test"}, ""},
		{"delete provider without token", http.MethodDelete, "/api/providers/1", nil, ""},
		{"list keys without token", http.MethodGet, "/api/providers/1/keys", nil, ""},
		{"upsert keys without token", http.MethodPost, "/api/providers/1/keys", map[string]any{"keys": []map[string]any{{"api_key": "sk-leak-1"}}}, ""},
		{"patch key without token", http.MethodPatch, "/api/keys/1", map[string]any{"status": "deactivated"}, ""},
		{"delete key without token", http.MethodDelete, "/api/keys/1", nil, ""},
		{"list providers with wrong token", http.MethodGet, "/api/providers", nil, "wrong-token"},
		{"create provider with wrong token", http.MethodPost, "/api/providers", map[string]any{"name": "acme", "base_url": "https://api.acme.test"}, "wrong-token"},
		{"patch key with wrong token", http.MethodPatch, "/api/keys/1", map[string]any{"status": "deactivated"}, "wrong-token"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := env.do(tc.method, tc.path, tc.token, tc.body)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body = %s", w.Code, w.Body.String())
			}
			for _, secret := range []string{adminToken, tc.token, "sk-leak-1"} {
				if secret != "" && strings.Contains(w.Body.String(), secret) {
					t.Fatalf("response leaked %q: %s", secret, w.Body.String())
				}
			}
		})
	}

	// Rejected requests must not touch the database.
	if got := env.revision(); got != before {
		t.Fatalf("rejected requests bumped revision: %d -> %d", before, got)
	}
	if got := env.countProviders(); got != 0 {
		t.Fatalf("rejected requests created %d provider(s)", got)
	}
}

func TestProvidersAdmin_PreflightAdvertisesPatchAndDelete(t *testing.T) {
	env := newProvidersEnv(t, nil)

	for _, path := range []string{"/api/providers", "/api/providers/1", "/api/providers/1/keys", "/api/keys/1"} {
		t.Run(path, func(t *testing.T) {
			w := env.admin(http.MethodOptions, path, nil)
			if w.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204", w.Code)
			}
			methods := w.Header().Get("Access-Control-Allow-Methods")
			for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
				if !strings.Contains(methods, m) {
					t.Errorf("Access-Control-Allow-Methods = %q, missing %s", methods, m)
				}
			}
		})
	}
}

func TestProvidersAdmin_FailsClosedWithoutStore(t *testing.T) {
	reg := registry.New()
	deps := RouterDeps{Snapshots: reg, Registry: reg}
	s := New(Config{Addr: "127.0.0.1:0"}, deps, context.Background(), nil)

	cases := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/api/providers", nil},
		{http.MethodPost, "/api/providers", map[string]any{"name": "acme", "base_url": "https://api.acme.test"}},
		{http.MethodGet, "/api/providers/1", nil},
		{http.MethodPut, "/api/providers/1", map[string]any{"base_url": "https://api.acme.test"}},
		{http.MethodDelete, "/api/providers/1", nil},
		{http.MethodGet, "/api/providers/1/keys", nil},
		{http.MethodPost, "/api/providers/1/keys", map[string]any{"keys": []map[string]any{{"api_key": "sk-no-store-1"}}}},
		{http.MethodPatch, "/api/keys/1", map[string]any{"status": "deactivated"}},
		{http.MethodDelete, "/api/keys/1", nil},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			var reader io.Reader
			if tc.body != nil {
				raw, err := json.Marshal(tc.body)
				if err != nil {
					t.Fatalf("marshal body: %v", err)
				}
				reader = bytes.NewReader(raw)
			}
			req := httptest.NewRequest(tc.method, tc.path, reader)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, req)
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503; body = %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "turso") {
				t.Errorf("503 body does not explain the storage requirement: %s", w.Body.String())
			}
			if strings.Contains(w.Body.String(), "sk-no-store-1") {
				t.Error("503 body echoes the submitted credential")
			}
		})
	}
}

func TestProvidersAdmin_ProviderCRUDRoundTrip(t *testing.T) {
	env := newProvidersEnv(t, nil)

	// Empty store lists cleanly.
	w := env.admin(http.MethodGet, "/api/providers", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list empty: status = %d; body = %s", w.Code, w.Body.String())
	}
	if resp := env.decode(w); resp["count"].(float64) != 0 {
		t.Fatalf("empty list count = %v, want 0", resp["count"])
	}

	id := env.createProvider("poolA", "https://api.pool-a.test")

	// Name is required and unique.
	w = env.admin(http.MethodPost, "/api/providers", map[string]any{"base_url": "https://api.x.test"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("create without name: status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	w = env.admin(http.MethodPost, "/api/providers", map[string]any{"name": "poolB"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("create without base_url: status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	w = env.admin(http.MethodPost, "/api/providers", map[string]any{"name": "poolA", "base_url": "https://api.other.test"})
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate name: status = %d, want 409; body = %s", w.Code, w.Body.String())
	}

	// Read back.
	w = env.admin(http.MethodGet, fmt.Sprintf("/api/providers/%d", id), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get: status = %d; body = %s", w.Code, w.Body.String())
	}
	prov := env.decode(w)["provider"].(map[string]any)
	if prov["name"] != "poolA" || prov["base_url"] != "https://api.pool-a.test" {
		t.Fatalf("get returned %v", prov)
	}
	if prov["is_active"] != true {
		t.Fatalf("new provider is_active = %v, want true", prov["is_active"])
	}

	w = env.admin(http.MethodGet, "/api/providers/987654", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("get missing: status = %d, want 404; body = %s", w.Code, w.Body.String())
	}
	w = env.admin(http.MethodGet, "/api/providers/abc", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("get bad id: status = %d, want 400; body = %s", w.Code, w.Body.String())
	}

	// Update: partial edits keep other fields.
	w = env.admin(http.MethodPut, fmt.Sprintf("/api/providers/%d", id), map[string]any{"base_url": "https://api.pool-a2.test"})
	if w.Code != http.StatusOK {
		t.Fatalf("update: status = %d; body = %s", w.Code, w.Body.String())
	}
	prov = env.decode(w)["provider"].(map[string]any)
	if prov["name"] != "poolA" || prov["base_url"] != "https://api.pool-a2.test" {
		t.Fatalf("update result = %v", prov)
	}
	w = env.admin(http.MethodPut, fmt.Sprintf("/api/providers/%d", id), map[string]any{"is_active": false})
	if w.Code != http.StatusOK {
		t.Fatalf("deactivate: status = %d; body = %s", w.Code, w.Body.String())
	}
	prov = env.decode(w)["provider"].(map[string]any)
	if prov["is_active"] != false || prov["base_url"] != "https://api.pool-a2.test" {
		t.Fatalf("deactivate result = %v", prov)
	}

	// The name is the harvester's natural key: renaming would orphan the pool.
	w = env.admin(http.MethodPut, fmt.Sprintf("/api/providers/%d", id), map[string]any{"name": "renamed"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("rename: status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	// An empty update is a client bug, not a no-op.
	w = env.admin(http.MethodPut, fmt.Sprintf("/api/providers/%d", id), map[string]any{})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty update: status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	// The adopted column widths are enforced by the writer, because SQLite would
	// store a longer value happily and every validating reader would then refuse
	// to touch the row.
	w = env.admin(http.MethodPut, fmt.Sprintf("/api/providers/%d", id), map[string]any{"base_url": strings.Repeat("u", 256)})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized base_url: status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	w = env.admin(http.MethodPost, "/api/providers", map[string]any{
		"name": strings.Repeat("n", 65), "base_url": "https://api.pool-b.test",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized name: status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	w = env.admin(http.MethodPut, "/api/providers/987654", map[string]any{"is_active": true})
	if w.Code != http.StatusNotFound {
		t.Fatalf("update missing: status = %d, want 404; body = %s", w.Code, w.Body.String())
	}

	// A provider with no upstreams deletes with its keys.
	w = env.upsertKeys(id, map[string]any{"keys": []map[string]any{{"api_key": "sk-crud-a"}, {"api_key": "sk-crud-b"}}})
	if w.Code != http.StatusOK {
		t.Fatalf("upsert keys: status = %d; body = %s", w.Code, w.Body.String())
	}
	w = env.admin(http.MethodDelete, fmt.Sprintf("/api/providers/%d", id), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("delete: status = %d; body = %s", w.Code, w.Body.String())
	}
	resp := env.decode(w)
	if resp["deleted_keys"].(float64) != 2 {
		t.Fatalf("deleted_keys = %v, want 2", resp["deleted_keys"])
	}
	var keysLeft int
	if err := env.db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM api_keys WHERE provider_id = ?", id).Scan(&keysLeft); err != nil {
		t.Fatalf("count leftover keys: %v", err)
	}
	if keysLeft != 0 {
		t.Fatalf("%d key(s) survived the provider delete", keysLeft)
	}
	w = env.admin(http.MethodDelete, fmt.Sprintf("/api/providers/%d", id), nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("delete twice: status = %d, want 404; body = %s", w.Code, w.Body.String())
	}
}

func TestProvidersAdmin_DeleteProviderRefusedWhileReferenced(t *testing.T) {
	env := newProvidersEnv(t, nil)

	keyIDs := env.seedLinkedUpstream("poolA", "upA", "sk-linked-1")
	if len(keyIDs) != 1 {
		t.Fatalf("seeded key ids = %v, want one", keyIDs)
	}
	id := env.providerIDByName("poolA")

	w := env.admin(http.MethodDelete, fmt.Sprintf("/api/providers/%d", id), nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("delete referenced provider: status = %d, want 409; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "upstream") {
		t.Errorf("409 body does not name the reference: %s", w.Body.String())
	}
	if w.Body.String() != "" && strings.Contains(w.Body.String(), "sk-linked-1") {
		t.Fatal("409 body leaked the key")
	}

	// Detaching the upstream is the operator's explicit step, after which the
	// delete proceeds.
	env.exec("UPDATE upstreams SET provider_id = NULL WHERE name = 'upA'")
	w = env.admin(http.MethodDelete, fmt.Sprintf("/api/providers/%d", id), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("delete after detach: status = %d; body = %s", w.Code, w.Body.String())
	}
}

func TestProvidersAdmin_KeyLifecycle(t *testing.T) {
	env := newProvidersEnv(t, nil)
	id := env.createProvider("poolA", "https://api.pool-a.test")

	// Batch upsert creates, and a replay is idempotent with a stable id.
	w := env.upsertKeys(id, map[string]any{"keys": []map[string]any{
		{"api_key": "sk-life-aaaaaaaa", "account_metadata": map[string]any{"tier": "vault-pro-7x"}},
	}})
	if w.Code != http.StatusOK {
		t.Fatalf("upsert: status = %d; body = %s", w.Code, w.Body.String())
	}
	resp := env.decode(w)
	if resp["created"].(float64) != 1 || resp["updated"].(float64) != 0 || resp["unchanged"].(float64) != 0 {
		t.Fatalf("create counters = %v", resp)
	}
	first := testKeyIDs(t, w)
	if len(first) != 1 {
		t.Fatalf("key_ids = %v, want one id", first)
	}

	w = env.upsertKeys(id, map[string]any{"keys": []map[string]any{
		{"api_key": "sk-life-aaaaaaaa", "account_metadata": map[string]any{"tier": "vault-pro-7x"}},
	}})
	resp = env.decode(w)
	if resp["unchanged"].(float64) != 1 || resp["created"].(float64) != 0 {
		t.Fatalf("replay counters = %v", resp)
	}
	if ids := testKeyIDs(t, w); len(ids) != 1 || ids[0] != first[0] {
		t.Fatalf("replay changed key ids: %v -> %v", first, ids)
	}

	w = env.upsertKeys(id, map[string]any{"keys": []map[string]any{{"api_key": "sk-life-bbbbbbbb"}}})
	if resp := env.decode(w); resp["created"].(float64) != 1 {
		t.Fatalf("second create counters = %v", resp)
	}

	// Validation: empty secret, mismatched provider name, oversized batch.
	w = env.upsertKeys(id, map[string]any{"keys": []map[string]any{{"api_key": "sk-life-cccccccc"}, {"api_key": ""}}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty api_key: status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	if keys, _ := env.listKeys(id); len(keys) != 2 {
		t.Fatalf("rejected batch was not rolled back: %d keys", len(keys))
	}
	w = env.upsertKeys(id, map[string]any{"keys": []map[string]any{}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty batch: status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	w = env.upsertKeys(id, map[string]any{"keys": []map[string]any{
		{"provider": "otherPool", "api_key": "sk-life-dddddddd"},
	}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("provider mismatch in body: status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	// The operator surface issues lifecycle commands, so it is narrower than the
	// harvester snapshot: known statuses only, and millisecond expiry only.
	w = env.upsertKeys(id, map[string]any{"keys": []map[string]any{
		{"api_key": "sk-life-ffffffff", "status": "dead"},
	}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status outside the operator vocabulary: status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	w = env.upsertKeys(id, map[string]any{"keys": []map[string]any{
		{"api_key": "sk-life-gggggggg", "expires_at": time.Now().Unix()},
	}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("second-scale expires_at: status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	w = env.upsertKeys(id, map[string]any{"keys": []map[string]any{
		{"api_key": "sk-life-hhhhhhhh", "account_metadata": `"` + strings.Repeat("v", maxHarvesterMetadataBytes+1) + `"`},
	}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized account_metadata: status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	oversized := make([]map[string]any, 0, maxHarvesterSyncKeys+1)
	for i := 0; i <= maxHarvesterSyncKeys; i++ {
		oversized = append(oversized, map[string]any{"api_key": fmt.Sprintf("sk-batch-%d", i)})
	}
	w = env.upsertKeys(id, map[string]any{"keys": oversized})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized batch: status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	w = env.upsertKeys(987654, map[string]any{"keys": []map[string]any{{"api_key": "sk-life-eeeeeeee"}}})
	if w.Code != http.StatusNotFound {
		t.Fatalf("upsert to missing provider: status = %d, want 404; body = %s", w.Code, w.Body.String())
	}

	// List: masked hints, no raw secrets, no account_metadata.
	keys, listW := env.listKeys(id)
	if len(keys) != 2 {
		t.Fatalf("listed %d keys, want 2", len(keys))
	}
	body := listW.Body.String()
	if strings.Contains(body, "sk-life-aaaaaaaa") || strings.Contains(body, "sk-life-bbbbbbbb") {
		t.Fatal("key list returned a raw secret")
	}
	if strings.Contains(body, "vault-pro-7x") || strings.Contains(body, "account_metadata") {
		t.Fatal("key list returned account_metadata contents")
	}
	if keys[0]["api_key_hint"] != "sk-...aaaa" {
		t.Fatalf("hint = %v, want sk-...aaaa", keys[0]["api_key_hint"])
	}
	if keys[0]["status"] != "active" || keys[0]["is_active"] != true {
		t.Fatalf("stored key state = %v", keys[0])
	}

	keyID := int64(keys[0]["id"].(float64))

	// Patch: status drives is_active, expiry may be set and cleared.
	w = env.admin(http.MethodPatch, fmt.Sprintf("/api/keys/%d", keyID), map[string]any{"status": "deactivated"})
	if w.Code != http.StatusOK {
		t.Fatalf("patch status: status = %d; body = %s", w.Code, w.Body.String())
	}
	patched := env.decode(w)["key"].(map[string]any)
	if patched["status"] != "deactivated" || patched["is_active"] != false {
		t.Fatalf("patched key = %v", patched)
	}
	if reloaded, _ := env.decode(w)["reloaded"].(bool); !reloaded {
		t.Error("patch did not reload the catalog")
	}

	w = env.admin(http.MethodPatch, fmt.Sprintf("/api/keys/%d", keyID), map[string]any{"status": "active"})
	if w.Code != http.StatusOK {
		t.Fatalf("reactivate: status = %d; body = %s", w.Code, w.Body.String())
	}
	patched = env.decode(w)["key"].(map[string]any)
	if patched["status"] != "active" || patched["is_active"] != true {
		t.Fatalf("reactivated key = %v", patched)
	}

	future := time.Now().Add(time.Hour).UnixMilli()
	w = env.admin(http.MethodPatch, fmt.Sprintf("/api/keys/%d", keyID), map[string]any{"expires_at": future})
	if w.Code != http.StatusOK {
		t.Fatalf("set expiry: status = %d; body = %s", w.Code, w.Body.String())
	}
	if got := env.decode(w)["key"].(map[string]any)["expires_at"]; got != float64(future) {
		t.Fatalf("expires_at = %v, want %d", got, future)
	}
	w = env.admin(http.MethodPatch, fmt.Sprintf("/api/keys/%d", keyID), map[string]any{"expires_at": nil})
	if w.Code != http.StatusOK {
		t.Fatalf("clear expiry: status = %d; body = %s", w.Code, w.Body.String())
	}
	if _, present := env.decode(w)["key"].(map[string]any)["expires_at"]; present {
		t.Fatal("cleared expiry is still present")
	}
	w = env.admin(http.MethodPatch, fmt.Sprintf("/api/keys/%d", keyID), map[string]any{"expires_at": time.Now().Unix()})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("second-scale expires_at: status = %d, want 400; body = %s", w.Code, w.Body.String())
	}

	// Patch validation.
	w = env.admin(http.MethodPatch, fmt.Sprintf("/api/keys/%d", keyID), map[string]any{"status": "bogus"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unsupported status: status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	w = env.admin(http.MethodPatch, fmt.Sprintf("/api/keys/%d", keyID), map[string]any{})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty patch: status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	w = env.admin(http.MethodPatch, "/api/keys/987654", map[string]any{"status": "active"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("patch missing key: status = %d, want 404; body = %s", w.Code, w.Body.String())
	}

	// Hard delete removes the row; the second attempt is a 404.
	w = env.admin(http.MethodDelete, fmt.Sprintf("/api/keys/%d", keyID), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("delete key: status = %d; body = %s", w.Code, w.Body.String())
	}
	if keys, _ := env.listKeys(id); len(keys) != 1 {
		t.Fatalf("after delete, %d keys remain, want 1", len(keys))
	}
	w = env.admin(http.MethodDelete, fmt.Sprintf("/api/keys/%d", keyID), nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("delete twice: status = %d, want 404; body = %s", w.Code, w.Body.String())
	}
}

func TestProvidersAdmin_PatchMirrorsRoutabilityOntoBoundCredentials(t *testing.T) {
	env := newProvidersEnv(t, nil)
	id := env.createProvider("poolA", "https://api.pool-a.test")

	w := env.upsertKeys(id, map[string]any{"keys": []map[string]any{{"api_key": "sk-mirror-aaaaaaa"}}})
	if w.Code != http.StatusOK {
		t.Fatalf("upsert: status = %d; body = %s", w.Code, w.Body.String())
	}
	keyID := testKeyIDs(t, w)[0]

	upstreamID := env.insertUpstream("upA", id)
	env.bindCredential(upstreamID, keyID, fmt.Sprintf("poolA-key-%d", keyID))

	w = env.admin(http.MethodPatch, fmt.Sprintf("/api/keys/%d", keyID), map[string]any{"is_active": false})
	if w.Code != http.StatusOK {
		t.Fatalf("patch: status = %d; body = %s", w.Code, w.Body.String())
	}
	var status string
	var isActive int
	if err := env.db.QueryRowContext(context.Background(),
		"SELECT status, is_active FROM upstream_credentials WHERE api_key_id = ?", keyID).Scan(&status, &isActive); err != nil {
		t.Fatalf("read bound credential: %v", err)
	}
	if status != "deactivated" || isActive != 0 {
		t.Fatalf("bound credential = (%q, %d), want (deactivated, 0)", status, isActive)
	}

	// A hard delete drops the bound row entirely so no ref survives.
	w = env.admin(http.MethodDelete, fmt.Sprintf("/api/keys/%d", keyID), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("delete: status = %d; body = %s", w.Code, w.Body.String())
	}
	if n := env.countCredentialsForKey(keyID); n != 0 {
		t.Fatalf("%d credential row(s) survived the key delete", n)
	}
}

func TestProvidersAdmin_KeyReassignPolicy(t *testing.T) {
	env := newProvidersEnv(t, nil)
	poolA := env.createProvider("poolA", "https://api.pool-a.test")
	poolB := env.createProvider("poolB", "https://api.pool-b.test")

	w := env.upsertKeys(poolA, map[string]any{"keys": []map[string]any{{"api_key": "sk-move-aaaaaaaa"}}})
	if w.Code != http.StatusOK {
		t.Fatalf("upsert to poolA: status = %d; body = %s", w.Code, w.Body.String())
	}
	keyID := testKeyIDs(t, w)[0]
	upstreamID := env.insertUpstream("upA", poolA)
	env.bindCredential(upstreamID, keyID, fmt.Sprintf("poolA-key-%d", keyID))

	// Without the explicit reassign flag the operator's mistake is surfaced, not
	// silently applied.
	w = env.upsertKeys(poolB, map[string]any{"keys": []map[string]any{{"api_key": "sk-move-aaaaaaaa"}}})
	if w.Code != http.StatusConflict {
		t.Fatalf("move without reassign: status = %d, want 409; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "reassign") {
		t.Errorf("409 body does not advertise the reassign flag: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "sk-move-aaaaaaaa") {
		t.Fatal("409 body leaked the key")
	}
	if got := env.keyProviderID(keyID); got != poolA {
		t.Fatalf("rejected move changed owner to %d", got)
	}

	w = env.upsertKeys(poolB, map[string]any{"reassign": true, "keys": []map[string]any{{"api_key": "sk-move-aaaaaaaa"}}})
	if w.Code != http.StatusOK {
		t.Fatalf("move with reassign: status = %d; body = %s", w.Code, w.Body.String())
	}
	resp := env.decode(w)
	if resp["reassigned"].(float64) != 1 || resp["updated"].(float64) != 1 {
		t.Fatalf("reassign counters = %v", resp)
	}
	if got := env.keyProviderID(keyID); got != poolB {
		t.Fatalf("key owner = %d, want %d", got, poolB)
	}
	// The old provider's upstream must stop serving the key.
	if n := env.countCredentialsForKey(keyID); n != 0 {
		t.Fatalf("%d stale credential row(s) left on the old provider", n)
	}
}

func TestProvidersAdmin_NeverLeaksSecretsOrMetadata(t *testing.T) {
	const (
		rawKey  = "sk-live-5UP3RS3CR3T-abcdefghijklmnop"
		metaTag = "dutch-cactus-harbor-mnemonic"
	)
	env := newProvidersEnv(t, nil)
	id := env.createProvider("poolA", "https://api.pool-a.test")

	w := env.upsertKeys(id, map[string]any{"keys": []map[string]any{
		{"api_key": rawKey, "account_metadata": map[string]any{"seed_phrase": metaTag}},
	}})
	if w.Code != http.StatusOK {
		t.Fatalf("upsert: status = %d; body = %s", w.Code, w.Body.String())
	}
	keyID := testKeyIDs(t, w)[0]

	w = env.admin(http.MethodPatch, fmt.Sprintf("/api/keys/%d", keyID), map[string]any{"status": "deactivated"})
	if w.Code != http.StatusOK {
		t.Fatalf("patch: status = %d; body = %s", w.Code, w.Body.String())
	}
	_, listW := env.listKeys(id)
	w = env.admin(http.MethodGet, fmt.Sprintf("/api/providers/%d", id), nil)
	provW := w.Body.String()

	// The vault still holds what was written: masking is a wire concern, the
	// routing store keeps the real material.
	row := env.keyRow(rawKey)
	if !row.Meta.Valid || !strings.Contains(row.Meta.String, metaTag) {
		t.Fatalf("account_metadata was not persisted: %v", row.Meta)
	}

	w = env.admin(http.MethodDelete, fmt.Sprintf("/api/keys/%d", keyID), nil)
	keyDelW := w.Body.String()

	bodies := map[string]string{
		"key list":     listW.Body.String(),
		"provider get": provW,
		"key delete":   keyDelW,
	}
	for name, body := range bodies {
		for _, secret := range []string{rawKey, metaTag} {
			if strings.Contains(body, secret) {
				t.Fatalf("%s leaked %q: %s", name, secret, body)
			}
		}
	}
	if !strings.Contains(listW.Body.String(), "sk-...mnop") {
		t.Fatalf("key list carries no masked hint: %s", listW.Body.String())
	}
	if strings.Contains(env.logs.String(), rawKey) || strings.Contains(env.logs.String(), metaTag) {
		t.Fatal("logs leaked the credential or its metadata")
	}
}

// keyExpiry reads one row's expires_at straight from the column. An omitted field
// and an explicit JSON null have opposite meanings, and only the stored value can
// tell them apart.
func keyExpiry(t *testing.T, db *sql.DB, secret string) *int64 {
	t.Helper()
	var value sql.NullInt64
	if err := db.QueryRowContext(context.Background(),
		"SELECT expires_at FROM api_keys WHERE api_key = ?", secret).Scan(&value); err != nil {
		t.Fatalf("read expires_at of %q: %v", secret, err)
	}
	if !value.Valid {
		return nil
	}
	out := value.Int64
	return &out
}

// The three expires_at intents are a wire distinction, so this goes through real
// JSON: an absent field must keep the stored expiry and a present null must clear
// it. A decoder that collapses the two lets a status-only batch edit un-expire a
// credential and put a dead secret back into rotation.
func TestProvidersAdmin_BatchExpiryIntentSurvivesTheWire(t *testing.T) {
	env := newProvidersEnv(t, nil)
	id := env.createProvider("poolA", "https://api.pool-a.test")

	const secret = "sk-wire-expiry"
	future := time.Now().Add(time.Hour).UnixMilli()

	w := env.upsertKeys(id, map[string]any{"keys": []map[string]any{
		{"api_key": secret, "expires_at": future},
	}})
	if w.Code != http.StatusOK {
		t.Fatalf("set expiry: status = %d; body = %s", w.Code, w.Body.String())
	}
	if got := keyExpiry(t, env.db, secret); got == nil || *got != future {
		t.Fatalf("expires_at = %v, want %d", got, future)
	}
	if ids := testKeyIDs(t, w); len(ids) != 1 {
		t.Fatalf("key_ids = %v, want a not-yet-expired key in the pool", ids)
	}

	w = env.upsertKeys(id, map[string]any{"keys": []map[string]any{{"api_key": secret}}})
	if w.Code != http.StatusOK {
		t.Fatalf("omit expiry: status = %d; body = %s", w.Code, w.Body.String())
	}
	if resp := env.decode(w); resp["updated"].(float64) != 0 || resp["unchanged"].(float64) != 1 {
		t.Fatalf("omit counters = %v, want the row untouched", resp)
	}
	if got := keyExpiry(t, env.db, secret); got == nil || *got != future {
		t.Fatalf("expires_at = %v, want the stored %d preserved", got, future)
	}

	w = env.upsertKeys(id, map[string]any{"keys": []map[string]any{
		{"api_key": secret, "expires_at": nil},
	}})
	if w.Code != http.StatusOK {
		t.Fatalf("clear expiry: status = %d; body = %s", w.Code, w.Body.String())
	}
	if resp := env.decode(w); resp["updated"].(float64) != 1 {
		t.Fatalf("clear counters = %v, want one update", resp)
	}
	if got := keyExpiry(t, env.db, secret); got != nil {
		t.Fatalf("expires_at = %d, want NULL", *got)
	}

	// The consequence that makes "keep" the safe default: an entry that says nothing
	// about expires_at must leave an expired credential expired. Only the lifetime
	// column is checked here, because the syncer independently rewrites status on
	// every reload it makes for a key whose expiry has passed.
	expired := time.Now().Add(-time.Hour).UnixMilli()
	if w = env.upsertKeys(id, map[string]any{"keys": []map[string]any{
		{"api_key": secret, "expires_at": expired},
	}}); w.Code != http.StatusOK {
		t.Fatalf("expire the key: status = %d; body = %s", w.Code, w.Body.String())
	}
	if w = env.upsertKeys(id, map[string]any{"keys": []map[string]any{{"api_key": secret}}}); w.Code != http.StatusOK {
		t.Fatalf("omit on an expired row: status = %d; body = %s", w.Code, w.Body.String())
	}
	if got := keyExpiry(t, env.db, secret); got == nil || *got != expired {
		t.Fatalf("expires_at = %v, want the stored %d preserved on an expired row", got, expired)
	}
	ids, _ := env.listKeys(id)
	if len(ids) != 1 || ids[0]["expires_at"] != float64(expired) {
		t.Fatalf("listed keys = %v, want the row still carrying its past expiry", ids)
	}
}

// Rotation is the one operator edit that replaces a row's identity material while
// keeping its id, because "<upstream>-key-<id>" refs are derived from that id. The
// bound credential copy has to move with it, or the pool keeps authenticating with
// the retired secret. Responses carry a masked hint of the new value only.
func TestProvidersAdmin_RotateSecretKeepsIdentityAndMasksHint(t *testing.T) {
	env := newProvidersEnv(t, nil)
	id := env.createProvider("poolA", "https://api.pool-a.test")

	const (
		oldSecret = "sk-rotate-old-abcdefgh"
		newSecret = "sk-rotate-new-ijklmnop"
	)
	w := env.upsertKeys(id, map[string]any{"keys": []map[string]any{{"api_key": oldSecret}}})
	if w.Code != http.StatusOK {
		t.Fatalf("upsert: status = %d; body = %s", w.Code, w.Body.String())
	}
	keyID := testKeyIDs(t, w)[0]

	// The name has to satisfy upstreamNameRe, or the snapshot this rotation is
	// supposed to reach can never be compiled.
	upstreamID := env.insertUpstream("pool-a", id)
	env.bindCredential(upstreamID, keyID, fmt.Sprintf("pool-a-key-%d", keyID))
	// A settings-saved pool row carries its own copy of the secret, and the snapshot
	// loader prefers it over the joined api_keys value.
	env.exec("UPDATE upstream_credentials SET secret = ? WHERE api_key_id = ?", oldSecret, keyID)

	w = env.admin(http.MethodPatch, fmt.Sprintf("/api/keys/%d", keyID), map[string]any{"api_key": newSecret})
	if w.Code != http.StatusOK {
		t.Fatalf("rotate: status = %d; body = %s", w.Code, w.Body.String())
	}
	resp := env.decode(w)
	key := resp["key"].(map[string]any)
	if int64(key["id"].(float64)) != keyID {
		t.Fatalf("rotation changed the key id: %d -> %v", keyID, key["id"])
	}
	if key["api_key_hint"] != "sk-...mnop" {
		t.Fatalf("hint = %v, want sk-...mnop (the mask of the new secret)", key["api_key_hint"])
	}
	if reloaded, _ := resp["reloaded"].(bool); !reloaded {
		t.Fatal("rotation did not reload the catalog")
	}
	body := w.Body.String()
	if strings.Contains(body, oldSecret) || strings.Contains(body, newSecret) {
		t.Fatalf("rotation response leaked a secret: %s", body)
	}

	if row := env.keyRow(newSecret); row.ID != keyID {
		t.Fatalf("rotated row id = %d, want %d", row.ID, keyID)
	}
	var bound string
	if err := env.db.QueryRowContext(context.Background(),
		"SELECT secret FROM upstream_credentials WHERE api_key_id = ?", keyID).Scan(&bound); err != nil {
		t.Fatalf("read bound credential: %v", err)
	}
	if bound != newSecret {
		t.Errorf("bound credential secret = %q, want the rotated value the pool serves", bound)
	}
	var retired int
	if err := env.db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM api_keys WHERE api_key = ?", oldSecret).Scan(&retired); err != nil {
		t.Fatalf("count retired key: %v", err)
	}
	if retired != 0 {
		t.Error("the retired secret is still stored as a credential")
	}

	// UNIQUE(api_key) is global, so a secret another row holds cannot be adopted:
	// applying it would merge two credentials into one row.
	otherSecret := "sk-rotate-other-qrstuvwx"
	if w = env.upsertKeys(id, map[string]any{"keys": []map[string]any{{"api_key": otherSecret}}}); w.Code != http.StatusOK {
		t.Fatalf("seed second key: status = %d; body = %s", w.Code, w.Body.String())
	}
	otherID := env.keyRow(otherSecret).ID
	w = env.admin(http.MethodPatch, fmt.Sprintf("/api/keys/%d", otherID), map[string]any{"api_key": newSecret})
	if w.Code != http.StatusConflict {
		t.Fatalf("rotate onto a taken secret: status = %d, want 409; body = %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), newSecret) {
		t.Fatalf("409 leaked the secret: %s", w.Body.String())
	}
	if env.keyRow(otherSecret).ID != otherID {
		t.Error("the rejected rotation moved a row")
	}
	if row := env.keyRow(newSecret); row.ID != keyID {
		t.Error("the rejected rotation overwrote the owning key")
	}

	// A hint this endpoint hands out is not a valid input: accepting it back would
	// store a display string as a routable credential.
	for _, bad := range []string{"sk-...mnop", "[REDACTED]", " sk-padded", strings.Repeat("s", 4097)} {
		w = env.admin(http.MethodPatch, fmt.Sprintf("/api/keys/%d", keyID), map[string]any{"api_key": bad})
		if w.Code != http.StatusBadRequest {
			t.Errorf("api_key %q: status = %d, want 400; body = %s", bad, w.Code, w.Body.String())
		}
	}
	if row := env.keyRow(newSecret); row.ID != keyID {
		t.Error("a rejected rotation changed the stored secret")
	}
	w = env.admin(http.MethodPatch, fmt.Sprintf("/api/keys/%d", keyID), map[string]any{"api_key": ""})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty api_key: status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	w = env.upsertKeys(id, map[string]any{"keys": []map[string]any{{"api_key": "sk-...wxyz"}}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("masked hint in a batch: status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	var placeholders int
	if err := env.db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM api_keys WHERE api_key LIKE 'sk-...%'").Scan(&placeholders); err != nil {
		t.Fatalf("count placeholder rows: %v", err)
	}
	if placeholders != 0 {
		t.Errorf("%d masked placeholder(s) stored as credentials", placeholders)
	}

	for _, secret := range []string{oldSecret, newSecret, otherSecret} {
		if strings.Contains(env.logs.String(), secret) {
			t.Fatalf("logs leaked %q", secret)
		}
	}
}

// A numeric expires_at that is too small to be a millisecond timestamp is a
// malformed intent, not the clear intent. Reading a zero as "clear" would silently
// wipe an expiry another writer set, so the number must reach the validator and
// come back as a 400 with the stored lifetime intact.
func TestProvidersAdmin_PatchRejectsNonPositiveExpiryWithoutClearing(t *testing.T) {
	env := newProvidersEnv(t, nil)
	id := env.createProvider("poolA", "https://api.pool-a.test")

	const secret = "sk-zero-expiry"
	future := time.Now().Add(time.Hour).UnixMilli()
	w := env.upsertKeys(id, map[string]any{"keys": []map[string]any{
		{"api_key": secret, "expires_at": future},
	}})
	if w.Code != http.StatusOK {
		t.Fatalf("seed: status = %d; body = %s", w.Code, w.Body.String())
	}
	keyID := testKeyIDs(t, w)[0]

	for _, bad := range []int64{0, -1} {
		w = env.admin(http.MethodPatch, fmt.Sprintf("/api/keys/%d", keyID), map[string]any{"expires_at": bad})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expires_at %d: status = %d, want 400; body = %s", bad, w.Code, w.Body.String())
		}
		if got := keyExpiry(t, env.db, secret); got == nil || *got != future {
			t.Fatalf("expires_at %d changed the stored lifetime to %v, want %d", bad, got, future)
		}
	}
}

// Go's JSON syntax error quotes the offending byte, and the bodies on these
// surfaces carry credentials: the failure has to name the position instead, so a
// malformed batch neither leaks a secret nor leaves the operator guessing.
func TestProvidersAdmin_MalformedJSONNamesTheOffsetNotTheBody(t *testing.T) {
	env := newProvidersEnv(t, nil)
	id := env.createProvider("poolA", "https://api.pool-a.test")

	const secret = "sk-malformed-zzzzzzzz"
	body := fmt.Sprintf(`{"keys":[{"api_key":%q"},]}`, secret)
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/providers/%d/keys", id), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	env.s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "byte offset") {
		t.Errorf("400 body does not report the byte offset: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), secret) || strings.Contains(w.Body.String(), "sk-") {
		t.Errorf("400 body echoed the payload: %s", w.Body.String())
	}
	if n := env.countProviders(); n != 1 {
		t.Fatalf("rejected body changed the provider count to %d", n)
	}
}

// The legacy /api/turso key surface is read by the dashboard, so it must expose
// hints only: the real secret reaches the data plane through the store's
// provider-bound pooling, never through an HTTP response.
func TestLegacyProviderKeysSurfaceReturnsHintsOnly(t *testing.T) {
	env := newProvidersEnv(t, nil)
	id := env.createProvider("pool-a", "https://api.example.test/v1")
	if w := env.upsertKeys(id, map[string]any{"keys": []map[string]any{
		{"api_key": "sk-legacy-hint-0f9c2b7a4d"},
	}}); w.Code != http.StatusOK {
		t.Fatalf("seed keys: status = %d; body = %s", w.Code, w.Body.String())
	}

	paths := []string{fmt.Sprintf("/api/turso/providers/%d/keys", id), "/api/turso/keys"}
	for _, path := range paths {
		w := env.admin(http.MethodGet, path, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200; body = %s", path, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "sk-legacy-hint-0f9c2b7a4d") {
			t.Fatalf("GET %s leaked the raw secret: %s", path, w.Body.String())
		}
		raw, ok := env.decode(w)["keys"].([]any)
		if !ok || len(raw) != 1 {
			t.Fatalf("GET %s keys = %s, want exactly one entry", path, w.Body.String())
		}
		entry, ok := raw[0].(map[string]any)
		if !ok {
			t.Fatalf("GET %s entry type = %T, want object", path, raw[0])
		}
		if _, exists := entry["api_key"]; exists {
			t.Errorf("GET %s still carries an api_key field: %s", path, w.Body.String())
		}
		if hint, _ := entry["api_key_hint"].(string); hint != "sk-...7a4d" {
			t.Errorf("GET %s api_key_hint = %q, want %q", path, hint, "sk-...7a4d")
		}
		if idFloat, _ := entry["provider_id"].(float64); int64(idFloat) != id {
			t.Errorf("GET %s provider_id = %v, want %d", path, entry["provider_id"], id)
		}
		if status, _ := entry["status"].(string); status != "active" {
			t.Errorf("GET %s status = %q, want active", path, status)
		}
	}
}
