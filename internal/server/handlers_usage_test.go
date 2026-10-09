package server

import (
	"net/http/httptest"
	"testing"
)

func TestParseUsageFromBytes_CachedTokens(t *testing.T) {
	tests := []struct {
		name       string
		data       string
		wantOK     bool
		wantPrompt int
		wantComp   int
		wantRead   int
		wantWrite  int
	}{
		{
			name:       "openai non-stream with prompt_tokens_details",
			data:       `{"usage":{"prompt_tokens":1200,"completion_tokens":80,"total_tokens":1280,"prompt_tokens_details":{"cached_tokens":1024}}}`,
			wantOK:     true,
			wantPrompt: 1200,
			wantComp:   80,
			wantRead:   1024,
		},
		{
			name:       "anthropic non-stream cache read and write",
			data:       `{"usage":{"input_tokens":15,"output_tokens":42,"cache_read_input_tokens":1800,"cache_creation_input_tokens":240}}`,
			wantOK:     true,
			wantPrompt: 15,
			wantComp:   42,
			wantRead:   1800,
			wantWrite:  240,
		},
		{
			name:       "openai sse final chunk carries usage",
			data:       "data: {\"id\":\"1\",\"choices\":[]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":64,\"completion_tokens\":16,\"prompt_tokens_details\":{\"cached_tokens\":48}}}\n\ndata: [DONE]\n\n",
			wantOK:     true,
			wantPrompt: 64,
			wantComp:   16,
			wantRead:   48,
		},
		{
			name:       "anthropic sse message_delta embedded usage",
			data:       "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{},\"usage\":{\"input_tokens\":12,\"output_tokens\":30,\"cache_read_input_tokens\":900}}\n\n",
			wantOK:     true,
			wantPrompt: 12,
			wantComp:   30,
			wantRead:   900,
		},
		{
			name:       "usage without any cache fields",
			data:       `{"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
			wantOK:     true,
			wantPrompt: 10,
			wantComp:   5,
		},
		{
			name:   "no usage at all",
			data:   `{"id":"1","choices":[{"message":{"content":"hi"}}]}`,
			wantOK: false,
		},
		{
			name:   "usage present but zero tokens",
			data:   `{"usage":{"prompt_tokens":0,"completion_tokens":0,"cache_read_input_tokens":500}}`,
			wantOK: false,
		},
		{
			name:   "empty payload",
			data:   "",
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseUsageFromBytes([]byte(tc.data))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if got.promptToks != tc.wantPrompt || got.compToks != tc.wantComp {
				t.Errorf("prompt/comp = %d/%d, want %d/%d", got.promptToks, got.compToks, tc.wantPrompt, tc.wantComp)
			}
			if got.cachedRead != tc.wantRead || got.cacheWrite != tc.wantWrite {
				t.Errorf("cached read/write = %d/%d, want %d/%d", got.cachedRead, got.cacheWrite, tc.wantRead, tc.wantWrite)
			}
		})
	}
}

// TestExtractUsage_TailBufferCachedTokens drives the responseTracker the way
// the forward handler does: the SSE tail window is the only place the final
// usage chunk survives, so cached tokens must be readable from it.
func TestExtractUsage_TailBufferCachedTokens(t *testing.T) {
	tracker := &responseTracker{ResponseWriter: httptest.NewRecorder()}
	chunk := "data: {\"usage\":{\"prompt_tokens\":300,\"completion_tokens\":45,\"prompt_tokens_details\":{\"cached_tokens\":256}}}\n\n"
	if _, err := tracker.Write([]byte(chunk)); err != nil {
		t.Fatalf("write chunk: %v", err)
	}

	got, ok := tracker.extractUsage()
	if !ok {
		t.Fatal("extractUsage reported not found")
	}
	if got.promptToks != 300 || got.compToks != 45 {
		t.Errorf("prompt/comp = %d/%d, want 300/45", got.promptToks, got.compToks)
	}
	if got.cachedRead != 256 {
		t.Errorf("cachedRead = %d, want 256", got.cachedRead)
	}
	if got.cacheWrite != 0 {
		t.Errorf("cacheWrite = %d, want 0", got.cacheWrite)
	}
}

func TestExtractUsage_NilTracker(t *testing.T) {
	var tracker *responseTracker
	if _, ok := tracker.extractUsage(); ok {
		t.Fatal("nil tracker must report no usage")
	}
}
