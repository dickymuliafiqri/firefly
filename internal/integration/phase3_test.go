package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/adapter/anthropic"
	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/observability/usage"
	"github.com/dickymuliafiqri/firefly/internal/registry"
	"github.com/dickymuliafiqri/firefly/internal/security/auth"
	"github.com/dickymuliafiqri/firefly/internal/server"
	"github.com/dickymuliafiqri/firefly/internal/transport/upstream"
)

// TestPhase3_UpstreamFallbackChain verifies that when the primary upstream's
// circuit breaker is open, the request is transparently served by the fallback upstream.
func TestPhase3_UpstreamFallbackChain(t *testing.T) {
	var primaryCalled atomic.Int64
	var fallbackCalled atomic.Int64

	primarySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalled.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"primary down"}`)
	}))
	t.Cleanup(primarySrv.Close)

	fallbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalled.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"fb-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello from fallback"}}]}`)
	}))
	t.Cleanup(fallbackSrv.Close)

	dir := t.TempDir()
	upstreams := []byte(fmt.Sprintf(`{"upstreams":[
		{"name":"u-primary","base_url":%q,"credential_ref":"KEY_P","protocol":"openai","allow_insecure":true},
		{"name":"u-fallback","base_url":%q,"credential_ref":"KEY_F","protocol":"openai","allow_insecure":true}
	]}`, primarySrv.URL, fallbackSrv.URL))

	models := []byte(`{"models":[
		{"public_name":"gpt-primary","upstream":"u-primary","upstream_model":"gpt-4o","enabled":true},
		{"public_name":"gpt-fallback","upstream":"u-fallback","upstream_model":"gpt-4o","enabled":true}
	]}`)

	combos := []byte(`{"combos":[{
		"name":"gpt-failover",
		"strategy":"failover",
		"models":["gpt-primary","gpt-fallback"],
		"enabled":true
	}]}`)

	tenants := []byte(fmt.Sprintf(`{"tenants":[{
		"key_hash":%q,
		"name":"test-tenant",
		"status":"active",
		"allowed_models":["gpt-failover"],
		"rate_limit":{"rps":100,"burst":100,"max_concurrent":50}
	}]}`, auth.HashKey(gatewayKey)))

	_ = os.WriteFile(filepath.Join(dir, config.FileNameUpstreams), upstreams, 0o600)
	_ = os.WriteFile(filepath.Join(dir, config.FileNameModels), models, 0o600)
	_ = os.WriteFile(filepath.Join(dir, config.FileNameTenants), tenants, 0o600)
	_ = os.WriteFile(filepath.Join(dir, config.FileNameCombos), combos, 0o600)

	reg := registry.New()
	build(t, reg, dir)

	pool := upstream.NewPool()
	breakers := upstream.NewBreakerRegistry(upstream.BreakerConfig{
		FailureThreshold: 1,
		Cooldown:         time.Hour,
	})

	openAIAdapter := openai.NewAdapter(pool, breakers, openai.Config{
		SecretLookup: func(string) (string, bool) { return "secret-key", true },
	})
	adapterReg := registry.NewAdapterRegistry()
	_ = adapterReg.Register(domain.ProtocolOpenAI, openAIAdapter)

	deps := server.RouterDeps{
		Snapshots:   reg,
		TenantStore: auth.NewStore(reg),
		Limiter:     limits.New(),
		Adapters:    adapterReg,
		Breakers:    breakers,
		Usage:       usage.NewCounters(),
		Logger:      discardLogger(),
	}

	base := serve(t, deps)

	// Trip the primary breaker explicitly
	breakers.For("u-primary").Report(false)

	reqBody := `{"model":"gpt-failover","messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer "+gatewayKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200 via fallback; body=%s", resp.StatusCode, string(body))
	}

	var respJSON map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&respJSON); err != nil {
		t.Fatalf("decode: %v", err)
	}
	choices := respJSON["choices"].([]any)
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	if msg["content"] != "hello from fallback" {
		t.Fatalf("expected fallback response, got %v", msg["content"])
	}
	if fallbackCalled.Load() != 1 {
		t.Fatalf("fallback was called %d times, want 1", fallbackCalled.Load())
	}
}

// TestPhase3_MultiKeyRetryLoop verifies that 429 triggers failover to Key 2
// and the client transparently receives the 200 response without error.
func TestPhase3_MultiKeyRetryLoop(t *testing.T) {
	var receivedAuth []string
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		mu.Lock()
		receivedAuth = append(receivedAuth, authHeader)
		mu.Unlock()

		if authHeader == "Bearer secret-key-1" {
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"rate limit exceeded"}}`)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"ok-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello from key 2"}}]}`)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	upstreams := []byte(fmt.Sprintf(`{"upstreams":[{
		"name":"openai-pool",
		"base_url":%q,
		"key_strategy":"round_robin",
		"credential_pool":[
			{"ref":"KEY_1"},
			{"ref":"KEY_2"}
		],
		"protocol":"openai",
		"allow_insecure":true
	}]}`, srv.URL))

	models := []byte(`{"models":[{
		"public_name":"gpt-multi",
		"upstream":"openai-pool",
		"upstream_model":"gpt-4o",
		"enabled":true
	}]}`)

	tenants := []byte(fmt.Sprintf(`{"tenants":[{
		"key_hash":%q,
		"name":"test-tenant",
		"status":"active",
		"allowed_models":["gpt-multi"],
		"rate_limit":{"rps":100,"burst":100,"max_concurrent":50}
	}]}`, auth.HashKey(gatewayKey)))

	_ = os.WriteFile(filepath.Join(dir, config.FileNameUpstreams), upstreams, 0o600)
	_ = os.WriteFile(filepath.Join(dir, config.FileNameModels), models, 0o600)
	_ = os.WriteFile(filepath.Join(dir, config.FileNameTenants), tenants, 0o600)

	reg := registry.New()
	if _, err := reg.BuildAndStore(context.Background(), config.NewFileConfigSource(dir), func(ref string) (string, bool) {
		if ref == "KEY_1" {
			return "secret-key-1", true
		}
		if ref == "KEY_2" {
			return "secret-key-2", true
		}
		return "", false
	}); err != nil {
		t.Fatalf("build: %v", err)
	}

	pool := upstream.NewPool()
	breakers := upstream.NewBreakerRegistry(upstream.BreakerConfig{FailureThreshold: 5})

	openAIAdapter := openai.NewAdapter(pool, breakers, openai.Config{
		SecretLookup: func(ref string) (string, bool) {
			if ref == "KEY_1" {
				return "secret-key-1", true
			}
			if ref == "KEY_2" {
				return "secret-key-2", true
			}
			return "", false
		},
	})
	adapterReg := registry.NewAdapterRegistry()
	_ = adapterReg.Register(domain.ProtocolOpenAI, openAIAdapter)

	deps := server.RouterDeps{
		Snapshots:   reg,
		TenantStore: auth.NewStore(reg),
		Limiter:     limits.New(),
		Adapters:    adapterReg,
		Breakers:    breakers,
		Usage:       usage.NewCounters(),
		Logger:      discardLogger(),
	}

	base := serve(t, deps)

	reqBody := `{"model":"gpt-multi","messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer "+gatewayKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200 after failover; body=%s", resp.StatusCode, string(body))
	}

	mu.Lock()
	defer mu.Unlock()
	if len(receivedAuth) != 2 {
		t.Fatalf("expected 2 attempts, got %d: %v", len(receivedAuth), receivedAuth)
	}
	if receivedAuth[0] != "Bearer secret-key-1" || receivedAuth[1] != "Bearer secret-key-2" {
		t.Fatalf("unexpected auth sequence: %v", receivedAuth)
	}
}

