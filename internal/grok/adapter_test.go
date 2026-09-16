package grok

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type mockPool struct {
	client *http.Client
}

func (p *mockPool) Client(u *domain.Upstream) *http.Client { return p.client }

type mockBreaker struct {
	reportedSuccess bool
	reportedFailure bool
}

func (b *mockBreaker) Allow(name string) error { return nil }

func (b *mockBreaker) Report(name string, ok bool) {
	if ok {
		b.reportedSuccess = true
	} else {
		b.reportedFailure = true
	}
}

// sse writes a Responses-API SSE event (event: line + data: line + blank line).
func sse(w http.ResponseWriter, eventType, data string) {
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, data)
}

func TestAdapter_StreamingRelay(t *testing.T) {
	t.Parallel()

	var reqBody []byte
	var reqHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqBody, _ = io.ReadAll(r.Body)
		reqHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		sse(w, "response.output_text.delta", `{"type":"response.output_text.delta","delta":"Hello"}`)
		sse(w, "response.output_text.delta", `{"type":"response.output_text.delta","delta":" world"}`)
		sse(w, "response.completed", `{"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":2}}}`)
		if fl != nil {
			fl.Flush()
		}
	}))
	defer srv.Close()

	adapter := NewAdapter(&mockPool{client: srv.Client()}, &mockBreaker{}, Config{
		SecretLookup: func(ref string) (string, bool) { return "ey-access-token", true },
	})
	u := &domain.Upstream{Name: "grok-test", Protocol: domain.ProtocolGrokCLI, BaseURL: srv.URL, CredentialRef: "grok-key-1"}
	target := &domain.Target{Upstream: u, UpstreamModel: "grok-build"}

	rec := httptest.NewRecorder()
	err := adapter.Forward(context.Background(), target, ports.ForwardRequest{
		Stream:    true,
		BodyBytes: []byte(`{"model":"grok-build","stream":true,"messages":[{"role":"user","content":"Hi"}]}`),
	}, rec)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
	out := rec.Body.String()
	assert.Contains(t, out, `"content":"Hello"`)
	assert.Contains(t, out, `"content":" world"`)
	assert.Contains(t, out, `"finish_reason":"stop"`)
	assert.Contains(t, out, "data: [DONE]")

	// Request shape + auth headers.
	assert.Equal(t, "grok-build", gjson.GetBytes(reqBody, "model").String())
	assert.Equal(t, "message", gjson.GetBytes(reqBody, "input.0.type").String())
	assert.Equal(t, "Bearer ey-access-token", reqHeaders.Get("Authorization"))
	assert.Equal(t, "xai-grok-cli", reqHeaders.Get("x-xai-token-auth"))
	assert.Equal(t, "grok-shell", reqHeaders.Get("x-grok-client-identifier"))
	assert.NotEmpty(t, reqHeaders.Get("x-grok-session-id"))
	assert.Equal(t, reqHeaders.Get("x-grok-session-id"), reqHeaders.Get("x-grok-conv-id"))
	assert.Contains(t, reqHeaders.Get("Accept"), "text/event-stream")
}

func TestAdapter_NonStreamingAggregation(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		sse(w, "response.reasoning_summary_text.delta", `{"delta":"let me think"}`)
		sse(w, "response.output_text.delta", `{"delta":"foo"}`)
		sse(w, "response.output_text.delta", `{"delta":"bar"}`)
		sse(w, "response.completed", `{"response":{"usage":{"input_tokens":7,"output_tokens":4}}}`)
	}))
	defer srv.Close()

	adapter := NewAdapter(&mockPool{client: srv.Client()}, &mockBreaker{}, Config{
		SecretLookup: func(ref string) (string, bool) { return "ey-tok", true },
	})
	u := &domain.Upstream{Name: "grok-test", Protocol: domain.ProtocolGrokCLI, BaseURL: srv.URL, CredentialRef: "grok-key-1"}
	target := &domain.Target{Upstream: u, UpstreamModel: "grok-build"}

	rec := httptest.NewRecorder()
	err := adapter.Forward(context.Background(), target, ports.ForwardRequest{
		Stream:    false,
		BodyBytes: []byte(`{"model":"grok-build","messages":[{"role":"user","content":"Hi"}]}`),
	}, rec)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	body := rec.Body.Bytes()
	require.True(t, gjson.ValidBytes(body))
	assert.Equal(t, "chat.completion", gjson.GetBytes(body, "object").String())
	assert.Equal(t, "foobar", gjson.GetBytes(body, "choices.0.message.content").String())
	assert.Equal(t, "let me think", gjson.GetBytes(body, "choices.0.message.reasoning_content").String())
	assert.Equal(t, "grok-build", gjson.GetBytes(body, "model").String())
	assert.Equal(t, int64(11), gjson.GetBytes(body, "usage.total_tokens").Int())
}

