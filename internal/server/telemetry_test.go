package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/httpx"
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

	// Case 1: Unauthorized
	req := httptest.NewRequest(http.MethodGet, "/api/telemetry", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want 401", w.Code)
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
