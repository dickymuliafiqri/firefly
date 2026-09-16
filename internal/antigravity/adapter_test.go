package antigravity

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	allowed atomic.Bool
	reports []bool
}

func (m *mockBreaker) Allow(name string) error {
	if m.allowed.Load() {
		return fmt.Errorf("circuit open for %s", name)
	}
	return nil
}

func (m *mockBreaker) Report(name string, ok bool) {
	m.reports = append(m.reports, ok)
}

func TestAdapter_Forward_Streaming(t *testing.T) {
	t.Parallel()

	// Mock upstream server simulating Google Cloud Code streamGenerateContent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1internal:streamGenerateContent", r.URL.Path)
		assert.Equal(t, "alt=sse", r.URL.RawQuery)
		assert.Equal(t, "Bearer test-access-token", r.Header.Get("Authorization"))
		assert.Equal(t, "antigravity/ide/2.11.0 darwin/arm64", r.Header.Get("User-Agent"))

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		require.True(t, ok)

		// 1. Text chunk
		_, _ = w.Write([]byte("data: {\"response\": {\"candidates\": [{\"content\": {\"parts\": [{\"text\": \"Hello\"}]}}]}}\n\n"))
		flusher.Flush()

		// 2. Finish chunk
		_, _ = w.Write([]byte("data: {\"response\": {\"candidates\": [{\"finishReason\": \"STOP\"}]}}\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	breaker := &mockBreaker{}
	pool := &mockClientPool{client: server.Client()}

	adapter := NewAdapter(pool, breaker, Config{
		TokenResolver: func(ctx context.Context, ref string) (string, error) {
			if ref == "oauth:test-conn" {
				return "test-access-token", nil
			}
			return "", fmt.Errorf("not found")
		},
	})

	u := &domain.Upstream{
		Name:          "antigravity-upstream",
		BaseURL:       server.URL,
		CredentialRef: "oauth:test-conn",
	}
	target := &domain.Target{
		Upstream:      u,
		UpstreamModel: "gemini-2.5-pro",
		CredentialRef: "oauth:test-conn",
	}

	reqBody := []byte(`{
		"model": "gemini-2.5-pro",
		"messages": [{"role": "user", "content": "Hi"}],
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

	respBody := rec.Body.String()
	assert.Contains(t, respBody, "data: [DONE]\n\n")
	assert.Contains(t, respBody, `"content":"Hello"`)
	assert.Contains(t, respBody, `"finish_reason":"stop"`)
}

func TestAdapter_Forward_NonStreaming(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1internal:generateContent", r.URL.Path)
		assert.Equal(t, "Bearer my-secret-token", r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		_, _ = w.Write([]byte(`{
			"response": {
				"candidates": [
					{
						"content": {
							"role": "model",
							"parts": [{"text": "Non-streaming answer"}]
						},
						"finishReason": "STOP"
					}
				],
				"usageMetadata": {
					"promptTokenCount": 10,
					"candidatesTokenCount": 5
				}
			}
		}`))
	}))
	defer server.Close()

	breaker := &mockBreaker{}
	pool := &mockClientPool{client: server.Client()}

	adapter := NewAdapter(pool, breaker, Config{
		SecretLookup: func(ref string) (string, bool) {
			if ref == "SECRET_ENV" {
				return "my-secret-token", true
			}
			return "", false
		},
	})

	u := &domain.Upstream{
		Name:          "antigravity-upstream",
		BaseURL:       server.URL,
		CredentialRef: "SECRET_ENV",
	}
	target := &domain.Target{
		Upstream:      u,
		UpstreamModel: "gemini-2.5-flash",
		CredentialRef: "SECRET_ENV",
	}

	reqBody := []byte(`{
		"model": "gemini-2.5-flash",
		"messages": [{"role": "user", "content": "Hi"}],
		"stream": false
	}`)

	rec := httptest.NewRecorder()
	req := ports.ForwardRequest{
		Method:    http.MethodPost,
		Path:      "/v1/chat/completions",
		BodyBytes: reqBody,
		Stream:    false,
	}

	err := adapter.Forward(context.Background(), target, req, rec)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.Bytes()
	require.True(t, gjson.ValidBytes(body))
	assert.Equal(t, "Non-streaming answer", gjson.GetBytes(body, "choices.0.message.content").String())
	assert.Equal(t, "stop", gjson.GetBytes(body, "choices.0.finish_reason").String())
	assert.Equal(t, int64(10), gjson.GetBytes(body, "usage.prompt_tokens").Int())
	assert.Equal(t, int64(5), gjson.GetBytes(body, "usage.completion_tokens").Int())
}

func TestAdapter_Forward_429CooldownAndFailover(t *testing.T) {
	t.Parallel()

	var attemptCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		att := attemptCount.Add(1)
		if att == 1 {
			// First key returns 429
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error": {"message": "Resource exhausted"}}`))
			return
		}
		// Second key succeeds
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"response": {
				"candidates": [
					{
						"content": {"parts": [{"text": "Success on key 2"}]},
						"finishReason": "STOP"
					}
				]
			}
		}`))
	}))
	defer server.Close()

	breaker := &mockBreaker{}
	pool := &mockClientPool{client: server.Client()}

	ring := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{
		{Ref: "oauth:conn-1"},
		{Ref: "oauth:conn-2"},
	})

	u := &domain.Upstream{
		Name:    "antigravity-multi-key",
		BaseURL: server.URL,
		KeyRing: ring,
	}
	target := &domain.Target{
		Upstream:      u,
		UpstreamModel: "gemini-2.5-pro",
		CredentialRef: "oauth:conn-1",
		KeySlot:       ring.Slots[0],
	}

	adapter := NewAdapter(pool, breaker, Config{
		TokenResolver: func(ctx context.Context, ref string) (string, error) {
			return "tok-" + ref, nil
		},
	})

	reqBody := []byte(`{"model": "gemini-2.5-pro", "messages": [{"role": "user", "content": "Hello"}]}`)
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
	assert.Equal(t, "Success on key 2", gjson.GetBytes(rec.Body.Bytes(), "choices.0.message.content").String())

	// Crucial rule: 429 must NOT report false to circuit breaker!
	for _, rep := range breaker.reports {
		assert.True(t, rep, "circuit breaker should not have received false for 429")
	}

	// First key should be in cooldown
	assert.True(t, ring.Slots[0].IsInCooldown(time.Now().UnixNano()))
}

func TestAdapter_Forward_ProjectIDResolution(t *testing.T) {
	t.Parallel()

	var receivedProject string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedProject = gjson.GetBytes(body, "project").String()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"response": {"candidates": [{"content": {"parts": [{"text": "OK"}]}}]}}`))
	}))
	defer server.Close()

	breaker := &mockBreaker{}
	pool := &mockClientPool{client: server.Client()}

	adapter := NewAdapter(pool, breaker, Config{
		TokenResolver: func(ctx context.Context, ref string) (string, error) {
			return "tok-123", nil
		},
		ConnectionLookup: func(ctx context.Context, ref string) (*domain.OAuthConnection, error) {
			if ref == "oauth:my-ag-conn" {
				return &domain.OAuthConnection{
					ID: "my-ag-conn",
					ProviderSpecificData: map[string]string{
						"project_id": "google-companion-project-888",
					},
				}, nil
			}
			return nil, errors.New("not found")
		},
	})

	u := &domain.Upstream{
		Name:          "antigravity-proj-test",
		BaseURL:       server.URL,
		CredentialRef: "oauth:my-ag-conn",
	}
	target := &domain.Target{
		Upstream:      u,
		UpstreamModel: "gemini-3.8-flash-high",
		CredentialRef: "oauth:my-ag-conn",
	}

	reqBody := []byte(`{"model": "gemini-3.8-flash-high", "messages": [{"role": "user", "content": "Hi"}]}`)
	rec := httptest.NewRecorder()
	req := ports.ForwardRequest{
		Method:    http.MethodPost,
		Path:      "/v1/chat/completions",
		BodyBytes: reqBody,
	}

	err := adapter.Forward(context.Background(), target, req, rec)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "google-companion-project-888", receivedProject)
}