func TestAdapter_TokenFromKeySlotSecret(t *testing.T) {
	t.Parallel()

	// Harvester path: the ref is an opaque id and the OAuth access token lives in
	// KeySlot.Secret. resolveToken must use it.
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		sse(w, "response.output_text.delta", `{"delta":"ok"}`)
		sse(w, "response.completed", `{"response":{}}`)
	}))
	defer srv.Close()

	adapter := NewAdapter(&mockPool{client: srv.Client()}, &mockBreaker{}, Config{
		SecretLookup: func(ref string) (string, bool) { return "", false },
	})
	ring := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{
		{Ref: "grok-key-1156", Secret: "eyJ0eXAiOiJhdCtqd3QixxxAccessTokenValue"},
	})
	u := &domain.Upstream{Name: "grok-harvest", Protocol: domain.ProtocolGrokCLI, BaseURL: srv.URL, KeyRing: ring}
	target := &domain.Target{Upstream: u, UpstreamModel: "grok-build", CredentialRef: "grok-key-1156", KeySlot: ring.Slots[0]}

	rec := httptest.NewRecorder()
	err := adapter.Forward(context.Background(), target, ports.ForwardRequest{
		Stream:    false,
		BodyBytes: []byte(`{"model":"grok-build","messages":[{"role":"user","content":"Hi"}]}`),
	}, rec)
	require.NoError(t, err)
	assert.Equal(t, "Bearer eyJ0eXAiOiJhdCtqd3QixxxAccessTokenValue", gotAuth)
}

func TestAdapter_5xxTripsBreaker(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"upstream down"}`))
	}))
	defer srv.Close()

	breaker := &mockBreaker{}
	adapter := NewAdapter(&mockPool{client: srv.Client()}, breaker, Config{
		SecretLookup: func(ref string) (string, bool) { return "ey-tok", true },
	})
	u := &domain.Upstream{Name: "grok-5xx", Protocol: domain.ProtocolGrokCLI, BaseURL: srv.URL, CredentialRef: "k"}
	target := &domain.Target{Upstream: u, UpstreamModel: "grok-build"}

	rec := httptest.NewRecorder()
	err := adapter.Forward(context.Background(), target, ports.ForwardRequest{
		BodyBytes: []byte(`{"model":"grok-build","messages":[{"role":"user","content":"Hi"}]}`),
	}, rec)
	require.NoError(t, err) // relayed
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.True(t, breaker.reportedFailure)
}

func TestAdapter_401DoesNotTripBreaker(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"expired token"}`))
	}))
	defer srv.Close()

	breaker := &mockBreaker{}
	adapter := NewAdapter(&mockPool{client: srv.Client()}, breaker, Config{
		SecretLookup: func(ref string) (string, bool) { return "ey-tok", true },
	})
	u := &domain.Upstream{Name: "grok-401", Protocol: domain.ProtocolGrokCLI, BaseURL: srv.URL, CredentialRef: "k"}
	target := &domain.Target{Upstream: u, UpstreamModel: "grok-build"}

	rec := httptest.NewRecorder()
	_ = adapter.Forward(context.Background(), target, ports.ForwardRequest{
		BodyBytes: []byte(`{"model":"grok-build","messages":[{"role":"user","content":"Hi"}]}`),
	}, rec)
	// A 401 (expired/invalid token) is a credential-layer problem, not a host failure.
	assert.False(t, breaker.reportedFailure)
}