// TestPhase3_AnthropicTransparentTranslation tests that a client sending an OpenAI
// request to an Anthropic-backed model receives an OpenAI-compatible response.
func TestPhase3_AnthropicTransparentTranslation(t *testing.T) {
	var gotAnthropicBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "anth-secret" {
			t.Errorf("expected x-api-key anth-secret, got %q", r.Header.Get("x-api-key"))
		}
		_ = json.NewDecoder(r.Body).Decode(&gotAnthropicBody)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{
			"id": "msg_anth_01",
			"type": "message",
			"role": "assistant",
			"content": [{"type": "text", "text": "Translated response from Claude"}],
			"model": "claude-3-5-sonnet-20241022",
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 12, "output_tokens": 6}
		}`)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	upstreams := []byte(fmt.Sprintf(`{"upstreams":[{
		"name":"anthropic-up",
		"base_url":%q,
		"credential_ref":"ANTH_KEY",
		"protocol":"anthropic",
		"allow_insecure":true
	}]}`, srv.URL))

	models := []byte(`{"models":[{
		"public_name":"claude-3-5",
		"upstream":"anthropic-up",
		"upstream_model":"claude-3-5-sonnet-20241022",
		"enabled":true
	}]}`)

	tenants := []byte(fmt.Sprintf(`{"tenants":[{
		"key_hash":%q,
		"name":"test-tenant",
		"status":"active",
		"allowed_models":["claude-3-5"],
		"rate_limit":{"rps":100,"burst":100,"max_concurrent":50}
	}]}`, auth.HashKey(gatewayKey)))

	_ = os.WriteFile(filepath.Join(dir, config.FileNameUpstreams), upstreams, 0o600)
	_ = os.WriteFile(filepath.Join(dir, config.FileNameModels), models, 0o600)
	_ = os.WriteFile(filepath.Join(dir, config.FileNameTenants), tenants, 0o600)

	reg := registry.New()
	if _, err := reg.BuildAndStore(context.Background(), config.NewFileConfigSource(dir), func(ref string) (string, bool) {
		return "anth-secret", true
	}); err != nil {
		t.Fatalf("build: %v", err)
	}

	pool := upstream.NewPool()
	breakers := upstream.NewBreakerRegistry(upstream.BreakerConfig{FailureThreshold: 5})

	anthropicAdapter := anthropic.NewAdapter(pool, breakers, anthropic.Config{
		SecretLookup: func(string) (string, bool) { return "anth-secret", true },
	})
	adapterReg := registry.NewAdapterRegistry()
	_ = adapterReg.Register(domain.ProtocolAnthropic, anthropicAdapter)

	deps := server.RouterDeps{
		Snapshots:   reg,
		TenantStore: auth.NewStore(reg),
		Limiter:     limits.New(),
		Adapters:    adapterReg,
		Breakers:    breakers,
		Usage:       usage.NewCounters(),
		Logger:      discardLogger(),
	}

	base := serve(t, deps)

	openAIReq := `{"model":"claude-3-5","messages":[{"role":"system","content":"be concise"},{"role":"user","content":"hello"}]}`
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", bytes.NewReader([]byte(openAIReq)))
	req.Header.Set("Authorization", "Bearer "+gatewayKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, string(body))
	}

	var openAIResp map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&openAIResp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	choices := openAIResp["choices"].([]any)
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	if msg["content"] != "Translated response from Claude" {
		t.Fatalf("unexpected content: %v", msg["content"])
	}
	if choices[0].(map[string]any)["finish_reason"] != "stop" {
		t.Fatalf("finish_reason = %v, want stop", choices[0].(map[string]any)["finish_reason"])
	}

	// Verify the Anthropic request was properly shaped
	if gotAnthropicBody["system"] != "be concise" {
		t.Fatalf("anthropic system = %v, want 'be concise'", gotAnthropicBody["system"])
	}
	msgs := gotAnthropicBody["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["role"] != "user" {
		t.Fatalf("anthropic messages = %v", msgs)
	}
}
