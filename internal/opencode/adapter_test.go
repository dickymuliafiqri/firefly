package opencode

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/httpx"
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

	t.Run("multi-turn conversation converts assistant messages to output_text", func(t *testing.T) {
		body := []byte(`{
			"model": "muse-spark-1.3",
			"messages": [
				{"role": "user", "content": "hihi"},
				{"role": "assistant", "content": "hihi"},
				{"role": "user", "content": "halo muse"}
			]
		}`)

		out, err := TransformChatToResponses(body, "muse-spark-1.3", true)
		require.NoError(t, err)

		inputs := gjson.GetBytes(out, "input").Array()
		require.Equal(t, 3, len(inputs))

		// First user turn
		assert.Equal(t, "user", inputs[0].Get("role").String())
		assert.Equal(t, "input_text", inputs[0].Get("content.0.type").String())
		assert.Equal(t, "hihi", inputs[0].Get("content.0.text").String())

		// Assistant turn MUST use output_text to satisfy OpenCode /responses API
		assert.Equal(t, "assistant", inputs[1].Get("role").String())
		assert.Equal(t, "output_text", inputs[1].Get("content.0.type").String())
		assert.Equal(t, "hihi", inputs[1].Get("content.0.text").String())

		// Second user turn
		assert.Equal(t, "user", inputs[2].Get("role").String())
		assert.Equal(t, "input_text", inputs[2].Get("content.0.type").String())
		assert.Equal(t, "halo muse", inputs[2].Get("content.0.text").String())
	})

	t.Run("assistant message with text and tool calls preserves both", func(t *testing.T) {
		body := []byte(`{
			"model": "muse-spark-1.3",
			"messages": [
				{"role": "user", "content": "run bash"},
				{
					"role": "assistant",
					"content": "Executing command now.",
					"tool_calls": [
						{
							"id": "call_123",
							"type": "function",
							"function": {"name": "bash", "arguments": "{\"command\":\"ls\"}"}
						}
					]
				},
				{"role": "tool", "tool_call_id": "call_123", "content": "file1.txt\nfile2.txt"}
			]
		}`)

		out, err := TransformChatToResponses(body, "muse-spark-1.3", false)
		require.NoError(t, err)

		inputs := gjson.GetBytes(out, "input").Array()
		require.Equal(t, 4, len(inputs))

		// 1. User message
		assert.Equal(t, "user", inputs[0].Get("role").String())
		assert.Equal(t, "input_text", inputs[0].Get("content.0.type").String())

		// 2. Assistant message text
		assert.Equal(t, "message", inputs[1].Get("type").String())
		assert.Equal(t, "assistant", inputs[1].Get("role").String())
		assert.Equal(t, "output_text", inputs[1].Get("content.0.type").String())
		assert.Equal(t, "Executing command now.", inputs[1].Get("content.0.text").String())

		// 3. Assistant function_call
		assert.Equal(t, "function_call", inputs[2].Get("type").String())
		assert.Equal(t, "call_123", inputs[2].Get("call_id").String())
		assert.Equal(t, "bash", inputs[2].Get("name").String())

		// 4. Tool output
		assert.Equal(t, "function_call_output", inputs[3].Get("type").String())
		assert.Equal(t, "call_123", inputs[3].Get("call_id").String())
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
	assert.Equal(t, "desktop", receivedClient)
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

func TestAdapter_FreeMode_ResponsesStream_MultiTurn(t *testing.T) {
	var receivedAuth string
	var receivedPath string
	var receivedBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		receivedPath = r.URL.Path
		receivedBody, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		require.True(t, ok)

		_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"delta\": \"Halo! Ada yang bisa saya bantu?\"}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("event: response.completed\ndata: {\"response\": {\"usage\": {\"input_tokens\": 15, \"output_tokens\": 8}}}\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	u := &domain.Upstream{
		Name:     "opencode-free",
		Protocol: domain.ProtocolOpenCode,
		BaseURL:  server.URL + "/zen/v1",
	}
	target := &domain.Target{
		Upstream:      u,
		UpstreamModel: "muse-spark-1.3",
		KeySlot: &domain.KeySlot{
			Ref:    "opencode-free-public",
			Secret: "public",
		},
	}

	adapter := NewAdapter(&mockClientPool{client: server.Client()}, &mockBreaker{allowed: true}, Config{})

	req := ports.ForwardRequest{
		Method: http.MethodPost,
		Path:   "/chat/completions",
		Stream: true,
		BodyBytes: []byte(`{
			"model": "muse-spark-1.3",
			"messages": [
				{"role": "user", "content": "hihi"},
				{"role": "assistant", "content": "hihi"},
				{"role": "user", "content": "halo muse"}
			]
		}`),
	}

	rec := httptest.NewRecorder()
	err := adapter.Forward(context.Background(), target, req, rec)
	require.NoError(t, err)

	assert.Equal(t, "Bearer public", receivedAuth)
	assert.Equal(t, "/zen/v1/responses", receivedPath)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Verify the request body sent to upstream OpenCode
	inputs := gjson.GetBytes(receivedBody, "input").Array()
	require.Equal(t, 3, len(inputs))
	assert.Equal(t, "input_text", inputs[0].Get("content.0.type").String())
	assert.Equal(t, "hihi", inputs[0].Get("content.0.text").String())
	// Assistant MUST be output_text
	assert.Equal(t, "assistant", inputs[1].Get("role").String())
	assert.Equal(t, "output_text", inputs[1].Get("content.0.type").String())
	assert.Equal(t, "hihi", inputs[1].Get("content.0.text").String())
	// Second user turn MUST be input_text
	assert.Equal(t, "user", inputs[2].Get("role").String())
	assert.Equal(t, "input_text", inputs[2].Get("content.0.type").String())
	assert.Equal(t, "halo muse", inputs[2].Get("content.0.text").String())

	resBody := rec.Body.String()
	assert.Contains(t, resBody, "Halo! Ada yang bisa saya bantu?")
	assert.Contains(t, resBody, "data: [DONE]")
}

func TestAdapter_ResponsesStream_ToolCallStreaming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		events := []string{
			"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"output_index\":2,\"item\":{\"id\":\"fc_123\",\"type\":\"function_call\",\"name\":\"execute_command\",\"call_id\":\"call_abc\",\"arguments\":\"\"}}\n\n",
			"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":2,\"output_index\":2,\"item_id\":\"fc_123\",\"delta\":\"{\\\"command\\\":\\\"pwd\\\"}\"}\n\n",
			"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":3,\"output_index\":2,\"item\":{\"id\":\"fc_123\",\"type\":\"function_call\",\"name\":\"execute_command\",\"call_id\":\"call_abc\",\"arguments\":\"{\\\"command\\\":\\\"pwd\\\"}\"}}\n\n",
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":4,\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":20}}}\n\n",
		}
		for _, ev := range events {
			_, _ = w.Write([]byte(ev))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer server.Close()

	u := &domain.Upstream{
		Name:     "opencode-test",
		Protocol: domain.ProtocolOpenCode,
		BaseURL:  server.URL,
	}
	target := &domain.Target{
		Upstream:      u,
		UpstreamModel: "muse-spark-1.3",
	}

	adapter := NewAdapter(&mockClientPool{client: server.Client()}, &mockBreaker{allowed: true}, Config{})

	req := ports.ForwardRequest{
		Method: http.MethodPost,
		Path:   "/chat/completions",
		Stream: true,
		BodyBytes: []byte(`{
			"model": "muse-spark-1.3",
			"messages": [{"role": "user", "content": "run pwd"}],
			"tools": [
				{
					"type": "function",
					"function": {
						"name": "execute_command",
						"parameters": {"type": "object", "properties": {"command": {"type": "string"}}}
					}
				}
			],
			"stream": true
		}`),
	}

	rec := httptest.NewRecorder()
	err := adapter.Forward(context.Background(), target, req, rec)
	require.NoError(t, err)

	resBody := rec.Body.String()
	assert.Contains(t, resBody, "chat.completion.chunk")
	// Verify tool declaration chunk has index: 0, id: call_abc, and function name
	assert.Contains(t, resBody, `"index":0`)
	assert.Contains(t, resBody, `"id":"call_abc"`)
	assert.Contains(t, resBody, `"name":"execute_command"`)
	// Verify arguments delta chunk
	assert.Contains(t, resBody, `\"command\":\"pwd\"`)
	// Verify terminal chunk has finish_reason: tool_calls
	assert.Contains(t, resBody, `"finish_reason":"tool_calls"`)
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

	t.Run("cline simulation", func(t *testing.T) {
		clineHeaders := make(http.Header)
		clineHeaders.Set("User-Agent", "Cline/3.5.0")
		clineHeaders.Set("HTTP-Referer", "https://github.com/cline/cline")
		clineHeaders.Set("X-Title", "Cline")

		req := ports.ForwardRequest{
			Method:  http.MethodPost,
			Path:    "/chat/completions",
			Stream:  true,
			Headers: clineHeaders,
			BodyBytes: []byte(`{
				"model": "muse-spark-1.3",
				"messages": [
					{"role": "system", "content": "You are Cline, a highly skilled software engineer."},
					{"role": "user", "content": "halo"}
				],
				"tools": [
					{
						"type": "function",
						"function": {
							"name": "execute_command",
							"description": "Run bash command",
							"parameters": {"type": "object", "properties": {"command": {"type": "string"}}, "required": ["command"]}
						}
					}
				],
				"stream": true
			}`),
		}

		targetMuse := &domain.Target{
			Upstream:      u,
			UpstreamModel: "muse-spark-1.3",
		}

		ctx := httpx.WithTenant(context.Background(), &domain.Tenant{Name: "test-tenant"})
		ctx = httpx.WithRequestID(ctx, "req-12345")

		rec := httptest.NewRecorder()
		err := adapter.Forward(ctx, targetMuse, req, rec)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "chat.completion.chunk")
		assert.Contains(t, rec.Body.String(), "data: [DONE]")

		// Turn 2: User sends "sumimasen" with prior assistant response
		req2 := ports.ForwardRequest{
			Method:  http.MethodPost,
			Path:    "/chat/completions",
			Stream:  true,
			Headers: clineHeaders,
			BodyBytes: []byte(`{
				"model": "muse-spark-1.3",
				"messages": [
					{"role": "system", "content": "You are Cline, a highly skilled software engineer."},
					{"role": "user", "content": "halo"},
					{"role": "assistant", "content": "Halo! Ada yang bisa saya bantu?"},
					{"role": "user", "content": "sumimasen"}
				],
				"tools": [
					{
						"type": "function",
						"function": {
							"name": "execute_command",
							"description": "Run bash command",
							"parameters": {"type": "object", "properties": {"command": {"type": "string"}}, "required": ["command"]}
						}
					}
				],
				"stream": true
			}`),
		}

		ctx2 := httpx.WithTenant(context.Background(), &domain.Tenant{Name: "test-tenant"})
		ctx2 = httpx.WithRequestID(ctx2, "req-67890")

		rec2 := httptest.NewRecorder()
		err2 := adapter.Forward(ctx2, targetMuse, req2, rec2)
		require.NoError(t, err2)
		assert.Equal(t, http.StatusOK, rec2.Code)
		assert.Contains(t, rec2.Body.String(), "chat.completion.chunk")
		assert.Contains(t, rec2.Body.String(), "data: [DONE]")

		// Turn 3: User prompts for python webserver with tool calling
		req3 := ports.ForwardRequest{
			Method:  http.MethodPost,
			Path:    "/chat/completions",
			Stream:  true,
			Headers: clineHeaders,
			BodyBytes: []byte(`{
				"model": "muse-spark-1.3",
				"messages": [
					{"role": "system", "content": "You are Cline, a highly skilled software engineer."},
					{"role": "user", "content": "Tolong buatkan kode python untuk webserver sederhana dengan endpoint / dan /ping"}
				],
				"tools": [
					{
						"type": "function",
						"function": {
							"name": "execute_command",
							"description": "Run bash command",
							"parameters": {"type": "object", "properties": {"command": {"type": "string"}}, "required": ["command"]}
						}
					}
				],
				"stream": true
			}`),
		}

		ctx3 := httpx.WithTenant(context.Background(), &domain.Tenant{Name: "test-tenant"})
		ctx3 = httpx.WithRequestID(ctx3, "req-webserver")

		rec3 := httptest.NewRecorder()
		err3 := adapter.Forward(ctx3, targetMuse, req3, rec3)
		require.NoError(t, err3)
		assert.Equal(t, http.StatusOK, rec3.Code)
		body3 := rec3.Body.String()
		assert.Contains(t, body3, "chat.completion.chunk")
		assert.Contains(t, body3, "data: [DONE]")
		// Ensure it never leaks /private/tmp/opencode-x64 into the response
		assert.NotContains(t, body3, "/private/tmp/opencode-x64")
	})
}
