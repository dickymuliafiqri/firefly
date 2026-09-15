package cline

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type mockClientPool struct {
	client *http.Client
}

func (m *mockClientPool) Client(u *domain.Upstream) *http.Client {
	return m.client
}

type mockBreaker struct {
	reports []bool
}

func (m *mockBreaker) Allow(name string) error { return nil }
func (m *mockBreaker) Report(name string, ok bool) {
	m.reports = append(m.reports, ok)
}

func TestUnwrapEnvelope(t *testing.T) {
	t.Parallel()

	// Wrapped Cline envelope
	wrapped := []byte(`{"success":true,"data":{"id":"chatcmpl-123","choices":[{"message":{"content":"Hello from Cline"}}]}}`)
	unwrapped := UnwrapEnvelope(wrapped)
	assert.Equal(t, "chatcmpl-123", gjson.GetBytes(unwrapped, "id").String())
	assert.Equal(t, "Hello from Cline", gjson.GetBytes(unwrapped, "choices.0.message.content").String())
	assert.False(t, gjson.GetBytes(unwrapped, "success").Exists())

	// Standard response (not wrapped)
	standard := []byte(`{"id":"chatcmpl-456","choices":[{"message":{"content":"Standard response"}}]}`)
	assert.Equal(t, standard, UnwrapEnvelope(standard))

	// Error response with success=false
	errResp := []byte(`{"success":false,"error":{"message":"Quota exceeded"}}`)
	assert.Equal(t, errResp, UnwrapEnvelope(errResp))
}

func BenchmarkUnwrapEnvelope(b *testing.B) {
	wrapped := []byte(`{"success":true,"data":{"id":"chatcmpl-123","choices":[{"message":{"content":"Hello from Cline"}}]}}`)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = UnwrapEnvelope(wrapped)
	}
}

func TestAdapter_Forward_Streaming(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/chat/completions", r.URL.Path)
		assert.Equal(t, "Bearer cline-access-token", r.Header.Get("Authorization"))
		assert.Equal(t, "https://cline.bot", r.Header.Get("HTTP-Referer"))
		assert.Equal(t, "Cline", r.Header.Get("X-Title"))

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		require.True(t, ok)

		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Cline stream\"}}]}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	breaker := &mockBreaker{}
	pool := &mockClientPool{client: server.Client()}

	adapter := NewAdapter(pool, breaker, Config{
		TokenResolver: func(ctx context.Context, ref string) (string, error) {
			if ref == "oauth:cline-test" {
				return "cline-access-token", nil
			}
			return "", fmt.Errorf("unknown ref")
		},
	})

	u := &domain.Upstream{
		Name:          "cline-upstream",
		BaseURL:       server.URL,
		CredentialRef: "oauth:cline-test",
	}
	target := &domain.Target{
		Upstream:      u,
		UpstreamModel: "anthropic/claude-sonnet-4.6",
		CredentialRef: "oauth:cline-test",
	}

	reqBody := []byte(`{
		"model": "claude-sonnet",
		"messages": [{"role": "user", "content": "Hello"}],
		"stream": true
	}`)

	rec := httptest.NewRecorder()
	req := ports.ForwardRequest{
		Method:    http.MethodPost,
		Path:      "/v1/chat/completions",
		BodyBytes: reqBody,
		Stream:    true,
	}

	err := adapter.Forward(context.Background(), target, req, rec)
	require.NoError(t, err)

	assert.Contains(t, rec.Body.String(), "Cline stream")
	assert.Contains(t, rec.Body.String(), "data: [DONE]\n\n")
}

func TestAdapter_Forward_NonStreamingUnwrapped(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/chat/completions", r.URL.Path)
		assert.Equal(t, "Bearer cline-secret-env", r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Return wrapped Cline response
		_, _ = w.Write([]byte(`{
			"success": true,
			"data": {
				"id": "chatcmpl-cline-999",
				"object": "chat.completion",
				"model": "anthropic/claude-sonnet-4.6",
				"choices": [
					{
						"index": 0,
						"message": {
							"role": "assistant",
							"content": "Unwrapped Cline completion"
						},
						"finish_reason": "stop"
					}
				],
				"usage": {
					"prompt_tokens": 15,
					"completion_tokens": 8,
					"total_tokens": 23
				}
			}
		}`))
	}))
	defer server.Close()

	breaker := &mockBreaker{}
	pool := &mockClientPool{client: server.Client()}

	adapter := NewAdapter(pool, breaker, Config{
		SecretLookup: func(ref string) (string, bool) {
			if ref == "CLINE_KEY" {
				return "cline-secret-env", true
			}
			return "", false
		},
	})

	u := &domain.Upstream{
		Name:          "cline-upstream",
		BaseURL:       server.URL,
		CredentialRef: "CLINE_KEY",
	}
	target := &domain.Target{
		Upstream:      u,
		UpstreamModel: "anthropic/claude-sonnet-4.6",
		CredentialRef: "CLINE_KEY",
	}

	reqBody := []byte(`{"model": "claude-sonnet", "messages": [{"role": "user", "content": "Hello"}]}`)
	rec := httptest.NewRecorder()
	req := ports.ForwardRequest{
		Method:    http.MethodPost,
		Path:      "/v1/chat/completions",
		BodyBytes: reqBody,
	}

	err := adapter.Forward(context.Background(), target, req, rec)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.Bytes()
	require.True(t, gjson.ValidBytes(body))
	assert.Equal(t, "chatcmpl-cline-999", gjson.GetBytes(body, "id").String())
	assert.Equal(t, "Unwrapped Cline completion", gjson.GetBytes(body, "choices.0.message.content").String())
	assert.False(t, gjson.GetBytes(body, "success").Exists())
}

func TestAdapter_Forward_429CooldownAndFailover(t *testing.T) {
	t.Parallel()

	var attemptCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		att := attemptCount.Add(1)
		if att == 1 {
			w.Header().Set("Retry-After", "10")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error": "rate limit exceeded"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices": [{"message": {"content": "Success on slot 2"}}]}`))
	}))
	defer server.Close()

	breaker := &mockBreaker{}
	pool := &mockClientPool{client: server.Client()}

	ring := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{
		{Ref: "oauth:cline-1"},
		{Ref: "oauth:cline-2"},
	})

	u := &domain.Upstream{
		Name:    "cline-multi",
		BaseURL: server.URL,
		KeyRing: ring,
	}
	target := &domain.Target{
		Upstream:      u,
		UpstreamModel: "anthropic/claude-sonnet-4.6",
		CredentialRef: "oauth:cline-1",
		KeySlot:       ring.Slots[0],
	}

	adapter := NewAdapter(pool, breaker, Config{
		TokenResolver: func(ctx context.Context, ref string) (string, error) {
			return "tok-" + ref, nil
		},
	})

	reqBody := []byte(`{"model": "claude-sonnet", "messages": [{"role": "user", "content": "Hi"}]}`)
	rec := httptest.NewRecorder()
	req := ports.ForwardRequest{
		Method:    http.MethodPost,
		Path:      "/v1/chat/completions",
		BodyBytes: reqBody,
	}

	err := adapter.Forward(context.Background(), target, req, rec)
	require.NoError(t, err)

	assert.Equal(t, int32(2), attemptCount.Load())
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "Success on slot 2", gjson.GetBytes(rec.Body.Bytes(), "choices.0.message.content").String())

	// Slot 1 should be in cooldown
	assert.True(t, ring.Slots[0].IsInCooldown(time.Now().UnixNano()))

	// 429 must NOT report false to circuit breaker
	for _, rep := range breaker.reports {
		assert.True(t, rep)
	}
}
