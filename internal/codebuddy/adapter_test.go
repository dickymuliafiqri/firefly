package codebuddy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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

func (p *mockPool) Client(u *domain.Upstream) *http.Client {
	return p.client
}

type mockBreaker struct {
	reportedSuccess bool
	reportedFailure bool
}

func (b *mockBreaker) Allow(name string) error {
	return nil
}

func (b *mockBreaker) Report(name string, ok bool) {
	if ok {
		b.reportedSuccess = true
	} else {
		b.reportedFailure = true
	}
}

func TestAdapter_NonStreamingAggregation(t *testing.T) {
	t.Parallel()

	// Upstream returns SSE chunks
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "CLI/2.108.1 CodeBuddy/2.108.1", r.Header.Get("User-Agent"))
		assert.Equal(t, "copilot.tencent.com", r.Header.Get("X-Domain"))
		assert.Equal(t, "SaaS", r.Header.Get("X-Product"))
		assert.Equal(t, "Bearer test-secret-token", r.Header.Get("Authorization"))

		bodyBytes, _ := io.ReadAll(r.Body)
		assert.True(t, gjson.GetBytes(bodyBytes, "stream").Bool())

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		chunks := []string{
			`data: {"id":"chatcmpl-123","created":1700000000,"model":"glm-5.2","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}]}`,
			`data: {"id":"chatcmpl-123","created":1700000000,"model":"glm-5.2","choices":[{"index":0,"delta":{"content":" World!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
			`data: [DONE]`,
		}

		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(w, "%s\n\n", chunk)
		}
	}))
	defer upstreamServer.Close()

	pool := &mockPool{client: upstreamServer.Client()}
	breaker := &mockBreaker{}
	adapter := NewAdapter(pool, breaker, Config{
		SecretLookup: func(ref string) (string, bool) {
			return "test-secret-token", true
		},
	})

	u := &domain.Upstream{
		Name:          "codebuddy-test",
		Protocol:      domain.ProtocolCodeBuddyCN,
		BaseURL:       upstreamServer.URL,
		CredentialRef: "test-secret-token",
	}
	target := &domain.Target{
		Upstream:      u,
		UpstreamModel: "glm-5.2",
	}

	req := ports.ForwardRequest{
		Stream:    false, // Client requested non-streaming
		BodyBytes: []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"Hi"}]}`),
	}

	rec := httptest.NewRecorder()
	err := adapter.Forward(context.Background(), target, req, rec)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	respBody := rec.Body.Bytes()
	require.True(t, gjson.ValidBytes(respBody))
	assert.Equal(t, "chatcmpl-123", gjson.GetBytes(respBody, "id").String())
	assert.Equal(t, "chat.completion", gjson.GetBytes(respBody, "object").String())
	assert.Equal(t, "Hello World!", gjson.GetBytes(respBody, "choices.0.message.content").String())
	assert.Equal(t, "stop", gjson.GetBytes(respBody, "choices.0.finish_reason").String())
	assert.Equal(t, int64(12), gjson.GetBytes(respBody, "usage.total_tokens").Int())
	assert.True(t, breaker.reportedSuccess)
}

func TestAdapter_StreamingRelay(t *testing.T) {
	t.Parallel()

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		require.True(t, ok)

		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"streamed\"}}]}\n\n")
		flusher.Flush()
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer upstreamServer.Close()

	pool := &mockPool{client: upstreamServer.Client()}
	breaker := &mockBreaker{}
	adapter := NewAdapter(pool, breaker, Config{
		SecretLookup: func(ref string) (string, bool) {
			return "token", true
		},
	})

	u := &domain.Upstream{
		Name:          "codebuddy-stream-test",
		Protocol:      domain.ProtocolCodeBuddyCN,
		BaseURL:       upstreamServer.URL,
		CredentialRef: "token",
	}
	target := &domain.Target{
		Upstream: u,
	}

	req := ports.ForwardRequest{
		Stream:    true, // Client requested streaming
		BodyBytes: []byte(`{"model":"glm-5.2","stream":true,"messages":[{"role":"user","content":"Hi"}]}`),
	}

	rec := httptest.NewRecorder()
	err := adapter.Forward(context.Background(), target, req, rec)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
	assert.Contains(t, rec.Body.String(), "streamed")
}

