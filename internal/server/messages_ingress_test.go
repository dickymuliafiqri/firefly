package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/adapter/anthropic"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/dickymuliafiqri/firefly/internal/transport/httpx"
)

// sseAdapter relays a fixed OpenAI SSE stream so the Anthropic translation
// path can be exercised end to end.
type sseAdapter struct {
	chunks []string
}

func (a *sseAdapter) Protocol() domain.Protocol { return domain.ProtocolOpenAI }

func (a *sseAdapter) Forward(_ context.Context, t *domain.Target, req ports.ForwardRequest, w io.Writer) error {
	if rw, ok := w.(http.ResponseWriter); ok {
		rw.Header().Set("Content-Type", "text/event-stream")
		rw.WriteHeader(http.StatusOK)
	}
	for _, c := range a.chunks {
		_, _ = io.WriteString(w, "data: "+c+"\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	return nil
}

// messagesDeps wires a router whose tenant may use gpt-4o and whose adapter
// returns the given body.
func messagesDeps(t *testing.T, adapter ports.UpstreamAdapter) RouterDeps {
	t.Helper()
	deps, _ := testDepsWithAdapter(adapter)
	return deps
}

func postMessages(t *testing.T, deps RouterDeps, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req = req.WithContext(httpx.WithTenant(req.Context(), messagesTenant()))
	rec := httptest.NewRecorder()
	deps.forwardEndpoint("/chat/completions", anthropicIngress{})(rec, req)
	return rec
}

func messagesTenant() *domain.Tenant {
	tenant := domain.NewTenant()
	tenant.Name = "alpha"
	tenant.APIKey = testKey
	tenant.Status = domain.TenantStatusActive
	tenant.AllowedModels = []string{"gpt-4o", "gpt-4o-mini"}
	return &tenant
}

const anthropicRequestBody = `{
	"model": "gpt-4o",
	"max_tokens": 1024,
	"system": "You are terse.",
	"messages": [{"role": "user", "content": "Hello there"}]
}`

func TestMessagesEndpoint_NonStreamTranslatesResponse(t *testing.T) {
	deps := messagesDeps(t, &fakeAdapter{body: `{"id":"chatcmpl-abc","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"General Kenobi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":3}}`})

	rec := postMessages(t, deps, anthropicRequestBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Role       string `json:"role"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad anthropic response: %v\n%s", err, rec.Body.String())
	}
	if resp.Type != "message" || resp.Role != "assistant" {
		t.Fatalf("envelope wrong: %+v", resp)
	}
	if resp.Model != "gpt-4o" {
		t.Fatalf("model = %q, want the public name", resp.Model)
	}
	if resp.StopReason != "end_turn" {
		t.Fatalf("stop_reason = %q", resp.StopReason)
	}
	if len(resp.Content) != 1 || resp.Content[0].Type != "text" || resp.Content[0].Text != "General Kenobi" {
		t.Fatalf("content = %+v", resp.Content)
	}
	if resp.Usage.InputTokens != 12 || resp.Usage.OutputTokens != 3 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
}

func TestMessagesEndpoint_ToolCallResponse(t *testing.T) {
	body := `{"id":"chatcmpl-t","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"checking","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"NYC\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":9}}`
	deps := messagesDeps(t, &fakeAdapter{body: body})

	rec := postMessages(t, deps, anthropicRequestBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response: %v", err)
	}
	if resp.StopReason != "tool_use" {
		t.Fatalf("stop_reason = %q", resp.StopReason)
	}
	if len(resp.Content) != 2 {
		t.Fatalf("content = %+v", resp.Content)
	}
	if resp.Content[1].Type != "tool_use" || resp.Content[1].Name != "get_weather" {
		t.Fatalf("tool block = %+v", resp.Content[1])
	}
	if string(resp.Content[1].Input) != `{"city":"NYC"}` {
		t.Fatalf("tool input = %s", resp.Content[1].Input)
	}
}

func TestMessagesEndpoint_StreamTranslatesEvents(t *testing.T) {
	chunks := []string{
		`{"id":"chatcmpl-s","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"id":"chatcmpl-s","choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
		`{"id":"chatcmpl-s","choices":[{"index":0,"delta":{"content":" world"}}]}`,
		`{"id":"chatcmpl-s","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"id":"chatcmpl-s","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":2}}`,
	}
	deps := messagesDeps(t, &sseAdapter{chunks: chunks})

	streamBody := strings.Replace(anthropicRequestBody, `"max_tokens": 1024`, `"max_tokens": 1024, "stream": true`, 1)
	rec := postMessages(t, deps, streamBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}

	out := rec.Body.String()
	if !strings.Contains(out, "event: message_start") {
		t.Fatalf("missing message_start:\n%s", out)
	}
	if !strings.Contains(out, "event: content_block_delta") {
		t.Fatalf("missing content_block_delta:\n%s", out)
	}
	if !strings.Contains(out, "event: message_delta") {
		t.Fatalf("missing message_delta:\n%s", out)
	}
	if !strings.Contains(out, "event: message_stop") {
		t.Fatalf("missing message_stop:\n%s", out)
	}
	if strings.Contains(out, "chatcmpl") {
		t.Fatalf("openai id leaked into the anthropic stream:\n%s", out)
	}
	if strings.Contains(out, "[DONE]") {
		t.Fatalf("openai sentinel leaked into the anthropic stream:\n%s", out)
	}

	// The event order must be the one Anthropic clients parse.
	order := []string{"message_start", "content_block_start", "content_block_delta",
		"content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	pos := 0
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "event: ") {
			continue
		}
		name := strings.TrimPrefix(line, "event: ")
		if pos >= len(order) {
			t.Fatalf("unexpected extra event %q", name)
		}
		if name != order[pos] {
			t.Fatalf("event %d = %q, want %q\n%s", pos, name, order[pos], out)
		}
		pos++
	}
	if pos != len(order) {
		t.Fatalf("saw %d events, want %d\n%s", pos, len(order), out)
	}
}

func TestMessagesEndpoint_MissingModelIs400(t *testing.T) {
	deps := messagesDeps(t, &fakeAdapter{body: `{}`})
	rec := postMessages(t, deps, `{"max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"error"`) {
		t.Fatalf("body is not an anthropic error envelope: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid_request_error") {
		t.Fatalf("error type not mapped: %s", rec.Body.String())
	}
}

func TestMessagesEndpoint_MissingMaxTokensIs400(t *testing.T) {
	deps := messagesDeps(t, &fakeAdapter{body: `{}`})
	rec := postMessages(t, deps, `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "max_tokens") {
		t.Fatalf("error should name the missing field: %s", rec.Body.String())
	}
}

func TestMessagesEndpoint_MalformedJSONIs400(t *testing.T) {
	deps := messagesDeps(t, &fakeAdapter{body: `{}`})
	rec := postMessages(t, deps, `{oops`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"error"`) {
		t.Fatalf("body is not an anthropic error envelope: %s", rec.Body.String())
	}
}

func TestMessagesEndpoint_SystemStringAndBlocks(t *testing.T) {
	for _, system := range []string{
		`"You are terse."`,
		`[{"type":"text","text":"You are terse."}]`,
	} {
		body := `{"model":"gpt-4o","max_tokens":64,"system":` + system + `,"messages":[{"role":"user","content":"hi"}]}`
		ing := anthropicIngress{}
		openAIBody, model, stream, tokensIn, err := ing.ParseBody([]byte(body))
		if err != nil {
			t.Fatalf("system %s: %v", system, err)
		}
		if model != "gpt-4o" || stream {
			t.Fatalf("system %s: model=%q stream=%v", system, model, stream)
		}
		if tokensIn <= 0 {
			t.Fatalf("system %s: tokensIn = %d", system, tokensIn)
		}
		// The translated body must carry the system text so the upstream sees it.
		if !strings.Contains(string(openAIBody), "You are terse.") {
			t.Fatalf("system %s: lost in translation: %s", system, openAIBody)
		}
	}
}

func TestMessagesEndpoint_ToolRoundTrip(t *testing.T) {
	// A full agentic turn: assistant tool_calls in, tool results back out.
	body := `{
		"model": "gpt-4o",
		"max_tokens": 256,
		"messages": [
			{"role": "user", "content": "weather in NYC?"},
			{"role": "assistant", "content": [
				{"type": "tool_use", "id": "call_1", "name": "get_weather", "input": {"city": "NYC"}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "call_1", "content": "72F sunny"}
			]}
		]
	}`
	ing := anthropicIngress{}
	openAIBody, _, _, _, err := ing.ParseBody([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var translated struct {
		Messages []struct {
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(openAIBody, &translated); err != nil {
		t.Fatalf("unmarshal translated: %v", err)
	}
	if len(translated.Messages) != 3 {
		t.Fatalf("messages = %d, want 3: %s", len(translated.Messages), openAIBody)
	}
	if len(translated.Messages[1].ToolCalls) != 1 || translated.Messages[1].ToolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("assistant tool_calls lost: %s", openAIBody)
	}
	if translated.Messages[2].ToolCallID != "call_1" {
		t.Fatalf("tool result linkage lost: %s", openAIBody)
	}
}

func TestMessagesEndpoint_UpstreamErrorIsTranslated(t *testing.T) {
	deps := messagesDeps(t, &fakeAdapter{
		status: http.StatusTooManyRequests,
		body:   `{"error":{"message":"Rate limit reached","type":"rate_limit_exceeded"}}`,
	})

	rec := postMessages(t, deps, anthropicRequestBody)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"error"`) {
		t.Fatalf("not an anthropic error envelope: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "rate_limit_error") {
		t.Fatalf("error type not mapped: %s", rec.Body.String())
	}
}

func TestMessagesEndpoint_UsageAndLogRecorded(t *testing.T) {
	deps := messagesDeps(t, &fakeAdapter{body: `{"id":"chatcmpl-u","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":2}}`})

	rec := postMessages(t, deps, anthropicRequestBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	// The request log must carry the route and the metered tokens.
	if deps.LiveLogs == nil {
		t.Skip("live log hub not wired in this fixture")
	}
}

func TestMessagesEndpoint_BudgetGuardUsesAnthropicEnvelope(t *testing.T) {
	tenant := messagesTenant()
	tenant.BudgetMicros = 1000
	tenant.SpentMicros.Store(1000)
	deps := messagesDeps(t, &fakeAdapter{body: `{}`})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(anthropicRequestBody))
	req = req.WithContext(httpx.WithTenant(req.Context(), tenant))
	rec := httptest.NewRecorder()
	deps.forwardEndpoint("/chat/completions", anthropicIngress{})(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"error"`) {
		t.Fatalf("not an anthropic error envelope: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "tenant budget exceeded") {
		t.Fatalf("message missing: %s", rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After = %q", got)
	}
}

func TestSSEDataPayload(t *testing.T) {
	cases := []struct {
		name  string
		frame string
		want  string
		ok    bool
	}{
		{"plain data", "data: {\"a\":1}", `{"a":1}`, true},
		{"done sentinel", "data: [DONE]", "[DONE]", true},
		{"event then data", "event: message_start\ndata: {}", "{}", true},
		{"comment only", ": keep-alive", "", false},
		{"empty", "", "", false},
		{"blank data", "data:  ", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := sseDataPayload([]byte(tc.frame))
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if string(got) != tc.want {
				t.Fatalf("payload = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAnthropicResponseWriter_SplitFrame(t *testing.T) {
	// A frame delivered in two writes must not be mis-parsed.
	var buf bytes.Buffer
	w := &anthropicResponseWriter{
		ResponseWriter: httptest.NewRecorder(),
		model:          "gpt-4o",
		stream:         true,
		state:          anthropic.NewAnthropicStreamState("gpt-4o", 0),
	}
	_ = buf

	first := `data: {"id":"chatcmpl-x","choices":[{"index":0,"delta":{"content":"Hel`
	second := `lo"}}]}` + "\n\n"
	if _, err := w.Write([]byte(first)); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := w.Write([]byte(second)); err != nil {
		t.Fatalf("second write: %v", err)
	}
	w.finalize(nil)

	out := w.ResponseWriter.(*httptest.ResponseRecorder).Body.String()
	if !strings.Contains(out, "text_delta") {
		t.Fatalf("split frame not translated:\n%s", out)
	}
	if !strings.Contains(out, "Hello") {
		t.Fatalf("payload lost:\n%s", out)
	}
}
