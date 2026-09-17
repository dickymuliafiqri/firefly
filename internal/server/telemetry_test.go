package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/auth"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/httpx"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/metrics"
	"github.com/dickymuliafiqri/firefly/internal/usage"
)

func TestTelemetry_CORS(t *testing.T) {
	deps := RouterDeps{}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	req := httptest.NewRequest(http.MethodOptions, "/api/telemetry", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS status = %d, want 204", w.Code)
	}
	if origin := w.Header().Get("Access-Control-Allow-Origin"); origin != "*" {
		t.Errorf("CORS origin = %q, want *", origin)
	}
}

func TestTelemetry_Get(t *testing.T) {
	mx := metrics.New()
	mx.ObserveHTTP("POST", "/v1/chat/completions", 200, 25*time.Millisecond)
	mx.ObserveHTTP("POST", "/v1/chat/completions", 500, 100*time.Millisecond)
	mx.IncInflight()

	counters := usage.NewCounters()
	counters.Record("KEY_A", "demo", "gpt-4o", 5)

	lim := httpx.NewGlobalLimiter(1500, 1500*time.Millisecond)

	deps := RouterDeps{
		Metrics:       mx,
		Usage:         counters,
		GlobalLimiter: lim,
	}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	req := httptest.NewRequest(http.MethodGet, "/api/telemetry", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/telemetry status = %d, want 200", w.Code)
	}

	var res TelemetryDTO
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal telemetry response: %v", err)
	}

	if res.Summary.TotalRequests != 2 {
		t.Errorf("TotalRequests = %d, want 2", res.Summary.TotalRequests)
	}
	if res.Summary.TotalErrors != 1 {
		t.Errorf("TotalErrors = %d, want 1", res.Summary.TotalErrors)
	}
	if res.Summary.ActiveStreams < 1 {
		t.Errorf("ActiveStreams = %d, want >= 1", res.Summary.ActiveStreams)
	}
	if res.GlobalAdmission.Capacity != 1500 {
		t.Errorf("GlobalAdmission.Capacity = %d, want 1500", res.GlobalAdmission.Capacity)
	}
	if len(res.TenantsUsage) == 0 {
		t.Errorf("TenantsUsage expected non-empty")
	} else if res.TenantsUsage[0].TotalRequests != 5 {
		t.Errorf("TenantsUsage[0].TotalRequests = %d, want 5", res.TenantsUsage[0].TotalRequests)
	}

	// Verify healthz and management APIs do NOT increment AI model telemetry requests
	s.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	s.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/settings", nil))

	w2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/api/telemetry", nil))
	var res2 TelemetryDTO
	if err := json.Unmarshal(w2.Body.Bytes(), &res2); err != nil {
		t.Fatalf("unmarshal second telemetry response: %v", err)
	}
	if res2.Summary.TotalRequests != 2 {
		t.Errorf("TotalRequests after healthz/settings = %d, want 2 (only AI requests)", res2.Summary.TotalRequests)
	}
}

func TestTelemetry_AuthGuarded(t *testing.T) {
	deps := RouterDeps{
		AdminToken: "secret-token",
	}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	// Case 1: Unauthenticated GET returns 200 OK with sanitized public telemetry
	req := httptest.NewRequest(http.MethodGet, "/api/telemetry", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unauthenticated status = %d, want 200", w.Code)
	}

	// Case 2: Authorized
	req = httptest.NewRequest(http.MethodGet, "/api/telemetry", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("authorized status = %d, want 200", w.Code)
	}
}

