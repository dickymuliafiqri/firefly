package upstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"go.uber.org/goleak"
)

type mockBreakerReporter struct {
	mu      sync.Mutex
	reports map[string][]bool
}

func newMockBreakerReporter() *mockBreakerReporter {
	return &mockBreakerReporter{reports: make(map[string][]bool)}
}

func (m *mockBreakerReporter) Allow(name string) error {
	return nil
}

func (m *mockBreakerReporter) Report(name string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reports[name] = append(m.reports[name], ok)
}

func (m *mockBreakerReporter) getReports(name string) []bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	rep := m.reports[name]
	out := make([]bool, len(rep))
	copy(out, rep)
	return out
}

func TestHealthChecker_ProbeOnce(t *testing.T) {
	defer goleak.VerifyNone(t)

	// Healthy server
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "healthy")
	}))
	defer srv1.Close()

	// Unhealthy 500 server
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "down")
	}))
	defer srv2.Close()

	// 401 server (Layer 1 key issue, but Layer 2 host is UP)
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "unauthorized")
	}))
	defer srv3.Close()

	up1 := &domain.Upstream{Name: "up1", BaseURL: srv1.URL}
	up2 := &domain.Upstream{Name: "up2", BaseURL: srv2.URL}
	up3 := &domain.Upstream{Name: "up3", BaseURL: srv3.URL}
	up4 := &domain.Upstream{Name: "up4", BaseURL: "http://127.0.0.1:1"} // connection refused / dead host

	reporter := newMockBreakerReporter()
	checker := NewHealthCheckerWithStaticUpstreams(
		HealthCheckConfig{
			Timeout:     1 * time.Second,
			Concurrency: 4,
		},
		[]*domain.Upstream{up1, up2, up3, up4},
		nil,
		reporter,
		nil,
	)

	ctx := context.Background()
	if err := checker.ProbeOnce(ctx); err != nil {
		t.Fatalf("ProbeOnce returned error: %v", err)
	}

	// Verify up1 is healthy
	if rep, ok := reporter.reports["up1"]; !ok || len(rep) == 0 || !rep[0] {
		t.Fatalf("expected up1 healthy, got: %v", rep)
	}

	// Verify up2 is unhealthy (500)
	if rep, ok := reporter.reports["up2"]; !ok || len(rep) == 0 || rep[0] {
		t.Fatalf("expected up2 unhealthy, got: %v", rep)
	}

	// Verify up3 is healthy (401 is client/key error, not host down)
	if rep, ok := reporter.reports["up3"]; !ok || len(rep) == 0 || !rep[0] {
		t.Fatalf("expected up3 healthy, got: %v", rep)
	}

	// Verify up4 is unhealthy (connection refused)
	if rep, ok := reporter.reports["up4"]; !ok || len(rep) == 0 || rep[0] {
		t.Fatalf("expected up4 unhealthy, got: %v", rep)
	}
}

func TestHealthChecker_ConcurrencyLimit(t *testing.T) {
	defer goleak.VerifyNone(t)

	var currentInflight atomic.Int32
	var maxInflightObserved atomic.Int32

	const concurrencyLimit = 3
	const numServers = 12

	servers := make([]*httptest.Server, numServers)
	upstreams := make([]*domain.Upstream, numServers)

	for i := 0; i < numServers; i++ {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cur := currentInflight.Add(1)
			for {
				oldMax := maxInflightObserved.Load()
				if cur <= oldMax || maxInflightObserved.CompareAndSwap(oldMax, cur) {
					break
				}
			}
			time.Sleep(30 * time.Millisecond)
			currentInflight.Add(-1)
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		servers[i] = srv
		upstreams[i] = &domain.Upstream{Name: srv.URL, BaseURL: srv.URL}
	}

	checker := NewHealthCheckerWithStaticUpstreams(
		HealthCheckConfig{
			Timeout:     1 * time.Second,
			Concurrency: concurrencyLimit,
		},
		upstreams,
		nil,
		newMockBreakerReporter(),
		nil,
	)

	ctx := context.Background()
	if err := checker.ProbeOnce(ctx); err != nil {
		t.Fatalf("ProbeOnce failed: %v", err)
	}

	observed := maxInflightObserved.Load()
	if observed > concurrencyLimit {
		t.Fatalf("observed max in-flight concurrency %d exceeded limit %d", observed, concurrencyLimit)
	}
	if observed == 0 {
		t.Fatal("observed max in-flight was 0, expected probes to execute")
	}
}

