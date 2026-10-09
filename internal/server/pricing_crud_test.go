package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/observability/usage"
	"github.com/dickymuliafiqri/firefly/internal/security/auth"
)

// pricedSheetDeps builds a router whose snapshot carries a price sheet plus the
// two models the CRUD tests route. It is distinct from cost_test.go's
// pricedDeps, which is shaped for the forward path.
func pricedSheetDeps(t *testing.T, entries ...domain.PricingEntry) RouterDeps {
	t.Helper()
	hash := auth.HashKey(testKey)
	snap := domain.NewCatalogSnapshot(
		1,
		map[string]*domain.Upstream{"u": {Name: "u", Protocol: domain.ProtocolOpenAI, BaseURL: "https://x/v1", CredentialRef: "UP_KEY"}},
		[]string{"u"},
		map[string]*domain.ModelEntry{
			"gpt-4o":      {PublicName: "gpt-4o", Upstream: "u", UpstreamModel: "gpt-4o", Enabled: true},
			"gpt-4o-mini": {PublicName: "gpt-4o-mini", Upstream: "u", UpstreamModel: "gpt-4o-mini", Enabled: true},
		},
		[]string{"gpt-4o", "gpt-4o-mini"},
		map[string]*domain.Tenant{hash: {
			KeyHash: hash, Name: "alpha", Status: domain.TenantStatusActive,
			AllowedModels: []string{"gpt-4o", "gpt-4o-mini"},
			RateLimit:     domain.RateLimit{RPS: 1000, Burst: 1000, MaxConcurrent: 100},
		}},
		[]string{hash},
		domain.WithPricing(domain.NewPricingTable(entries)),
	)
	return RouterDeps{
		Snapshots:   fakeProvider{snap},
		TenantStore: auth.NewStore(fakeProvider{snap}),
		Limiter:     limits.New(),
		Adapter:     &fakeAdapter{body: `{"ok":true}`},
		Usage:       usage.NewCounters(),
		Logger:      discardLogger(),
	}
}

func adminReq(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer admin-token")
	return req
}

