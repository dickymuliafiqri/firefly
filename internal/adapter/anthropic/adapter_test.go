package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
)

type fixedPool struct{ c *http.Client }

func (p fixedPool) Client(*domain.Upstream) *http.Client { return p.c }

type allowAllBreaker struct {
	mu      sync.Mutex
	reports []bool
}

func (b *allowAllBreaker) Allow(string) error { return nil }
func (b *allowAllBreaker) Report(_ string, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reports = append(b.reports, ok)
}

func TestTranslateOpenAIToAnthropic(t *testing.T) {
	openAIReq := []byte(`{
		"model": "gpt-4o",
		"messages": [
			{"role": "system", "content": "You are an assistant."},
			{"role": "user", "content": "Hello!"},
			{"role": "assistant", "content": "Hi there!"},
			{"role": "user", "content": "How are you?"}
		],
		"max_tokens": 1000,
		"temperature": 0.5,
		"stream": true,
		"stop": ["STOP"]
	}`)

	anthropicBytes, err := TranslateOpenAIToAnthropic(openAIReq, "claude-3-5-sonnet-20241022")
	if err != nil {
		t.Fatalf("TranslateOpenAIToAnthropic failed: %v", err)
	}

	var parsed anthropicRequest
	if err := json.Unmarshal(anthropicBytes, &parsed); err != nil {
		t.Fatalf("failed to parse translated anthropic request: %v", err)
	}

	if parsed.Model != "claude-3-5-sonnet-20241022" {
		t.Fatalf("model = %q, want claude-3-5-sonnet-20241022", parsed.Model)
	}
	if parsed.System != "You are an assistant." {
		t.Fatalf("system = %q, want 'You are an assistant.'", parsed.System)
	}
	if len(parsed.Messages) != 3 {
		t.Fatalf("messages count = %d, want 3", len(parsed.Messages))
	}
	if parsed.Messages[0].Role != "user" || parsed.Messages[0].Content != "Hello!" {
		t.Fatalf("unexpected message 0: %+v", parsed.Messages[0])
	}
	if parsed.Messages[1].Role != "assistant" || parsed.Messages[1].Content != "Hi there!" {
		t.Fatalf("unexpected message 1: %+v", parsed.Messages[1])
	}
	if parsed.MaxTokens != 1000 {
		t.Fatalf("max_tokens = %d, want 1000", parsed.MaxTokens)
	}
	if parsed.Temperature == nil || *parsed.Temperature != 0.5 {
		t.Fatalf("temperature = %v, want 0.5", parsed.Temperature)
	}
	if !parsed.Stream {
		t.Fatal("stream flag lost")
	}
	if len(parsed.StopSequences) != 1 || parsed.StopSequences[0] != "STOP" {
		t.Fatalf("stop_sequences = %v, want ['STOP']", parsed.StopSequences)
	}
}

func TestTranslateOpenAIToAnthropic_ArrayContent(t *testing.T) {
	openAIReq := []byte(`{
		"model": "gpt-4o",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "First line of prompt."},
					{"type": "text", "text": "Second line of prompt."}
				]
			}
		],
		"max_tokens": 500
	}`)

	anthropicBytes, err := TranslateOpenAIToAnthropic(openAIReq, "claude-3-5-sonnet-20241022")
	if err != nil {
		t.Fatalf("TranslateOpenAIToAnthropic failed: %v", err)
	}

	var parsed anthropicRequest
	if err := json.Unmarshal(anthropicBytes, &parsed); err != nil {
		t.Fatalf("failed to parse translated anthropic request: %v", err)
	}

	if len(parsed.Messages) != 1 {
		t.Fatalf("messages count = %d, want 1", len(parsed.Messages))
	}
	wantContent := "First line of prompt.\nSecond line of prompt."
	if parsed.Messages[0].Content != wantContent {
		t.Fatalf("content = %q, want %q", parsed.Messages[0].Content, wantContent)
	}
}

