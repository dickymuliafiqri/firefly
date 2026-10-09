package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/observability/usage"
	"github.com/dickymuliafiqri/firefly/internal/security/auth"
)

// pricedDeps rebuilds the standard test snapshot with a price sheet attached,
// so the forward path resolves real prices instead of the flat-rate fallback.
func pricedDeps(t *testing.T, adapter *fakeAdapter, entries []domain.PricingEntry) (RouterDeps, *LiveLogHub) {
	t.Helper()
	hash := auth.HashKey(testKey)
	snap := domain.NewCatalogSnapshot(
		1,
		map[string]*domain.Upstream{"u": {Name: "u", Protocol: domain.ProtocolOpenAI, BaseURL: "https://x/v1", CredentialRef: "UP_KEY"}},
		[]string{"u"},
		map[string]*domain.ModelEntry{
			"gpt-4o": {PublicName: "gpt-4o", Upstream: "u", UpstreamModel: "gpt-4o-2024", Enabled: true},
		},
		[]string{"gpt-4o"},
		map[string]*domain.Tenant{hash: {
			KeyHash: hash, Name: "alpha", Status: domain.TenantStatusActive,
			AllowedModels: []string{"gpt-4o"},
			RateLimit:     domain.RateLimit{RPS: 1000, Burst: 1000, MaxConcurrent: 100},
		}},
		[]string{hash},
		domain.WithPricing(domain.NewPricingTable(entries)),
	)
	hub := NewLiveLogHub()
	deps := RouterDeps{
		Snapshots:   fakeProvider{snap},
		TenantStore: auth.NewStore(fakeProvider{snap}),
		Limiter:     limits.New(),
		Adapter:     adapter,
		Usage:       usage.NewCounters(),
		Logger:      discardLogger(),
		LiveLogs:    hub,
	}
	return deps, hub
}

func runPricedRequest(t *testing.T, deps RouterDeps, hub *LiveLogHub) LiveLog {
	t.Helper()
	s := newTestServer(t, deps)
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testKey)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
	logs := hub.Snapshot()
	if len(logs) == 0 {
		t.Fatal("no live log recorded")
	}
	return logs[len(logs)-1]
}

func TestMicrosToUSD(t *testing.T) {
	if got := microsToUSD(4_920_000); got != 4.92 {
		t.Fatalf("microsToUSD(4920000) = %v, want 4.92", got)
	}
	if got := microsToUSD(0); got != 0 {
		t.Fatalf("microsToUSD(0) = %v, want 0", got)
	}
}