func TestHandlePricingList(t *testing.T) {
	deps := pricedSheetDeps(t,
		domain.PricingEntry{Model: "gpt-4o", InputMicrosPerM: 2_500_000, OutputMicrosPerM: 10_000_000, Source: "manual"},
		domain.PricingEntry{Model: "claude-*", InputMicrosPerM: 3_000_000, OutputMicrosPerM: 15_000_000, Source: "models.dev"},
	)
	deps.AdminToken = "admin-token"

	rec := httptest.NewRecorder()
	deps.handlePricingList(rec, adminReq(http.MethodGet, "/api/pricing", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	var out struct {
		Status  string `json:"status"`
		Total   int    `json:"total"`
		Entries []struct {
			Model           string `json:"model"`
			InputMicrosPerM int64  `json:"input_micros_per_m"`
			Source          string `json:"source"`
			Used            bool   `json:"used"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad payload: %v", err)
	}
	if out.Total != 2 || len(out.Entries) != 2 {
		t.Fatalf("total = %d, entries = %d", out.Total, len(out.Entries))
	}
	// gpt-4o is routed by the catalog; the claude wildcard is not.
	byModel := map[string]bool{}
	for _, e := range out.Entries {
		byModel[e.Model] = e.Used
	}
	if !byModel["gpt-4o"] {
		t.Error("gpt-4o should be marked used")
	}
	if byModel["claude-*"] {
		t.Error("claude-* should not be marked used")
	}
}

func TestHandlePricingList_RequiresAdmin(t *testing.T) {
	deps := pricedSheetDeps(t)
	deps.AdminToken = "admin-token"
	rec := httptest.NewRecorder()
	deps.handlePricingList(rec, httptest.NewRequest(http.MethodGet, "/api/pricing", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandlePricingUpsert_ValidatesKey(t *testing.T) {
	deps := pricedSheetDeps(t)
	deps.AdminToken = "admin-token"

	req := adminReq(http.MethodPut, "/api/pricing/", `{"input_micros_per_m":1,"output_micros_per_m":2}`)
	req.SetPathValue("key", "")
	rec := httptest.NewRecorder()
	deps.handlePricingUpsert(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandlePricingUpsert_RejectsNegativePrices(t *testing.T) {
	deps := pricedSheetDeps(t)
	deps.AdminToken = "admin-token"

	req := adminReq(http.MethodPut, "/api/pricing/gpt-4o", `{"input_micros_per_m":-1,"output_micros_per_m":2}`)
	req.SetPathValue("key", "gpt-4o")
	rec := httptest.NewRecorder()
	deps.handlePricingUpsert(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandlePricingResolve(t *testing.T) {
	deps := pricedSheetDeps(t,
		domain.PricingEntry{Model: "gpt-4o", InputMicrosPerM: 2_500_000, OutputMicrosPerM: 10_000_000, Source: "manual"},
		domain.PricingEntry{Model: "claude-*", InputMicrosPerM: 3_000_000, OutputMicrosPerM: 15_000_000, Source: "models.dev"},
	)
	deps.AdminToken = "admin-token"

	body := `{"models":[
		{"public_name":"gpt-4o","upstream":"u","upstream_model":"gpt-4o"},
		{"public_name":"claude-sonnet-4","upstream":"u","upstream_model":"claude-sonnet-4-20250514"},
		{"public_name":"unpriced-model","upstream":"u","upstream_model":"unpriced-model"}
	]}`

	rec := httptest.NewRecorder()
	deps.handlePricingResolve(rec, adminReq(http.MethodPost, "/api/pricing/resolve", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	var out struct {
		Status  string `json:"status"`
		Entries []struct {
			PublicName string `json:"public_name"`
			MatchedKey string `json:"matched_key"`
			Entry      *struct {
				InputMicrosPerM int64 `json:"input_micros_per_m"`
			} `json:"entry"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad payload: %v", err)
	}
	if len(out.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(out.Entries))
	}
	if out.Entries[0].Entry == nil || out.Entries[0].MatchedKey != "gpt-4o" {
		t.Fatalf("exact match failed: %+v", out.Entries[0])
	}
	if out.Entries[0].Entry.InputMicrosPerM != 2_500_000 {
		t.Fatalf("price wrong: %+v", out.Entries[0].Entry)
	}
	if out.Entries[1].Entry == nil || out.Entries[1].Entry.InputMicrosPerM != 3_000_000 {
		t.Fatalf("wildcard match failed: %+v", out.Entries[1])
	}
	if out.Entries[2].Entry != nil {
		t.Fatalf("unpriced model must resolve to null: %+v", out.Entries[2])
	}
}

func TestHandlePricingResolve_FallsBackToUpstreamModel(t *testing.T) {
	// A provider may price the private id and not the public alias, so the
	// upstream model name is the second lookup key.
	deps := pricedSheetDeps(t,
		domain.PricingEntry{Model: "gpt-4o-2024-08-06", InputMicrosPerM: 2_500_000, OutputMicrosPerM: 10_000_000, Source: "models.dev"},
	)
	deps.AdminToken = "admin-token"

	body := `{"models":[{"public_name":"my-alias","upstream":"u","upstream_model":"gpt-4o-2024-08-06"}]}`
	rec := httptest.NewRecorder()
	deps.handlePricingResolve(rec, adminReq(http.MethodPost, "/api/pricing/resolve", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "gpt-4o-2024-08-06") {
		t.Fatalf("upstream model fallback failed: %s", rec.Body.String())
	}
}

func TestHandlePricingDelete_UnknownKeyIs500(t *testing.T) {
	deps := pricedSheetDeps(t,
		domain.PricingEntry{Model: "gpt-4o", InputMicrosPerM: 1, OutputMicrosPerM: 2, Source: "manual"},
	)
	deps.AdminToken = "admin-token"

	req := adminReq(http.MethodDelete, "/api/pricing/nope", "")
	req.SetPathValue("key", "nope")
	rec := httptest.NewRecorder()
	deps.handlePricingDelete(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestUsedModelKeys(t *testing.T) {
	deps, _ := testDeps()
	snap := deps.currentSnapshot()
	used := usedModelKeys(snap)
	if !used["gpt-4o"] {
		t.Error("public name should be marked used")
	}
	if !used["gpt-4o-mini"] {
		t.Error("second public name should be marked used")
	}
	if used["nonexistent"] {
		t.Error("unknown model must not be marked used")
	}
}

func TestDomainPricingEntryDTO(t *testing.T) {
	e := domain.PricingEntry{
		Model:                "m",
		InputMicrosPerM:      1,
		OutputMicrosPerM:     2,
		CacheReadMicrosPerM:  3,
		CacheWriteMicrosPerM: 4,
		Source:               "manual",
		CanonicalModelID:     "openai/m",
	}
	got := domainPricingEntryDTO(e)
	if got.Model != "m" || got.InputMicrosPerM != 1 || got.OutputMicrosPerM != 2 ||
		got.CacheReadMicrosPerM != 3 || got.CacheWriteMicrosPerM != 4 ||
		got.Source != "manual" || got.CanonicalModelID != "openai/m" {
		t.Fatalf("projection lost fields: %+v", got)
	}
}