func TestTranslateAnthropicToOpenAI(t *testing.T) {
	anthropicResp := []byte(`{
		"id": "msg_01X",
		"type": "message",
		"role": "assistant",
		"content": [{"type": "text", "text": "Hello world!"}],
		"model": "claude-3-5-sonnet-20241022",
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 15, "output_tokens": 8}
	}`)

	openAIBytes, err := TranslateAnthropicToOpenAI(anthropicResp, "my-claude")
	if err != nil {
		t.Fatalf("TranslateAnthropicToOpenAI failed: %v", err)
	}

	var parsed openAIResponse
	if err := json.Unmarshal(openAIBytes, &parsed); err != nil {
		t.Fatalf("failed to parse translated openai response: %v", err)
	}

	if !strings.HasPrefix(parsed.ID, "chatcmpl-msg_01X") {
		t.Fatalf("id = %q", parsed.ID)
	}
	if parsed.Model != "my-claude" {
		t.Fatalf("model = %q, want my-claude", parsed.Model)
	}
	if len(parsed.Choices) != 1 {
		t.Fatalf("choices count = %d", len(parsed.Choices))
	}
	if parsed.Choices[0].Message.Content != "Hello world!" {
		t.Fatalf("content = %q", parsed.Choices[0].Message.Content)
	}
	if parsed.Choices[0].FinishReason != "stop" {
		t.Fatalf("finish_reason = %q, want stop", parsed.Choices[0].FinishReason)
	}
	if parsed.Usage.PromptTokens != 15 || parsed.Usage.CompletionTokens != 8 || parsed.Usage.TotalTokens != 23 {
		t.Fatalf("usage = %+v", parsed.Usage)
	}
}

func TestTranslateAnthropicSSEChunks(t *testing.T) {
	var msgID string
	startEv := []byte(`{"type":"message_start","message":{"id":"msg_123","role":"assistant"}}`)
	c1, done1, err := TranslateAnthropicChunkToOpenAI("message_start", startEv, "claude", &msgID)
	if err != nil || done1 {
		t.Fatalf("message_start failed: %v", err)
	}
	if !strings.Contains(string(c1), `"role":"assistant"`) {
		t.Fatalf("c1 = %s", string(c1))
	}

	deltaEv := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}`)
	c2, done2, err := TranslateAnthropicChunkToOpenAI("content_block_delta", deltaEv, "claude", &msgID)
	if err != nil || done2 {
		t.Fatalf("content_block_delta failed: %v", err)
	}
	if !strings.Contains(string(c2), `"content":"Hi"`) {
		t.Fatalf("c2 = %s", string(c2))
	}

	msgDeltaEv := []byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`)
	c3, done3, err := TranslateAnthropicChunkToOpenAI("message_delta", msgDeltaEv, "claude", &msgID)
	if err != nil || done3 {
		t.Fatalf("message_delta failed: %v", err)
	}
	if !strings.Contains(string(c3), `"finish_reason":"stop"`) {
		t.Fatalf("c3 = %s", string(c3))
	}

	_, done4, err := TranslateAnthropicChunkToOpenAI("message_stop", nil, "claude", &msgID)
	if err != nil || !done4 {
		t.Fatalf("message_stop should be done: done=%v err=%v", done4, err)
	}
}

func TestTranslateAnthropicErrors(t *testing.T) {
	errPayload := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Number of request tokens has exceeded your daily limit."}}`)
	status, body := TranslateAnthropicError(http.StatusTooManyRequests, errPayload)
	if status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", status)
	}
	var env OpenAIErrorEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("unmarshal error envelope: %v", err)
	}
	if env.Error.Type != "rate_limit_error" {
		t.Fatalf("type = %q", env.Error.Type)
	}
	if !strings.Contains(env.Error.Message, "Number of request tokens") {
		t.Fatalf("message = %q", env.Error.Message)
	}
}

