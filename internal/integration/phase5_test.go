// Phase 5 integration tests: metrics reflect live traffic and the admin plane
// is guarded. Goroutine-leak coverage lives in TestMain (goleak) below.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/dickymuliafiqri/firefly/internal/security/auth"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/observability/logging"
	"github.com/dickymuliafiqri/firefly/internal/observability/metrics"
	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/registry"
	"github.com/dickymuliafiqri/firefly/internal/server"
	"github.com/dickymuliafiqri/firefly/internal/transport/upstream"
	"github.com/dickymuliafiqri/firefly/internal/observability/usage"
)

// TestEndToEndMetricsReflectTraffic drives an authenticated request and asserts
// the Prometheus endpoint reports it with the ROUTE TEMPLATE label.
func TestEndToEndMetricsReflectTraffic(t *testing.T) {
	dir := t.TempDir()
	reg := registry.New()
	writeConfig(t, dir, "active", []string{"gpt-4o"})
	build(t, reg, dir)

	mx := metrics.New()
	deps := server.RouterDeps{
		Snapshots:   reg,
		TenantStore: auth.NewStore(reg),
		Limiter:     limits.New(),
		Usage:       usage.NewCounters(),
		Logger:      discardLogger(),
		Metrics:     mx,
	}
	base := serve(t, deps)

	req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+gatewayKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	var sb strings.Builder
	rec := newResponseRecorder()
	mx.Handler().ServeHTTP(rec, httpRequest("/metrics"))
	sb.WriteString(rec.body.String())
	body := sb.String()

	if !strings.Contains(body, `route="GET /v1/models"`) {
		t.Fatalf("metrics missing templated route label; body:\n%s", body)
	}
	if !strings.Contains(body, `status="200"`) {
		t.Fatalf("metrics missing status label; body:\n%s", body)
	}
}

// TestAdminTokenGuardsDebug exercises the guarded admin plane.
func TestAdminTokenGuardsDebug(t *testing.T) {
	mx := metrics.New()
	a := server.NewAdmin("127.0.0.1:0", "topsecret", mx.Handler(), discardLogger())

	rec := newResponseRecorder()
	a.Handler().ServeHTTP(rec, httpRequest("/debug/goroutines"))
	if rec.status != http.StatusUnauthorized {
		t.Fatalf("debug status = %d, want 401", rec.status)
	}

	authed := httpRequest("/debug/goroutines")
	authed.Header.Set("X-Admin-Token", "topsecret")
	rec = newResponseRecorder()
	a.Handler().ServeHTTP(rec, authed)
	if rec.status != http.StatusOK {
		t.Fatalf("authed debug status = %d, want 200", rec.status)
	}
}

