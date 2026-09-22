package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/registry"
	"github.com/dickymuliafiqri/firefly/internal/security/auth"
)

func TestSettingsCORS(t *testing.T) {
	reg := registry.New()
	deps := RouterDeps{
		Snapshots: reg,
		Registry:  reg,
	}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	req := httptest.NewRequest("OPTIONS", "/api/settings", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS status = %d, want 204", w.Code)
	}
	if origin := w.Header().Get("Access-Control-Allow-Origin"); origin != "*" {
		t.Errorf("CORS origin = %q, want *", origin)
	}
	if methods := w.Header().Get("Access-Control-Allow-Methods"); methods == "" {
		t.Errorf("CORS methods empty")
	}
}

func TestSettingsGetAndPost(t *testing.T) {
	tmpDir := t.TempDir()
	reg := registry.New()

	// Initial empty snapshot
	src := config.NewFileConfigSource(tmpDir)
	_, err := reg.BuildAndStore(context.Background(), src, os.LookupEnv)
	if err != nil {
		t.Fatalf("initial build and store failed: %v", err)
	}

	deps := RouterDeps{
		Snapshots:   reg,
		Registry:    reg,
		ConfigDir:   tmpDir,
		TenantStore: auth.NewStore(reg),
		Limiter:     limits.New(),
	}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	// 1. GET /api/settings initially empty
	req := httptest.NewRequest("GET", "/api/settings", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/settings status = %d, want 200", w.Code)
	}
	var initSettings config.SettingsDTO
	if err := json.Unmarshal(w.Body.Bytes(), &initSettings); err != nil {
		t.Fatalf("unmarshal init settings: %v", err)
	}
	if len(initSettings.Upstreams) != 0 || len(initSettings.Models) != 0 || len(initSettings.Tenants) != 0 {
		t.Fatalf("expected empty initial settings, got %+v", initSettings)
	}

	// 2. POST /api/settings with direct API keys and multiple base URLs
	newSettings := config.SettingsDTO{
		Upstreams: []config.UpstreamDTO{
			{
				Name:     "test-openai",
				Protocol: "openai",
				BaseURLs: []string{"https://api1.openai.com/v1", "https://api2.openai.com/v1"},
				APIKeys:  []string{"sk-secret-key-1", "sk-secret-key-2"},
			},
		},
		Models: []config.ModelDTO{
			{
				PublicName:    "gpt-4o",
				Upstream:      "test-openai",
				UpstreamModel: "gpt-4o",
			},
		},
		Tenants: []config.TenantDTO{
			{
				Name:          "frontend-tenant",
				APIKey:        "sk-gw-client-token-12345",
				AllowedModels: []string{"*"},
			},
		},
	}
	body, _ := json.Marshal(newSettings)
	postReq := httptest.NewRequest("POST", "/api/settings", bytes.NewReader(body))
	postReq.Header.Set("Content-Type", "application/json")
	postW := httptest.NewRecorder()
	s.Handler().ServeHTTP(postW, postReq)

	if postW.Code != http.StatusOK {
		t.Fatalf("POST /api/settings status = %d, body = %s", postW.Code, postW.Body.String())
	}

	var postResp map[string]any
	if err := json.Unmarshal(postW.Body.Bytes(), &postResp); err != nil {
		t.Fatalf("unmarshal post resp: %v", err)
	}
	if postResp["status"] != "ok" {
		t.Fatalf("want status ok, got %v", postResp["status"])
	}

	// Verify snapshot was swapped in registry
	snap := reg.Current()
	if snap == nil {
		t.Fatal("registry current snapshot is nil")
	}
	u, ok := snap.Upstream("test-openai")
	if !ok {
		t.Fatal("upstream test-openai not in snapshot")
	}
	if len(u.BaseURLs) != 2 {
		t.Fatalf("want 2 BaseURLs, got %v", u.BaseURLs)
	}
	if u.KeyRing.SlotCount() != 2 {
		t.Fatalf("want 2 key slots, got %d", u.KeyRing.SlotCount())
	}

	// Verify files were written to disk
	if _, err := os.Stat(filepath.Join(tmpDir, config.FileNameUpstreams)); err != nil {
		t.Errorf("upstreams.json was not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, config.FileNameModels)); err != nil {
		t.Errorf("models.json was not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, config.FileNameTenants)); err != nil {
		t.Errorf("tenants.json was not written: %v", err)
	}

	// Credential-bearing files must be owner-only: upstreams.json holds raw
	// provider keys and tenants.json holds tenant gateway keys.
	for _, name := range []string{config.FileNameUpstreams, config.FileNameTenants} {
		info, err := os.Stat(filepath.Join(tmpDir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if got := info.Mode().Perm(); got != config.SecretFileMode {
			t.Errorf("%s mode = %o, want %o", name, got, config.SecretFileMode)
		}
	}

	// 3. GET /api/settings should return settings with masked secrets
	getReq := httptest.NewRequest("GET", "/api/settings", nil)
	getW := httptest.NewRecorder()
	s.Handler().ServeHTTP(getW, getReq)

	if getW.Code != http.StatusOK {
		t.Fatalf("GET /api/settings status = %d", getW.Code)
	}
	var fetched config.SettingsDTO
	if err := json.Unmarshal(getW.Body.Bytes(), &fetched); err != nil {
		t.Fatalf("unmarshal fetched: %v", err)
	}
	if len(fetched.Upstreams) != 1 || len(fetched.Upstreams[0].APIKeys) != 2 {
		t.Fatalf("fetched upstreams mismatch: %+v", fetched.Upstreams)
	}
	for _, k := range fetched.Upstreams[0].APIKeys {
		if k == "sk-secret-key-1" || k == "sk-secret-key-2" {
			t.Errorf("secret was leaked in plain text: %q", k)
		}
	}

	// 4. PUT /api/settings back with masked secrets: secrets should be retained!
	putReq := httptest.NewRequest("PUT", "/api/settings", bytes.NewReader(getW.Body.Bytes()))
	putReq.Header.Set("Content-Type", "application/json")
	putW := httptest.NewRecorder()
	s.Handler().ServeHTTP(putW, putReq)

	if putW.Code != http.StatusOK {
		t.Fatalf("PUT /api/settings status = %d, body = %s", putW.Code, putW.Body.String())
	}

	// Verify the original secrets were preserved in the newly swapped snapshot
	snap2 := reg.Current()
	u2, _ := snap2.Upstream("test-openai")
	if u2.KeyRing.Slots[0].Secret != "sk-secret-key-1" || u2.KeyRing.Slots[1].Secret != "sk-secret-key-2" {
		t.Fatalf("secrets not retained after masked update: %+v", u2.KeyRing.Slots)
	}
}

func TestSettingsUpdateOversizedBody(t *testing.T) {
	tmpDir := t.TempDir()
	reg := registry.New()

	src := config.NewFileConfigSource(tmpDir)
	if _, err := reg.BuildAndStore(context.Background(), src, os.LookupEnv); err != nil {
		t.Fatalf("initial build and store failed: %v", err)
	}

	deps := RouterDeps{
		Snapshots: reg,
		Registry:  reg,
		ConfigDir: tmpDir,
	}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	// Stream one byte past the gateway-wide limit without allocating 32 MiB.
	oversize := io.LimitReader(zeroReader{}, maxRequestBodyBytes+1)
	req := httptest.NewRequest(http.MethodPost, "/api/settings", oversize)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("POST /api/settings oversized body status = %d, want 413; body = %s", w.Code, w.Body.String())
	}
}

func TestSettingsAdminAuth(t *testing.T) {
	reg := registry.New()
	deps := RouterDeps{
		Snapshots:  reg,
		Registry:   reg,
		AdminToken: "super-secret-admin-pass",
	}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	// Unauthenticated GET returns 200 OK with sanitized public view
	req := httptest.NewRequest("GET", "/api/settings", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 without admin token, got %d", w.Code)
	}

	// Unauthorized PUT returns 401
	putReq := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(`{}`))
	putW := httptest.NewRecorder()
	s.Handler().ServeHTTP(putW, putReq)
	if putW.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 for PUT without admin token, got %d", putW.Code)
	}

	// Authorized GET
	reqAuth := httptest.NewRequest("GET", "/api/settings", nil)
	reqAuth.Header.Set("Authorization", "Bearer super-secret-admin-pass")
	wAuth := httptest.NewRecorder()
	s.Handler().ServeHTTP(wAuth, reqAuth)
	if wAuth.Code != http.StatusOK {
		t.Fatalf("want 200 with admin token, got %d", wAuth.Code)
	}
}

func TestRootServeIndexHTML(t *testing.T) {
	reg := registry.New()
	deps := RouterDeps{
		Snapshots: reg,
		Registry:  reg,
	}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !bytes.Contains(w.Body.Bytes(), []byte("Firefly")) {
		t.Fatalf("GET / response does not contain Firefly: %s", body)
	}
}

var _ = openai.WriteError

func TestSettingsPreservesAutoTLSWhenCatalogMutationOmitsIt(t *testing.T) {
	tmpDir := t.TempDir()
	reg := registry.New()
	deps := RouterDeps{Snapshots: reg, Registry: reg, ConfigDir: tmpDir}
	s := New(Config{Addr: "127.0.0.1:0"}, deps, context.Background(), nil)

	initial := config.SettingsDTO{
		AutoTLS: &config.AutoTLSDTO{
			Enabled: true,
			Domain:  "ai.example.com",
			Email:   "ops@example.com",
		},
	}
	body, _ := json.Marshal(initial)
	req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("initial TLS save status = %d, body = %s", w.Code, w.Body.String())
	}

	// Existing catalog pages send only their catalog fields. The absence of
	// auto_tls must preserve, not disable, the administrator's TLS setting.
	legacyBody, _ := json.Marshal(config.SettingsDTO{})
	legacyReq := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(legacyBody))
	legacyW := httptest.NewRecorder()
	s.Handler().ServeHTTP(legacyW, legacyReq)
	if legacyW.Code != http.StatusOK {
		t.Fatalf("legacy catalog save status = %d, body = %s", legacyW.Code, legacyW.Body.String())
	}

	got, err := config.LoadAutoTLS(tmpDir)
	if err != nil {
		t.Fatalf("LoadAutoTLS() error = %v", err)
	}
	if !got.Enabled || got.Domain != "ai.example.com" || got.Email != "ops@example.com" {
		t.Fatalf("TLS configuration was not preserved: %#v", got)
	}
}

