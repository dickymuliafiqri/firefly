package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/adapter/opencode"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/security/oauth"
)

func TestUpstreamCheck_CORS(t *testing.T) {
	deps := RouterDeps{}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	req := httptest.NewRequest(http.MethodOptions, "/api/upstreams/check", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS status = %d, want 204", w.Code)
	}
	if origin := w.Header().Get("Access-Control-Allow-Origin"); origin != "*" {
		t.Errorf("CORS origin = %q, want *", origin)
	}
}

func TestUpstreamCheck_Validation(t *testing.T) {
	deps := RouterDeps{}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	// Case 1: Empty BaseURL
	payload := UpstreamCheckRequest{
		BaseURL: "",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/upstreams/check", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty base_url status = %d, want 400", w.Code)
	}

	// Case 2: Invalid BaseURL scheme
	payload = UpstreamCheckRequest{
		BaseURL: "ftp://invalid-host",
	}
	body, _ = json.Marshal(payload)
	req = httptest.NewRequest(http.MethodPost, "/api/upstreams/check", bytes.NewReader(body))
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid scheme status = %d, want 400", w.Code)
	}
}

func TestUpstreamCheck_HealthyOpenAI(t *testing.T) {
	// Mock OpenAI server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" && r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		authHdr := r.Header.Get("Authorization")
		if authHdr != "Bearer sk-test-key-123" {
			t.Errorf("unexpected auth header: %s", authHdr)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-4o"},{"id":"gpt-4o-mini"}]}`))
	}))
	defer mockServer.Close()

	deps := RouterDeps{}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	payload := UpstreamCheckRequest{
		Protocol:  "openai",
		BaseURL:   mockServer.URL,
		APIKey:    "sk-test-key-123",
		TimeoutMs: 5000,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/upstreams/check", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var res UpstreamCheckResponse
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if !res.Healthy {
		t.Errorf("expected healthy=true, got false, msg=%s", res.Message)
	}
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected statusCode=200, got %d", res.StatusCode)
	}
	if res.ModelCount != 2 {
		t.Errorf("expected modelCount=2, got %d", res.ModelCount)
	}
	if len(res.Models) != 2 || res.Models[0] != "gpt-4o" || res.Models[1] != "gpt-4o-mini" {
		t.Errorf("expected models [gpt-4o, gpt-4o-mini], got %v", res.Models)
	}
}

func TestUpstreamCheck_AuthFailure(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided","type":"invalid_request_error"}}`))
	}))
	defer mockServer.Close()

	deps := RouterDeps{}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	payload := UpstreamCheckRequest{
		Protocol:  "openai",
		BaseURL:   mockServer.URL,
		APIKey:    "sk-bad-key",
		TimeoutMs: 5000,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/upstreams/check", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var res UpstreamCheckResponse
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if res.Healthy {
		t.Errorf("expected healthy=false, got true")
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected statusCode=401, got %d", res.StatusCode)
	}
	if !bytes.Contains([]byte(res.Message), []byte("Incorrect API key provided")) {
		t.Errorf("message should mention incorrect API key, got: %s", res.Message)
	}
}

func TestUpstreamCheck_Anthropic(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("x-api-key")
		version := r.Header.Get("anthropic-version")
		if key != "sk-ant-test" || version != "2023-06-01" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-3-5-sonnet-20241022"}]}`))
	}))
	defer mockServer.Close()

	deps := RouterDeps{}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	payload := UpstreamCheckRequest{
		Protocol: "anthropic",
		BaseURL:  mockServer.URL + "/v1",
		APIKey:   "sk-ant-test",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/upstreams/check", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var res UpstreamCheckResponse
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if !res.Healthy {
		t.Errorf("expected healthy=true, got false: %s", res.Message)
	}
	if res.ModelCount != 1 {
		t.Errorf("expected modelCount=1, got %d", res.ModelCount)
	}
	if len(res.Models) != 1 || res.Models[0] != "claude-3-5-sonnet-20241022" {
		t.Errorf("expected [claude-3-5-sonnet-20241022], got %v", res.Models)
	}
}

