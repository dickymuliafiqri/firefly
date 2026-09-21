package qoder

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/tidwall/gjson"
)

// stubPool returns the default client for any upstream.
type stubPool struct{ c *http.Client }

func (p stubPool) Client(_ *domain.Upstream) *http.Client {
	if p.c != nil {
		return p.c
	}
	return http.DefaultClient
}

type stubBreaker struct{}

func (stubBreaker) Allow(string) error  { return nil }
func (stubBreaker) Report(string, bool) {}

// identityBlob returns a JSON secret blob the adapter can parse into an identity.
func identityBlob(token string) string {
	b, _ := json.Marshal(map[string]string{"token": token, "userId": "user-1", "machineId": "mach-1"})
	return string(b)
}

// qoderEnvelope wraps an inner OpenAI chunk in Qoder's SSE envelope.
func qoderEnvelope(inner string) string {
	env, _ := json.Marshal(map[string]any{"statusCodeValue": 200, "body": inner})
	return "data: " + string(env) + "\n\n"
}

// newMockQoder builds a mock Qoder server serving /model/list and the chat endpoint.
func newMockQoder(t *testing.T, chatSSE string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/algo/api/v2/model/list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"chat":[{"key":"auto","display_name":"Auto","max_input_tokens":200000,"max_output_tokens":32768,"is_reasoning":false,"enable":true}]}`)
	})
	mux.HandleFunc("/algo/api/v2/service/pro/sse/agent_chat_generation", func(w http.ResponseWriter, r *http.Request) {
		// Verify COSY headers are present.
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer COSY.") {
			t.Errorf("missing COSY authorization: %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Cosy-Key") == "" || r.Header.Get("Cosy-User") == "" {
			t.Error("missing Cosy-Key/Cosy-User")
		}
		if r.URL.Query().Get("Encode") != "1" {
			t.Error("missing Encode=1")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, chatSSE)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestForward_StreamingHappyPath(t *testing.T) {
	sse := qoderEnvelope(`{"id":"x","choices":[{"index":0,"delta":{"content":"Hello"}}]}`) +
		qoderEnvelope(`{"id":"x","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":"stop"}]}`) +
		qoderEnvelope(`{"id":"x","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`) +
		"data: [DONE]\n\n"
	srv := newMockQoder(t, sse)

	a := NewAdapter(stubPool{}, stubBreaker{}, Config{
		SecretLookup: func(string) (string, bool) { return identityBlob("dt-token"), true },
	})
	u := &domain.Upstream{Name: "qoder-test", Protocol: domain.ProtocolQoder, BaseURL: srv.URL, CredentialRef: "qoder-key-1"}
	target := &domain.Target{Upstream: u, UpstreamModel: "qoder/auto"}

	rec := httptest.NewRecorder()
	body := `{"model":"auto","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	err := a.Forward(context.Background(), target, ports.ForwardRequest{BodyBytes: []byte(body), Stream: true}, rec)
	if err != nil {
		t.Fatalf("Forward: %v", err)
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"content":"Hello"`) || !strings.Contains(out, `"content":" world"`) {
		t.Errorf("missing content deltas:\n%s", out)
	}
	if !strings.Contains(out, `"finish_reason":"stop"`) {
		t.Errorf("missing finish_reason:\n%s", out)
	}
	if !strings.Contains(out, `"usage"`) || !strings.Contains(out, `"total_tokens":7`) {
		t.Errorf("missing coalesced usage:\n%s", out)
	}
	if !strings.HasSuffix(strings.TrimSpace(out), "data: [DONE]") {
		t.Errorf("stream must end with [DONE]:\n%s", out)
	}
}