func TestTursoProvidersConfiguredFlow(t *testing.T) {
	tmpDir := t.TempDir()
	reg := registry.New()
	deps := RouterDeps{
		Snapshots: reg,
		Registry:  reg,
		ConfigDir: tmpDir,
	}
	s := New(Config{Addr: "127.0.0.1:0"}, deps, context.Background(), nil)

	// Step 1: Query without credentials -> configured: false
	req := httptest.NewRequest(http.MethodGet, "/api/turso/providers", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/turso/providers status = %d, want 200", w.Code)
	}
	var resp struct {
		Configured bool  `json:"configured"`
		Providers  []any `json:"providers"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Configured {
		t.Fatalf("expected configured = false, got true")
	}

	// Step 2: Save Turso credentials via settings
	settingsUpdate := config.SettingsDTO{
		Turso: &config.TursoDTO{
			DatabaseURL: "libsql://test-db.turso.io",
			AuthToken:   "dummy-token",
			LocalPath:   filepath.Join(os.TempDir(), "firefly_turso_test", "test.db"),
		},
	}
	body, _ := json.Marshal(settingsUpdate)
	putReq := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(body))
	putW := httptest.NewRecorder()
	s.Handler().ServeHTTP(putW, putReq)
	if putW.Code != http.StatusOK {
		t.Fatalf("PUT /api/settings status = %d, body = %s", putW.Code, putW.Body.String())
	}

	// Step 3: Query /api/turso/providers again -> configured: true
	req2 := httptest.NewRequest(http.MethodGet, "/api/turso/providers", nil)
	w2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("GET /api/turso/providers status = %d, want 200", w2.Code)
	}
	var resp2 struct {
		Configured bool  `json:"configured"`
		Providers  []any `json:"providers"`
	}
	if err := json.NewDecoder(w2.Body).Decode(&resp2); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp2.Configured {
		t.Fatalf("expected configured = true after saving credentials, got false")
	}

	// Step 4: Query /api/turso/providers/1/keys
	req3 := httptest.NewRequest(http.MethodGet, "/api/turso/providers/1/keys", nil)
	w3 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w3, req3)
	// Even if dummy turso remote is unreachable in test, endpoint responds with 200 or 503
	if w3.Code != http.StatusOK && w3.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/turso/providers/1/keys status = %d, want 200 or 503", w3.Code)
	}
}

func TestSettingsAutoTLSApplyFailureReturns400(t *testing.T) {
	tmpDir := t.TempDir()
	reg := registry.New()

	// An unattached AutoTLS controller (handler == nil) fails Apply when enabled
	unattachedTLS := NewAutoTLS(filepath.Join(tmpDir, "certs"), nil)

	deps := RouterDeps{
		Snapshots: reg,
		Registry:  reg,
		ConfigDir: tmpDir,
		AutoTLS:   unattachedTLS,
	}
	s := New(Config{Addr: "127.0.0.1:0"}, deps, context.Background(), nil)

	settingsUpdate := config.SettingsDTO{
		AutoTLS: &config.AutoTLSDTO{
			Enabled: true,
			Domain:  "ai.example.com",
			Email:   "admin@example.com",
		},
	}
	body, _ := json.Marshal(settingsUpdate)
	req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("PUT /api/settings status = %d, want 400 Bad Request; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "apply auto TLS:") {
		t.Fatalf("expected error body to contain 'apply auto TLS:', got %s", w.Body.String())
	}
}

func TestPublicSanitizedSettingsAndTelemetry(t *testing.T) {
	reg := registry.New()
	adminToken := "secret-admin-token"
	authMgr := auth.NewManager("", adminToken, "pass1234")

	liveLogs := NewLiveLogHub()
	liveLogs.Publish(LiveLog{
		ID:       "req-1",
		Tenant:   "tenant-private-corp",
		KeyRef:   "slot-private-key",
		Model:    "gpt-4o",
		Upstream: "openai",
		Status:   200,
	})

	deps := RouterDeps{
		Snapshots:  reg,
		Registry:   reg,
		AdminToken: adminToken,
		Auth:       authMgr,
		LiveLogs:   liveLogs,
	}
	s := New(Config{Addr: "127.0.0.1:0"}, deps, context.Background(), nil)

	// Populate catalog with upstream and model
	authReq := httptest.NewRequest("POST", "/api/settings", strings.NewReader(`{
		"upstreams": [{
			"name": "openai-primary",
			"protocol": "openai",
			"base_url": "https://api.openai.com/v1",
			"api_key": "sk-secret-123456789"
		}],
		"models": [{
			"public_name": "gpt-4o",
			"upstream": "openai-primary",
			"upstream_model": "gpt-4o"
		}],
		"tenants": [{
			"name": "tenant-corp",
			"api_key": "sk-tenant-secret"
		}]
	}`))
	authReq.Header.Set("Authorization", "Bearer "+adminToken)
	authW := httptest.NewRecorder()
	s.Handler().ServeHTTP(authW, authReq)
	if authW.Code != http.StatusOK {
		t.Fatalf("setup catalog failed: %d, body: %s", authW.Code, authW.Body.String())
	}

	// 1. Guest / Unauthenticated GET /api/settings
	guestReq := httptest.NewRequest("GET", "/api/settings", nil)
	guestW := httptest.NewRecorder()
	s.Handler().ServeHTTP(guestW, guestReq)
	if guestW.Code != http.StatusOK {
		t.Fatalf("guest GET /api/settings = %d, want 200", guestW.Code)
	}
	var guestSettings config.SettingsDTO
	if err := json.Unmarshal(guestW.Body.Bytes(), &guestSettings); err != nil {
		t.Fatalf("unmarshal guest settings: %v", err)
	}
	if len(guestSettings.Upstreams) != 1 || guestSettings.Upstreams[0].Name != "openai-primary" {
		t.Fatalf("expected 1 public upstream, got: %+v", guestSettings.Upstreams)
	}
	if guestSettings.Upstreams[0].APIKey != "" || len(guestSettings.Upstreams[0].CredentialPool) != 0 {
		t.Fatalf("guest upstream leaked credentials: %+v", guestSettings.Upstreams[0])
	}
	if len(guestSettings.Tenants) != 0 {
		t.Fatalf("guest settings leaked tenants: %+v", guestSettings.Tenants)
	}
	if guestSettings.Turso != nil || guestSettings.AutoTLS != nil {
		t.Fatalf("guest settings leaked turso or autotls")
	}

	// 2. Admin GET /api/settings
	adminReq := httptest.NewRequest("GET", "/api/settings", nil)
	adminReq.Header.Set("Authorization", "Bearer "+adminToken)
	adminW := httptest.NewRecorder()
	s.Handler().ServeHTTP(adminW, adminReq)
	if adminW.Code != http.StatusOK {
		t.Fatalf("admin GET /api/settings = %d, want 200", adminW.Code)
	}
	var adminSettings config.SettingsDTO
	if err := json.Unmarshal(adminW.Body.Bytes(), &adminSettings); err != nil {
		t.Fatalf("unmarshal admin settings: %v", err)
	}
	if len(adminSettings.Tenants) != 1 {
		t.Fatalf("admin settings should include tenants: %+v", adminSettings.Tenants)
	}
	if adminSettings.Upstreams[0].APIKey == "" {
		t.Fatalf("admin settings should include masked API key")
	}

	// 3. Guest GET /api/telemetry
	guestTelemReq := httptest.NewRequest("GET", "/api/telemetry", nil)
	guestTelemW := httptest.NewRecorder()
	s.Handler().ServeHTTP(guestTelemW, guestTelemReq)
	if guestTelemW.Code != http.StatusOK {
		t.Fatalf("guest GET /api/telemetry = %d, want 200", guestTelemW.Code)
	}
	var guestTelem TelemetryDTO
	if err := json.Unmarshal(guestTelemW.Body.Bytes(), &guestTelem); err != nil {
		t.Fatalf("unmarshal guest telemetry: %v", err)
	}
	if len(guestTelem.TenantsUsage) != 0 {
		t.Fatalf("guest telemetry leaked tenants usage")
	}
	if len(guestTelem.RecentLogs) > 0 {
		for _, l := range guestTelem.RecentLogs {
			if l.Tenant != "public" || l.KeyRef != "" {
				t.Fatalf("guest telemetry log leaked tenant/keyRef: %+v", l)
			}
		}
	}

	// 4. Guest GET /api/history
	guestHistReq := httptest.NewRequest("GET", "/api/history", nil)
	guestHistW := httptest.NewRecorder()
	s.Handler().ServeHTTP(guestHistW, guestHistReq)
	if guestHistW.Code != http.StatusOK {
		t.Fatalf("guest GET /api/history = %d, want 200", guestHistW.Code)
	}
	var histResp struct {
		History []LiveLog `json:"history"`
	}
	if err := json.Unmarshal(guestHistW.Body.Bytes(), &histResp); err != nil {
		t.Fatalf("unmarshal guest history: %v", err)
	}
	if len(histResp.History) > 0 {
		for _, l := range histResp.History {
			if l.Tenant != "public" || l.KeyRef != "" {
				t.Fatalf("guest history leaked tenant/keyRef: %+v", l)
			}
		}
	}
}

func TestSettingsTenantAPIKeyPlaintext(t *testing.T) {
	tmpDir := t.TempDir()
	reg := registry.New()

	rawKey := "sk-gw-7a1330d1ff033da47fd4afeaf23f6507"
	tenantsJSON := `{"tenants":[{"name":"client-a","api_key":"` + rawKey + `","status":"active"}]}`
	_ = os.WriteFile(filepath.Join(tmpDir, "tenants.json"), []byte(tenantsJSON), 0o644)
	_ = os.WriteFile(filepath.Join(tmpDir, "upstreams.json"), []byte(`{"upstreams":[{"name":"u1","protocol":"openai","base_url":"https://x"}]}`), 0o644)
	_ = os.WriteFile(filepath.Join(tmpDir, "models.json"), []byte(`{"models":[]}`), 0o644)

	src := config.NewFileConfigSource(tmpDir)
	_, err := reg.BuildAndStore(context.Background(), src, os.LookupEnv)
	if err != nil {
		t.Fatalf("initial build and store failed: %v", err)
	}

	deps := RouterDeps{
		Snapshots:   reg,
		Registry:    reg,
		ConfigDir:   tmpDir,
		TenantStore: auth.NewStore(reg),
		Limiter:     limits.New(),
	}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	// 1. GET /api/settings must return unredacted full tenant API key
	getReq := httptest.NewRequest("GET", "/api/settings", nil)
	getW := httptest.NewRecorder()
	s.Handler().ServeHTTP(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("GET /api/settings = %d, want 200", getW.Code)
	}

	var loaded config.SettingsDTO
	if err := json.Unmarshal(getW.Body.Bytes(), &loaded); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}
	if len(loaded.Tenants) == 0 {
		t.Fatalf("expected tenants in settings, got 0")
	}
	if loaded.Tenants[0].APIKey != rawKey {
		t.Fatalf("expected plaintext api_key %q, got %q", rawKey, loaded.Tenants[0].APIKey)
	}

	// 2. PUT /api/settings with masked key placeholder should restore original key
	putPayload := loaded
	putPayload.Tenants[0].APIKey = "sk-...6507"
	putBody, _ := json.Marshal(putPayload)
	putReq := httptest.NewRequest("PUT", "/api/settings", bytes.NewReader(putBody))
	putW := httptest.NewRecorder()
	s.Handler().ServeHTTP(putW, putReq)
	if putW.Code != http.StatusOK {
		t.Fatalf("PUT /api/settings = %d, want 200: %s", putW.Code, putW.Body.String())
	}

	// Verify snapshot still has full plaintext key
	snap := reg.Current()
	tSnap, ok := snap.TenantByKey(rawKey)
	if !ok || tSnap == nil {
		t.Fatalf("expected tenant to still be found by rawKey %q after PUT with masked key", rawKey)
	}
	if tSnap.APIKey != rawKey {
		t.Fatalf("expected tenant.APIKey %q, got %q", rawKey, tSnap.APIKey)
	}

	// The files were seeded world-readable (0644) above; the save must tighten
	// them, since os.WriteFile alone leaves an existing file's mode untouched.
	for _, name := range []string{"upstreams.json", "tenants.json"} {
		info, err := os.Stat(filepath.Join(tmpDir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if got := info.Mode().Perm(); got != config.SecretFileMode {
			t.Errorf("%s mode after save = %o, want %o", name, got, config.SecretFileMode)
		}
	}
}

// TestRestoreMaskedSecretsDropsUnresolvablePoolEntries covers the second half of
// the masked-secret contract: a placeholder that cannot be matched back to a live
// key slot must be removed from the payload, never persisted as a credential.
func TestRestoreMaskedSecretsDropsUnresolvablePoolEntries(t *testing.T) {
	const (
		realA = "sk-live-aaaaaaaaaaaaaaaaaaaa"
		realB = "sk-live-bbbbbbbbbbbbbbbbbbbb"
	)
	// A mask that matches no live slot; mirrors the junk produced by older
	// dashboard saves (maskSecret output stored as if it were a credential).
	const orphanMask = "69Y...IftQ"

	newSnap := func() *domain.CatalogSnapshot {
		ring := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{
			{Ref: "u1-key-11", Secret: realA},
			{Ref: "u1-key-12", Secret: realB},
		})
		return domain.NewCatalogSnapshot(
			1,
			map[string]*domain.Upstream{"u1": {Name: "u1", KeyRing: ring}},
			[]string{"u1"},
			nil, nil, nil, nil,
		)
	}

	t.Run("resolvable masks are restored, not dropped", func(t *testing.T) {
		payload := config.SettingsDTO{Upstreams: []config.UpstreamDTO{{
			Name: "u1",
			CredentialPool: []config.CredentialKeyDTO{
				{Ref: "u1-key-11", Secret: maskSecret(realA), APIKey: maskSecret(realA)},
				{Ref: "u1-key-12", Secret: maskSecret(realB), APIKey: maskSecret(realB)},
			},
		}}}

		restoreMaskedSecrets(&payload, newSnap())

		pool := payload.Upstreams[0].CredentialPool
		if len(pool) != 2 {
			t.Fatalf("want 2 pool entries, got %d: %+v", len(pool), pool)
		}
		if pool[0].Secret != realA || pool[1].Secret != realB {
			t.Fatalf("secrets not restored from live slots: %+v", pool)
		}
	})

	t.Run("orphan mask is dropped from pool and api_keys in lockstep", func(t *testing.T) {
		payload := config.SettingsDTO{Upstreams: []config.UpstreamDTO{{
			Name:    "u1",
			APIKeys: []string{maskSecret(realA), maskSecret(realB), orphanMask},
			CredentialPool: []config.CredentialKeyDTO{
				{Ref: "u1-key-11", Secret: maskSecret(realA)},
				{Ref: "u1-key-12", Secret: maskSecret(realB)},
				{Ref: "u1-key-local-13", Secret: orphanMask},
			},
		}}}

		restoreMaskedSecrets(&payload, newSnap())

		up := payload.Upstreams[0]
		// The pool is longer than the ring, so the orphan has no positional
		// fallback slot either — it survives every restore attempt as a mask.
		if len(up.CredentialPool) != 2 {
			t.Fatalf("want orphan pool entry dropped, got %d entries: %+v", len(up.CredentialPool), up.CredentialPool)
		}
		if up.CredentialPool[0].Secret != realA || up.CredentialPool[1].Secret != realB {
			t.Fatalf("surviving pool entries lost their secrets: %+v", up.CredentialPool)
		}
		if len(up.APIKeys) != 2 {
			t.Fatalf("want api_keys filtered in lockstep, got %d entries: %+v", len(up.APIKeys), up.APIKeys)
		}
		if up.APIKeys[0] != realA || up.APIKeys[1] != realB {
			t.Fatalf("api_keys not aligned with pool after drop: %+v", up.APIKeys)
		}
	})

	t.Run("secret-less entries survive", func(t *testing.T) {
		payload := config.SettingsDTO{Upstreams: []config.UpstreamDTO{{
			Name: "u1",
			CredentialPool: []config.CredentialKeyDTO{
				{Ref: "u1-key-11", Secret: realA},
				{Ref: "oauth:grok-key-1156"},
			},
		}}}

		restoreMaskedSecrets(&payload, newSnap())

		pool := payload.Upstreams[0].CredentialPool
		if len(pool) != 2 {
			t.Fatalf("secret-less entry must not be treated as a mask, got %+v", pool)
		}
		if pool[1].Ref != "oauth:grok-key-1156" {
			t.Fatalf("wrong entry dropped: %+v", pool)
		}
	})

	t.Run("live secret containing dots is not mistaken for a mask", func(t *testing.T) {
		// Opaque tokens from self-hosted gateways may legitimately contain "...".
		const dotted = "on-prem...token-9f3c"
		if !isMasked(dotted) {
			t.Fatalf("precondition: %q should look masked to isMasked", dotted)
		}
		ring := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{
			{Ref: "u2-key-21", Secret: dotted},
		})
		snap := domain.NewCatalogSnapshot(
			1,
			map[string]*domain.Upstream{"u2": {Name: "u2", KeyRing: ring}},
			[]string{"u2"},
			nil, nil, nil, nil,
		)
		payload := config.SettingsDTO{Upstreams: []config.UpstreamDTO{{
			Name: "u2",
			CredentialPool: []config.CredentialKeyDTO{
				{Ref: "u2-key-21", Secret: dotted},
			},
		}}}

		restoreMaskedSecrets(&payload, snap)

		pool := payload.Upstreams[0].CredentialPool
		if len(pool) != 1 || pool[0].Secret != dotted {
			t.Fatalf("live dotted secret must survive the drop filter: %+v", pool)
		}
	})

	t.Run("misaligned non-empty api_keys nulls the pool instead of partial filtering", func(t *testing.T) {
		payload := config.SettingsDTO{Upstreams: []config.UpstreamDTO{{
			Name:    "u1",
			APIKeys: []string{maskSecret(realA), maskSecret(realB)},
			CredentialPool: []config.CredentialKeyDTO{
				{Ref: "u1-key-11", Secret: maskSecret(realA)},
				{Ref: "u1-key-12", Secret: maskSecret(realB)},
				{Ref: "u1-key-local-13", Secret: orphanMask},
			},
		}}}

		restoreMaskedSecrets(&payload, newSnap())

		up := payload.Upstreams[0]
		if up.CredentialPool != nil {
			t.Fatalf("misaligned pool must be discarded wholesale, got %+v", up.CredentialPool)
		}
	})
}
