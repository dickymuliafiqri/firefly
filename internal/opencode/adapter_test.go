package opencode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
	allowed bool
	report  bool
}

func (m *mockBreaker) Allow(name string) error {
	if !m.allowed {
		return http.ErrHandlerTimeout
	}
	return nil
}

func (m *mockBreaker) Report(name string, ok bool) {
	m.report = ok
}

func TestResolveSessionID(t *testing.T) {
	t.Run("preserves valid canonical native header", func(t *testing.T) {
		canonical := "ses_f50b1e8a4175RyWGSMj14y2e90"
		assert.True(t, IsValidSessionID(canonical))
		h := make(http.Header)
		h.Set(HeaderSession, canonical)
		id := ResolveSessionID(h, "tenant1", "req1")
		assert.Equal(t, canonical, id)
	})

	t.Run("normalizes non-canonical header to valid format", func(t *testing.T) {
		h := make(http.Header)
		h.Set(HeaderSession, "custom-legacy-session-id")
		id1 := ResolveSessionID(h, "tenant1", "req1")
		id2 := ResolveSessionID(h, "tenant1", "req2")
		assert.True(t, IsValidSessionID(id1))
		assert.Equal(t, id1, id2, "same custom session should map to same canonical session")
	})

	t.Run("generates deterministic canonical hash when missing", func(t *testing.T) {
		id1 := ResolveSessionID(nil, "tenant1", "req1")
		id2 := ResolveSessionID(nil, "tenant1", "req1")
		assert.True(t, IsValidSessionID(id1))
		assert.Equal(t, id1, id2)

		// Different tenant produces different session
		id3 := ResolveSessionID(nil, "tenant2", "req1")
		assert.True(t, IsValidSessionID(id3))
		assert.NotEqual(t, id1, id3)
	})

	t.Run("generates valid descending IDs", func(t *testing.T) {
		ses := GenerateSessionID()
		assert.True(t, IsValidSessionID(ses))
		req := GenerateRequestID()
		assert.True(t, strings.HasPrefix(req, "msg_"))
		assert.Equal(t, 30, len(req))
	})
}

func TestSanitizeTools(t *testing.T) {
	body := []byte(`{
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "run_command",
					"parameters": {
						"type": "object"
					}
				}
			}
		]
	}`)

	out, modified := SanitizeTools(body)
	assert.True(t, modified)
	assert.True(t, gjson.GetBytes(out, "tools.0.function.parameters.properties").Exists())
}

func TestNormalizeReasoning(t *testing.T) {
	t.Run("converts reasoning_effort string", func(t *testing.T) {
		body := []byte(`{"model": "qwen", "reasoning_effort": "high"}`)
		out, modified := NormalizeReasoning(body)
		assert.True(t, modified)
		assert.False(t, gjson.GetBytes(out, "reasoning_effort").Exists())
		assert.Equal(t, "high", gjson.GetBytes(out, "reasoning.effort").String())
		assert.Equal(t, "auto", gjson.GetBytes(out, "reasoning.summary").String())
	})
}

func TestTransformChatToResponses(t *testing.T) {
	t.Run("paid mode extracts instructions and leaves tools untouched", func(t *testing.T) {
		body := []byte(`{
			"model": "muse-spark-1.2-contributor",
			"messages": [
				{"role": "system", "content": "You are an assistant"},
				{"role": "user", "content": "Hello world"}
			],
			"max_tokens": 1024,
			"reasoning_effort": "medium"
		}`)

		out, err := TransformChatToResponses(body, "muse-spark-1.2-contributor", false)
		require.NoError(t, err)

		assert.Equal(t, "muse-spark-1.2-contributor", gjson.GetBytes(out, "model").String())
		assert.Equal(t, int64(1024), gjson.GetBytes(out, "max_output_tokens").Int())
		assert.Equal(t, "medium", gjson.GetBytes(out, "reasoning.effort").String())
		assert.Equal(t, "You are an assistant", gjson.GetBytes(out, "instructions").String())
		assert.Equal(t, 1, len(gjson.GetBytes(out, "input").Array()))
	})

	t.Run("free mode injects official prompt and official tools", func(t *testing.T) {
		body := []byte(`{
			"model": "muse-spark-1.3-contributor-free",
			"messages": [
				{"role": "user", "content": "hi"}
			]
		}`)

		out, err := TransformChatToResponses(body, "muse-spark-1.3-contributor-free", true)
		require.NoError(t, err)

		assert.Equal(t, "muse-spark-1.3-contributor-free", gjson.GetBytes(out, "model").String())
		assert.Contains(t, gjson.GetBytes(out, "instructions").String(), "You are opencode")
		assert.Contains(t, gjson.GetBytes(out, "instructions").String(), "muse-spark-1.3-contributor-free")
		assert.Equal(t, int64(32000), gjson.GetBytes(out, "max_output_tokens").Int())
		assert.NotEmpty(t, gjson.GetBytes(out, "tools").Array())
	})

	t.Run("clamps when max_tokens < 16", func(t *testing.T) {
		lowTokensBody := []byte(`{"model":"muse-spark-1.2-contributor","messages":[{"role":"user","content":"hi"}],"max_tokens":5}`)
		outClamped, err := TransformChatToResponses(lowTokensBody, "muse-spark-1.2-contributor", false)
		require.NoError(t, err)
		assert.Equal(t, int64(16), gjson.GetBytes(outClamped, "max_output_tokens").Int())
	})
}