func TestHealthChecker_BreakerTrippingAndRecovery(t *testing.T) {
	defer goleak.VerifyNone(t)

	var return500 atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if return500.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	upstreamName := "test-target"
	up := &domain.Upstream{Name: upstreamName, BaseURL: srv.URL}

	breakers := NewBreakerRegistry(BreakerConfig{
		FailureThreshold: 3,
		SuccessThreshold: 2,
		Cooldown:         50 * time.Millisecond,
	})

	checker := NewHealthCheckerWithStaticUpstreams(
		HealthCheckConfig{
			Timeout:     500 * time.Millisecond,
			Concurrency: 2,
		},
		[]*domain.Upstream{up},
		nil,
		breakers,
		nil,
	)

	ctx := context.Background()

	// 1. Initial probe: healthy
	_ = checker.ProbeOnce(ctx)
	if err := breakers.Allow(upstreamName); err != nil {
		t.Fatalf("expected breaker closed, got: %v", err)
	}

	// 2. Server starts returning 500: trip breaker after FailureThreshold=3
	return500.Store(true)
	for i := 0; i < 3; i++ {
		_ = checker.ProbeOnce(ctx)
	}

	if err := breakers.Allow(upstreamName); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected breaker open, got: %v", err)
	}

	// 3. Wait for cooldown to transition to HalfOpen
	time.Sleep(60 * time.Millisecond)

	// 4. Server recovers: returning 200
	return500.Store(false)

	// In half-open, 2 successes will close the breaker
	for i := 0; i < 2; i++ {
		_ = checker.ProbeOnce(ctx)
	}

	if err := breakers.Allow(upstreamName); err != nil {
		t.Fatalf("expected breaker closed after recovery, got: %v", err)
	}
}

func TestHealthChecker_RunLifecycleAndCleanShutdown(t *testing.T) {
	defer goleak.VerifyNone(t)

	var probeCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probeCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	up := &domain.Upstream{Name: "up", BaseURL: srv.URL}
	checker := NewHealthCheckerWithStaticUpstreams(
		HealthCheckConfig{
			Interval:    10 * time.Millisecond,
			Timeout:     100 * time.Millisecond,
			Concurrency: 2,
		},
		[]*domain.Upstream{up},
		nil,
		newMockBreakerReporter(),
		nil,
	)

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- checker.Run(ctx)
	}()

	// Allow several probe ticks
	time.Sleep(50 * time.Millisecond)

	// Shutdown
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled error, got: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("HealthChecker.Run did not exit on context cancellation")
	}

	if probeCount.Load() == 0 {
		t.Fatal("expected at least 1 probe to execute during Run")
	}
}

func TestHealthChecker_ProbeModel_RevokesKeyOn401(t *testing.T) {
	defer goleak.VerifyNone(t)

	var receivedPath string
	var receivedAuth string
	var receivedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedAuth = r.Header.Get("Authorization")
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"Invalid API key","type":"invalid_request_error"}}`)
	}))
	defer srv.Close()

	slot := &domain.KeySlot{
		Ref:    "k1",
		Secret: "sk-test-secret-401",
	}
	kr := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{slot})

	up := &domain.Upstream{
		Name:       "probe-up-401",
		BaseURL:    srv.URL,
		ProbeModel: "gpt-4o-mini",
		KeyRing:    kr,
	}

	reporter := newMockBreakerReporter()
	checker := NewHealthCheckerWithStaticUpstreams(
		HealthCheckConfig{
			Timeout:     1 * time.Second,
			Concurrency: 2,
		},
		[]*domain.Upstream{up},
		nil,
		reporter,
		nil,
	)

	ctx := context.Background()
	if err := checker.ProbeOnce(ctx); err != nil {
		t.Fatalf("ProbeOnce failed: %v", err)
	}

	if receivedPath != "/v1/chat/completions" {
		t.Errorf("expected path /v1/chat/completions, got %s", receivedPath)
	}
	if receivedAuth != "Bearer sk-test-secret-401" {
		t.Errorf("expected Authorization header Bearer sk-test-secret-401, got %s", receivedAuth)
	}
	expectedBody := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"ping"}],"max_tokens":1}`
	if string(receivedBody) != expectedBody {
		t.Errorf("expected body %s, got %s", expectedBody, string(receivedBody))
	}

	// Layer 1: Slot must be marked revoked
	if !slot.Revoked.Load() {
		t.Fatal("expected key slot to be marked revoked on 401")
	}

	// Layer 2: Host must be reported as healthy (ok=true) to breaker
	reps := reporter.getReports("probe-up-401")
	if len(reps) != 1 || !reps[0] {
		t.Fatalf("expected breaker to report host healthy (true), got %v", reps)
	}
}

