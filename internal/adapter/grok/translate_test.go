package grok

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestTranslateOpenAIToGrokCLI_BasicChat(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"grok-build","messages":[{"role":"user","content":"Hello Grok"}]}`)
	out, plan, err := TranslateOpenAIToGrokCLI(body, "grok-build")
	require.NoError(t, err)

	assert.Equal(t, "grok-build", plan.UpstreamModel)
	assert.False(t, plan.SupportsEffort)

	assert.Equal(t, "grok-build", gjson.GetBytes(out, "model").String())
	assert.True(t, gjson.GetBytes(out, "stream").Bool())
	assert.False(t, gjson.GetBytes(out, "store").Bool())
	assert.Equal(t, "message", gjson.GetBytes(out, "input.0.type").String())
	assert.Equal(t, "user", gjson.GetBytes(out, "input.0.role").String())
	assert.Equal(t, "input_text", gjson.GetBytes(out, "input.0.content.0.type").String())
	assert.Equal(t, "Hello Grok", gjson.GetBytes(out, "input.0.content.0.text").String())
	assert.Equal(t, "concise", gjson.GetBytes(out, "reasoning.summary").String())
	// grok-build does not support effort -> no effort field, no include.
	assert.False(t, gjson.GetBytes(out, "reasoning.effort").Exists())
	assert.False(t, gjson.GetBytes(out, "include").Exists())
}

func TestTranslateOpenAIToGrokCLI_Grok45Effort(t *testing.T) {
	t.Parallel()

	// grok-4.5-high -> upstream grok-4.5, effort high, include encrypted reasoning.
	out, plan, err := TranslateOpenAIToGrokCLI([]byte(`{"model":"grok-4.5-high","messages":[{"role":"user","content":"hi"}]}`), "grok-4.5-high")
	require.NoError(t, err)
	assert.Equal(t, "grok-4.5", plan.UpstreamModel)
	assert.True(t, plan.SupportsEffort)
	assert.Equal(t, "high", gjson.GetBytes(out, "reasoning.effort").String())
	assert.Equal(t, "reasoning.encrypted_content", gjson.GetBytes(out, "include.0").String())

	// Explicit reasoning_effort overrides the model suffix.
	out2, _, err := TranslateOpenAIToGrokCLI([]byte(`{"model":"grok-4.5-low","reasoning_effort":"medium","messages":[{"role":"user","content":"hi"}]}`), "grok-4.5-low")
	require.NoError(t, err)
	assert.Equal(t, "medium", gjson.GetBytes(out2, "reasoning.effort").String())
}

func TestTranslateOpenAIToGrokCLI_MultiTurnRoles(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"grok-build","messages":[
		{"role":"system","content":"Be concise."},
		{"role":"user","content":"First"},
		{"role":"assistant","content":"Answer"},
		{"role":"user","content":"Second"}
	]}`)
	out, _, err := TranslateOpenAIToGrokCLI(body, "grok-build")
	require.NoError(t, err)

	arr := gjson.GetBytes(out, "input").Array()
	require.Len(t, arr, 4)
	assert.Equal(t, "system", arr[0].Get("role").String())
	assert.Equal(t, "input_text", arr[0].Get("content.0.type").String())
	assert.Equal(t, "assistant", arr[2].Get("role").String())
	// Assistant turns use output_text.
	assert.Equal(t, "output_text", arr[2].Get("content.0.type").String())
	assert.Equal(t, "Second", arr[3].Get("content.0.text").String())
}