func TestUpstreamCheck_Cline_OAuthResolution(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" && r.URL.Path != "/api/v1/models" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer cline-access-token-999" {
			t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("HTTP-Referer") != "https://cline.bot" {
			t.Errorf("unexpected referer: %s", r.Header.Get("HTTP-Referer"))
		}
		if r.Header.Get("X-Title") != "Cline" {
			t.Errorf("unexpected X-Title: %s", r.Header.Get("X-Title"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-3-7-sonnet"},{"id":"deepseek/deepseek-chat"}]}`))
	}))
	defer mockServer.Close()

	dir := t.TempDir()
	store, err := oauth.NewStore(filepath.Join(dir, "oauth.json"))
	if err != nil {
		t.Fatalf("NewStore error: %v", err)
	}

	conn := &domain.OAuthConnection{
		ID:       "cline-user@example.com",
		Provider: "cline",
		Email:    "user@example.com",
		Token: domain.OAuthToken{
			AccessToken: "cline-access-token-999",
			ExpiresAt:   time.Now().Add(1 * time.Hour),
		},
	}
	if err := store.Save(context.Background(), conn); err != nil {
		t.Fatalf("store.Save error: %v", err)
	}

	mgr := oauth.NewManager(store)
	deps := RouterDeps{OAuthManager: mgr}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	payload := UpstreamCheckRequest{
		Protocol:  "cline",
		BaseURL:   mockServer.URL,
		KeyRef:    "oauth:cline-user@example.com",
		TimeoutMs: 5000,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/upstreams/check", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var res UpstreamCheckResponse
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if !res.Healthy {
		t.Errorf("expected healthy=true, got false: %s", res.Message)
	}
	if res.ModelCount != 2 {
		t.Errorf("expected modelCount=2, got %d", res.ModelCount)
	}
	if len(res.Models) != 2 || res.Models[0] != "claude-3-7-sonnet" || res.Models[1] != "deepseek/deepseek-chat" {
		t.Errorf("expected models, got %v", res.Models)
	}
}

func TestUpstreamCheck_Antigravity(t *testing.T) {
	deps := RouterDeps{}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	payload := UpstreamCheckRequest{
		Protocol: "antigravity",
		BaseURL:  "https://cloudsandbox-pa.googleapis.com",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/upstreams/check", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var res UpstreamCheckResponse
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if !res.Healthy {
		t.Errorf("expected healthy=true, got false: %s", res.Message)
	}
	if res.ModelCount == 0 || len(res.Models) == 0 {
		t.Errorf("expected models for antigravity, got none")
	}
}

func TestUpstreamCheck_Model_OpenAI_Success(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" && r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		var payload struct {
			Model    string `json:"model"`
			Messages []any  `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("failed to decode request body: %v", err)
		}
		if payload.Model != "gpt-4o" {
			t.Errorf("expected model gpt-4o, got %s", payload.Model)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-123","choices":[{"message":{"role":"assistant","content":"pong"}}]}`))
	}))
	defer mockServer.Close()

	deps := RouterDeps{}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	payload := UpstreamCheckRequest{
		Protocol:  "openai",
		BaseURL:   mockServer.URL,
		APIKey:    "sk-test-key",
		Model:     "gpt-4o",
		TimeoutMs: 5000,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/upstreams/check", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var res UpstreamCheckResponse
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if !res.Healthy {
		t.Errorf("expected healthy=true, got false: %s", res.Message)
	}
	if res.StatusCode != 200 {
		t.Errorf("expected status=200, got %d", res.StatusCode)
	}
	if !bytes.Contains([]byte(res.Message), []byte("gpt-4o")) {
		t.Errorf("expected message to mention gpt-4o, got %s", res.Message)
	}
}

func TestUpstreamCheck_Model_OpenAI_NotFound(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"The model 'gpt-nonexistent' does not exist","type":"invalid_request_error"}}`))
	}))
	defer mockServer.Close()

	deps := RouterDeps{}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	payload := UpstreamCheckRequest{
		Protocol:  "openai",
		BaseURL:   mockServer.URL,
		APIKey:    "sk-test-key",
		Model:     "gpt-nonexistent",
		TimeoutMs: 5000,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/upstreams/check", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var res UpstreamCheckResponse
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if res.Healthy {
		t.Errorf("expected healthy=false, got true")
	}
	if res.StatusCode != 404 {
		t.Errorf("expected status=404, got %d", res.StatusCode)
	}
	if !bytes.Contains([]byte(res.Message), []byte("The model 'gpt-nonexistent' does not exist")) {
		t.Errorf("expected message to contain error message, got %s", res.Message)
	}
}

func TestUpstreamCheck_Model_Anthropic_Success(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" && r.URL.Path != "/messages" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "sk-ant-test" {
			t.Errorf("unexpected api key: %s", r.Header.Get("x-api-key"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_123","content":[{"type":"text","text":"pong"}]}`))
	}))
	defer mockServer.Close()

	deps := RouterDeps{}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	payload := UpstreamCheckRequest{
		Protocol:  "anthropic",
		BaseURL:   mockServer.URL + "/v1",
		APIKey:    "sk-ant-test",
		Model:     "claude-3-7-sonnet",
		TimeoutMs: 5000,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/upstreams/check", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var res UpstreamCheckResponse
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if !res.Healthy {
		t.Errorf("expected healthy=true, got false: %s", res.Message)
	}
	if res.StatusCode != 200 {
		t.Errorf("expected status=200, got %d", res.StatusCode)
	}
}