func TestAdapter_429CooldownAndFailover(t *testing.T) {
	t.Parallel()

	var attempt int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt == 1 {
			w.Header().Set("Retry-After", "10")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		sse(w, "response.output_text.delta", `{"delta":"second key ok"}`)
		sse(w, "response.completed", `{"response":{}}`)
	}))
	defer srv.Close()

	breaker := &mockBreaker{}
	ring := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{
		{Ref: "grok-key-14"},
		{Ref: "grok-key-15"},
	})
	u := &domain.Upstream{Name: "grok-multi", Protocol: domain.ProtocolGrokCLI, BaseURL: srv.URL, KeyRing: ring}
	target := &domain.Target{
		Upstream:      u,
		UpstreamModel: "grok-build",
		CredentialRef: "grok-key-14",
		KeySlot:       ring.Slots[0],
	}

	adapter := NewAdapter(&mockPool{client: srv.Client()}, breaker, Config{
		SecretLookup: func(ref string) (string, bool) { return "ey-tok-" + ref, true },
	})

	rec := httptest.NewRecorder()
	err := adapter.Forward(context.Background(), target, ports.ForwardRequest{
		Stream:    false,
		BodyBytes: []byte(`{"model":"grok-build","messages":[{"role":"user","content":"Hi"}]}`),
	}, rec)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "second key ok")
	assert.True(t, ring.Slots[0].CooldownUntil.Load() > 0)
	assert.False(t, breaker.reportedFailure)
	assert.True(t, breaker.reportedSuccess)
}

func TestAdapter_UpstreamErrorEvent(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		sse(w, "error", `{"error":{"message":"model_not_found"}}`)
	}))
	defer srv.Close()

	adapter := NewAdapter(&mockPool{client: srv.Client()}, &mockBreaker{}, Config{
		SecretLookup: func(ref string) (string, bool) { return "ey-tok", true },
	})
	u := &domain.Upstream{Name: "grok-err", Protocol: domain.ProtocolGrokCLI, BaseURL: srv.URL, CredentialRef: "k"}
	target := &domain.Target{Upstream: u, UpstreamModel: "grok-build"}

	// Streaming: the error is surfaced inline in the SSE stream.
	rec := httptest.NewRecorder()
	err := adapter.Forward(context.Background(), target, ports.ForwardRequest{
		Stream:    true,
		BodyBytes: []byte(`{"model":"grok-build","stream":true,"messages":[{"role":"user","content":"Hi"}]}`),
	}, rec)
	require.NoError(t, err)
	assert.Contains(t, rec.Body.String(), "model_not_found")
}

func TestAdapter_InvalidRequestBody(t *testing.T) {
	t.Parallel()

	adapter := NewAdapter(&mockPool{client: http.DefaultClient}, &mockBreaker{}, Config{
		SecretLookup: func(ref string) (string, bool) { return "ey-tok", true },
	})
	u := &domain.Upstream{Name: "grok", Protocol: domain.ProtocolGrokCLI, BaseURL: "https://cli-chat-proxy.grok.com/v1", CredentialRef: "k"}
	target := &domain.Target{Upstream: u, UpstreamModel: "grok-build"}

	rec := httptest.NewRecorder()
	err := adapter.Forward(context.Background(), target, ports.ForwardRequest{
		BodyBytes: []byte(`{"model":"grok-build","messages":[]}`),
	}, rec)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.True(t, strings.Contains(rec.Body.String(), "content"))
}

func TestAdapter_ProtocolAndNilTarget(t *testing.T) {
	t.Parallel()

	adapter := NewAdapter(&mockPool{client: http.DefaultClient}, &mockBreaker{}, Config{})
	assert.Equal(t, domain.ProtocolGrokCLI, adapter.Protocol())

	rec := httptest.NewRecorder()
	err := adapter.Forward(context.Background(), nil, ports.ForwardRequest{}, rec)
	assert.Error(t, err)
}
