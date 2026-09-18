package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/security/auth"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/transport/httpx"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/dickymuliafiqri/firefly/internal/registry"
	"github.com/dickymuliafiqri/firefly/internal/observability/usage"

	"go.uber.org/goleak"
)

type fakeProvider struct{ s *domain.CatalogSnapshot }

func (f fakeProvider) Current() *domain.CatalogSnapshot { return f.s }

// fakeAdapter records the last forwarded request and writes a canned response,
// so server-level tests can assert routing without a real upstream.
type fakeAdapter struct {
	lastTarget *domain.Target
	lastReq    ports.ForwardRequest
	status     int
	body       string
	err        error
}

func (f *fakeAdapter) Protocol() domain.Protocol { return domain.ProtocolOpenAI }

func (f *fakeAdapter) Forward(_ context.Context, t *domain.Target, req ports.ForwardRequest, w io.Writer) error {
	f.lastTarget = t
	f.lastReq = req
	if f.err != nil {
		return f.err
	}
	status := f.status
	if status == 0 {
		status = http.StatusOK
	}
	if rw, ok := w.(http.ResponseWriter); ok {
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(status)
	}
	_, _ = io.WriteString(w, f.body)
	return nil
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

const testKey = "sk-gw-good"

func testDeps() (RouterDeps, *domain.CatalogSnapshot) {
	deps, snap := testDepsWithAdapter(&fakeAdapter{body: `{"ok":true}`})
	return deps, snap
}

func testDepsWithAdapter(adapter ports.UpstreamAdapter) (RouterDeps, *domain.CatalogSnapshot) {
	hash := auth.HashKey(testKey)
	snap := domain.NewCatalogSnapshot(
		1,
		map[string]*domain.Upstream{"u": {Name: "u", Protocol: domain.ProtocolOpenAI, BaseURL: "https://x/v1", CredentialRef: "UP_KEY"}},
		[]string{"u"},
		map[string]*domain.ModelEntry{
			"gpt-4o":      {PublicName: "gpt-4o", Upstream: "u", UpstreamModel: "gpt-4o", Enabled: true},
			"gpt-4o-mini": {PublicName: "gpt-4o-mini", Upstream: "u", UpstreamModel: "gpt-4o-mini", Enabled: true},
			"secret":      {PublicName: "secret", Upstream: "u", UpstreamModel: "s", Enabled: true},
		},
		[]string{"gpt-4o", "gpt-4o-mini", "secret"},
		map[string]*domain.Tenant{hash: {
			KeyHash: hash, Name: "alpha", Status: domain.TenantStatusActive,
			AllowedModels: []string{"gpt-4o", "gpt-4o-mini"},
			RateLimit:     domain.RateLimit{RPS: 1000, Burst: 1000, MaxConcurrent: 100},
		}},
		[]string{hash},
	)
	store := auth.NewStore(fakeProvider{snap})
	return RouterDeps{
		Snapshots:   fakeProvider{snap},
		TenantStore: store,
		Limiter:     limits.New(),
		Adapter:     adapter,
		Usage:       usage.NewCounters(),
		Logger:      discardLogger(),
	}, snap
}

func newTestServer(t *testing.T, deps RouterDeps) *Server {
	t.Helper()
	return New(Config{Addr: "127.0.0.1:0"}, deps, context.Background(), discardLogger())
}

// ptrProvider is a *ptrProvider used to construct a typed-nil SnapshotProvider
// (an interface holding a nil pointer, which is NOT == nil).
type ptrProvider struct{ s *domain.CatalogSnapshot }

func (p *ptrProvider) Current() *domain.CatalogSnapshot { return p.s }

// TestTypedNilSnapshotProviderIs503 proves a mis-wired (typed-nil) snapshot
// provider fails closed with 503 instead of panicking on first request. This is
// the typed-nil-interface hazard: `deps.Snapshots != nil` is true here.
func TestTypedNilSnapshotProviderIs503(t *testing.T) {
	deps, _ := testDeps()
	var nilProvider *ptrProvider // typed nil
	deps.Snapshots = nilProvider // interface != nil
	s := newTestServer(t, deps)

	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s, want 503", rec.Code, rec.Body.String())
	}
}