func TestUpstreamCheck_Model_Antigravity(t *testing.T) {
	deps := RouterDeps{}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	// Valid Antigravity model
	payload := UpstreamCheckRequest{
		Protocol: "antigravity",
		BaseURL:  "https://cloudsandbox-pa.googleapis.com",
		Model:    "gemini-2.5-pro",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/upstreams/check", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	var res UpstreamCheckResponse
	_ = json.NewDecoder(w.Body).Decode(&res)
	if !res.Healthy || res.StatusCode != 200 {
		t.Errorf("expected gemini-2.5-pro to be healthy, got %v", res)
	}

	// Custom / unlisted Antigravity model: now ACCEPTED (custom models allowed),
	// not rejected. The curated list is not exhaustive.
	payload = UpstreamCheckRequest{
		Protocol: "antigravity",
		BaseURL:  "https://cloudsandbox-pa.googleapis.com",
		Model:    "random-nonexistent-model",
	}
	body, _ = json.Marshal(payload)
	req = httptest.NewRequest(http.MethodPost, "/api/upstreams/check", bytes.NewReader(body))
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	_ = json.NewDecoder(w.Body).Decode(&res)
	if !res.Healthy || res.StatusCode == http.StatusBadRequest {
		t.Errorf("expected custom model to be accepted (healthy, no 400), got %v", res)
	}
}

func TestUpstreamCheck_KeyRefResolutionFromSnapshot(t *testing.T) {
	var receivedAuthHeader string
	var receivedMaxTokens float64

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthHeader = r.Header.Get("Authorization")

		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if mt, ok := body["max_tokens"].(float64); ok {
			receivedMaxTokens = mt
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-123","choices":[{"message":{"role":"assistant","content":"pong"}}]}`))
	}))
	defer mockServer.Close()

	// Build snapshot with an upstream containing a KeyRing
	keySlot := &domain.KeySlot{
		Ref:    "test-up-key-1",
		Secret: "sk-real-secret-12345",
	}
	up := &domain.Upstream{
		Name:     "test-up",
		Protocol: domain.ProtocolOpenAI,
		BaseURL:  mockServer.URL,
		KeyRing:  domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{keySlot}),
	}
	snap := domain.NewCatalogSnapshot(
		1,
		map[string]*domain.Upstream{"test-up": up},
		[]string{"test-up"},
		map[string]*domain.ModelEntry{},
		nil,
		map[string]*domain.Tenant{},
		nil,
	)

	deps := RouterDeps{Snapshots: fakeProvider{snap}}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	// Caller specifies existing upstream Name and KeyRef, but empty or masked APIKey
	payload := UpstreamCheckRequest{
		Name:      "test-up",
		KeyRef:    "test-up-key-1",
		APIKey:    "",
		Model:     "gpt-4o",
		TimeoutMs: 5000,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/upstreams/check", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var res UpstreamCheckResponse
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if !res.Healthy {
		t.Fatalf("expected healthy=true, got false: %s", res.Message)
	}

	if receivedAuthHeader != "Bearer sk-real-secret-12345" {
		t.Errorf("expected mock server to receive unmasked secret, got %q", receivedAuthHeader)
	}

	if receivedMaxTokens != 10 {
		t.Errorf("expected max_tokens = 10, got %v", receivedMaxTokens)
	}
}

func TestUpstreamCheck_GrokCLIModelList(t *testing.T) {
	deps := RouterDeps{}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	// No base_url reachability needed: grok-cli returns a static curated list.
	payload := UpstreamCheckRequest{
		Protocol: "grok-cli",
		BaseURL:  "https://cli-chat-proxy.grok.com/v1",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/upstreams/check", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("grok-cli probe status = %d, want 200", w.Code)
	}
	var resp UpstreamCheckResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Healthy {
		t.Fatalf("grok-cli probe not healthy: %s", resp.Message)
	}
	if resp.ModelCount == 0 || len(resp.Models) == 0 {
		t.Fatalf("grok-cli probe returned no models: %+v", resp)
	}
	found := false
	for _, m := range resp.Models {
		if m == "grok-build" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected grok-build in models, got %v", resp.Models)
	}
}