func TestAdapter_FreeMode(t *testing.T) {
	var receivedAuth string
	var receivedUA string
	var receivedClient string
	var receivedSession string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		receivedUA = r.Header.Get("User-Agent")
		receivedClient = r.Header.Get(HeaderClient)
		receivedSession = r.Header.Get(HeaderSession)

		assert.Equal(t, "/chat/completions", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id": "cmpl-1", "choices": [{"message": {"role": "assistant", "content": "hello"}}]}`))
	}))
	defer server.Close()

	u := &domain.Upstream{
		Name:     "opencode-free",
		Protocol: domain.ProtocolOpenCode,
		BaseURL:  server.URL,
	}
	target := &domain.Target{
		Upstream:      u,
		UpstreamModel: "glm-5.3",
	}

	adapter := NewAdapter(&mockClientPool{client: server.Client()}, &mockBreaker{allowed: true}, Config{})

	req := ports.ForwardRequest{
		Method:    http.MethodPost,
		Path:      "/chat/completions",
		BodyBytes: []byte(`{"model": "glm-5.3", "messages": [{"role": "user", "content": "hi"}]}`),
	}

	rec := httptest.NewRecorder()
	err := adapter.Forward(context.Background(), target, req, rec)
	require.NoError(t, err)

	assert.Equal(t, "Bearer public", receivedAuth)
	assert.Equal(t, OpenCodeUserAgent, receivedUA)
	assert.Equal(t, "cli", receivedClient)
	assert.True(t, strings.HasPrefix(receivedSession, "ses_"))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAdapter_GoMode_ResponsesStream(t *testing.T) {
	var receivedAuth string
	var receivedPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		receivedPath = r.URL.Path

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		require.True(t, ok)

		_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"delta\": \"Hi from Muse!\"}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("event: response.completed\ndata: {\"response\": {\"usage\": {\"input_tokens\": 10, \"output_tokens\": 5}}}\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	u := &domain.Upstream{
		Name:          "opencode-go",
		Protocol:      domain.ProtocolOpenCode,
		BaseURL:       server.URL,
		CredentialRef: "oc-secret-1",
	}
	target := &domain.Target{
		Upstream:      u,
		UpstreamModel: "muse-spark-1.2-contributor",
		KeySlot: &domain.KeySlot{
			Ref:    "oc-secret-1",
			Secret: "sk-oc-go-testkey",
		},
	}

	adapter := NewAdapter(&mockClientPool{client: server.Client()}, &mockBreaker{allowed: true}, Config{})

	req := ports.ForwardRequest{
		Method:    http.MethodPost,
		Path:      "/chat/completions",
		Stream:    true,
		BodyBytes: []byte(`{"model": "muse-spark-1.2-contributor", "messages": [{"role": "user", "content": "hi"}]}`),
	}

	rec := httptest.NewRecorder()
	err := adapter.Forward(context.Background(), target, req, rec)
	require.NoError(t, err)

	assert.Equal(t, "Bearer sk-oc-go-testkey", receivedAuth)
	assert.Equal(t, "/responses", receivedPath)
	assert.Equal(t, http.StatusOK, rec.Code)

	resBody := rec.Body.String()
	assert.Contains(t, resBody, "Hi from Muse!")
	assert.Contains(t, resBody, "data: [DONE]")
}

