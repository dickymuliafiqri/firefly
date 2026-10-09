package anthropic

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestMapFinishReasonToAnthropic(t *testing.T) {
	cases := []struct{ in, want string }{
		{"stop", "end_turn"},
		{"length", "max_tokens"},
		{"tool_calls", "tool_use"},
		{"function_call", "tool_use"},
		{"content_filter", "refusal"},
		{"", "end_turn"},
		{"something_new", "end_turn"},
	}
	for _, tc := range cases {
		if got := mapFinishReasonToAnthropic(tc.in); got != tc.want {
			t.Errorf("mapFinishReasonToAnthropic(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAnthropicMessageID(t *testing.T) {
	if got := anthropicMessageID("chatcmpl-abc123"); got != "msg_abc123" {
		t.Errorf("got %q", got)
	}
	if got := anthropicMessageID("msg-abc"); got != "msg_msg-abc" {
		t.Errorf("got %q", got)
	}
	if got := anthropicMessageID(""); !strings.HasPrefix(got, "msg_") {
		t.Errorf("empty id should synthesise, got %q", got)
	}
}

func TestTranslateOpenAIToAnthropicResponse(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		publicName string
		want       string
	}{
		{
			name:       "text only",
			publicName: "claude-sonnet-4",
			body:       `{"id":"chatcmpl-abc","object":"chat.completion","model":"gpt-4o-2024","choices":[{"index":0,"message":{"role":"assistant","content":"Hello there"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}`,
			want:       `{"id":"msg_abc","type":"message","role":"assistant","content":[{"type":"text","text":"Hello there"}],"model":"claude-sonnet-4","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":12,"output_tokens":3}}`,
		},
		{
			name:       "tool call",
			publicName: "claude-sonnet-4",
			body:       `{"id":"chatcmpl-xyz","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"Let me check","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"NYC\",\"unit\":\"c\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":40,"completion_tokens":18}}`,
			want:       `{"id":"msg_xyz","type":"message","role":"assistant","content":[{"type":"text","text":"Let me check"},{"type":"tool_use","id":"call_1","name":"get_weather","input":{"city":"NYC","unit":"c"}}],"model":"claude-sonnet-4","stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":40,"output_tokens":18}}`,
		},
		{
			name:       "length truncation",
			publicName: "claude-sonnet-4",
			body:       `{"id":"chatcmpl-len","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"cut off"},"finish_reason":"length"}],"usage":{"prompt_tokens":5,"completion_tokens":4096}}`,
			want:       `{"id":"msg_len","type":"message","role":"assistant","content":[{"type":"text","text":"cut off"}],"model":"claude-sonnet-4","stop_reason":"max_tokens","stop_sequence":null,"usage":{"input_tokens":5,"output_tokens":4096}}`,
		},
		{
			name:       "cache usage counters",
			publicName: "claude-sonnet-4",
			body:       `{"id":"chatcmpl-cache","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"cached"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":64}}}`,
			want:       `{"id":"msg_cache","type":"message","role":"assistant","content":[{"type":"text","text":"cached"}],"model":"claude-sonnet-4","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":64}}`,
		},
		{
			name:       "anthropic style usage spelling",
			publicName: "claude-sonnet-4",
			body:       `{"id":"chatcmpl-alt","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"alt"},"finish_reason":"stop"}],"usage":{"input_tokens":7,"output_tokens":9}}`,
			want:       `{"id":"msg_alt","type":"message","role":"assistant","content":[{"type":"text","text":"alt"}],"model":"claude-sonnet-4","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":7,"output_tokens":9}}`,
		},
		{
			name:       "model falls back to upstream name",
			publicName: "",
			body:       `{"id":"chatcmpl-m","model":"gpt-4o-2024-08-06","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			want:       `{"id":"msg_m","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"model":"gpt-4o-2024-08-06","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`,
		},
		{
			name:       "empty content becomes empty text block",
			publicName: "claude-sonnet-4",
			body:       `{"id":"chatcmpl-empty","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":0}}`,
			want:       `{"id":"msg_empty","type":"message","role":"assistant","content":[{"type":"text","text":""}],"model":"claude-sonnet-4","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := TranslateOpenAIToAnthropicResponse([]byte(tc.body), tc.publicName)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertSameJSON(t, tc.want, string(got))
		})
	}
}

func TestTranslateOpenAIToAnthropicResponse_Errors(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"empty body", ""},
		{"not json", `{oops`},
		{"no choices", `{"id":"chatcmpl-a","model":"gpt-4o","usage":{}}`},
		{"tool call without name", `{"id":"chatcmpl-b","choices":[{"index":0,"message":{"role":"assistant","content":"x","tool_calls":[{"id":"call_1","function":{"arguments":"{}"}}]}}]}`},
		{"tool call with broken arguments", `{"id":"chatcmpl-c","choices":[{"index":0,"message":{"role":"assistant","content":"x","tool_calls":[{"id":"call_1","function":{"name":"f","arguments":"{not json"}}]}}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := TranslateOpenAIToAnthropicResponse([]byte(tc.body), "claude-sonnet-4")
			if err == nil {
				t.Fatalf("expected error, got %s", got)
			}
			if got != nil {
				t.Fatalf("expected nil payload alongside error, got %s", got)
			}
		})
	}

	if _, err := TranslateOpenAIToAnthropicResponse([]byte(""), "m"); !errors.Is(err, ErrMalformedJSON) {
		t.Fatalf("empty body should be ErrMalformedJSON, got %v", err)
	}
}

// assertSameJSON compares two JSON documents structurally so key order and
// whitespace never matter.
func assertSameJSON(t *testing.T, want, got string) {
	t.Helper()
	var wantV, gotV any
	if err := json.Unmarshal([]byte(want), &wantV); err != nil {
		t.Fatalf("want is not valid json: %v", err)
	}
	if err := json.Unmarshal([]byte(got), &gotV); err != nil {
		t.Fatalf("got is not valid json: %v\n%s", err, got)
	}
	wantNorm, _ := json.Marshal(wantV)
	gotNorm, _ := json.Marshal(gotV)
	if string(wantNorm) != string(gotNorm) {
		t.Fatalf("json mismatch\nwant: %s\ngot:  %s", wantNorm, gotNorm)
	}
}

func eventNames(events []AnthropicStreamEvent) []string {
	names := make([]string, len(events))
	for i, ev := range events {
		names[i] = ev.Event
	}
	return names
}

func TestTranslateOpenAIChunkToAnthropic_TextStream(t *testing.T) {
	chunks := []string{
		`{"id":"chatcmpl-s1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"id":"chatcmpl-s1","choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
		`{"id":"chatcmpl-s1","choices":[{"index":0,"delta":{"content":" world"}}]}`,
		`{"id":"chatcmpl-s1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"id":"chatcmpl-s1","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":2,"total_tokens":13}}`,
	}

	st := NewAnthropicStreamState("claude-sonnet-4", 0)
	var all []AnthropicStreamEvent
	for _, c := range chunks {
		events, err := TranslateOpenAIChunkToAnthropic([]byte(c), st)
		if err != nil {
			t.Fatalf("chunk %s: %v", c, err)
		}
		all = append(all, events...)
	}
	all = append(all, FlushAnthropicStream(st)...)

	want := []string{
		"message_start",
		"content_block_start", "content_block_delta", "content_block_delta", "content_block_stop",
		"message_delta",
		"message_stop",
	}
	got := eventNames(all)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("event sequence mismatch\nwant: %v\ngot:  %v", want, got)
	}

	assertSameJSON(t, `{"type":"message_start","message":{"id":"msg_s1","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0}}}`, string(all[0].Data))
	assertSameJSON(t, `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`, string(all[1].Data))
	assertSameJSON(t, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`, string(all[2].Data))
	assertSameJSON(t, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`, string(all[3].Data))
	assertSameJSON(t, `{"type":"content_block_stop","index":0}`, string(all[4].Data))
	assertSameJSON(t, `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":11,"output_tokens":2}}`, string(all[5].Data))
	assertSameJSON(t, `{"type":"message_stop"}`, string(all[6].Data))
}

func TestTranslateOpenAIChunkToAnthropic_ToolStream(t *testing.T) {
	chunks := []string{
		`{"id":"chatcmpl-t1","choices":[{"index":0,"delta":{"role":"assistant","content":null}}]}`,
		`{"id":"chatcmpl-t1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}`,
		`{"id":"chatcmpl-t1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]}}]}`,
		`{"id":"chatcmpl-t1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"NYC\"}"}}]}}]}`,
		`{"id":"chatcmpl-t1","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"get_time","arguments":"{\"tz\":\"UTC\"}"}}]}}]}`,
		`{"id":"chatcmpl-t1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"id":"chatcmpl-t1","choices":[],"usage":{"prompt_tokens":30,"completion_tokens":44}}`,
	}

	st := NewAnthropicStreamState("claude-sonnet-4", 30)
	var all []AnthropicStreamEvent
	for _, c := range chunks {
		events, err := TranslateOpenAIChunkToAnthropic([]byte(c), st)
		if err != nil {
			t.Fatalf("chunk %s: %v", c, err)
		}
		all = append(all, events...)
	}
	all = append(all, FlushAnthropicStream(st)...)

	want := []string{
		"message_start",
		"content_block_start", "content_block_delta", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_stop",
		"message_delta",
		"message_stop",
	}
	got := eventNames(all)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("event sequence mismatch\nwant: %v\ngot:  %v", want, got)
	}

	assertSameJSON(t, `{"type":"message_start","message":{"id":"msg_t1","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":30}}}`, string(all[0].Data))
	assertSameJSON(t, `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_a","name":"get_weather","input":{}}}`, string(all[1].Data))
	assertSameJSON(t, `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`, string(all[2].Data))
	assertSameJSON(t, `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"NYC\"}"}}`, string(all[3].Data))
	assertSameJSON(t, `{"type":"content_block_stop","index":0}`, string(all[4].Data))
	assertSameJSON(t, `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_b","name":"get_time","input":{}}}`, string(all[5].Data))
	assertSameJSON(t, `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"tz\":\"UTC\"}"}}`, string(all[6].Data))
	assertSameJSON(t, `{"type":"content_block_stop","index":1}`, string(all[7].Data))
	assertSameJSON(t, `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":30,"output_tokens":44}}`, string(all[8].Data))
	assertSameJSON(t, `{"type":"message_stop"}`, string(all[9].Data))
}