func TestForward_NonStreamingAggregation(t *testing.T) {
	sse := qoderEnvelope(`{"choices":[{"index":0,"delta":{"content":"AB"}}]}`) +
		qoderEnvelope(`{"choices":[{"index":0,"delta":{"content":"CD"},"finish_reason":"stop"}]}`) +
		qoderEnvelope(`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`) +
		"data: [DONE]\n\n"
	srv := newMockQoder(t, sse)

	a := NewAdapter(stubPool{}, stubBreaker{}, Config{
		SecretLookup: func(string) (string, bool) { return identityBlob("dt-token"), true },
	})
	u := &domain.Upstream{Name: "qoder-test", Protocol: domain.ProtocolQoder, BaseURL: srv.URL, CredentialRef: "k"}
	target := &domain.Target{Upstream: u, UpstreamModel: "qoder/auto"}

	rec := httptest.NewRecorder()
	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
	err := a.Forward(context.Background(), target, ports.ForwardRequest{BodyBytes: []byte(body), Stream: false}, rec)
	if err != nil {
		t.Fatalf("Forward: %v", err)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type=%q", ct)
	}
	root := gjson.ParseBytes(rec.Body.Bytes())
	if got := root.Get("choices.0.message.content").String(); got != "ABCD" {
		t.Errorf("content=%q want ABCD", got)
	}
	if got := root.Get("usage.total_tokens").Int(); got != 3 {
		t.Errorf("total_tokens=%d want 3", got)
	}
}

func TestForward_MissingIdentity(t *testing.T) {
	srv := newMockQoder(t, "")
	a := NewAdapter(stubPool{}, stubBreaker{}, Config{
		SecretLookup: func(string) (string, bool) { return "dt-bare-token-no-metadata", true },
	})
	u := &domain.Upstream{Name: "qoder-test", Protocol: domain.ProtocolQoder, BaseURL: srv.URL, CredentialRef: "k"}
	target := &domain.Target{Upstream: u, UpstreamModel: "qoder/auto"}

	rec := httptest.NewRecorder()
	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
	_ = a.Forward(context.Background(), target, ports.ForwardRequest{BodyBytes: []byte(body), Stream: false}, rec)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing identity, got %d", rec.Code)
	}
}

func TestForward_BillingBlockClassifiedAs429(t *testing.T) {
	// First frame is a billing block; no content bytes are sent.
	sse := "data: " + mustJSON(map[string]any{"statusCodeValue": 403, "body": `{"code":"112","pricingUrl":"https://qoder.com/pricing"}`}) + "\n\n"
	srv := newMockQoder(t, sse)

	a := NewAdapter(stubPool{}, stubBreaker{}, Config{
		SecretLookup: func(string) (string, bool) { return identityBlob("dt-token"), true },
	})
	// Single key so the 429 is terminal (relayed), not retried indefinitely.
	u := &domain.Upstream{Name: "qoder-test", Protocol: domain.ProtocolQoder, BaseURL: srv.URL, CredentialRef: "k"}
	target := &domain.Target{Upstream: u, UpstreamModel: "qoder/auto"}

	rec := httptest.NewRecorder()
	body := `{"model":"auto","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	_ = a.Forward(context.Background(), target, ports.ForwardRequest{BodyBytes: []byte(body), Stream: true}, rec)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("expected billing block relayed as 429, got %d", rec.Code)
	}
}

func TestBuildQoderPayload_HoistsSystem(t *testing.T) {
	body := `{"model":"auto","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hello"}]}`
	out, err := buildQoderPayload([]byte(body), "auto", "user-1", json.RawMessage(`{"key":"auto","is_reasoning":false}`))
	if err != nil {
		t.Fatalf("buildQoderPayload: %v", err)
	}
	root := gjson.ParseBytes(out)
	if got := root.Get("system").String(); got != "be brief" {
		t.Errorf("system=%q want 'be brief'", got)
	}
	msgs := root.Get("messages").Array()
	if len(msgs) != 1 || msgs[0].Get("role").String() != "user" {
		t.Errorf("system message not hoisted out of messages: %s", root.Get("messages").Raw)
	}
	if root.Get("session_type").String() != "qodercli" {
		t.Errorf("session_type=%q", root.Get("session_type").String())
	}
	if root.Get("chat_context.text").String() != "hello" {
		t.Errorf("chat_context.text=%q", root.Get("chat_context.text").String())
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