func TestHealthChecker_ProbeModel_CooldownOn429(t *testing.T) {
	defer goleak.VerifyNone(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"Rate limit reached"}}`)
	}))
	defer srv.Close()

	slot := &domain.KeySlot{
		Ref:    "k1",
		Secret: "sk-test-secret-429",
	}
	kr := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{slot})

	up := &domain.Upstream{
		Name:       "probe-up-429",
		BaseURL:    srv.URL,
		ProbeModel: "gpt-4o-mini",
		KeyRing:    kr,
	}

	reporter := newMockBreakerReporter()
	checker := NewHealthCheckerWithStaticUpstreams(
		HealthCheckConfig{
			Timeout:     1 * time.Second,
			Concurrency: 2,
		},
		[]*domain.Upstream{up},
		nil,
		reporter,
		nil,
	)

	ctx := context.Background()
	if err := checker.ProbeOnce(ctx); err != nil {
		t.Fatalf("ProbeOnce failed: %v", err)
	}

	// Layer 1: Slot must be placed in cooldown, but NOT revoked
	if slot.Revoked.Load() {
		t.Fatal("key slot must not be marked revoked on 429")
	}
	if !slot.IsInCooldown(time.Now().UnixNano()) {
		t.Fatal("expected key slot to be in cooldown on 429")
	}

	// Layer 2: Host must be reported as healthy (ok=true) to breaker
	reps := reporter.getReports("probe-up-429")
	if len(reps) != 1 || !reps[0] {
		t.Fatalf("expected breaker to report host healthy (true), got %v", reps)
	}
}

func TestHealthChecker_ProbeModel_AnthropicProtocol(t *testing.T) {
	defer goleak.VerifyNone(t)

	var receivedPath string
	var receivedKey string
	var receivedVersion string
	var receivedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedKey = r.Header.Get("x-api-key")
		receivedVersion = r.Header.Get("anthropic-version")
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"content":[{"text":"pong"}]}`)
	}))
	defer srv.Close()

	slot := &domain.KeySlot{
		Ref:    "k_anthropic",
		Secret: "sk-ant-test-key",
	}
	kr := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{slot})

	up := &domain.Upstream{
		Name:       "probe-up-anthropic",
		Protocol:   domain.ProtocolAnthropic,
		BaseURL:    srv.URL,
		ProbeModel: "claude-3-haiku-20240307",
		KeyRing:    kr,
	}

	reporter := newMockBreakerReporter()
	checker := NewHealthCheckerWithStaticUpstreams(
		HealthCheckConfig{
			Timeout:     1 * time.Second,
			Concurrency: 2,
		},
		[]*domain.Upstream{up},
		nil,
		reporter,
		nil,
	)

	ctx := context.Background()
	if err := checker.ProbeOnce(ctx); err != nil {
		t.Fatalf("ProbeOnce failed: %v", err)
	}

	if receivedPath != "/v1/messages" {
		t.Errorf("expected path /v1/messages, got %s", receivedPath)
	}
	if receivedKey != "sk-ant-test-key" {
		t.Errorf("expected x-api-key sk-ant-test-key, got %s", receivedKey)
	}
	if receivedVersion != "2023-06-01" {
		t.Errorf("expected anthropic-version 2023-06-01, got %s", receivedVersion)
	}
	expectedBody := `{"model":"claude-3-haiku-20240307","messages":[{"role":"user","content":"ping"}],"max_tokens":1}`
	if string(receivedBody) != expectedBody {
		t.Errorf("expected body %s, got %s", expectedBody, string(receivedBody))
	}

	if slot.Revoked.Load() {
		t.Fatal("key slot must not be revoked on 200 OK")
	}

	reps := reporter.getReports("probe-up-anthropic")
	if len(reps) != 1 || !reps[0] {
		t.Fatalf("expected breaker to report host healthy (true), got %v", reps)
	}
}

