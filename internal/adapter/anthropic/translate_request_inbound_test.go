package anthropic

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTranslateAnthropicRequestToOpenAI_Simple(t *testing.T) {
	body := `{
		"model": "claude-sonnet-4",
		"max_tokens": 1024,
		"system": "You are terse.",
		"messages": [{"role": "user", "content": "Hello there"}]
	}`

	got, err := TranslateAnthropicRequestToOpenAI([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `{
		"model": "claude-sonnet-4",
		"max_tokens": 1024,
		"messages": [
			{"role": "system", "content": "You are terse."},
			{"role": "user", "content": "Hello there"}
		]
	}`
	assertSameJSON(t, want, string(got))
}

func TestTranslateAnthropicRequestToOpenAI_SystemBlocks(t *testing.T) {
	body := `{"model":"m","max_tokens":8,"system":[{"type":"text","text":"First."},{"type":"text","text":"Second."}],"messages":[{"role":"user","content":"hi"}]}`

	got, err := TranslateAnthropicRequestToOpenAI([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The JSON body carries the newline as the two-character \n escape, so the
	// expectation is written as a raw string.
	if !strings.Contains(string(got), `First.\n\nSecond.`) {
		t.Fatalf("system blocks not joined: %s", got)
	}
}

func TestTranslateAnthropicRequestToOpenAI_SamplingAndStop(t *testing.T) {
	body := `{"model":"m","max_tokens":64,"temperature":0.4,"top_p":0.9,"stop_sequences":["END"],"stream":true,"messages":[{"role":"user","content":"hi"}]}`

	got, err := TranslateAnthropicRequestToOpenAI([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `{
		"model": "m",
		"max_tokens": 64,
		"temperature": 0.4,
		"top_p": 0.9,
		"stream": true,
		"stop": ["END"],
		"messages": [{"role": "user", "content": "hi"}]
	}`
	assertSameJSON(t, want, string(got))
}

func TestTranslateAnthropicRequestToOpenAI_ToolRoundTrip(t *testing.T) {
	body := `{
		"model": "m",
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

	got, err := TranslateAnthropicRequestToOpenAI([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed struct {
		Messages []struct {
			Role      string `json:"role"`
			Content   any    `json:"content"`
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
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Messages) != 3 {
		t.Fatalf("messages = %d, want 3 (no empty user turn): %s", len(parsed.Messages), got)
	}
	if len(parsed.Messages[1].ToolCalls) != 1 {
		t.Fatalf("assistant tool_calls = %+v", parsed.Messages[1].ToolCalls)
	}
	call := parsed.Messages[1].ToolCalls[0]
	if call.ID != "call_1" || call.Function.Name != "get_weather" {
		t.Fatalf("tool call = %+v", call)
	}
	if call.Function.Arguments != `{"city":"NYC"}` {
		t.Fatalf("arguments = %q, want the input object as a JSON string", call.Function.Arguments)
	}
	if parsed.Messages[2].Role != "tool" || parsed.Messages[2].ToolCallID != "call_1" {
		t.Fatalf("tool message = %+v", parsed.Messages[2])
	}
	if parsed.Messages[2].Content != "72F sunny" {
		t.Fatalf("tool content = %v", parsed.Messages[2].Content)
	}
}

func TestTranslateAnthropicRequestToOpenAI_MultimodalUser(t *testing.T) {
	body := `{"model":"m","max_tokens":8,"messages":[{"role":"user","content":[
		{"type":"text","text":"what is this"},
		{"type":"image","source":{"type":"url","url":"https://example.com/cat.png"}}
	]}]}`

	got, err := TranslateAnthropicRequestToOpenAI([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `{"model":"m","max_tokens":8,"messages":[{"role":"user","content":[
		{"type":"text","text":"what is this"},
		{"type":"image_url","image_url":{"url":"https://example.com/cat.png"}}
	]}]}`
	assertSameJSON(t, want, string(got))
}

func TestTranslateAnthropicRequestToOpenAI_Base64Image(t *testing.T) {
	body := `{"model":"m","max_tokens":8,"messages":[{"role":"user","content":[
		{"type":"text","text":"describe"},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"QUJD"}}
	]}]}`

	got, err := TranslateAnthropicRequestToOpenAI([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(got), "data:image/png;base64,QUJD") {
		t.Fatalf("base64 image not reassembled: %s", got)
	}
}

func TestTranslateAnthropicRequestToOpenAI_Tools(t *testing.T) {
	body := `{"model":"m","max_tokens":8,"tools":[{"name":"get_weather","description":"Look up weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}],"messages":[{"role":"user","content":"hi"}]}`

	got, err := TranslateAnthropicRequestToOpenAI([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed struct {
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Tools) != 1 || parsed.Tools[0].Type != "function" {
		t.Fatalf("tools = %+v", parsed.Tools)
	}
	if parsed.Tools[0].Function.Name != "get_weather" {
		t.Fatalf("tool name = %q", parsed.Tools[0].Function.Name)
	}
	if parsed.Tools[0].Function.Description != "Look up weather" {
		t.Fatalf("description lost: %q", parsed.Tools[0].Function.Description)
	}
	if !strings.Contains(string(parsed.Tools[0].Function.Parameters), `"city"`) {
		t.Fatalf("parameters not carried: %s", parsed.Tools[0].Function.Parameters)
	}
}

func TestTranslateAnthropicRequestToOpenAI_ToolChoice(t *testing.T) {
	base := `"model":"m","max_tokens":8,"messages":[{"role":"user","content":"hi"}]`
	cases := []struct {
		name   string
		choice string
		want   string
	}{
		{"auto", `,"tool_choice":"auto"`, `{"type":"auto"}`},
		{"any becomes required", `,"tool_choice":"any"`, `{"type":"required"}`},
		{"named tool", `,"tool_choice":{"type":"tool","name":"get_weather"}`, `{"type":"function","function":{"name":"get_weather"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := TranslateAnthropicRequestToOpenAI([]byte("{" + base + tc.choice + "}"))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var parsed struct {
				ToolChoice json.RawMessage `json:"tool_choice"`
			}
			if err := json.Unmarshal(got, &parsed); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			assertSameJSON(t, tc.want, string(parsed.ToolChoice))
		})
	}
}

func TestTranslateAnthropicRequestToOpenAI_Errors(t *testing.T) {
	cases := []struct{ name, body string }{
		{"empty", ""},
		{"not json", `{oops`},
		{"tool without name", `{"model":"m","max_tokens":8,"tools":[{"input_schema":{"type":"object"}}],"messages":[]}`},
		{"tool without schema", `{"model":"m","max_tokens":8,"tools":[{"name":"f"}],"messages":[]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := TranslateAnthropicRequestToOpenAI([]byte(tc.body))
			if err == nil {
				t.Fatalf("expected error, got %s", got)
			}
			if got != nil {
				t.Fatalf("expected nil payload alongside error, got %s", got)
			}
		})
	}
}

func TestTranslateAnthropicRequestToOpenAI_RoundTripsWithOutbound(t *testing.T) {
	// Whatever the outbound translator emits, the inbound one must accept and
	// recover. This is the property that keeps a client<->gateway<->upstream
	// chain from losing a turn.
	openAIReq := `{
		"model": "gpt-4o",
		"messages": [
			{"role": "system", "content": "You are an assistant."},
			{"role": "user", "content": "Hello!"},
			{"role": "assistant", "content": "Hi there!"},
			{"role": "user", "content": "How are you?"}
		],
		"max_tokens": 512,
		"temperature": 0.7
	}`

	anthropicReq, err := TranslateOpenAIToAnthropic([]byte(openAIReq), "")
	if err != nil {
		t.Fatalf("outbound: %v", err)
	}
	back, err := TranslateAnthropicRequestToOpenAI(anthropicReq)
	if err != nil {
		t.Fatalf("inbound: %v", err)
	}

	var parsed struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Messages  []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(back, &parsed); err != nil {
		t.Fatalf("unmarshal round trip: %v", err)
	}
	if parsed.Model != "gpt-4o" || parsed.MaxTokens != 512 {
		t.Fatalf("scalars lost: %+v", parsed)
	}
	if len(parsed.Messages) != 4 {
		t.Fatalf("messages = %d, want 4: %s", len(parsed.Messages), back)
	}
	if parsed.Messages[0].Role != "system" || parsed.Messages[0].Content != "You are an assistant." {
		t.Fatalf("system turn = %+v", parsed.Messages[0])
	}
	if parsed.Messages[3].Content != "How are you?" {
		t.Fatalf("last turn = %+v", parsed.Messages[3])
	}
}