func TestUpstreamCheck_GrokCLILiveModelDiscovery(t *testing.T) {
	// A mock Grok CLI server that serves /models when the bearer token is present.
	var gotAuth, gotPath string
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"grok-build"},{"id":"grok-4.5"},{"id":"grok-live-only"}]}`))
	}))
	defer mockServer.Close()

	keySlot := &domain.KeySlot{Ref: "grok-key-1156", Secret: "ey-live-access-token"}
	up := &domain.Upstream{
		Name:     "grok",
		Protocol: domain.ProtocolGrokCLI,
		BaseURL:  mockServer.URL + "/v1",
		KeyRing:  domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{keySlot}),
	}
	snap := domain.NewCatalogSnapshot(
		1,
		map[string]*domain.Upstream{"grok": up},
		[]string{"grok"},
		map[string]*domain.ModelEntry{},
		nil,
		map[string]*domain.Tenant{},
		nil,
	)

	deps := RouterDeps{Snapshots: fakeProvider{snap}}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	// Existing upstream by name; the token is resolved from the key ring.
	payload := UpstreamCheckRequest{
		Name:      "grok",
		Protocol:  "grok-cli",
		TimeoutMs: 5000,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/upstreams/check", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("live discovery probe status = %d, want 200", w.Code)
	}
	var resp UpstreamCheckResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Healthy {
		t.Fatalf("live discovery not healthy: %s", resp.Message)
	}
	// The live-only model proves the list came from the upstream, not the static set.
	found := false
	for _, m := range resp.Models {
		if m == "grok-live-only" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected live-discovered model in list, got %v (msg: %s)", resp.Models, resp.Message)
	}
	if gotAuth != "Bearer ey-live-access-token" {
		t.Errorf("expected /models called with resolved bearer, got %q", gotAuth)
	}
	if gotPath != "/v1/models" {
		t.Errorf("expected /v1/models path, got %q", gotPath)
	}
}

func TestUpstreamCheck_GrokCLIProtocolAlias(t *testing.T) {
	deps := RouterDeps{}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	// The "grok" alias (as stored by the harvester provider) must resolve to grok-cli.
	payload := UpstreamCheckRequest{
		Protocol: "grok",
		BaseURL:  "https://cli-chat-proxy.grok.com/v1",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/upstreams/check", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("grok alias probe status = %d, want 200", w.Code)
	}
	var resp UpstreamCheckResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Healthy || len(resp.Models) == 0 {
		t.Fatalf("grok alias probe failed: %+v", resp)
	}
}

func TestUpstreamCheck_GrokCLICustomModelAccepted(t *testing.T) {
	deps := RouterDeps{}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	// A model not in the curated list must be ACCEPTED as a custom model, not rejected.
	payload := UpstreamCheckRequest{
		Protocol: "grok-cli",
		BaseURL:  "https://cli-chat-proxy.grok.com/v1",
		Model:    "grok-5-preview",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/upstreams/check", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("custom-model probe status = %d, want 200", w.Code)
	}
	var resp UpstreamCheckResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Healthy {
		t.Fatalf("custom model must be accepted (healthy), got: %s", resp.Message)
	}
	if resp.StatusCode == http.StatusBadRequest {
		t.Fatalf("custom model must not return 400: %+v", resp)
	}
}

func TestUpstreamCheck_AntigravityCustomModelAccepted(t *testing.T) {
	deps := RouterDeps{}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	payload := UpstreamCheckRequest{
		Protocol: "antigravity",
		BaseURL:  "https://cloudsandbox-pa.googleapis.com",
		Model:    "gemini-9-ultra",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/upstreams/check", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	var resp UpstreamCheckResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Healthy {
		t.Fatalf("antigravity custom model must be accepted, got: %s", resp.Message)
	}
}

func TestUpstreamCheck_GrokCLIExpiredKey(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"Invalid or expired credentials (auth_kind=bearer, x_xai_token_auth=xai-grok-cli, upstream=PermissionDenied, reason=no auth context)"}`))
	}))
	defer mockServer.Close()

	deps := RouterDeps{}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	payload := UpstreamCheckRequest{
		Protocol:  "grok-cli",
		BaseURL:   mockServer.URL + "/v1",
		APIKey:    "ey-expired-test-token",
		TimeoutMs: 5000,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/upstreams/check", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp UpstreamCheckResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Healthy {
		t.Fatalf("expired key must NOT be healthy: %+v", resp)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected status code 401, got %d", resp.StatusCode)
	}
	if resp.LatencyMs < 0 {
		t.Errorf("latency_ms should be non-negative, got %d", resp.LatencyMs)
	}
}