func TestHealthChecker_RotatesKeySlotsAcrossIntervals(t *testing.T) {
	defer goleak.VerifyNone(t)

	var mu sync.Mutex
	var receivedAuths []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedAuths = append(receivedAuths, r.Header.Get("Authorization"))
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"pong"}}]}`)
	}))
	defer srv.Close()

	slot1 := &domain.KeySlot{Ref: "k1", Secret: "sk-probe-1"}
	slot2 := &domain.KeySlot{Ref: "k2", Secret: "sk-probe-2"}
	kr := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{slot1, slot2})

	up := &domain.Upstream{
		Name:       "probe-rotates",
		BaseURL:    srv.URL,
		ProbeModel: "gpt-4o-mini",
		KeyRing:    kr,
	}

	checker := NewHealthCheckerWithStaticUpstreams(
		HealthCheckConfig{
			Timeout:     1 * time.Second,
			Concurrency: 2,
		},
		[]*domain.Upstream{up},
		nil,
		newMockBreakerReporter(),
		nil,
	)

	ctx := context.Background()
	// Run 4 consecutive probes
	for i := 0; i < 4; i++ {
		if err := checker.ProbeOnce(ctx); err != nil {
			t.Fatalf("ProbeOnce probe %d failed: %v", i, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()

	// The ring's starting slot is seeded from a process-wide rotation counter
	// (so rotation survives snapshot rebuilds); the absolute first probed key
	// is therefore not fixed. Assert the invariant under test: consecutive
	// probes rotate strictly across both slots, alternating from whichever key
	// was probed first.
	if len(receivedAuths) != 4 {
		t.Fatalf("expected 4 probes, got %d", len(receivedAuths))
	}
	first := receivedAuths[0]
	if first != "Bearer sk-probe-1" && first != "Bearer sk-probe-2" {
		t.Fatalf("unexpected first probe auth %q", first)
	}
	other := "Bearer sk-probe-2"
	if first == "Bearer sk-probe-2" {
		other = "Bearer sk-probe-1"
	}
	expected := []string{first, other, first, other}
	for i, want := range expected {
		if receivedAuths[i] != want {
			t.Errorf("probe %d: got %s, want %s (alternation from %s)", i, receivedAuths[i], want, first)
		}
	}
}

func TestHealthChecker_OpenCode_FreeResponsesProbe(t *testing.T) {
	defer goleak.VerifyNone(t)

	var receivedPath string
	var receivedAuth string
	var receivedClient string
	var receivedSession string
	var receivedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedAuth = r.Header.Get("Authorization")
		receivedClient = r.Header.Get("x-opencode-client")
		receivedSession = r.Header.Get("x-opencode-session")
		receivedBody, _ = io.ReadAll(r.Body)

		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `data: {"id":"resp_123"}`+"\n\n")
	}))
	defer srv.Close()

	slot := &domain.KeySlot{Ref: "opencode-free-public", Secret: "public"}
	kr := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{slot})

	up := &domain.Upstream{
		Name:       "probe-opencode-responses",
		Protocol:   domain.ProtocolOpenCode,
		BaseURL:    srv.URL + "/v1",
		ProbeModel: "muse-spark-1.3",
		KeyRing:    kr,
	}

	reporter := newMockBreakerReporter()
	checker := NewHealthCheckerWithStaticUpstreams(
		HealthCheckConfig{Timeout: 2 * time.Second},
		[]*domain.Upstream{up},
		nil,
		reporter,
		nil,
	)

	if err := checker.ProbeOnce(context.Background()); err != nil {
		t.Fatalf("ProbeOnce failed: %v", err)
	}

	if receivedPath != "/v1/responses" {
		t.Errorf("expected path /v1/responses, got %s", receivedPath)
	}
	if receivedAuth != "Bearer public" {
		t.Errorf("expected Authorization Bearer public, got %s", receivedAuth)
	}
	if receivedClient != "desktop" {
		t.Errorf("expected x-opencode-client desktop, got %s", receivedClient)
	}
	if !strings.HasPrefix(receivedSession, "ses_") {
		t.Errorf("expected x-opencode-session starting with ses_, got %s", receivedSession)
	}
	if !strings.Contains(string(receivedBody), "muse-spark-1.3") {
		t.Errorf("expected body to contain model muse-spark-1.3, got %s", string(receivedBody))
	}
	if !strings.Contains(string(receivedBody), "You are a title generator") {
		t.Errorf("expected body to contain title prompt, got %s", string(receivedBody))
	}

	if slot.Revoked.Load() {
		t.Fatal("opencode-free-public slot must not be revoked on 200 OK")
	}
	reps := reporter.getReports("probe-opencode-responses")
	if len(reps) != 1 || !reps[0] {
		t.Fatalf("expected breaker to report healthy (true), got %v", reps)
	}
}

func TestHealthChecker_OpenCode_DoesNotRevokePublicKeyOn401(t *testing.T) {
	defer goleak.VerifyNone(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"ModelError","message":"Model not supported"}}`)
	}))
	defer srv.Close()

	slot := &domain.KeySlot{Ref: "opencode-free-public", Secret: "public"}
	kr := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{slot})

	up := &domain.Upstream{
		Name:       "probe-opencode-401",
		Protocol:   domain.ProtocolOpenCode,
		BaseURL:    srv.URL,
		ProbeModel: "muse-spark-1.3",
		KeyRing:    kr,
	}

	reporter := newMockBreakerReporter()
	checker := NewHealthCheckerWithStaticUpstreams(
		HealthCheckConfig{Timeout: 2 * time.Second},
		[]*domain.Upstream{up},
		nil,
		reporter,
		nil,
	)

	if err := checker.ProbeOnce(context.Background()); err != nil {
		t.Fatalf("ProbeOnce failed: %v", err)
	}

	if slot.Revoked.Load() {
		t.Fatal("opencode-free-public MUST NOT be marked revoked on 401")
	}
}