// TestTypedNilTenantStoreIs401 proves a mis-wired (typed-nil) tenant store
// yields a clean 401 rather than a panic.
func TestTypedNilTenantStoreIs401(t *testing.T) {
	deps, _ := testDeps()
	var nilStore *auth.Store // typed nil; *auth.Store satisfies ports.TenantStore
	deps.TenantStore = nilStore
	s := newTestServer(t, deps)

	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s, want 401", rec.Code, rec.Body.String())
	}
}

// TestNilLimiterStillServes proves a missing limiter (nil *limits.Limiter) does
// not panic the data path — it degrades to "unlimited".
func TestNilLimiterStillServes(t *testing.T) {
	deps, _ := testDeps()
	deps.Limiter = nil
	s := newTestServer(t, deps)

	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
}

func TestHealthz(t *testing.T) {
	deps, _ := testDeps()
	s := newTestServer(t, deps)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 || rec.Body.String() != "ok" {
		t.Fatalf("healthz = %d %q", rec.Code, rec.Body.String())
	}
}

func TestListModelsFiltersByTenant(t *testing.T) {
	deps, _ := testDeps()
	s := newTestServer(t, deps)
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var got ModelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ids := map[string]bool{}
	for _, m := range got.Data {
		ids[m.ID] = true
	}
	if !ids["gpt-4o"] || !ids["gpt-4o-mini"] {
		t.Fatalf("allowed models missing: %v", ids)
	}
	if ids["secret"] {
		t.Fatal("tenant must NOT see models outside allowed_models")
	}
}

func TestEndpointsRequireAuth(t *testing.T) {
	deps, _ := testDeps()
	s := newTestServer(t, deps)
	for _, p := range []string{"/v1/chat/completions", "/v1/completions", "/v1/embeddings"} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", p, strings.NewReader("{}")))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s without auth = %d, want 401", p, rec.Code)
		}
	}
}

func TestChatCompletionsRoutesToUpstream(t *testing.T) {
	fa := &fakeAdapter{body: `{"id":"x","object":"chat.completion"}`}
	deps, _ := testDepsWithAdapter(fa)
	s := newTestServer(t, deps)

	body := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"stream":true}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testKey)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if fa.lastTarget == nil || fa.lastTarget.UpstreamModel != "gpt-4o-mini" {
		t.Fatalf("target = %+v", fa.lastTarget)
	}
	if fa.lastTarget.CredentialRef != "UP_KEY" {
		t.Fatalf("credential ref = %q, want UP_KEY", fa.lastTarget.CredentialRef)
	}
	if fa.lastReq.Path != "/chat/completions" {
		t.Fatalf("upstream path = %q", fa.lastReq.Path)
	}
	if !fa.lastReq.Stream {
		t.Fatal("stream flag not propagated to adapter")
	}
}