func TestUpstreamCheck_GrokCLIRateLimited(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"Rate limit exceeded"}`))
	}))
	defer mockServer.Close()

	deps := RouterDeps{}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	payload := UpstreamCheckRequest{
		Protocol:  "grok-cli",
		BaseURL:   mockServer.URL + "/v1",
		APIKey:    "ey-rate-limited-token",
		TimeoutMs: 5000,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/upstreams/check", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp UpstreamCheckResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Healthy {
		t.Fatalf("rate limited key must NOT be healthy: %+v", resp)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected status code 429, got %d", resp.StatusCode)
	}
}

func TestUpstreamCheck_ProbeModelFromSnapshot(t *testing.T) {
	var receivedModel string
	var receivedPath string

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if m, ok := body["model"].(string); ok {
			receivedModel = m
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer mockServer.Close()

	keySlot := &domain.KeySlot{
		Ref:    "test-up-key-1",
		Secret: "sk-real-secret-12345",
	}
	up := &domain.Upstream{
		Name:       "test-up",
		Protocol:   domain.ProtocolOpenAI,
		BaseURL:    mockServer.URL,
		ProbeModel: "deepseek-chat",
		KeyRing:    domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{keySlot}),
	}
	snap := domain.NewCatalogSnapshot(
		1,
		map[string]*domain.Upstream{"test-up": up},
		[]string{"test-up"},
		map[string]*domain.ModelEntry{},
		nil,
		map[string]*domain.Tenant{},
		nil,
	)

	deps := RouterDeps{Snapshots: fakeProvider{snap}}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	// Caller provides Name but leaves Model empty. System must auto-resolve probe_model!
	payload := UpstreamCheckRequest{
		Name:      "test-up",
		KeyRef:    "test-up-key-1",
		Model:     "", // empty!
		TimeoutMs: 5000,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/upstreams/check", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var res UpstreamCheckResponse
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !res.Healthy {
		t.Fatalf("expected healthy=true, got false: %s", res.Message)
	}
	if receivedPath != "/chat/completions" {
		t.Errorf("expected path /chat/completions, got %s", receivedPath)
	}
	if receivedModel != "deepseek-chat" {
		t.Errorf("expected probe model deepseek-chat, got %s", receivedModel)
	}
}

func TestUpstreamModels_CORS(t *testing.T) {
	deps := RouterDeps{}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	req := httptest.NewRequest(http.MethodOptions, "/api/upstreams/models", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS status = %d, want 204", w.Code)
	}
	if origin := w.Header().Get("Access-Control-Allow-Origin"); origin != "*" {
		t.Errorf("CORS origin = %q, want *", origin)
	}
}

func TestUpstreamModels_Success(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" && r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-test-key-models" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid key"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object": "list",
			"data": [
				{"id": "gpt-4o"},
				{"id": "gpt-4o-mini"},
				{"id": "text-embedding-3-small"}
			]
		}`))
	}))
	defer mockServer.Close()

	deps := RouterDeps{}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	payload := UpstreamModelsRequest{
		BaseURL:   mockServer.URL,
		APIKey:    "sk-test-key-models",
		Protocol:  "openai",
		TimeoutMs: 5000,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/upstreams/models", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var res UpstreamModelsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if res.ModelCount != 3 {
		t.Fatalf("expected 3 models, got %d", res.ModelCount)
	}
	expected := []string{"gpt-4o", "gpt-4o-mini", "text-embedding-3-small"}
	for _, exp := range expected {
		if !slices.Contains(res.Models, exp) {
			t.Errorf("missing expected model %q in %v", exp, res.Models)
		}
	}
}