func TestHealthChecker_OpenCode_HostFallbackOnModelFailure(t *testing.T) {
	defer goleak.VerifyNone(t)

	var hitCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitCount.Add(1)
		if r.URL.Path == "/v1/responses" {
			// Simulate model 500 or timeout
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":"model unavailable"}`)
			return
		}
		if r.URL.Path == "/v1/models" {
			// Host itself is alive
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"data":[]}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	slot := &domain.KeySlot{Ref: "opencode-free-public", Secret: "public"}
	kr := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{slot})

	up := &domain.Upstream{
		Name:       "probe-opencode-fallback",
		Protocol:   domain.ProtocolOpenCode,
		BaseURL:    srv.URL + "/v1",
		ProbeModel: "muse-spark-1.3",
		KeyRing:    kr,
	}

	reporter := newMockBreakerReporter()
	checker := NewHealthCheckerWithStaticUpstreams(
		HealthCheckConfig{Timeout: 2 * time.Second},
		[]*domain.Upstream{up},
		nil,
		reporter,
		nil,
	)

	if err := checker.ProbeOnce(context.Background()); err != nil {
		t.Fatalf("ProbeOnce failed: %v", err)
	}

	// Should have hit responses first, then fallen back to models
	if hitCount.Load() < 2 {
		t.Fatalf("expected at least 2 hits (model probe + host fallback), got %d", hitCount.Load())
	}

	// Host breaker must report healthy (true) because host fallback /models succeeded
	reps := reporter.getReports("probe-opencode-fallback")
	if len(reps) != 1 || !reps[0] {
		t.Fatalf("expected host to be reported healthy via fallback, got %v", reps)
	}
}

