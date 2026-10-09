package anthropic

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTranslateOpenAIToAnthropic_ToolCallTurn(t *testing.T) {
	req := `{
		"model": "gpt-4o",
		"messages": [
			{"role": "user", "content": "What is the weather in NYC?"},
			{"role": "assistant", "content": "Let me look that up.", "tool_calls": [
				{"id": "call_1", "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\":\"NYC\"}"}}
			]},
			{"role": "tool", "tool_call_id": "call_1", "content": "72F and sunny"}
		]
	}`

	got, err := TranslateOpenAIToAnthropic([]byte(req), "claude-sonnet-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := `{
		"model": "claude-sonnet-4",
		"max_tokens": 4096,
		"messages": [
			{"role": "user", "content": "What is the weather in NYC?"},
			{"role": "assistant", "content": [
				{"type": "text", "text": "Let me look that up."},
				{"type": "tool_use", "id": "call_1", "name": "get_weather", "input": {"city": "NYC"}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "call_1", "content": "72F and sunny"}
			]}
		]
	}`
	assertSameJSON(t, want, string(got))
}

func TestTranslateOpenAIToAnthropic_ParallelToolResultsMerge(t *testing.T) {
	// Anthropic requires every tool_result of one assistant turn to sit in the
	// same user message; OpenAI sends them as separate tool role messages.
	req := `{"model":"gpt-4o","messages":[
		{"role":"assistant","tool_calls":[
			{"id":"call_a","function":{"name":"f","arguments":"{}"}},
			{"id":"call_b","function":{"name":"g","arguments":"{}"}}
		]},
		{"role":"tool","tool_call_id":"call_a","content":"one"},
		{"role":"tool","tool_call_id":"call_b","content":"two"},
		{"role":"user","content":"thanks"}
	]}`

	got, err := TranslateOpenAIToAnthropic([]byte(req), "claude-sonnet-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Messages) != 3 {
		t.Fatalf("expected 3 messages (assistant, merged tool results, user), got %d: %s", len(parsed.Messages), got)
	}
	var results []anthropicToolResultBlock
	if err := json.Unmarshal(parsed.Messages[1].Content, &results); err != nil {
		t.Fatalf("merged content is not a tool_result array: %v", err)
	}
	if len(results) != 2 || results[0].ToolUseID != "call_a" || results[1].ToolUseID != "call_b" {
		t.Fatalf("merged tool results wrong: %+v", results)
	}
}

func TestTranslateOpenAIToAnthropic_ToolsAndToolChoice(t *testing.T) {
	base := `"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"get_weather","description":"Look up weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}]`

	cases := []struct {
		name       string
		choice     string
		wantChoice string
	}{
		{"auto string", `,"tool_choice":"auto"`, `{"type":"auto"}`},
		{"required string", `,"tool_choice":"required"`, `{"type":"any"}`},
		{"named function", `,"tool_choice":{"type":"function","function":{"name":"get_weather"}}`, `{"type":"tool","name":"get_weather"}`},
		{"none is dropped", `,"tool_choice":"none"`, ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := TranslateOpenAIToAnthropic([]byte("{"+base+tc.choice+"}"), "claude-sonnet-4")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var parsed struct {
				Tools []struct {
					Name        string          `json:"name"`
					Description string          `json:"description"`
					InputSchema json.RawMessage `json:"input_schema"`
				} `json:"tools"`
				ToolChoice json.RawMessage `json:"tool_choice"`
			}
			if err := json.Unmarshal(got, &parsed); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(parsed.Tools) != 1 || parsed.Tools[0].Name != "get_weather" {
				t.Fatalf("tools not mapped: %s", got)
			}
			if parsed.Tools[0].Description != "Look up weather" {
				t.Fatalf("description lost: %s", got)
			}
			if !strings.Contains(string(parsed.Tools[0].InputSchema), `"city"`) {
				t.Fatalf("input_schema not carried: %s", got)
			}
			if tc.wantChoice == "" {
				if parsed.ToolChoice != nil {
					t.Fatalf("tool_choice should be absent, got %s", parsed.ToolChoice)
				}
				return
			}
			assertSameJSON(t, tc.wantChoice, string(parsed.ToolChoice))
		})
	}
}

func TestTranslateOpenAIToAnthropic_MultimodalUser(t *testing.T) {
	req := `{"model":"gpt-4o","messages":[{"role":"user","content":[
		{"type":"text","text":"what is this"},
		{"type":"image_url","image_url":{"url":"https://example.com/cat.png"}}
	]}]}`

	got, err := TranslateOpenAIToAnthropic([]byte(req), "claude-sonnet-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `{"model":"claude-sonnet-4","max_tokens":4096,"messages":[{"role":"user","content":[
		{"type":"text","text":"what is this"},
		{"type":"image","source":{"type":"url","url":"https://example.com/cat.png"}}
	]}]}`
	assertSameJSON(t, want, string(got))
}

func TestTranslateOpenAIToAnthropic_Base64Image(t *testing.T) {
	req := `{"model":"gpt-4o","messages":[{"role":"user","content":[
		{"type":"text","text":"describe"},
		{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}
	]}]}`

	got, err := TranslateOpenAIToAnthropic([]byte(req), "claude-sonnet-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `{"model":"claude-sonnet-4","max_tokens":4096,"messages":[{"role":"user","content":[
		{"type":"text","text":"describe"},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"QUJD"}}
	]}]}`
	assertSameJSON(t, want, string(got))
}

func TestTranslateOpenAIToAnthropic_SamplingAndStop(t *testing.T) {
	req := `{"model":"gpt-4o","temperature":0.4,"top_p":0.9,"stop":["END","STOP"],"max_tokens":256,"stream":true,"messages":[{"role":"user","content":"hi"}]}`

	got, err := TranslateOpenAIToAnthropic([]byte(req), "claude-sonnet-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `{"model":"claude-sonnet-4","max_tokens":256,"temperature":0.4,"top_p":0.9,"stream":true,"stop_sequences":["END","STOP"],"messages":[{"role":"user","content":"hi"}]}`
	assertSameJSON(t, want, string(got))
}

func TestTranslateOpenAIToAnthropic_BrokenToolArguments(t *testing.T) {
	req := `{"model":"gpt-4o","messages":[{"role":"assistant","tool_calls":[{"id":"c1","function":{"name":"f","arguments":"{broken"}}]}]}`
	got, err := TranslateOpenAIToAnthropic([]byte(req), "claude-sonnet-4")
	if err == nil {
		t.Fatalf("expected error for broken tool arguments, got %s", got)
	}
	if got != nil {
		t.Fatalf("expected nil payload alongside error, got %s", got)
	}
}