func TestUpstreamModels_IgnoresProbeModel(t *testing.T) {
	var receivedPaths []string
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPaths = append(receivedPaths, r.URL.Path)
		if r.URL.Path == "/v1/models" || r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"data": [
					{"id": "deepseek-chat"},
					{"id": "deepseek-coder"},
					{"id": "deepseek-reasoner"}
				]
			}`))
			return
		}
		if r.URL.Path == "/chat/completions" || r.URL.Path == "/v1/chat/completions" {
			t.Errorf("ERROR: /api/upstreams/models must NEVER trigger inference! received path: %s", r.URL.Path)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer mockServer.Close()

	up := &domain.Upstream{
		Name:       "probe-up",
		BaseURL:    mockServer.URL,
		ProbeModel: "deepseek-chat", // Upstream has probe model configured!
		KeyRing: domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{
			{Ref: "probe-up-key-1", Secret: "sk-test-secret"},
		}),
	}
	snap := domain.NewCatalogSnapshot(
		1,
		map[string]*domain.Upstream{up.Name: up},
		[]string{up.Name},
		map[string]*domain.ModelEntry{},
		nil,
		map[string]*domain.Tenant{},
		nil,
	)
	deps := RouterDeps{Snapshots: fakeProvider{snap}}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	payload := UpstreamModelsRequest{
		Name:      "probe-up",
		TimeoutMs: 5000,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/upstreams/models", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var res UpstreamModelsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if res.ModelCount != 3 {
		t.Fatalf("expected 3 models discovered, got %d: %v", res.ModelCount, res.Models)
	}

	// Verify no inference endpoints were called
	for _, p := range receivedPaths {
		if p == "/chat/completions" || p == "/v1/chat/completions" {
			t.Fatalf("models endpoint called inference path %s!", p)
		}
	}
}

func TestUpstreamCheck_LoadBalancingRoundRobin(t *testing.T) {
	var mu sync.Mutex
	var authHeaders []string

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer mockServer.Close()

	slot1 := &domain.KeySlot{Ref: "k1", Secret: "sk-key-1"}
	slot2 := &domain.KeySlot{Ref: "k2", Secret: "sk-key-2"}
	kr := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{slot1, slot2})

	up := &domain.Upstream{
		Name:       "lb-upstream",
		BaseURL:    mockServer.URL,
		Protocol:   domain.ProtocolOpenAI,
		ProbeModel: "gpt-4o-mini",
		KeyRing:    kr,
	}

	snap := domain.NewCatalogSnapshot(
		1,
		map[string]*domain.Upstream{"lb-upstream": up},
		[]string{"lb-upstream"},
		map[string]*domain.ModelEntry{},
		nil,
		map[string]*domain.Tenant{},
		nil,
	)
	deps := RouterDeps{Snapshots: fakeProvider{snap}}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	// Make 4 consecutive check requests without specifying key_ref
	var resKeyRefs []string
	for i := 0; i < 4; i++ {
		payload := UpstreamCheckRequest{
			Name:      "lb-upstream",
			Model:     "gpt-4o-mini",
			TimeoutMs: 5000,
		}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/api/upstreams/check", bytes.NewReader(body))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, w.Code)
		}
		var res UpstreamCheckResponse
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
			t.Fatalf("request %d: unmarshal response: %v", i, err)
		}
		if !res.Healthy {
			t.Fatalf("request %d: expected healthy, got false: %s", i, res.Message)
		}
		resKeyRefs = append(resKeyRefs, res.KeyRef)
	}

	mu.Lock()
	defer mu.Unlock()

	// The ring's starting slot is seeded from a process-wide rotation counter
	// (so rotation survives snapshot rebuilds), so the absolute first key is
	// not fixed. Assert strict round-robin alternation relative to the first
	// observed key rather than a fixed [k1,k2,k1,k2] sequence.
	authForRef := map[string]string{"k1": "Bearer sk-key-1", "k2": "Bearer sk-key-2"}
	firstRef := resKeyRefs[0]
	if firstRef != "k1" && firstRef != "k2" {
		t.Fatalf("unexpected first key_ref %q", firstRef)
	}
	otherRef := "k2"
	if firstRef == "k2" {
		otherRef = "k1"
	}
	wantRefs := []string{firstRef, otherRef, firstRef, otherRef}
	for i, want := range wantRefs {
		if resKeyRefs[i] != want {
			t.Errorf("request %d: got key_ref %s, want %s (alternation from %s)", i, resKeyRefs[i], want, firstRef)
		}
		if authHeaders[i] != authForRef[want] {
			t.Errorf("request %d: got auth header %s, want %s", i, authHeaders[i], authForRef[want])
		}
	}
}

func TestUpstreamCheck_CooldownSkipsRateLimitedKey(t *testing.T) {
	var mu sync.Mutex
	var authHeaders []string
	// The ring's first selected key is not fixed (its cursor is seeded from a
	// process-wide rotation counter so rotation survives reloads). Rate-limit
	// whichever key the FIRST request happens to pick, so the test asserts the
	// real invariant: a 429'd key enters cooldown and the next request skips to
	// the other key. The first Authorization header seen determines the "hot"
	// (rate-limited) key.
	var rateLimitedAuth string

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth := r.Header.Get("Authorization")
		authHeaders = append(authHeaders, auth)
		if rateLimitedAuth == "" {
			rateLimitedAuth = auth
		}
		isRateLimited := auth == rateLimitedAuth
		mu.Unlock()

		if isRateLimited {
			w.Header().Set("Retry-After", "60")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"Rate limit exceeded"}}`))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer mockServer.Close()

	slot1 := &domain.KeySlot{Ref: "k1", Secret: "sk-k1"}
	slot2 := &domain.KeySlot{Ref: "k2", Secret: "sk-k2"}
	kr := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{slot1, slot2})
	slotByRef := map[string]*domain.KeySlot{"k1": slot1, "k2": slot2}

	up := &domain.Upstream{
		Name:       "cooldown-up",
		BaseURL:    mockServer.URL,
		Protocol:   domain.ProtocolOpenAI,
		ProbeModel: "gpt-4o-mini",
		KeyRing:    kr,
	}

	snap := domain.NewCatalogSnapshot(
		1,
		map[string]*domain.Upstream{"cooldown-up": up},
		[]string{"cooldown-up"},
		map[string]*domain.ModelEntry{},
		nil,
		map[string]*domain.Tenant{},
		nil,
	)
	deps := RouterDeps{Snapshots: fakeProvider{snap}}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	// First request hits whichever key is selected first and gets 429.
	payload1 := UpstreamCheckRequest{
		Name:      "cooldown-up",
		Model:     "gpt-4o-mini",
		TimeoutMs: 5000,
	}
	body1, _ := json.Marshal(payload1)
	req1 := httptest.NewRequest(http.MethodPost, "/api/upstreams/check", bytes.NewReader(body1))
	w1 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w1, req1)

	var res1 UpstreamCheckResponse
	_ = json.Unmarshal(w1.Body.Bytes(), &res1)
	if res1.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("first request expected status 429, got %d", res1.StatusCode)
	}
	firstRef := res1.KeyRef
	if firstRef != "k1" && firstRef != "k2" {
		t.Fatalf("first request unexpected key_ref %q", firstRef)
	}

	// The first-selected slot must now be in cooldown.
	if !slotByRef[firstRef].IsInCooldown(time.Now().UnixNano()) {
		t.Fatalf("expected slot %s to be in cooldown after 429", firstRef)
	}

	// Next request should automatically skip the cooled-down slot and select
	// the other key, which succeeds.
	otherRef := "k2"
	if firstRef == "k2" {
		otherRef = "k1"
	}
	payload2 := UpstreamCheckRequest{
		Name:      "cooldown-up",
		Model:     "gpt-4o-mini",
		TimeoutMs: 5000,
	}
	body2, _ := json.Marshal(payload2)
	req2 := httptest.NewRequest(http.MethodPost, "/api/upstreams/check", bytes.NewReader(body2))
	w2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w2, req2)

	var res2 UpstreamCheckResponse
	_ = json.Unmarshal(w2.Body.Bytes(), &res2)
	if !res2.Healthy || res2.StatusCode != 200 {
		t.Fatalf("second request expected healthy 200, got status=%d: %s", res2.StatusCode, res2.Message)
	}
	if res2.KeyRef != otherRef {
		t.Fatalf("second request expected key_ref %s, got %s", otherRef, res2.KeyRef)
	}
}