func TestHealthChecker_OpenCode_HostProbeWithoutModel(t *testing.T) {
	defer goleak.VerifyNone(t)

	var receivedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer srv.Close()

	up := &domain.Upstream{
		Name:     "probe-opencode-host",
		Protocol: domain.ProtocolOpenCode,
		BaseURL:  srv.URL + "/zen/v1",
	}

	reporter := newMockBreakerReporter()
	checker := NewHealthCheckerWithStaticUpstreams(
		HealthCheckConfig{Timeout: 2 * time.Second},
		[]*domain.Upstream{up},
		nil,
		reporter,
		nil,
	)

	if err := checker.ProbeOnce(context.Background()); err != nil {
		t.Fatalf("ProbeOnce failed: %v", err)
	}

	if receivedPath != "/zen/v1/models" {
		t.Errorf("expected path /zen/v1/models, got %s", receivedPath)
	}

	reps := reporter.getReports("probe-opencode-host")
	if len(reps) != 1 || !reps[0] {
		t.Fatalf("expected host reported healthy, got %v", reps)
	}
}

// TestHealthChecker_ThresholdActionOnProbeFailure verifies the probe -> key
// error policy wiring: consecutive 401 probe failures advance the slot's
// ConsecutiveErrors counter, and reaching u.KeyErrorThreshold triggers the
// configured deactivate action plus a ports.KeyActionNotifier notification
// (which in production persists the DB row via the usage flusher).
func TestHealthChecker_ThresholdActionOnProbeFailure(t *testing.T) {
	defer goleak.VerifyNone(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"Invalid API key","type":"invalid_request_error"}}`)
	}))
	defer srv.Close()

	slot := &domain.KeySlot{
		Ref:       "k1",
		Secret:    "sk-test-threshold",
		APIKeyID: 101,
	}
	kr := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{slot})

	up := &domain.Upstream{
		Name:              "probe-threshold",
		BaseURL:           srv.URL,
		ProbeModel:        "gpt-4o-mini",
		KeyRing:           kr,
		KeyErrorThreshold: 3,
		KeyErrorAction:    "deactivate",
	}

	notifier := &mockKeyNotifier{}
	checker := NewHealthCheckerWithStaticUpstreams(
		HealthCheckConfig{
			Timeout:     1 * time.Second,
			Concurrency: 2,
		},
		[]*domain.Upstream{up},
		nil,
		newMockBreakerReporter(),
		notifier,
	)

	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		if err := checker.ProbeOnce(ctx); err != nil {
			t.Fatalf("ProbeOnce #%d failed: %v", i, err)
		}
	}

	if got := slot.ConsecutiveErrors.Load(); got != 3 {
		t.Fatalf("expected consecutive errors to reach 3 after three 401 probes, got %d", got)
	}
	if !slot.Revoked.Load() {
		t.Fatal("expected slot revoked once threshold action executed")
	}

	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.actions) != 1 || notifier.actions[0] != ports.KeyActionDeactivate {
		t.Fatalf("expected exactly one deactivate notification, got %v", notifier.actions)
	}
	if len(notifier.refs) != 1 || notifier.refs[0] != "k1" {
		t.Fatalf("expected notification ref k1, got %v", notifier.refs)
	}
	if len(notifier.ids) != 1 || notifier.ids[0] != 101 {
		t.Fatalf("expected notification key ID 101, got %v", notifier.ids)
	}
	if kr.SlotCount() != 0 {
		t.Fatalf("expected KeyRing to have 0 slots after deactivate threshold action, got %d", kr.SlotCount())
	}
	if kr.SlotByRef("k1") != nil {
		t.Fatal("expected k1 removed from KeyRing")
	}
}

// TestHealthChecker_ThresholdDeleteRemovesKeyAndProbesRemainingKey verifies that
// when a key is deleted by the threshold policy during background probes, it is
// removed from the KeyRing so subsequent probes cleanly select the remaining key.
func TestHealthChecker_ThresholdDeleteRemovesKeyAndProbesRemainingKey(t *testing.T) {
	defer goleak.VerifyNone(t)

	var probedKeys []string
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		mu.Lock()
		probedKeys = append(probedKeys, auth)
		mu.Unlock()

		if auth == "Bearer sk-test-bad-k1" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"Invalid key","type":"invalid_request_error"}}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"pong"}}]}`)
	}))
	defer srv.Close()

	k1 := &domain.KeySlot{Ref: "k1", Secret: "sk-test-bad-k1", APIKeyID: 101}
	k2 := &domain.KeySlot{Ref: "k2", Secret: "sk-test-good-k2", APIKeyID: 102}
	kr := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{k1, k2})

	up := &domain.Upstream{
		Name:              "probe-del-test",
		BaseURL:           srv.URL,
		ProbeModel:        "gpt-4o-mini",
		KeyRing:           kr,
		KeyErrorThreshold: 1,
		KeyErrorAction:    "delete",
	}

	notifier := &mockKeyNotifier{}
	checker := NewHealthCheckerWithStaticUpstreams(
		HealthCheckConfig{Timeout: 1 * time.Second},
		[]*domain.Upstream{up},
		nil,
		newMockBreakerReporter(),
		notifier,
	)

	ctx := context.Background()

	// Force probe 1 to select k1 by temporarily putting k2 in cooldown
	k2.CooldownUntil.Store(time.Now().Add(time.Hour).UnixNano())

	// Probe 1: probes bad key (k1) -> 401 -> threshold reached -> k1 deleted from KeyRing!
	if err := checker.ProbeOnce(ctx); err != nil {
		t.Fatalf("ProbeOnce #1 failed: %v", err)
	}

	if kr.SlotCount() != 1 {
		t.Fatalf("expected 1 slot remaining in KeyRing, got %d", kr.SlotCount())
	}
	if kr.SlotByRef("k1") != nil {
		t.Fatal("k1 must be removed from KeyRing")
	}
	if kr.SlotByRef("k2") != k2 {
		t.Fatal("k2 must remain in KeyRing")
	}

	notifier.mu.Lock()
	if len(notifier.actions) != 1 || notifier.actions[0] != ports.KeyActionDelete {
		t.Fatalf("expected delete notification, got %v", notifier.actions)
	}
	notifier.mu.Unlock()

	// Clear k2 cooldown so probe 2 can probe k2
	k2.CooldownUntil.Store(0)

	// Probe 2: k1 is gone, so checker MUST probe k2 -> 200 OK!
	if err := checker.ProbeOnce(ctx); err != nil {
		t.Fatalf("ProbeOnce #2 failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(probedKeys) != 2 {
		t.Fatalf("expected 2 probes, got %d", len(probedKeys))
	}
	if probedKeys[0] != "Bearer sk-test-bad-k1" {
		t.Errorf("probe 1 key mismatch: %s", probedKeys[0])
	}
	if probedKeys[1] != "Bearer sk-test-good-k2" {
		t.Errorf("probe 2 key mismatch: %s", probedKeys[1])
	}
}

