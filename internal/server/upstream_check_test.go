package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/oauth"
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

	// Invalid Antigravity model
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
	if res.Healthy || res.StatusCode != 400 {
		t.Errorf("expected random-nonexistent-model to be rejected with 400, got %v", res)
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