// TestTelemetry_PerSlotCounters verifies the per-key telemetry values shown on the
// Telemetry KeyRing card really count: in-flight (from the domain KeySlot), 429s
// (ObserveKeyCooldown), and total requests (ObserveKeyRequest) are joined to the
// correct slot by "<upstream>/<ref>" and surfaced in the DTO.
func TestTelemetry_PerSlotCounters(t *testing.T) {
	mx := metrics.New()

	// Two keys on one upstream.
	slotA := &domain.KeySlot{Ref: "up-key-a", Secret: "sk-a"}
	slotB := &domain.KeySlot{Ref: "up-key-b", Secret: "sk-b"}
	// In-flight is the live domain atomic the handler reads directly.
	slotA.Inflight.Store(2)

	// Requests + cooldowns recorded exactly as the forward path does.
	mx.ObserveKeyRequest("up", "up-key-a", 200)
	mx.ObserveKeyRequest("up", "up-key-a", 200)
	mx.ObserveKeyRequest("up", "up-key-a", 429)
	mx.ObserveKeyCooldown("up", "up-key-a") // one 429 strike
	mx.ObserveKeyRequest("up", "up-key-b", 200)

	up := &domain.Upstream{
		Name:     "up",
		Protocol: domain.ProtocolOpenAI,
		BaseURL:  "https://example.com/v1",
		KeyRing:  domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{slotA, slotB}),
	}
	snap := domain.NewCatalogSnapshot(
		1,
		map[string]*domain.Upstream{"up": up},
		[]string{"up"},
		map[string]*domain.ModelEntry{},
		nil,
		map[string]*domain.Tenant{},
		nil,
	)

	deps := RouterDeps{
		Metrics:   mx,
		Usage:     usage.NewCounters(),
		Snapshots: fakeProvider{snap},
	}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/telemetry", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("telemetry status = %d, want 200", w.Code)
	}

	var res TelemetryDTO
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	var found *UpstreamTelemetryDTO
	for i := range res.Upstreams {
		if res.Upstreams[i].Name == "up" {
			found = &res.Upstreams[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("upstream 'up' missing from telemetry: %+v", res.Upstreams)
	}
	if len(found.Slots) != 2 {
		t.Fatalf("slots = %d, want 2", len(found.Slots))
	}

	slots := map[string]KeySlotTelemetryDTO{}
	for _, s := range found.Slots {
		slots[s.Ref] = s
	}

	a, ok := slots["up-key-a"]
	if !ok {
		t.Fatalf("slot up-key-a missing")
	}
	if a.Inflight != 2 {
		t.Errorf("up-key-a inflight = %d, want 2", a.Inflight)
	}
	if a.RequestsTotal != 3 {
		t.Errorf("up-key-a requests_total = %d, want 3 (2x200 + 1x429)", a.RequestsTotal)
	}
	if a.TotalCooldownEvents != 1 {
		t.Errorf("up-key-a total_cooldown_events = %d, want 1", a.TotalCooldownEvents)
	}

	b, ok := slots["up-key-b"]
	if !ok {
		t.Fatalf("slot up-key-b missing")
	}
	if b.Inflight != 0 {
		t.Errorf("up-key-b inflight = %d, want 0", b.Inflight)
	}
	if b.RequestsTotal != 1 {
		t.Errorf("up-key-b requests_total = %d, want 1", b.RequestsTotal)
	}
	if b.TotalCooldownEvents != 0 {
		t.Errorf("up-key-b total_cooldown_events = %d, want 0", b.TotalCooldownEvents)
	}

	// Upstream total requests is the sum across its slots (3 + 1).
	if found.TotalRequests != 4 {
		t.Errorf("upstream total_requests = %d, want 4", found.TotalRequests)
	}
}

// TestTelemetry_PerSlotCountersEndToEnd drives a real chat request through the
// full handler (routing → key selection → forward → recordLog/ObserveKeyRequest)
// against a fake adapter, then reads /api/telemetry and asserts the selected
// key slot's request counter actually incremented and in-flight settled to 0.
func TestTelemetry_PerSlotCountersEndToEnd(t *testing.T) {
	hash := auth.HashKey(testKey)
	mx := metrics.New()

	slot := &domain.KeySlot{Ref: "u-key-1", Secret: "sk-1"}
	up := &domain.Upstream{
		Name:     "u",
		Protocol: domain.ProtocolOpenAI,
		BaseURL:  "https://x/v1",
		KeyRing:  domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{slot}),
	}
	snap := domain.NewCatalogSnapshot(
		1,
		map[string]*domain.Upstream{"u": up},
		[]string{"u"},
		map[string]*domain.ModelEntry{
			"gpt-4o": {PublicName: "gpt-4o", Upstream: "u", UpstreamModel: "gpt-4o", Enabled: true},
		},
		[]string{"gpt-4o"},
		map[string]*domain.Tenant{hash: {
			KeyHash: hash, Name: "alpha", Status: domain.TenantStatusActive,
			AllowedModels: []string{"gpt-4o"},
			RateLimit:     domain.RateLimit{RPS: 1000, Burst: 1000, MaxConcurrent: 100},
		}},
		[]string{hash},
	)

	deps := RouterDeps{
		Snapshots:   fakeProvider{snap},
		TenantStore: auth.NewStore(fakeProvider{snap}),
		Limiter:     limits.New(),
		Adapter:     &fakeAdapter{body: `{"ok":true}`},
		Usage:       usage.NewCounters(),
		Metrics:     mx,
		Logger:      discardLogger(),
	}
	s := New(Config{Addr: "127.0.0.1:0"}, deps, context.Background(), discardLogger())

	// Drive two real inference requests through the gateway.
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Authorization", "Bearer "+testKey)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("chat request %d status = %d body=%s", i, rec.Code, rec.Body.String())
		}
	}

	// Read telemetry and confirm the slot recorded 2 requests and settled in-flight.
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/telemetry", nil))
	var res TelemetryDTO
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	var slotDTO *KeySlotTelemetryDTO
	for i := range res.Upstreams {
		if res.Upstreams[i].Name != "u" {
			continue
		}
		for j := range res.Upstreams[i].Slots {
			if res.Upstreams[i].Slots[j].Ref == "u-key-1" {
				slotDTO = &res.Upstreams[i].Slots[j]
			}
		}
	}
	if slotDTO == nil {
		t.Fatalf("slot u-key-1 missing from telemetry: %+v", res.Upstreams)
	}
	if slotDTO.RequestsTotal != 2 {
		t.Errorf("requests_total = %d, want 2 (two forwarded requests really counted)", slotDTO.RequestsTotal)
	}
	if slotDTO.Inflight != 0 {
		t.Errorf("inflight = %d, want 0 after requests completed", slotDTO.Inflight)
	}
}