func TestChatCompletionsMissingModelIs400(t *testing.T) {
	deps, _ := testDeps()
	s := newTestServer(t, deps)
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+testKey)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid_request_error") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestChatCompletionsMalformedJSONIs400(t *testing.T) {
	deps, _ := testDeps()
	s := newTestServer(t, deps)
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{not json`))
	req.Header.Set("Authorization", "Bearer "+testKey)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestUnknownModelIs404(t *testing.T) {
	deps, _ := testDeps()
	s := newTestServer(t, deps)
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"nope"}`))
	req.Header.Set("Authorization", "Bearer "+testKey)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestForbiddenModelIs403(t *testing.T) {
	deps, _ := testDeps()
	s := newTestServer(t, deps)
	// "secret" exists and is enabled but is not in alpha's allowed_models.
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"secret"}`))
	req.Header.Set("Authorization", "Bearer "+testKey)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestRetrieveModelHidesForbidden(t *testing.T) {
	deps, _ := testDeps()
	s := newTestServer(t, deps)

	okReq := httptest.NewRequest("GET", "/v1/models/gpt-4o", nil)
	okReq.Header.Set("Authorization", "Bearer "+testKey)
	okRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(okRec, okReq)
	if okRec.Code != http.StatusOK {
		t.Fatalf("allowed model = %d, want 200", okRec.Code)
	}

	// "secret" is enabled but forbidden: must look like 404, not 403.
	noReq := httptest.NewRequest("GET", "/v1/models/secret", nil)
	noReq.Header.Set("Authorization", "Bearer "+testKey)
	noRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(noRec, noReq)
	if noRec.Code != http.StatusNotFound {
		t.Fatalf("forbidden model = %d, want 404", noRec.Code)
	}
}

func TestUpstreamFailureMapsTo502(t *testing.T) {
	fa := &fakeAdapter{err: &openai.ErrUpstream{Status: 500}}
	deps, _ := testDepsWithAdapter(fa)
	s := newTestServer(t, deps)
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o"}`))
	req.Header.Set("Authorization", "Bearer "+testKey)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCircuitOpenMapsTo503(t *testing.T) {
	fa := &fakeAdapter{err: &openai.ErrUpstream{Status: http.StatusServiceUnavailable, Cause: errors.New("open")}}
	deps, _ := testDepsWithAdapter(fa)
	s := newTestServer(t, deps)
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o"}`))
	req.Header.Set("Authorization", "Bearer "+testKey)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestDisabledUpstreamMapsTo503(t *testing.T) {
	deps, snap := testDeps()
	// Disable upstream "u" in snapshot
	if u, ok := snap.Upstream("u"); ok {
		u.Disabled = true
	}
	s := newTestServer(t, deps)
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o"}`))
	req.Header.Set("Authorization", "Bearer "+testKey)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
}

func TestGracefulShutdown(t *testing.T) {
	defer goleak.VerifyNone(t)

	deps, _ := testDeps()
	ctx, cancel := context.WithCancel(context.Background())
	s := New(Config{Addr: "127.0.0.1:0", ShutdownGrace: 2 * time.Second}, deps, ctx, discardLogger())

	// Use a listener we control so we know when it's ready.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.ServeOnListener(ln) }()

	// Hit healthz to prove it's up.
	addr := "http://" + ln.Addr().String()
	resp, err := http.Get(addr + "/healthz")
	if err != nil {
		t.Fatalf("get healthz: %v", err)
	}
	resp.Body.Close()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not shut down")
	}
}