func TestTranslateOpenAIChunkToAnthropic_TextThenTool(t *testing.T) {
	// A preamble text block must be closed before the tool_use block opens, and
	// text arriving after a tool call opens a fresh block at a new index.
	st := NewAnthropicStreamState("claude-sonnet-4", 0)
	var all []AnthropicStreamEvent
	for _, c := range []string{
		`{"id":"chatcmpl-x","choices":[{"index":0,"delta":{"content":"thinking"}}]}`,
		`{"id":"chatcmpl-x","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f","arguments":"{}"}}]}}]}`,
		`{"id":"chatcmpl-x","choices":[{"index":0,"delta":{"content":"after"}}]}`,
	} {
		events, err := TranslateOpenAIChunkToAnthropic([]byte(c), st)
		if err != nil {
			t.Fatalf("chunk %s: %v", c, err)
		}
		all = append(all, events...)
	}
	all = append(all, FlushAnthropicStream(st)...)

	want := []string{
		"message_start",
		"content_block_start", "content_block_delta",
		"content_block_stop",
		"content_block_start", "content_block_delta",
		"content_block_stop",
		"content_block_start", "content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}
	got := eventNames(all)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("event sequence mismatch\nwant: %v\ngot:  %v", want, got)
	}
	assertSameJSON(t, `{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`, string(all[7].Data))
}

