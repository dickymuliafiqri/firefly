package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/auth"
	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/openai"
	"github.com/dickymuliafiqri/firefly/internal/registry"
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

func TestSettingsAdminAuth(t *testing.T) {
	reg := registry.New()
	deps := RouterDeps{
		Snapshots:  reg,
		Registry:   reg,
		AdminToken: "super-secret-admin-pass",
	}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	// Unauthorized GET
	req := httptest.NewRequest("GET", "/api/settings", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 without admin token, got %d", w.Code)
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
		Configured bool `json:"configured"`
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
			LocalPath:   filepath.Join(tmpDir, "test.db"),
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
		Configured bool `json:"configured"`
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



