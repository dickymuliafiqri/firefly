package httpx

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/auth"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/limits"
)

type fakeProvider struct{ s *domain.CatalogSnapshot }

func (f fakeProvider) Current() *domain.CatalogSnapshot { return f.s }

func testStore(key string) (*auth.Store, string) {
	hash := auth.HashKey(key)
	snap := domain.NewCatalogSnapshot(1, nil, nil, nil, nil,
		map[string]*domain.Tenant{hash: {KeyHash: hash, Name: "alpha", Status: domain.TenantStatusActive,
			RateLimit: domain.RateLimit{RPS: 100, Burst: 100, MaxConcurrent: 10}}},
		[]string{hash})
	return auth.NewStore(fakeProvider{snap}), hash
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestAuthMiddlewareRejectsMissingKey(t *testing.T) {
	store, _ := testStore("sk-gw-good")
	h := AuthMiddleware(store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be reached")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "authentication_error") {
		t.Fatalf("body missing error type: %s", rec.Body.String())
	}
}

func TestAuthMiddlewareRejectsUnknownKey(t *testing.T) {
	store, _ := testStore("sk-gw-good")
	h := AuthMiddleware(store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be reached")
	}))
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-gw-bad")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAuthMiddlewareInjectsTenant(t *testing.T) {
	store, _ := testStore("sk-gw-good")
	var got *domain.Tenant
	h := AuthMiddleware(store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = TenantFrom(r.Context())
	}))
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-gw-good")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got == nil || got.Name != "alpha" {
		t.Fatalf("tenant not injected: %v", got)
	}
}

func TestRequestIDMiddleware(t *testing.T) {
	var seen string
	h := RequestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestIDFrom(r.Context())
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if seen == "" || rec.Header().Get(HeaderRequestID) != seen {
		t.Fatalf("request id not set/echoed: seen=%q header=%q", seen, rec.Header().Get(HeaderRequestID))
	}

	// Client-provided id is honored.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Header.Set(HeaderRequestID, "client-abc")
	h.ServeHTTP(rec2, req2)
	if rec2.Header().Get(HeaderRequestID) != "client-abc" {
		t.Fatalf("client id not honored: %s", rec2.Header().Get(HeaderRequestID))
	}
}

func TestRecoverMiddleware(t *testing.T) {
	h := RecoverMiddleware(discardLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "internal_error") {
		t.Fatalf("body: %s", rec.Body.String())
	}
}

func TestAdmissionMiddleware429(t *testing.T) {
	store, _ := testStore("sk-gw-good") // tenant alpha: MaxConcurrent 10
	lim := limits.New()

	base := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := Chain(base,
		RequestIDMiddleware,
		RecoverMiddleware(discardLogger()),
		AuthMiddleware(store),
		AdmissionMiddleware(lim),
	)

	// Saturate the tenant's concurrency by holding all 10 slots with the SAME
	// policy the middleware will request (so the channel is shared).
	policy := limits.AcquireInput{TenantName: "alpha", RPS: 100, Burst: 100, MaxConcurrent: 10}
	var releases []limits.ReleaseFunc
	for i := 0; i < 10; i++ {
		rel, err := lim.Acquire(policy)
		if err != nil {
			t.Fatalf("pre-acquire %d: %v", i, err)
		}
		releases = append(releases, rel)
	}
	defer func() {
		for _, rel := range releases {
			rel()
		}
	}()

	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-gw-good")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body=%s)", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After header")
	}
}

func TestStatusRecorderFlushPassthrough(t *testing.T) {
	rec := httptest.NewRecorder() // implements http.Flusher
	sr := &statusRecorder{ResponseWriter: rec, status: 200}
	if _, ok := any(sr).(http.Flusher); !ok {
		t.Fatal("statusRecorder must implement http.Flusher for SSE")
	}
	sr.WriteHeader(200)
	sr.Write([]byte("data: x\n\n"))
	sr.Flush()
	if sr.status != 200 || sr.bytes != int64(len("data: x\n\n")) {
		t.Fatalf("status=%d bytes=%d", sr.status, sr.bytes)
	}
}

// stubMetrics records calls so tests can assert the middleware wiring.
type stubMetrics struct {
	observed []struct {
		method, route string
		status        int
	}
	inflightPeak int
	inflight     int
}

func (s *stubMetrics) ObserveHTTP(method, route string, status int, _ time.Duration) {
	s.observed = append(s.observed, struct {
		method, route string
		status        int
	}{method, route, status})
}
func (s *stubMetrics) IncInflight() {
	s.inflight++
	if s.inflight > s.inflightPeak {
		s.inflightPeak = s.inflight
	}
}
func (s *stubMetrics) DecInflight() { s.inflight-- }