func TestTranslateOpenAIChunkToAnthropic_Errors(t *testing.T) {
	if _, err := TranslateOpenAIChunkToAnthropic([]byte(`{}`), nil); err == nil {
		t.Fatal("nil state must error")
	}
	st := NewAnthropicStreamState("m", 0)
	if _, err := TranslateOpenAIChunkToAnthropic([]byte(""), st); !errors.Is(err, ErrMalformedJSON) {
		t.Fatalf("empty chunk should be ErrMalformedJSON, got %v", err)
	}
	if _, err := TranslateOpenAIChunkToAnthropic([]byte(`not json`), st); err == nil {
		t.Fatal("malformed chunk must error")
	}
}

func TestFlushAnthropicStream_Idempotent(t *testing.T) {
	st := NewAnthropicStreamState("claude-sonnet-4", 5)
	first := FlushAnthropicStream(st)
	if len(first) == 0 {
		t.Fatal("flush on a fresh stream must still terminate it")
	}
	if first[len(first)-1].Event != "message_stop" {
		t.Fatalf("flush must end with message_stop, got %q", first[len(first)-1].Event)
	}
	if second := FlushAnthropicStream(st); second != nil {
		t.Fatalf("second flush must be a no-op, got %v", eventNames(second))
	}
	if FlushAnthropicStream(nil) != nil {
		t.Fatal("nil state flush must be a no-op")
	}
}