// TestHealthChecker_ProbeSuccessResetsCounter verifies the reset half of the
// invariant: a successful probe zeroes any accumulated consecutive errors and
// never fires a key action notification.
func TestHealthChecker_ProbeSuccessResetsCounter(t *testing.T) {
	defer goleak.VerifyNone(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"pong"}}]}`)
	}))
	defer srv.Close()

	slot := &domain.KeySlot{Ref: "k1", Secret: "sk-test-success", APIKeyID: 101}
	slot.ConsecutiveErrors.Store(2)
	kr := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{slot})

	up := &domain.Upstream{
		Name:              "probe-reset",
		BaseURL:           srv.URL,
		ProbeModel:        "gpt-4o-mini",
		KeyRing:           kr,
		KeyErrorThreshold: 3,
		KeyErrorAction:    "deactivate",
	}

	notifier := &mockKeyNotifier{}
	checker := NewHealthCheckerWithStaticUpstreams(
		HealthCheckConfig{Timeout: 1 * time.Second},
		[]*domain.Upstream{up},
		nil,
		newMockBreakerReporter(),
		notifier,
	)

	if err := checker.ProbeOnce(context.Background()); err != nil {
		t.Fatalf("ProbeOnce failed: %v", err)
	}

	if got := slot.ConsecutiveErrors.Load(); got != 0 {
		t.Fatalf("expected counter reset to 0 on probe success, got %d", got)
	}
	if slot.Revoked.Load() {
		t.Fatal("slot must not be revoked on probe success")
	}
	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.actions) != 0 {
		t.Fatalf("no key action should fire on probe success, got %v", notifier.actions)
	}
}