func TestAnthropicAdapter_ForwardNonStreaming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "anthropic-secret" {
			t.Errorf("x-api-key = %q", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("anthropic-version = %q", r.Header.Get("anthropic-version"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{
			"id": "msg_01",
			"type": "message",
			"role": "assistant",
			"content": [{"type": "text", "text": "Claude response"}],
			"model": "claude-3-5-sonnet-20241022",
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 10, "output_tokens": 5}
		}`)
	}))
	t.Cleanup(srv.Close)

	adapter := NewAdapter(
		fixedPool{c: srv.Client()},
		&allowAllBreaker{},
		Config{SecretLookup: func(ref string) (string, bool) { return "anthropic-secret", true }},
	)

	target := &domain.Target{
		Upstream:      &domain.Upstream{Name: "claude-up", BaseURL: srv.URL, CredentialRef: "ANTH_KEY"},
		UpstreamModel: "claude-3-5-sonnet-20241022",
		CredentialRef: "ANTH_KEY",
	}

	rec := httptest.NewRecorder()
	err := adapter.Forward(context.Background(), target, ports.ForwardRequest{
		Method:    http.MethodPost,
		Path:      "/v1/chat/completions",
		BodyBytes: []byte(`{"model":"claude","messages":[{"role":"user","content":"Hi"}]}`),
	}, rec)

	if err != nil {
		t.Fatalf("Forward failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var oResp openAIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &oResp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if oResp.Choices[0].Message.Content != "Claude response" {
		t.Fatalf("content = %q", oResp.Choices[0].Message.Content)
	}
}

func TestAnthropicAdapter_ForwardStreaming(t *testing.T) {
	sseData := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_sse\",\"role\":\"assistant\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello from stream\"}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" +
		"event: message_stop\ndata: {}\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sseData)
	}))
	t.Cleanup(srv.Close)

	adapter := NewAdapter(
		fixedPool{c: srv.Client()},
		&allowAllBreaker{},
		Config{SecretLookup: func(ref string) (string, bool) { return "anthropic-secret", true }},
	)

	target := &domain.Target{
		Upstream:      &domain.Upstream{Name: "claude-up", BaseURL: srv.URL, CredentialRef: "ANTH_KEY"},
		UpstreamModel: "claude-3-5-sonnet-20241022",
		CredentialRef: "ANTH_KEY",
	}

	rec := httptest.NewRecorder()
	err := adapter.Forward(context.Background(), target, ports.ForwardRequest{
		Method:    http.MethodPost,
		Path:      "/v1/chat/completions",
		BodyBytes: []byte(`{"model":"claude","messages":[{"role":"user","content":"Hi"}],"stream":true}`),
		Stream:    true,
	}, rec)

	if err != nil {
		t.Fatalf("Forward failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Hello from stream") {
		t.Fatalf("stream body missing delta text: %s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("stream body missing [DONE]: %s", body)
	}
}

func TestAnthropicAdapter_MultiKeyFailover(t *testing.T) {
	k1 := &domain.KeySlot{Ref: "K1", Secret: "sk-anth-1"}
	k2 := &domain.KeySlot{Ref: "K2", Secret: "sk-anth-2"}
	ring := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{k1, k2})

	var usedKeys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("x-api-key")
		usedKeys = append(usedKeys, auth)
		if auth == "sk-anth-1" {
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{
			"id": "msg_02",
			"type": "message",
			"role": "assistant",
			"content": [{"type": "text", "text": "Claude key 2 response"}],
			"stop_reason": "end_turn"
		}`)
	}))
	t.Cleanup(srv.Close)

	adapter := NewAdapter(
		fixedPool{c: srv.Client()},
		&allowAllBreaker{},
		Config{SecretLookup: func(ref string) (string, bool) {
			if ref == "K1" {
				return "sk-anth-1", true
			}
			return "sk-anth-2", true
		}},
	)

	target := &domain.Target{
		Upstream:      &domain.Upstream{Name: "claude-up", BaseURL: srv.URL, KeyRing: ring},
		UpstreamModel: "claude-3-5-sonnet",
		CredentialRef: "K1",
		KeySlot:       k1,
	}

	rec := httptest.NewRecorder()
	err := adapter.Forward(context.Background(), target, ports.ForwardRequest{
		Method:    http.MethodPost,
		Path:      "/v1/chat/completions",
		BodyBytes: []byte(`{"model":"claude","messages":[{"role":"user","content":"Hi"}]}`),
	}, rec)

	if err != nil {
		t.Fatalf("Forward failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after failover; body=%s", rec.Code, rec.Body.String())
	}
	if len(usedKeys) != 2 || usedKeys[0] != "sk-anth-1" || usedKeys[1] != "sk-anth-2" {
		t.Fatalf("keys used = %v", usedKeys)
	}
}
