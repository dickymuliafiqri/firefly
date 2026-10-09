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

	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/pricing"
	"github.com/dickymuliafiqri/firefly/internal/registry"
	"github.com/dickymuliafiqri/firefly/internal/security/auth"
)

// pricingFixtureCatalog is a trimmed models.dev api.json: two providers, one
// model without a cost object (must be skipped by the fetcher).
const pricingFixtureCatalog = `{
  "openai": {
    "id": "openai",
    "name": "OpenAI",
    "models": {
      "gpt-4o": {
        "id": "gpt-4o",
        "name": "GPT-4o",
        "cost": {"input": 2.5, "output": 10, "cache_read": 1.25},
        "limit": {"context": 128000, "output": 16384},
        "canonical_model_id": "openai/gpt-4o"
      },
      "gpt-4o-mini": {
        "id": "gpt-4o-mini",
        "name": "GPT-4o mini",
        "cost": {"input": 0.15, "output": 0.6}
      }
    }
  },
  "google": {
    "id": "google",
    "name": "Google",
    "models": {
      "gemini-2.5-pro": {
        "id": "gemini-2.5-pro",
        "name": "Gemini 2.5 Pro",
        "cost": {"input": 1.25, "output": 10, "cache_read": 0.31, "cache_write": 1.25},
        "limit": {"context": 1048576, "output": 65536}
      },
      "embedding-001": {
        "id": "embedding-001",
        "name": "Embedding 001"
      }
    }
  }
}`

// newPricingTestServer builds a server over an empty config dir with an admin
// token, mirroring the settings tests.
func newPricingTestServer(t *testing.T) (*Server, string) {
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
		AdminToken:  "test-admin-token",
	}
	return New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil), tmpDir
}

// pointCatalogAt redirects the models.dev fetcher at srv for the test's
// duration.
func pointCatalogAt(t *testing.T, url string) {
	t.Helper()
	orig := pricing.CatalogURL
	pricing.CatalogURL = url
	t.Cleanup(func() { pricing.CatalogURL = orig })
}