func TestUpstreamCheck_OpenCode(t *testing.T) {
	var (
		receivedAuthHeader    string
		receivedUAHeader      string
		receivedClientHeader  string
		receivedSessionHeader string
		receivedRequestHeader string
		receivedPath          string
		receivedBody          []byte
	)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthHeader = r.Header.Get("Authorization")
		receivedUAHeader = r.Header.Get("User-Agent")
		receivedClientHeader = r.Header.Get("x-opencode-client")
		receivedSessionHeader = r.Header.Get("x-opencode-session")
		receivedRequestHeader = r.Header.Get("x-opencode-request")
		receivedPath = r.URL.Path
		receivedBody, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/models" || r.URL.Path == "/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "list",
				"data": []map[string]any{
					{"id": "deepseek-v3"},
					{"id": "muse-spark-free"},
				},
			})
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "mock-resp-1",
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": "pong"}},
			},
		})
	}))
	defer mockServer.Close()

	deps := RouterDeps{}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	t.Run("OpenCode Free standard probe", func(t *testing.T) {
		payload := UpstreamCheckRequest{
			Protocol:  "opencode",
			BaseURL:   mockServer.URL + "/v1",
			APIKey:    "", // Empty -> free tier
			Model:     "deepseek-v3",
			TimeoutMs: 5000,
		}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/api/upstreams/check", bytes.NewReader(body))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		var res UpstreamCheckResponse
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		if !res.Healthy {
			t.Fatalf("expected healthy=true, got false: %s", res.Message)
		}
		if receivedAuthHeader != "Bearer public" {
			t.Errorf("expected Bearer public, got %s", receivedAuthHeader)
		}
		if receivedUAHeader != opencode.OpenCodeUserAgent {
			t.Errorf("expected %s, got %s", opencode.OpenCodeUserAgent, receivedUAHeader)
		}
		if receivedClientHeader != "cli" {
			t.Errorf("expected x-opencode-client: cli, got %s", receivedClientHeader)
		}
		if !opencode.IsValidSessionID(receivedSessionHeader) {
			t.Errorf("expected valid canonical session header, got %s", receivedSessionHeader)
		}
		if !strings.HasPrefix(receivedRequestHeader, "msg_") || len(receivedRequestHeader) != 30 {
			t.Errorf("expected request header starting with msg_ with len 30, got %s", receivedRequestHeader)
		}
		if receivedPath != "/v1/chat/completions" {
			t.Errorf("expected /v1/chat/completions path, got %s", receivedPath)
		}
	})

	t.Run("OpenCode Responses model probe", func(t *testing.T) {
		payload := UpstreamCheckRequest{
			Protocol:  "opencode",
			BaseURL:   mockServer.URL + "/v1",
			APIKey:    "public",
			Model:     "muse-spark-pro",
			TimeoutMs: 5000,
		}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/api/upstreams/check", bytes.NewReader(body))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		var res UpstreamCheckResponse
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		if !res.Healthy {
			t.Fatalf("expected healthy=true, got false: %s", res.Message)
		}
		if receivedPath != "/v1/responses" {
			t.Errorf("expected /v1/responses path, got %s", receivedPath)
		}
		if !strings.Contains(string(receivedBody), `"input"`) {
			t.Errorf("expected responses input field in body, got: %s", string(receivedBody))
		}
	})

	t.Run("OpenCode Models fetch", func(t *testing.T) {
		payload := UpstreamModelsRequest{
			Protocol:  "opencode",
			BaseURL:   mockServer.URL + "/v1",
			APIKey:    "",
			TimeoutMs: 5000,
		}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/api/upstreams/models", bytes.NewReader(body))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		var res UpstreamModelsResponse
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		if res.ModelCount != 2 {
			t.Fatalf("expected 2 models, got %d: %s", res.ModelCount, res.Message)
		}
		if receivedAuthHeader != "Bearer public" {
			t.Errorf("expected Bearer public for models fetch, got %s", receivedAuthHeader)
		}
	})
}

