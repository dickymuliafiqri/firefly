package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