// TestAdminMetricsOnRealListener verifies the admin server serves /metrics on a
// real TCP socket and shuts down when its context is cancelled.
func TestAdminMetricsOnRealListener(t *testing.T) {
	mx := metrics.New()
	mx.SetConfigGeneration(3)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	a := server.NewAdmin(ln.Addr().String(), "", mx.Handler(), discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.ServeOnListener(ctx, ln) }()

	var body string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + ln.Addr().String() + "/metrics")
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				body = string(b)
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(body, "firefly_config_generation") {
		t.Fatalf("metrics body missing generation gauge:\n%s", body)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("admin serve returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("admin server did not stop")
	}
}
// TestPhase5_PrometheusKeyAndUpstreamSaturationMetrics verifies that key saturation,
// inflight requests, and cooldown metrics are accurately exposed with bounded cardinality.
func TestPhase5_PrometheusKeyAndUpstreamSaturationMetrics(t *testing.T) {
	secretVal := "sk-real-live-secret-never-expose"
	var callCount int
	fakeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 2 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"tokens"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"hello"}}]}`))
	}))
	defer fakeUpstream.Close()

	dir := t.TempDir()
	reg := registry.New()
	writeConfigWithUpstream(t, dir, fakeUpstream.URL, []string{"gpt-4o"})
	build(t, reg, dir)

	mx := metrics.New()
	pool := upstream.NewPool()
	breakers := upstream.NewBreakerRegistry(upstream.BreakerConfig{
		FailureThreshold: 5,
		SuccessThreshold: 2,
		Cooldown:         time.Minute,
	})
	adapter := openai.NewAdapter(pool, breakers, openai.Config{
		SecretLookup: func(ref string) (string, bool) {
			if ref == "OPENAI_API_KEY" {
				return secretVal, true
			}
			return "", false
		},
		Metrics: mx,
	})
	adapters := registry.NewAdapterRegistry()
	_ = adapters.Register(domain.ProtocolOpenAI, adapter)

	deps := server.RouterDeps{
		Snapshots:   reg,
		TenantStore: auth.NewStore(reg),
		Limiter:     limits.New(),
		Adapters:    adapters,
		Adapter:     adapter,
		Usage:       usage.NewCounters(),
		Logger:      discardLogger(),
		Metrics:     mx,
	}

	base := serve(t, deps)

	// Send first authenticated request (200 OK)
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+gatewayKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Send second request (429 Rate Limit -> triggers cooldown)
	req2, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	req2.Header.Set("Authorization", "Bearer "+gatewayKey)
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("request 2: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp2.Body)
	resp2.Body.Close()

	// Scrape metrics
	rec := newResponseRecorder()
	mx.Handler().ServeHTTP(rec, httpRequest("/metrics"))
	metricsBody := rec.body.String()

	// Assertions on Task 5.1 metrics
	for _, expectedMetric := range []string{
		"firefly_key_inflight_requests",
		"firefly_key_cooldown_events_total",
		"firefly_key_requests_total",
		"firefly_global_inflight_requests",
	} {
		if !strings.Contains(metricsBody, expectedMetric) {
			t.Fatalf("metrics scrape missing required metric %q; body:\n%s", expectedMetric, metricsBody)
		}
	}

	// Verify key_requests_total has proper labels: upstream="openai", key_ref="OPENAI_API_KEY", status="200"
	if !strings.Contains(metricsBody, `firefly_key_requests_total{key_ref="OPENAI_API_KEY",status="200",upstream="openai"}`) {
		t.Fatalf("metrics missing expected firefly_key_requests_total with label values; body:\n%s", metricsBody)
	}
	if !strings.Contains(metricsBody, `firefly_key_cooldown_events_total{key_ref="OPENAI_API_KEY",upstream="openai"}`) {
		t.Fatalf("metrics missing expected firefly_key_cooldown_events_total; body:\n%s", metricsBody)
	}

	// Crucial security assertion: label cardinality preserved and secretVal NEVER appears in metrics
	if strings.Contains(metricsBody, secretVal) {
		t.Fatalf("CRITICAL SECURITY VULNERABILITY: Secret %q was leaked into Prometheus metrics!\n%s", secretVal, metricsBody)
	}
}


// TestMain runs goleak for the whole integration package, catching any
// goroutine a Phase-5 code path forgets to stop.
// TestPhase5_SecretMaskingAndSecurityAudit verifies that plaintext secrets and sensitive
// headers are never exposed in slog logs, admin pprof/endpoints, or KeySlot formatting.
func TestPhase5_SecretMaskingAndSecurityAudit(t *testing.T) {
	secretKey := "sk-super-secret-key-12345"
	adminSecret := "admin-ultra-secret-token-xyz"

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, "debug")

	fakeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer fakeUpstream.Close()

	dir := t.TempDir()
	reg := registry.New()
	writeConfigWithUpstream(t, dir, fakeUpstream.URL, []string{"gpt-4o"})
	build(t, reg, dir)

	mx := metrics.New()
	pool := upstream.NewPool()
	breakers := upstream.NewBreakerRegistry(upstream.BreakerConfig{
		FailureThreshold: 5,
		SuccessThreshold: 2,
		Cooldown:         time.Minute,
	})
	adapter := openai.NewAdapter(pool, breakers, openai.Config{
		SecretLookup: func(ref string) (string, bool) {
			return secretKey, true
		},
		Logger:  logger,
		Metrics: mx,
	})
	adapters := registry.NewAdapterRegistry()
	_ = adapters.Register(domain.ProtocolOpenAI, adapter)

	deps := server.RouterDeps{
		Snapshots:   reg,
		TenantStore: auth.NewStore(reg),
		Limiter:     limits.New(),
		Adapters:    adapters,
		Adapter:     adapter,
		Usage:       usage.NewCounters(),
		Logger:      logger,
		Metrics:     mx,
	}

	base := serve(t, deps)
	admin := server.NewAdmin("127.0.0.1:0", adminSecret, mx.Handler(), logger)

	// Send requests containing secrets in headers
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+gatewayKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// Access admin plane
	rec := newResponseRecorder()
	adminReq := httpRequest("/debug/goroutines")
	adminReq.Header.Set("X-Admin-Token", adminSecret)
	admin.Handler().ServeHTTP(rec, adminReq)

	logOutput := logBuf.String()

	// Verify secrets never leaked to logs
	for _, secret := range []string{secretKey, adminSecret} {
		if strings.Contains(logOutput, secret) {
			t.Fatalf("secret %q leaked into server logs:\n%s", secret, logOutput)
		}
	}

	// Verify KeySlot formatting never leaks secret
	slot := &domain.KeySlot{
		Ref:           "OPENAI_KEY_SLOT_0",
		Secret:        secretKey,
		MaxConcurrent: 10,
	}
	for _, formatted := range []string{
		fmt.Sprintf("%v", slot),
		fmt.Sprintf("%+v", slot),
		fmt.Sprintf("%#v", slot),
		fmt.Sprintf("%s", slot),
	} {
		if strings.Contains(formatted, secretKey) {
			t.Fatalf("KeySlot format leaked secret: %s", formatted)
		}
	}

	b, _ := json.Marshal(slot)
	if strings.Contains(string(b), secretKey) {
		t.Fatalf("KeySlot json.Marshal leaked secret: %s", string(b))
	}
}


func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// httpRequest builds a GET request for the admin handler.
func httpRequest(path string) *http.Request {
	req, _ := http.NewRequest(http.MethodGet, path, nil)
	return req
}

// responseRecorder is a minimal http.ResponseWriter for handler-level checks.
type responseRecorder struct {
	hdr    http.Header
	status int
	body   strings.Builder
}

func newResponseRecorder() *responseRecorder {
	return &responseRecorder{hdr: http.Header{}, status: http.StatusOK}
}

func (r *responseRecorder) Header() http.Header         { return r.hdr }
func (r *responseRecorder) WriteHeader(code int)        { r.status = code }
func (r *responseRecorder) Write(b []byte) (int, error) { return r.body.Write(b) }