// TestMetricsMiddlewareUsesRoutePattern verifies metrics are labelled by the
// mux's route TEMPLATE, not the raw path, and that in-flight returns to zero.
func TestMetricsMiddlewareUsesRoutePattern(t *testing.T) {
	sm := &stubMetrics{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := MetricsMiddleware(sm)(mux)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models/gpt-4o", nil))

	if len(sm.observed) != 1 {
		t.Fatalf("observed %d requests, want 1", len(sm.observed))
	}
	got := sm.observed[0]
	if got.route != "GET /v1/models/{id}" {
		t.Fatalf("route = %q, want the mux template (not raw path)", got.route)
	}
	if got.status != 200 {
		t.Fatalf("status = %d, want 200", got.status)
	}
	if sm.inflight != 0 || sm.inflightPeak != 1 {
		t.Fatalf("inflight leak: current=%d peak=%d", sm.inflight, sm.inflightPeak)
	}
}

// TestMetricsMiddlewareUnmatchedRoute proves unmatched requests are labelled
// "unmatched" to keep metric cardinality bounded.
func TestMetricsMiddlewareUnmatchedRoute(t *testing.T) {
	sm := &stubMetrics{}
	h := MetricsMiddleware(sm)(http.NewServeMux())
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/does-not-exist/"+strings.Repeat("x", 100), nil))
	if sm.observed[0].route != "unmatched" {
		t.Fatalf("route = %q, want unmatched", sm.observed[0].route)
	}
}

// TestMetricsMiddlewareNilStub ensures a nil metrics implementation still serves.
func TestMetricsMiddlewareNilStub(t *testing.T) {
	var sm *stubMetrics // typed nil satisfies the interface but is not "nil interface"
	_ = sm
	h := MetricsMiddleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

func TestGlobalLimiterAcquireAndRelease(t *testing.T) {
	gl := NewGlobalLimiter(2, 50*time.Millisecond)
	if gl.Capacity() != 2 {
		t.Fatalf("expected capacity 2, got %d", gl.Capacity())
	}
	if gl.Inflight() != 0 {
		t.Fatalf("expected 0 in-flight, got %d", gl.Inflight())
	}

	var reached atomic.Int64
	handler := gl.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		if gl.Inflight() != 1 {
			t.Errorf("expected 1 in-flight during request, got %d", gl.Inflight())
		}
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/models", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if reached.Load() != 1 {
		t.Fatalf("expected handler reached 1, got %d", reached.Load())
	}
	if gl.Inflight() != 0 {
		t.Fatalf("expected 0 in-flight after request, got %d", gl.Inflight())
	}
}

func TestGlobalLimiterBoundedWaitAnd429(t *testing.T) {
	gl := NewGlobalLimiter(1, 60*time.Millisecond)
	block := make(chan struct{})
	done := make(chan struct{})

	handler := gl.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
		w.WriteHeader(http.StatusOK)
	}))

	// Occupy the 1 available slot.
	go func() {
		defer close(done)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/v1/models", nil)
		handler.ServeHTTP(rec, req)
	}()

	// Wait for slot to be occupied.
	time.Sleep(20 * time.Millisecond)
	if gl.Inflight() != 1 {
		t.Fatalf("expected in-flight 1, got %d", gl.Inflight())
	}

	// Second request should wait up to 60ms and fail fast with 429 and Retry-After: 2.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/v1/models", nil)
	start := time.Now()
	handler.ServeHTTP(rec2, req2)
	elapsed := time.Since(start)

	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected status 429, got %d (body: %s)", rec2.Code, rec2.Body.String())
	}
	if rec2.Header().Get("Retry-After") != "2" {
		t.Fatalf("expected Retry-After: 2, got %q", rec2.Header().Get("Retry-After"))
	}
	if elapsed < 50*time.Millisecond {
		t.Fatalf("expected bounded wait >50ms, elapsed %v", elapsed)
	}

	// Release first request.
	close(block)
	<-done
}

func TestGlobalLimiterHealthzBypass(t *testing.T) {
	gl := NewGlobalLimiter(1, 20*time.Millisecond)
	// Saturate the slot manually.
	gl.sem <- struct{}{}
	defer func() { <-gl.sem }()

	handler := gl.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/healthz", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected /healthz to bypass admission limiter, got %d", rec.Code)
	}
}

func TestGlobalLimiterContextCancelWhileWaiting(t *testing.T) {
	gl := NewGlobalLimiter(1, 500*time.Millisecond)
	// Occupy slot.
	gl.sem <- struct{}{}
	defer func() { <-gl.sem }()

	handler := gl.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/v1/models", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	handler.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if elapsed > 200*time.Millisecond {
		t.Fatalf("expected prompt return on context cancel, took %v", elapsed)
	}
}