func serveFixture(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(pricingFixtureCatalog))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func pricingReq(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Authorization", "Bearer test-admin-token")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestPricingImportRequiresAuth(t *testing.T) {
	s, _ := newPricingTestServer(t)
	req := httptest.NewRequest("POST", "/api/pricing/import", bytes.NewReader([]byte(`{"all":true}`)))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestPricingImportRejectsBadPayload(t *testing.T) {
	s, _ := newPricingTestServer(t)
	w := pricingReq(t, s, "POST", "/api/pricing/import", "not json")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestPricingImportRequiresSelection(t *testing.T) {
	s, _ := newPricingTestServer(t)
	w := pricingReq(t, s, "POST", "/api/pricing/import", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestPricingImportAllSeedsAndPersists(t *testing.T) {
	srv := serveFixture(t)
	pointCatalogAt(t, srv.URL)
	s, tmpDir := newPricingTestServer(t)

	w := pricingReq(t, s, "POST", "/api/pricing/import", `{"all":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", w.Code, w.Body.String())
	}
	var resp pricingImportResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Fixture has 3 priced models, all under mapped providers.
	if resp.Added != 3 || resp.Skipped != 0 || resp.Total != 3 {
		t.Fatalf("resp = %+v, want added=3 skipped=0 total=3", resp)
	}

	// The sheet must be on disk as pricing.json.
	raw, err := os.ReadFile(filepath.Join(tmpDir, "pricing.json"))
	if err != nil {
		t.Fatalf("read pricing.json: %v", err)
	}
	var pf config.PricingFile
	if err := json.Unmarshal(raw, &pf); err != nil {
		t.Fatalf("unmarshal pricing.json: %v", err)
	}
	if len(pf.Entries) != 3 {
		t.Fatalf("pricing.json entries = %d, want 3", len(pf.Entries))
	}
	for _, e := range pf.Entries {
		if e.Source != "models.dev" {
			t.Fatalf("entry %q source = %q, want models.dev", e.Model, e.Source)
		}
	}

	// Re-import must be a no-op: seed-once.
	w2 := pricingReq(t, s, "POST", "/api/pricing/import", `{"all":true}`)
	if w2.Code != http.StatusOK {
		t.Fatalf("re-import status = %d, want 200", w2.Code)
	}
	var resp2 pricingImportResponse
	if err := json.Unmarshal(w2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("unmarshal re-import: %v", err)
	}
	if resp2.Added != 0 || resp2.Skipped != 3 {
		t.Fatalf("re-import = %+v, want added=0 skipped=3", resp2)
	}
}

func TestPricingImportByModels(t *testing.T) {
	srv := serveFixture(t)
	pointCatalogAt(t, srv.URL)
	s, _ := newPricingTestServer(t)

	w := pricingReq(t, s, "POST", "/api/pricing/import", `{"models":["openai/gpt-4o"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", w.Code, w.Body.String())
	}
	var resp pricingImportResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Added != 1 || resp.Total != 1 {
		t.Fatalf("resp = %+v, want added=1 total=1", resp)
	}

	// Unknown keys select nothing.
	w2 := pricingReq(t, s, "POST", "/api/pricing/import", `{"models":["openai/nope"]}`)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("unknown-key status = %d, want 400", w2.Code)
	}
}

func TestPricingCatalogBrowse(t *testing.T) {
	srv := serveFixture(t)
	pointCatalogAt(t, srv.URL)
	s, _ := newPricingTestServer(t)

	w := pricingReq(t, s, "GET", "/api/pricing/catalog", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", w.Code, w.Body.String())
	}
	var resp pricingCatalogResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Total != 3 || len(resp.Entries) != 3 {
		t.Fatalf("total = %d, want 3 (cost-less model skipped)", resp.Total)
	}
	if len(resp.Providers) != 2 {
		t.Fatalf("providers = %v, want 2", resp.Providers)
	}

	// Provider filter.
	w2 := pricingReq(t, s, "GET", "/api/pricing/catalog?provider=openai", "")
	var filtered pricingCatalogResponse
	_ = json.Unmarshal(w2.Body.Bytes(), &filtered)
	if filtered.Total != 2 {
		t.Fatalf("provider=openai total = %d, want 2", filtered.Total)
	}

	// Substring query over key and name.
	w3 := pricingReq(t, s, "GET", "/api/pricing/catalog?q=gemini", "")
	var queried pricingCatalogResponse
	_ = json.Unmarshal(w3.Body.Bytes(), &queried)
	if queried.Total != 1 || queried.Entries[0].Key != "google/gemini-2.5-pro" {
		t.Fatalf("q=gemini total = %d, want 1 google/gemini-2.5-pro", queried.Total)
	}
}

func TestPricingCatalogRefresh(t *testing.T) {
	srv := serveFixture(t)
	pointCatalogAt(t, srv.URL)
	s, _ := newPricingTestServer(t)

	w := pricingReq(t, s, "POST", "/api/pricing/catalog/refresh", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", w.Code, w.Body.String())
	}
	var resp pricingCatalogResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 3 {
		t.Fatalf("total = %d, want 3", resp.Total)
	}
}

func TestPricingCatalogUnreachableUpstream(t *testing.T) {
	// A closed port fails fast and must surface as 502 without touching the
	// live price sheet.
	pointCatalogAt(t, "http://127.0.0.1:1")
	s, _ := newPricingTestServer(t)

	w := pricingReq(t, s, "GET", "/api/pricing/catalog", "")
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}

	w2 := pricingReq(t, s, "POST", "/api/pricing/import", `{"all":true}`)
	if w2.Code != http.StatusBadGateway {
		t.Fatalf("import status = %d, want 502", w2.Code)
	}
}

func TestPricingCatalogRequiresAuth(t *testing.T) {
	s, _ := newPricingTestServer(t)
	req := httptest.NewRequest("GET", "/api/pricing/catalog", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}