func TestAdapterRegistryRouting(t *testing.T) {
	hash := auth.HashKey(testKey)
	snap := domain.NewCatalogSnapshot(
		1,
		map[string]*domain.Upstream{
			"u-openai":    {Name: "u-openai", Protocol: domain.ProtocolOpenAI, BaseURL: "https://o/v1", CredentialRef: "O_KEY"},
			"u-anthropic": {Name: "u-anthropic", Protocol: domain.Protocol("anthropic"), BaseURL: "https://a/v1", CredentialRef: "A_KEY"},
		},
		[]string{"u-openai", "u-anthropic"},
		map[string]*domain.ModelEntry{
			"gpt-4o": {PublicName: "gpt-4o", Upstream: "u-openai", UpstreamModel: "gpt-4o", Enabled: true},
			"claude": {PublicName: "claude", Upstream: "u-anthropic", UpstreamModel: "claude-3-5-sonnet", Enabled: true},
		},
		[]string{"gpt-4o", "claude"},
		map[string]*domain.Tenant{hash: {
			KeyHash: hash, Name: "alpha", Status: domain.TenantStatusActive,
			AllowedModels: []string{"gpt-4o", "claude"},
			RateLimit:     domain.RateLimit{RPS: 1000, Burst: 1000, MaxConcurrent: 100},
		}},
		[]string{hash},
	)

	openAIAdapter := &fakeAdapter{body: `{"provider":"openai"}`}
	anthropicAdapter := &fakeAdapter{body: `{"provider":"anthropic"}`}

	reg := registry.NewAdapterRegistry()
	_ = reg.Register(domain.ProtocolOpenAI, openAIAdapter)
	_ = reg.Register(domain.Protocol("anthropic"), anthropicAdapter)

	deps := RouterDeps{
		Snapshots:   fakeProvider{snap},
		TenantStore: auth.NewStore(fakeProvider{snap}),
		Limiter:     limits.New(),
		Adapters:    reg,
		Usage:       usage.NewCounters(),
		Logger:      discardLogger(),
	}
	s := newTestServer(t, deps)

	// Call openai model
	req1 := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o"}`))
	req1.Header.Set("Authorization", "Bearer "+testKey)
	rec1 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK || !strings.Contains(rec1.Body.String(), "openai") {
		t.Fatalf("openai route: code=%d body=%s", rec1.Code, rec1.Body.String())
	}
	if openAIAdapter.lastTarget == nil || openAIAdapter.lastTarget.Upstream.Name != "u-openai" {
		t.Fatalf("expected call to u-openai, got %v", openAIAdapter.lastTarget)
	}

	// Call anthropic model
	req2 := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"claude"}`))
	req2.Header.Set("Authorization", "Bearer "+testKey)
	rec2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), "anthropic") {
		t.Fatalf("anthropic route: code=%d body=%s", rec2.Code, rec2.Body.String())
	}
	if anthropicAdapter.lastTarget == nil || anthropicAdapter.lastTarget.Upstream.Name != "u-anthropic" {
		t.Fatalf("expected call to u-anthropic, got %v", anthropicAdapter.lastTarget)
	}
}

type mockBreakerLookup struct {
	openUpstreams map[string]bool
}

func (m *mockBreakerLookup) Allow(name string) error {
	if m.openUpstreams[name] {
		return errors.New("breaker open")
	}
	return nil
}

func (m *mockBreakerLookup) Report(name string, ok bool) {}

func TestForwardFallbackChain(t *testing.T) {
	hash := auth.HashKey(testKey)
	snap := domain.NewCatalogSnapshot(
		1,
		map[string]*domain.Upstream{
			"u-primary":  {Name: "u-primary", Protocol: domain.ProtocolOpenAI, BaseURL: "https://p/v1", CredentialRef: "P_KEY"},
			"u-fallback": {Name: "u-fallback", Protocol: domain.ProtocolOpenAI, BaseURL: "https://f/v1", CredentialRef: "F_KEY"},
		},
		[]string{"u-primary", "u-fallback"},
		map[string]*domain.ModelEntry{
			"gpt-4o-primary": {
				PublicName:    "gpt-4o-primary",
				Upstream:      "u-primary",
				UpstreamModel: "gpt-4o",
				Enabled:       true,
			},
			"gpt-4o-fallback": {
				PublicName:    "gpt-4o-fallback",
				Upstream:      "u-fallback",
				UpstreamModel: "gpt-4o",
				Enabled:       true,
			},
		},
		[]string{"gpt-4o-primary", "gpt-4o-fallback"},
		map[string]*domain.Tenant{hash: {
			KeyHash: hash, Name: "alpha", Status: domain.TenantStatusActive,
			AllowedModels: []string{"gpt-4o"},
			RateLimit:     domain.RateLimit{RPS: 1000, Burst: 1000, MaxConcurrent: 100},
		}},
		[]string{hash},
		domain.WithCombos(
			map[string]*domain.Combo{
				"gpt-4o": {
					Name:     "gpt-4o",
					Strategy: domain.RoutingStrategyFailover,
					Models:   []string{"gpt-4o-primary", "gpt-4o-fallback"},
					Enabled:  true,
				},
			},
			[]string{"gpt-4o"},
		),
	)

	fakeAd := &fakeAdapter{body: `{"id":"test","object":"chat.completion"}`}
	breakers := &mockBreakerLookup{
		openUpstreams: map[string]bool{"u-primary": true},
	}

	var logBuf strings.Builder
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	deps := RouterDeps{
		Snapshots:   fakeProvider{snap},
		TenantStore: auth.NewStore(fakeProvider{snap}),
		Limiter:     limits.New(),
		Adapter:     fakeAd,
		Breakers:    breakers,
		Usage:       usage.NewCounters(),
		Logger:      logger,
	}
	s := newTestServer(t, deps)

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o"}`))
	req.Header.Set("Authorization", "Bearer "+testKey)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 via fallback; body=%s", rec.Code, rec.Body.String())
	}
	if fakeAd.lastTarget == nil || fakeAd.lastTarget.Upstream.Name != "u-fallback" {
		t.Fatalf("expected call to u-fallback, got %+v", fakeAd.lastTarget)
	}

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "upstream fallback triggered") {
		t.Fatalf("missing fallback warning in log: %s", logOutput)
	}
	if !strings.Contains(logOutput, "from_upstream=u-primary") {
		t.Fatalf("missing from_upstream in log: %s", logOutput)
	}
	if !strings.Contains(logOutput, "to_upstream=u-fallback") {
		t.Fatalf("missing to_upstream in log: %s", logOutput)
	}
	if !strings.Contains(logOutput, "request_id=") {
		t.Fatalf("missing request_id in log: %s", logOutput)
	}
}