func TestAdapter_FailoverOn429(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "10")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error": {"message": "rate limit"}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices": [{"message": {"content": "ok"}}]}`))
	}))
	defer server.Close()

	ring := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{
		{Ref: "key1", Secret: "token-1"},
		{Ref: "key2", Secret: "token-2"},
	})

	u := &domain.Upstream{
		Name:     "opencode-retry",
		Protocol: domain.ProtocolOpenCode,
		BaseURL:  server.URL,
		KeyRing:  ring,
	}
	target := &domain.Target{
		Upstream:      u,
		UpstreamModel: "glm-5.3",
		KeySlot:       ring.Slots[0],
	}

	breaker := &mockBreaker{allowed: true}
	adapter := NewAdapter(&mockClientPool{client: server.Client()}, breaker, Config{})

	req := ports.ForwardRequest{
		Method:    http.MethodPost,
		Path:      "/chat/completions",
		BodyBytes: []byte(`{"model": "glm-5.3", "messages": [{"role": "user", "content": "hi"}]}`),
	}

	rec := httptest.NewRecorder()
	err := adapter.Forward(context.Background(), target, req, rec)
	require.NoError(t, err)
	assert.Equal(t, 2, attempts)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestEnforceFreeSessionPayload(t *testing.T) {
	t.Run("injects system prompt and tools for minimal body", func(t *testing.T) {
		input := []byte(`{"model":"big-pickle","messages":[{"role":"user","content":"hi"}]}`)
		out := EnforceFreeSessionPayload(input)

		assert.True(t, gjson.GetBytes(out, "stream").Bool())
		assert.True(t, gjson.GetBytes(out, "stream_options.include_usage").Bool())
		assert.Equal(t, int64(32000), gjson.GetBytes(out, "max_tokens").Int())

		msgs := gjson.GetBytes(out, "messages").Array()
		require.Len(t, msgs, 2)
		assert.Equal(t, "system", msgs[0].Get("role").String())
		assert.Contains(t, msgs[0].Get("content").String(), "You are opencode")
		assert.Equal(t, "user", msgs[1].Get("role").String())

		tools := gjson.GetBytes(out, "tools").Array()
		assert.NotEmpty(t, tools)
		assert.Equal(t, "auto", gjson.GetBytes(out, "tool_choice").String())
	})

	t.Run("prepends official prompt to existing user system message", func(t *testing.T) {
		input := []byte(`{"model":"big-pickle","messages":[{"role":"system","content":"Be very brief."},{"role":"user","content":"hi"}]}`)
		out := EnforceFreeSessionPayload(input)

		msgs := gjson.GetBytes(out, "messages").Array()
		require.Len(t, msgs, 2)
		assert.Equal(t, "system", msgs[0].Get("role").String())
		content := msgs[0].Get("content").String()
		assert.Contains(t, content, "You are opencode")
		assert.Contains(t, content, "Be very brief.")
	})
}

func TestAdapter_LiveMuseSparkForward(t *testing.T) {
	if os.Getenv("FIREFLY_LIVE_TESTS") != "1" {
		t.Skip("skipping live test; set FIREFLY_LIVE_TESTS=1 to run")
	}

	u := &domain.Upstream{
		Name:     "opencode-free",
		Protocol: domain.ProtocolOpenCode,
		BaseURL:  "https://opencode.ai/zen/v1",
	}
	target := &domain.Target{
		Upstream:      u,
		UpstreamModel: "muse-spark-1.3-contributor-free",
	}

	adapter := NewAdapter(&mockClientPool{client: &http.Client{Timeout: 30 * time.Second}}, &mockBreaker{allowed: true}, Config{})

	t.Run("streaming", func(t *testing.T) {
		req := ports.ForwardRequest{
			Method:    http.MethodPost,
			Path:      "/chat/completions",
			Stream:    true,
			BodyBytes: []byte(`{"model": "muse-spark-1.3-contributor-free", "messages": [{"role": "user", "content": "Write one short greeting."}], "stream": true}`),
		}

		rec := httptest.NewRecorder()
		err := adapter.Forward(context.Background(), target, req, rec)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, rec.Code)
		body := rec.Body.String()
		assert.Contains(t, body, "chat.completion.chunk")
		assert.Contains(t, body, "data: [DONE]")
	})

	t.Run("non-streaming", func(t *testing.T) {
		req := ports.ForwardRequest{
			Method:    http.MethodPost,
			Path:      "/chat/completions",
			Stream:    false,
			BodyBytes: []byte(`{"model": "muse-spark-1.3-contributor-free", "messages": [{"role": "user", "content": "Say ping."}], "stream": false}`),
		}

		rec := httptest.NewRecorder()
		err := adapter.Forward(context.Background(), target, req, rec)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, rec.Code)
		body := rec.Body.String()
		assert.Contains(t, body, "chat.completion")
		assert.Contains(t, body, `"content":`)
	})
}
