package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/adapter/antigravity"
)

// These tests pin the promise that replaced the hardcoded antigravity list:
// every Fetch models and every model health check is a real call to the host,
// and the response can only be as healthy as the upstream actually was.

type agStub struct {
	*httptest.Server
	mu       sync.Mutex
	models   []string // models the host was asked about, in arrival order
	authSeen string
}

func newAGStub(t *testing.T, served map[string]bool, refuseStatus int) *agStub {
	t.Helper()
	stub := &agStub{}
	stub.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != antigravity.GenerateContentPath {
			http.Error(w, `{"error":{"message":"not found"}}`, http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var envelope antigravity.AntigravityEnvelope
		_ = json.Unmarshal(body, &envelope)
		stub.mu.Lock()
		stub.models = append(stub.models, envelope.Model)
		stub.authSeen = r.Header.Get("Authorization")
		stub.mu.Unlock()
		if served[envelope.Model] {
			_, _ = io.WriteString(w, `{"response":{"candidates":[]}}`)
			return
		}
		w.WriteHeader(refuseStatus)
		_, _ = io.WriteString(w, `{"error":{"message":"model is not available for this account"}}`)
	}))
	t.Cleanup(stub.Close)
	return stub
}

func (s *agStub) asked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.models))
	copy(out, s.models)
	return out
}

func (s *agStub) auth() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authSeen
}

// agServer builds a router with no admin token (authorizeAdmin is open then) and
// posts a payload to one of the two upstream-check endpoints, decoding the reply.
func agPost(t *testing.T, path string, payload any, out any) {
	t.Helper()
	s := New(Config{Addr: "0.0.0.0:8080"}, RouterDeps{}, context.Background(), nil)
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s status = %d, want 200 (body: %s)", path, rec.Code, rec.Body.String())
	}
	if err := json.NewDecoder(rec.Body).Decode(out); err != nil {
		t.Fatalf("decode %s response: %v", path, err)
	}
}

func TestAntigravityCheck_ProbesGoogleLive(t *testing.T) {
	stub := newAGStub(t, map[string]bool{"gemini-2.5-pro": true}, http.StatusNotFound)

	var res UpstreamCheckResponse
	agPost(t, "/api/upstreams/check", UpstreamCheckRequest{
		Protocol:  "antigravity",
		BaseURL:   stub.URL,
		APIKey:    "ya29.live-token",
		Model:     "gemini-2.5-pro",
		TimeoutMs: 5000,
	}, &res)

	if !res.Healthy {
		t.Fatalf("live check should be healthy: %s", res.Message)
	}
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	if got := stub.asked(); len(got) != 1 || got[0] != "gemini-2.5-pro" {
		t.Errorf("upstream was asked %v, want exactly the probed model", got)
	}
	if got := stub.auth(); got != "Bearer ya29.live-token" {
		t.Errorf("Authorization = %q, want the resolved OAuth token", got)
	}
	// No latency magic number is asserted: a local stub legitimately answers in
	// 1-2ms, and the old mock's tell was structural (it never called the host) —
	// which `asked()` above already pins.
	if !strings.Contains(res.Message, "answered live") {
		t.Errorf("message must state the verdict came from a live call: %q", res.Message)
	}
}

func TestAntigravityCheck_ReportsUpstreamFailureTruthfully(t *testing.T) {
	stub := newAGStub(t, nil, http.StatusForbidden)

	var res UpstreamCheckResponse
	agPost(t, "/api/upstreams/check", UpstreamCheckRequest{
		Protocol:  "antigravity",
		BaseURL:   stub.URL,
		APIKey:    "ya29.live-token",
		Model:     "gemini-2.5-pro",
		TimeoutMs: 5000,
	}, &res)

	if res.Healthy {
		t.Fatal("a refused model must not report healthy (this is exactly what the mock did)")
	}
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", res.StatusCode)
	}
	if !strings.Contains(res.Message, "model is not available for this account") {
		t.Errorf("message must carry the upstream reason: %q", res.Message)
	}
}

func TestAntigravityCheck_WithoutCredentialNeverClaimsHealthy(t *testing.T) {
	stub := newAGStub(t, map[string]bool{"gemini-2.5-pro": true}, http.StatusNotFound)

	var res UpstreamCheckResponse
	agPost(t, "/api/upstreams/check", UpstreamCheckRequest{
		Protocol:  "antigravity",
		BaseURL:   stub.URL,
		Model:     "gemini-2.5-pro",
		TimeoutMs: 5000,
	}, &res)

	if res.Healthy {
		t.Fatal("no credential must never yield a healthy verdict")
	}
	if !strings.Contains(res.Message, "No Google credential") {
		t.Errorf("message = %q, want the connect-OAuth guidance", res.Message)
	}
	if got := stub.asked(); len(got) != 0 {
		t.Errorf("no anonymous call may be made: %v", got)
	}
}

func TestAntigravityModels_VerifiesCandidatesLive(t *testing.T) {
	served := map[string]bool{}
	for _, m := range antigravity.CuratedModels() {
		served[m] = m == "gemini-2.5-pro" || m == "gemini-2.5-flash"
	}
	stub := newAGStub(t, served, http.StatusNotFound)

	var res UpstreamModelsResponse
	agPost(t, "/api/upstreams/models", UpstreamModelsRequest{
		Protocol:  "antigravity",
		BaseURL:   stub.URL,
		APIKey:    "ya29.live-token",
		TimeoutMs: 20000,
	}, &res)

	if res.ModelCount != 2 {
		t.Fatalf("verified %d models, want the 2 the host served: %+v", res.ModelCount, res)
	}
	for _, m := range res.Models {
		if m == "gemini-2.0-flash" {
			t.Errorf("a refused model must not be reported: %v", res.Models)
		}
	}
	if len(res.Unavailable) == 0 {
		t.Error("refused candidates must be reported in unavailable")
	}
	if got := stub.asked(); len(got) != len(antigravity.CuratedModels()) {
		t.Errorf("upstream saw %d probes, want one per candidate (%d)", len(got), len(antigravity.CuratedModels()))
	}
	for _, asked := range stub.asked() {
		if asked == "" {
			t.Fatal("every probe must name the model it verifies")
		}
	}
	if !strings.Contains(res.Message, "Cloud Code serves no model-list route") {
		t.Errorf("message must explain the verification method: %q", res.Message)
	}
}

func TestAntigravityModels_WithoutCredentialServesNoList(t *testing.T) {
	stub := newAGStub(t, nil, http.StatusNotFound)

	var res UpstreamModelsResponse
	agPost(t, "/api/upstreams/models", UpstreamModelsRequest{
		Protocol:  "antigravity",
		BaseURL:   stub.URL,
		TimeoutMs: 5000,
	}, &res)

	if len(res.Models) != 0 {
		t.Fatalf("no credential must produce no model list: %v", res.Models)
	}
	if !strings.Contains(res.Message, "connect a Google account") {
		t.Errorf("message = %q, want the connect-OAuth guidance", res.Message)
	}
	if got := stub.asked(); len(got) != 0 {
		t.Errorf("no anonymous sweep may be made: %v", got)
	}
}