func TestAdapter_SystemPromptSanitization(t *testing.T) {
	t.Parallel()

	var receivedBody []byte
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstreamServer.Close()

	pool := &mockPool{client: upstreamServer.Client()}
	adapter := NewAdapter(pool, &mockBreaker{}, Config{
		SecretLookup: func(ref string) (string, bool) { return "token", true },
	})

	u := &domain.Upstream{
		Name:          "codebuddy-cn",
		Protocol:      domain.ProtocolCodeBuddyCN,
		BaseURL:       upstreamServer.URL,
		CredentialRef: "token",
	}
	target := &domain.Target{Upstream: u}

	// 1. Prompt with Agent trigger phrase
	req := ports.ForwardRequest{
		Stream: false,
		BodyBytes: []byte(`{
			"model": "glm-5.2",
			"reasoning_effort": "high",
			"messages": [
				{"role": "system", "content": "You are Claude Code, Anthropic's official CLI tool."},
				{"role": "user", "content": "Write a test"}
			]
		}`),
	}

	rec := httptest.NewRecorder()
	err := adapter.Forward(context.Background(), target, req, rec)
	require.NoError(t, err)

	// Verify sanitized
	assert.Equal(t, neutralPrompt, gjson.GetBytes(receivedBody, "messages.0.content").String())
	assert.Equal(t, "auto", gjson.GetBytes(receivedBody, "reasoning_summary").String())
	assert.True(t, gjson.GetBytes(receivedBody, "stream").Bool())

	// 2. Legitimate short user system prompt should NOT be replaced
	reqNormal := ports.ForwardRequest{
		Stream: false,
		BodyBytes: []byte(`{
			"model": "glm-5.2",
			"messages": [
				{"role": "system", "content": "You are a concise translator."},
				{"role": "user", "content": "Hello"}
			]
		}`),
	}
	rec = httptest.NewRecorder()
	err = adapter.Forward(context.Background(), target, reqNormal, rec)
	require.NoError(t, err)

	assert.Equal(t, "You are a concise translator.", gjson.GetBytes(receivedBody, "messages.0.content").String())
}

func TestAdapter_IntlHeaders(t *testing.T) {
	t.Parallel()

	var headers http.Header
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstreamServer.Close()

	pool := &mockPool{client: upstreamServer.Client()}
	adapter := NewAdapter(pool, &mockBreaker{}, Config{
		SecretLookup: func(ref string) (string, bool) { return "token", true },
	})

	u := &domain.Upstream{
		Name:          "codebuddy-intl",
		Protocol:      domain.ProtocolCodeBuddyIntl,
		BaseURL:       upstreamServer.URL,
		CredentialRef: "token",
	}
	target := &domain.Target{Upstream: u}

	req := ports.ForwardRequest{
		Stream:    false,
		BodyBytes: []byte(`{"model": "glm-5.2"}`),
	}

	rec := httptest.NewRecorder()
	_ = adapter.Forward(context.Background(), target, req, rec)

	assert.Equal(t, "IDE/2.108.1 CodeBuddy/2.108.1", headers.Get("User-Agent"))
	assert.Equal(t, "www.codebuddy.ai", headers.Get("X-Domain"))
	assert.Equal(t, "IDE", headers.Get("X-IDE-Type"))
	assert.Equal(t, "IDE", headers.Get("X-IDE-Name"))
}

func TestAdapter_FailuresAndBreaker(t *testing.T) {
	t.Parallel()

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal_error"}`))
	}))
	defer upstreamServer.Close()

	pool := &mockPool{client: upstreamServer.Client()}
	breaker := &mockBreaker{}
	adapter := NewAdapter(pool, breaker, Config{
		SecretLookup: func(ref string) (string, bool) { return "token", true },
	})

	u := &domain.Upstream{
		Name:          "codebuddy-500",
		Protocol:      domain.ProtocolCodeBuddyCN,
		BaseURL:       upstreamServer.URL,
		CredentialRef: "token",
	}
	target := &domain.Target{Upstream: u}

	rec := httptest.NewRecorder()
	err := adapter.Forward(context.Background(), target, ports.ForwardRequest{BodyBytes: []byte(`{}`)}, rec)
	require.NoError(t, err) // relayed error
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.True(t, breaker.reportedFailure)
}

func TestAdapter_Forward_429CooldownAndFailover(t *testing.T) {
	t.Parallel()

	var attemptCount int
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attemptCount++
		if attemptCount == 1 {
			w.Header().Set("Retry-After", "10")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error": "rate limit exceeded"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Key 2 Success\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstreamServer.Close()

	breaker := &mockBreaker{}
	pool := &mockPool{client: upstreamServer.Client()}

	ring := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{
		{Ref: "oauth:cb-1"},
		{Ref: "oauth:cb-2"},
	})

	u := &domain.Upstream{
		Name:     "codebuddy-multi",
		Protocol: domain.ProtocolCodeBuddyCN,
		BaseURL:  upstreamServer.URL,
		KeyRing:  ring,
	}
	target := &domain.Target{
		Upstream:      u,
		UpstreamModel: "glm-5.2",
		CredentialRef: "oauth:cb-1",
		KeySlot:       ring.Slots[0],
	}

	adapter := NewAdapter(pool, breaker, Config{
		TokenResolver: func(ctx context.Context, ref string) (string, error) {
			return "tok-" + ref, nil
		},
	})

	rec := httptest.NewRecorder()
	err := adapter.Forward(context.Background(), target, ports.ForwardRequest{
		Stream:    false,
		BodyBytes: []byte(`{"model": "glm-5.2"}`),
	}, rec)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Key 2 Success")
	// Slot 1 must be in cooldown
	assert.True(t, ring.Slots[0].CooldownUntil.Load() > 0)
	// Key 429 must NEVER trip circuit breaker (Invariant #2)
	assert.False(t, breaker.reportedFailure)
	assert.True(t, breaker.reportedSuccess)
}