func TestUpstreamCheck_OpenCodeLive(t *testing.T) {
	if os.Getenv("FIREFLY_LIVE_TESTS") != "1" {
		t.Skip("skipping live test; set FIREFLY_LIVE_TESTS=1 to run")
	}

	deps := RouterDeps{}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	testModels := []string{"big-pickle", "mimo-v2.5-free", "muse-spark-1.3-contributor-free"}
	for _, model := range testModels {
		t.Run(model, func(t *testing.T) {
			payload := UpstreamCheckRequest{
				Protocol:  "opencode",
				BaseURL:   "https://opencode.ai/zen/v1",
				APIKey:    "", // Free tier
				Model:     model,
				TimeoutMs: 15000,
			}
			body, _ := json.Marshal(payload)
			req := httptest.NewRequest(http.MethodPost, "/api/upstreams/check", bytes.NewReader(body))
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", w.Code)
			}
			var res UpstreamCheckResponse
			_ = json.Unmarshal(w.Body.Bytes(), &res)
			if !res.Healthy {
				t.Fatalf("live OpenCode free check failed for %s: statusCode=%d msg=%s", model, res.StatusCode, res.Message)
			}
			t.Logf("OpenCode Free live probe passed for %s: %s (latency: %dms)", model, res.Message, res.LatencyMs)
		})
	}
}
