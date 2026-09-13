package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
				Name:      "test-openai",
				Protocol:  "openai",
				BaseURLs:  []string{"https://api1.openai.com/v1", "https://api2.openai.com/v1"},
				APIKeys:   []string{"sk-secret-key-1", "sk-secret-key-2"},
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