func TestFormatAnthropicSSE(t *testing.T) {
	events := []AnthropicStreamEvent{
		{Event: "message_start", Data: json.RawMessage(`{"type":"message_start"}`)},
		{Event: "message_stop", Data: json.RawMessage(`{"type":"message_stop"}`)},
	}
	want := "event: message_start\ndata: {\"type\":\"message_start\"}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	if got := string(FormatAnthropicSSE(events)); got != want {
		t.Fatalf("sse framing mismatch\nwant: %q\ngot:  %q", want, got)
	}
	if got := string(FormatAnthropicSSE(nil)); got != "" {
		t.Fatalf("empty events must render nothing, got %q", got)
	}
}

func TestTranslateOpenAIErrorToAnthropic(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{
			name:   "openai invalid request",
			status: 400,
			body:   `{"error":{"message":"model is required","type":"invalid_request_error","param":"model","code":"missing"}}`,
			want:   `{"type":"error","error":{"type":"invalid_request_error","message":"model is required"}}`,
		},
		{
			name:   "openai rate limit",
			status: 429,
			body:   `{"error":{"message":"Rate limit reached","type":"rate_limit_exceeded"}}`,
			want:   `{"type":"error","error":{"type":"rate_limit_error","message":"Rate limit reached"}}`,
		},
		{
			name:   "openai auth",
			status: 401,
			body:   `{"error":{"message":"Incorrect API key","type":"invalid_api_key"}}`,
			want:   `{"type":"error","error":{"type":"authentication_error","message":"Incorrect API key"}}`,
		},
		{
			name:   "bare status with plain text",
			status: 503,
			body:   `upstream is on fire`,
			want:   `{"type":"error","error":{"type":"overloaded_error","message":"upstream is on fire"}}`,
		},
		{
			name:   "empty body falls back to status text",
			status: 404,
			body:   ``,
			want:   `{"type":"error","error":{"type":"not_found_error","message":"Not Found"}}`,
		},
		{
			name:   "unknown status",
			status: 418,
			body:   `{"error":{"message":"teapot","type":"weird"}}`,
			want:   `{"type":"error","error":{"type":"api_error","message":"teapot"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TranslateOpenAIErrorToAnthropic(tc.status, []byte(tc.body))
			assertSameJSON(t, tc.want, string(got))
		})
	}
}

func TestAnthropicErrorSSE(t *testing.T) {
	got := string(AnthropicErrorSSE(429, []byte(`{"error":{"message":"slow down","type":"rate_limit_exceeded"}}`)))
	if !strings.HasPrefix(got, "event: error\ndata: ") || !strings.HasSuffix(got, "\n\n") {
		t.Fatalf("bad sse framing: %q", got)
	}
	if !strings.Contains(got, `"type":"rate_limit_error"`) {
		t.Fatalf("error type not mapped: %q", got)
	}
}
