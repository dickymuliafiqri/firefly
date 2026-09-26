package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCanonicalProbeProtocol(t *testing.T) {
	cases := map[string]string{
		"codebuddy":      "codebuddy-cn",
		"codebuddy_cn":   "codebuddy-cn",
		"codebuddy-cn":   "codebuddy-cn",
		"codebuddy_intl": "codebuddy-intl",
		"codebuddy-intl": "codebuddy-intl",
		"antigravity-go": "antigravity",
		"antigravity_go": "antigravity",
		"antigravity":    "antigravity",
		"grok":           "grok-cli",
		"grok_cli":       "grok-cli",
		"qodercli":       "qoder",
		"qoder-cli":      "qoder",
		"qoder":          "qoder",
		"openai":         "openai",
		"anthropic":      "anthropic",
		"cline":          "cline",
	}
	for in, want := range cases {
		if got := canonicalProbeProtocol(in); got != want {
			t.Errorf("canonicalProbeProtocol(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProbeFallbackToOAuthManagedEndpoint(t *testing.T) {
	deps := RouterDeps{}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	payload := UpstreamCheckRequest{
		Protocol:  "codebuddy",
		APIKey:    "cb-token",
		Model:     "gpt-4o-mini",
		TimeoutMs: 1500,
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
		t.Fatalf("decode response: %v", err)
	}
	// A pinned OAuth path must never be blocked by "base_url is required":
	// the managed endpoint is automatically used.
	if strings.Contains(res.Message, "base_url is required") {
		t.Errorf("bare codebuddy probe hit empty base_url gate: %s", res.Message)
	}
}

func TestDiscoverModelsFallbackToOAuthManagedEndpoint(t *testing.T) {
	deps := RouterDeps{}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	payload := UpstreamModelsRequest{
		Protocol:  "codebuddy",
		APIKey:    "cb-token",
		TimeoutMs: 1500,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/upstreams/models", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var res UpstreamModelsResponse
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// A pinned OAuth path must never be blocked by "base_url is required":
	// the managed endpoint is automatically used.
	if strings.Contains(res.Message, "base_url is required") {
		t.Errorf("bare codebuddy discover hit empty base_url gate: %s", res.Message)
	}
}