func TestTranslateOpenAIToGrokCLI_ArrayContentAndDeveloper(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"grok-build","messages":[
		{"role":"developer","content":[{"type":"text","text":"sys"}]},
		{"role":"user","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}
	]}`)
	out, _, err := TranslateOpenAIToGrokCLI(body, "grok-build")
	require.NoError(t, err)

	arr := gjson.GetBytes(out, "input").Array()
	require.Len(t, arr, 2)
	// developer normalized to system.
	assert.Equal(t, "system", arr[0].Get("role").String())
	assert.Equal(t, "sys", arr[0].Get("content.0.text").String())
	assert.Equal(t, "a b", arr[1].Get("content.0.text").String())
}

func TestTranslateOpenAIToGrokCLI_CustomModelPassthrough(t *testing.T) {
	t.Parallel()

	// Unknown/custom model id is passed through verbatim as the upstream model.
	out, plan, err := TranslateOpenAIToGrokCLI([]byte(`{"model":"grok-5-preview","messages":[{"role":"user","content":"hi"}]}`), "grok-5-preview")
	require.NoError(t, err)
	assert.Equal(t, "grok-5-preview", plan.UpstreamModel)
	assert.Equal(t, "grok-5-preview", gjson.GetBytes(out, "model").String())
}

func TestTranslateOpenAIToGrokCLI_StringInput(t *testing.T) {
	t.Parallel()

	// A Responses-style string input is accepted directly.
	out, _, err := TranslateOpenAIToGrokCLI([]byte(`{"model":"grok-build","input":"just a string"}`), "grok-build")
	require.NoError(t, err)
	assert.Equal(t, "just a string", gjson.GetBytes(out, "input.0.content.0.text").String())
}

func TestTranslateOpenAIToGrokCLI_Errors(t *testing.T) {
	t.Parallel()

	_, _, err := TranslateOpenAIToGrokCLI([]byte(`not json`), "grok-build")
	assert.ErrorIs(t, err, ErrMalformedJSON)

	_, _, err = TranslateOpenAIToGrokCLI([]byte(`{"model":"grok-build","messages":[]}`), "grok-build")
	assert.ErrorIs(t, err, ErrEmptyMessages)

	_, _, err = TranslateOpenAIToGrokCLI([]byte(`{"model":"grok-build","messages":[{"role":"user","content":"   "}]}`), "grok-build")
	assert.ErrorIs(t, err, ErrEmptyMessages)
}

func TestSupportedModels(t *testing.T) {
	t.Parallel()
	models := SupportedModels()
	assert.Contains(t, models, "grok-build")
	assert.Contains(t, models, "grok-4.5")
	assert.Contains(t, models, "grok-4.6")
	assert.Contains(t, models, "grok-4.7")
	assert.Contains(t, models, "grok-4.7-high")
	assert.Contains(t, models, "grok-4.7-medium")
	assert.Contains(t, models, "grok-4.7-low")
	// xhigh is accepted on the wire but not advertised in discovery.
	assert.NotContains(t, models, "grok-4.7-xhigh")
	// grok-build is not an effort family: no synthesized variants.
	assert.NotContains(t, models, "grok-build-high")

	assert.True(t, SupportsModel("grok-build"))
	assert.True(t, SupportsModel("grok-4.5-high"))
	assert.True(t, SupportsModel("grok-4.7-medium"))
	assert.False(t, SupportsModel("grok-4.7-xhigh"))
	assert.False(t, SupportsModel("some-random-model"))

	// The discovery list must be a copy: a mutating caller cannot corrupt it.
	models[0] = "mutated"
	assert.Equal(t, "grok-build", SupportedModels()[0])
}

func TestResolveModel_EffortFamilies(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		model    string
		upstream string
		effort   string
		supports bool
	}{
		{name: "build has no effort", model: "grok-build", upstream: "grok-build"},
		{name: "4.5 base", model: "grok-4.5", upstream: "grok-4.5", supports: true},
		{name: "4.5 suffix", model: "grok-4.5-low", upstream: "grok-4.5", effort: "low", supports: true},
		{name: "4.6 base", model: "grok-4.6", upstream: "grok-4.6", supports: true},
		{name: "4.7 base", model: "grok-4.7", upstream: "grok-4.7", supports: true},
		{name: "4.7 suffix", model: "grok-4.7-high", upstream: "grok-4.7", effort: "high", supports: true},
		{name: "4.7 xhigh", model: "grok-4.7-xhigh", upstream: "grok-4.7", effort: "xhigh", supports: true},
		// A suffix on a non-effort family is not stripped: the id is verbatim.
		{name: "build suffix is verbatim", model: "grok-build-high", upstream: "grok-build-high"},
		// Unknown families stay verbatim and effort-free.
		{name: "unknown verbatim", model: "grok-5-preview", upstream: "grok-5-preview"},
		{name: "unknown with suffix verbatim", model: "custom-model-low", upstream: "custom-model-low"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plan := resolveModel(tc.model)
			assert.Equal(t, tc.upstream, plan.UpstreamModel)
			assert.Equal(t, tc.effort, plan.Effort)
			assert.Equal(t, tc.supports, plan.SupportsEffort)
		})
	}
}

func TestTranslateOpenAIToGrokCLI_Grok47Effort(t *testing.T) {
	t.Parallel()
	// A curated effort family defaults to high effort and requests encrypted
	// reasoning continuity, exactly like grok-4.5.
	out, plan, err := TranslateOpenAIToGrokCLI([]byte(`{"model":"grok-4.7","messages":[{"role":"user","content":"hi"}]}`), "grok-4.7")
	require.NoError(t, err)
	assert.Equal(t, "grok-4.7", plan.UpstreamModel)
	assert.True(t, plan.SupportsEffort)
	assert.Equal(t, "grok-4.7", gjson.GetBytes(out, "model").String())
	assert.Equal(t, "high", gjson.GetBytes(out, "reasoning.effort").String())
	assert.Equal(t, "reasoning.encrypted_content", gjson.GetBytes(out, "include.0").String())

	// An unknown family must not gain an effort field.
	out2, plan2, err := TranslateOpenAIToGrokCLI([]byte(`{"model":"grok-5-preview","messages":[{"role":"user","content":"hi"}]}`), "grok-5-preview")
	require.NoError(t, err)
	assert.False(t, plan2.SupportsEffort)
	assert.Equal(t, "grok-5-preview", gjson.GetBytes(out2, "model").String())
	assert.False(t, gjson.GetBytes(out2, "reasoning.effort").Exists())
	assert.False(t, gjson.GetBytes(out2, "include").Exists())
}

func TestParseResponsesEvent(t *testing.T) {
	t.Parallel()

	// Text delta.
	ev, ok := parseResponsesEvent("response.output_text.delta", []byte(`{"type":"response.output_text.delta","delta":"Hel"}`))
	require.True(t, ok)
	assert.Equal(t, "Hel", ev.Delta)

	// Reasoning delta.
	ev, ok = parseResponsesEvent("response.reasoning_summary_text.delta", []byte(`{"delta":"thinking..."}`))
	require.True(t, ok)
	assert.Equal(t, "thinking...", ev.Reasoning)

	// Completed with usage.
	ev, ok = parseResponsesEvent("response.completed", []byte(`{"response":{"usage":{"input_tokens":10,"output_tokens":5}}}`))
	require.True(t, ok)
	assert.True(t, ev.Done)
	require.NotNil(t, ev.Usage)
	assert.Equal(t, 10, ev.Usage.Prompt)
	assert.Equal(t, 5, ev.Usage.Completion)
	assert.Equal(t, 15, ev.Usage.Total)

	// Error.
	ev, ok = parseResponsesEvent("error", []byte(`{"error":{"message":"model_not_found"}}`))
	require.True(t, ok)
	assert.Equal(t, "model_not_found", ev.Error)
	assert.True(t, ev.Done)

	// Event type taken from JSON when SSE event line absent.
	ev, ok = parseResponsesEvent("", []byte(`{"type":"response.output_text.delta","delta":"x"}`))
	require.True(t, ok)
	assert.Equal(t, "x", ev.Delta)

	// Ignored event.
	_, ok = parseResponsesEvent("response.output_item.added", []byte(`{"type":"response.output_item.added"}`))
	assert.False(t, ok)
}

func TestBuildChunkAndCompletion(t *testing.T) {
	t.Parallel()

	c := buildChunk("id1", "grok-build", 1700000000, map[string]any{"content": "hi"}, "", nil)
	require.True(t, gjson.ValidBytes(c))
	assert.Equal(t, "chat.completion.chunk", gjson.GetBytes(c, "object").String())
	assert.Equal(t, "hi", gjson.GetBytes(c, "choices.0.delta.content").String())
	assert.Equal(t, gjson.Null, gjson.GetBytes(c, "choices.0.finish_reason").Type)

	stop := buildChunk("id1", "grok-build", 1700000000, map[string]any{}, "stop", &openAIUsage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3})
	assert.Equal(t, "stop", gjson.GetBytes(stop, "choices.0.finish_reason").String())
	assert.Equal(t, int64(3), gjson.GetBytes(stop, "usage.total_tokens").Int())

	full := buildCompletion("cid", "grok-build", 1700000000, "Hello world", "reasoned", &usageInfo{Prompt: 4, Completion: 6, Total: 10})
	require.True(t, gjson.ValidBytes(full))
	assert.Equal(t, "chat.completion", gjson.GetBytes(full, "object").String())
	assert.Equal(t, "Hello world", gjson.GetBytes(full, "choices.0.message.content").String())
	assert.Equal(t, "reasoned", gjson.GetBytes(full, "choices.0.message.reasoning_content").String())
	assert.Equal(t, "stop", gjson.GetBytes(full, "choices.0.finish_reason").String())
	assert.Equal(t, int64(10), gjson.GetBytes(full, "usage.total_tokens").Int())
}