func TestBillableInputTokens(t *testing.T) {
	cases := []struct {
		name           string
		prompt         int
		cachedRead     int
		cacheWrite     int
		cachedInPrompt bool
		want           int
	}{
		{"openai shape subtracts cached reads", 1_000_000, 400_000, 0, true, 600_000},
		{"anthropic shape passes through", 600_000, 400_000, 100_000, false, 600_000},
		{"no cached tokens", 1_000_000, 0, 0, true, 1_000_000},
		{"cached exceeds prompt clamps to zero", 100, 400_000, 0, true, 0},
		{"cache write also subtracted on openai shape", 1_000_000, 100_000, 200_000, true, 700_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := billableInputTokens(tc.prompt, tc.cachedRead, tc.cacheWrite, tc.cachedInPrompt)
			if got != tc.want {
				t.Fatalf("billableInputTokens = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestEstimateRequestCost_PricingTable(t *testing.T) {
	entries := []domain.PricingEntry{{
		Model: "gpt-4o", InputMicrosPerM: 3_000_000, OutputMicrosPerM: 15_000_000,
		CacheReadMicrosPerM: 300_000, CacheWriteMicrosPerM: 3_750_000,
	}}
	deps, _ := pricedDeps(t, &fakeAdapter{}, entries)
	snap := deps.Snapshots.Current()

	// 600k uncached @ $3 + 400k cached @ $0.30 + 200k out @ $15 = $4.92.
	got := estimateRequestCost(snap, "gpt-4o", "gpt-4o-2024", 600_000, 200_000, 400_000, 0)
	if want := int64(4_920_000); got != want {
		t.Fatalf("estimateRequestCost = %d, want %d", got, want)
	}
}

func TestEstimateRequestCost_UpstreamModelKey(t *testing.T) {
	// The public name misses; the upstream's private name carries the price.
	entries := []domain.PricingEntry{{Model: "gpt-4o-2024", InputMicrosPerM: 3_000_000}}
	deps, _ := pricedDeps(t, &fakeAdapter{}, entries)
	snap := deps.Snapshots.Current()
	got := estimateRequestCost(snap, "gpt-4o", "gpt-4o-2024", 1_000_000, 0, 0, 0)
	if want := int64(3_000_000); got != want {
		t.Fatalf("estimateRequestCost = %d, want %d", got, want)
	}
}

func TestEstimateRequestCost_FlatRateFallback(t *testing.T) {
	deps, _ := pricedDeps(t, &fakeAdapter{}, nil)
	snap := deps.Snapshots.Current()
	// Legacy flat rate: $0.0000025 in / $0.0000100 out per token.
	// 1M in == $2.50; 500k out == $5.00; total == 7_500_000 micro-USD.
	got := estimateRequestCost(snap, "gpt-4o", "gpt-4o-2024", 1_000_000, 500_000, 0, 0)
	if want := int64(7_500_000); got != want {
		t.Fatalf("estimateRequestCost = %d, want %d", got, want)
	}
}

// TestForwardEndpointEstimatedCostUsesPricingTable drives a real request whose
// upstream reports OpenAI-shaped cached usage and asserts the recorded cost
// matches the price sheet rather than the flat rate.
func TestForwardEndpointEstimatedCostUsesPricingTable(t *testing.T) {
	entries := []domain.PricingEntry{{
		Model: "gpt-4o", InputMicrosPerM: 3_000_000, OutputMicrosPerM: 15_000_000,
		CacheReadMicrosPerM: 300_000, CacheWriteMicrosPerM: 3_750_000,
	}}
	ad := &fakeAdapter{body: `{"id":"x","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1000000,"completion_tokens":200000,"prompt_tokens_details":{"cached_tokens":400000}}}`}
	deps, hub := pricedDeps(t, ad, entries)
	log := runPricedRequest(t, deps, hub)

	if log.CachedReadTokens != 400_000 {
		t.Fatalf("CachedReadTokens = %d, want 400000", log.CachedReadTokens)
	}
	// 600k uncached @ $3 + 400k cached @ $0.30 + 200k out @ $15 = $4.92.
	if log.EstimatedCost != 4.92 {
		t.Fatalf("EstimatedCost = %v, want 4.92", log.EstimatedCost)
	}
}

// TestForwardEndpointEstimatedCostAnthropicShape proves the Anthropic usage
// shape -- where input_tokens already excludes the cache counters -- is not
// double-subtracted.
func TestForwardEndpointEstimatedCostAnthropicShape(t *testing.T) {
	entries := []domain.PricingEntry{{
		Model: "gpt-4o", InputMicrosPerM: 3_000_000, OutputMicrosPerM: 15_000_000,
		CacheReadMicrosPerM: 300_000, CacheWriteMicrosPerM: 3_750_000,
	}}
	ad := &fakeAdapter{body: `{"id":"x","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"input_tokens":600000,"output_tokens":200000,"cache_read_input_tokens":400000,"cache_creation_input_tokens":100000}}`}
	deps, hub := pricedDeps(t, ad, entries)
	log := runPricedRequest(t, deps, hub)

	if log.CachedReadTokens != 400_000 || log.CacheWriteTokens != 100_000 {
		t.Fatalf("cached = %d/%d, want 400000/100000", log.CachedReadTokens, log.CacheWriteTokens)
	}
	// 600k @ $3 + 400k cached @ $0.30 + 100k write @ $3.75 + 200k out @ $15
	// = 1.80 + 0.12 + 0.375 + 3.00 = $5.295.
	if log.EstimatedCost != 5.295 {
		t.Fatalf("EstimatedCost = %v, want 5.295", log.EstimatedCost)
	}
}

func TestForwardEndpointEstimatedCostFlatRateFallback(t *testing.T) {
	ad := &fakeAdapter{body: `{"id":"x","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1000000,"completion_tokens":200000}}`}
	deps, hub := pricedDeps(t, ad, nil)
	log := runPricedRequest(t, deps, hub)

	// No price sheet: the legacy flat rate prices the whole prompt figure.
	if want := 1_000_000*0.0000025 + 200_000*0.0000100; log.EstimatedCost != want {
		t.Fatalf("EstimatedCost = %v, want %v", log.EstimatedCost, want)
	}
}

func TestLiveLogHubCumulativeCostMicros(t *testing.T) {
	hub := NewLiveLogHub()
	hub.Publish(LiveLog{ID: "req-1", Status: 200, TokensIn: 10, TokensOut: 5, EstimatedCost: 1.5})
	hub.Publish(LiveLog{ID: "req-2", Status: 200, TokensIn: 10, TokensOut: 5, EstimatedCost: 0.25})
	// An in-flight log (status 0) must not accumulate.
	hub.Publish(LiveLog{Status: 0, TokensIn: 999, EstimatedCost: 99})
	if got := hub.CumulativeCostMicros(); got != 1_750_000 {
		t.Fatalf("CumulativeCostMicros = %d, want 1750000", got)
	}
}