func TestGlobalAdmissionMiddlewareServer429(t *testing.T) {
	block := make(chan struct{})
	unblockOnce := sync.Once{}
	defer unblockOnce.Do(func() { close(block) })

	slowAdapter := &blockingAdapter{block: block}
	deps, _ := testDepsWithAdapter(slowAdapter)
	deps.GlobalLimiter = httpx.NewGlobalLimiter(1, 50*time.Millisecond)

	s := newTestServer(t, deps)

	// Launch in-flight request to occupy the single slot.
	req1Done := make(chan struct{})
	go func() {
		defer close(req1Done)
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o"}`))
		req.Header.Set("Authorization", "Bearer "+testKey)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("req1 status = %d, want 200", rec.Code)
		}
	}()

	// Wait for slot to be claimed.
	time.Sleep(20 * time.Millisecond)

	// Second request should wait up to 50ms and fail with 429.
	req2 := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o"}`))
	req2.Header.Set("Authorization", "Bearer "+testKey)
	rec2 := httptest.NewRecorder()
	start := time.Now()
	s.Handler().ServeHTTP(rec2, req2)
	elapsed := time.Since(start)

	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("req2 status = %d, want 429; body = %s", rec2.Code, rec2.Body.String())
	}
	if rec2.Header().Get("Retry-After") != "2" {
		t.Fatalf("expected Retry-After: 2, got %q", rec2.Header().Get("Retry-After"))
	}
	if elapsed < 40*time.Millisecond {
		t.Fatalf("expected bounded wait >40ms, got %v", elapsed)
	}

	// Release first request.
	unblockOnce.Do(func() { close(block) })
	<-req1Done

	// Third request should now succeed.
	req3 := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o"}`))
	req3.Header.Set("Authorization", "Bearer "+testKey)
	rec3 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("req3 status = %d, want 200 after slot released", rec3.Code)
	}
}

type blockingAdapter struct {
	block chan struct{}
}

func (b *blockingAdapter) Protocol() domain.Protocol { return domain.ProtocolOpenAI }
func (b *blockingAdapter) Forward(ctx context.Context, _ *domain.Target, _ ports.ForwardRequest, w io.Writer) error {
	select {
	case <-b.block:
	case <-ctx.Done():
		return ctx.Err()
	}
	if rw, ok := w.(http.ResponseWriter); ok {
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusOK)
	}
	_, _ = io.WriteString(w, `{"id":"ok","choices":[{"message":{"role":"assistant","content":"done"}}]}`)
	return nil
}


func BenchmarkForwardEndpointMemory(b *testing.B) {
	fakeAd := &fakeAdapter{body: `{"id":"ok","object":"chat.completion","choices":[{"message":{"content":"hello"}}]}`}
	deps, _ := testDepsWithAdapter(fakeAd)
	deps.Limiter = nil
	deps.DisableGlobalAdmission = true
	s := newTestServer(&testing.T{}, deps)
	handler := s.Handler()

	bodyBytes := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"say hello world"}]}`)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(bodyBytes))
		req.Header.Set("Authorization", "Bearer "+testKey)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status = %d", rec.Code)
		}
	}
}


