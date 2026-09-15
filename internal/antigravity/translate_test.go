package antigravity

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestTranslateOpenAIToAntigravity(t *testing.T) {
	t.Parallel()

	t.Run("basic user prompt and system rewrite", func(t *testing.T) {
		t.Parallel()
		inbound := []byte(`{
			"model": "gemini-2.5-pro",
			"messages": [
				{"role": "system", "content": "You are a Claude agent, built on Anthropic's Claude Agent SDK. Solve tasks using opencode tools."},
				{"role": "user", "content": "Hello, write a quicksort algorithm."}
			],
			"temperature": 0.7,
			"max_tokens": 2048
		}`)

		out, err := TranslateOpenAIToAntigravity(inbound, "gemini-2.5-pro-experimental", "my-project-123")
		require.NoError(t, err)
		require.True(t, gjson.ValidBytes(out))

		assert.Equal(t, "my-project-123", gjson.GetBytes(out, "project").String())
		assert.Equal(t, "gemini-2.5-pro-experimental", gjson.GetBytes(out, "model").String())
		assert.Equal(t, "antigravity", gjson.GetBytes(out, "userAgent").String())
		assert.Contains(t, gjson.GetBytes(out, "requestId").String(), "agent/")

		// Verify system prompt was rewritten
		sysText := gjson.GetBytes(out, "request.systemInstruction.parts.0.text").String()
		assert.NotContains(t, sysText, "Claude agent")
		assert.Contains(t, sysText, "antigravity tools")

		// Verify user content
		contents := gjson.GetBytes(out, "request.contents").Array()
		require.Len(t, contents, 1)
		assert.Equal(t, "user", contents[0].Get("role").String())
		assert.Equal(t, "Hello, write a quicksort algorithm.", contents[0].Get("parts.0.text").String())

		// Verify generation config
		assert.Equal(t, 0.7, gjson.GetBytes(out, "request.generationConfig.temperature").Float())
		assert.Equal(t, int64(2048), gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int())
	})

	t.Run("tools and function calling", func(t *testing.T) {
		t.Parallel()
		inbound := []byte(`{
			"model": "gemini-2.5-flash",
			"messages": [
				{"role": "user", "content": "What is the weather?"}
			],
			"tools": [
				{
					"type": "function",
					"function": {
						"name": "get-weather!special",
						"description": "Fetch weather",
						"parameters": {
							"type": "object",
							"properties": {"city": {"type": "string"}}
						}
					}
				}
			]
		}`)

		out, err := TranslateOpenAIToAntigravity(inbound, "", "")
		require.NoError(t, err)

		tools := gjson.GetBytes(out, "request.tools.0.functionDeclarations").Array()
		require.Len(t, tools, 1)
		// Sanitized function name
		assert.Equal(t, "get-weather_special", tools[0].Get("name").String())
		assert.Equal(t, "Fetch weather", tools[0].Get("description").String())
	})

	t.Run("empty messages error", func(t *testing.T) {
		t.Parallel()
		inbound := []byte(`{"model": "gemini-2.5-flash", "messages": []}`)
		_, err := TranslateOpenAIToAntigravity(inbound, "", "")
		assert.ErrorIs(t, err, ErrEmptyMessages)
	})

	t.Run("malformed json error", func(t *testing.T) {
		t.Parallel()
		_, err := TranslateOpenAIToAntigravity([]byte(`invalid-json`), "", "")
		assert.ErrorIs(t, err, ErrMalformedJSON)
	})
}

func TestTranslateAntigravityChunkToOpenAI(t *testing.T) {
	t.Parallel()

	state := &StreamState{Model: "gemini-2.5-pro"}

	// 1. Text chunk
	chunk1 := []byte(`{
		"response": {
			"candidates": [
				{
					"content": {
						"role": "model",
						"parts": [
							{"text": "Hello world!"}
						]
					}
				}
			]
		}
	}`)

	chunks, isDone, err := TranslateAntigravityChunkToOpenAI(chunk1, "gemini-2.5-pro", state)
	require.NoError(t, err)
	assert.False(t, isDone)
	// First chunk should include role chunk + content chunk
	require.Len(t, chunks, 2)
	assert.Equal(t, "assistant", gjson.GetBytes(chunks[0], "choices.0.delta.role").String())
	assert.Equal(t, "Hello world!", gjson.GetBytes(chunks[1], "choices.0.delta.content").String())

	// 2. Reasoning chunk
	chunk2 := []byte(`{
		"response": {
			"candidates": [
				{
					"content": {
						"role": "model",
						"parts": [
							{"text": "thinking step 1", "thought": true}
						]
					}
				}
			]
		}
	}`)

	chunks, isDone, err = TranslateAntigravityChunkToOpenAI(chunk2, "gemini-2.5-pro", state)
	require.NoError(t, err)
	assert.False(t, isDone)
	require.Len(t, chunks, 1)
	assert.Equal(t, "thinking step 1", gjson.GetBytes(chunks[0], "choices.0.delta.reasoning_content").String())

	// 3. Finish chunk
	chunk3 := []byte(`{
		"response": {
			"candidates": [
				{
					"finishReason": "STOP"
				}
			]
		}
	}`)

	chunks, isDone, err = TranslateAntigravityChunkToOpenAI(chunk3, "gemini-2.5-pro", state)
	require.NoError(t, err)
	assert.True(t, isDone)
	require.Len(t, chunks, 1)
	assert.Equal(t, "stop", gjson.GetBytes(chunks[0], "choices.0.finish_reason").String())
}

func TestTranslateAntigravityToOpenAI(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"response": {
			"candidates": [
				{
					"content": {
						"role": "model",
						"parts": [
							{"text": "Here is the response from Antigravity."}
						]
					},
					"finishReason": "STOP"
				}
			],
			"usageMetadata": {
				"promptTokenCount": 20,
				"candidatesTokenCount": 10,
				"totalTokenCount": 30
			}
		}
	}`)

	out, err := TranslateAntigravityToOpenAI(raw, "gemini-2.5-pro")
	require.NoError(t, err)
	require.True(t, gjson.ValidBytes(out))

	assert.Equal(t, "chat.completion", gjson.GetBytes(out, "object").String())
	assert.Equal(t, "gemini-2.5-pro", gjson.GetBytes(out, "model").String())
	assert.Equal(t, "Here is the response from Antigravity.", gjson.GetBytes(out, "choices.0.message.content").String())
	assert.Equal(t, "stop", gjson.GetBytes(out, "choices.0.finish_reason").String())
	assert.Equal(t, int64(20), gjson.GetBytes(out, "usage.prompt_tokens").Int())
	assert.Equal(t, int64(10), gjson.GetBytes(out, "usage.completion_tokens").Int())
	assert.Equal(t, int64(30), gjson.GetBytes(out, "usage.total_tokens").Int())
}

func TestSanitizeFunctionName(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "_unknown", SanitizeFunctionName(""))
	assert.Equal(t, "_123test", SanitizeFunctionName("123test"))
	assert.Equal(t, "valid_func_name-123", SanitizeFunctionName("valid_func_name-123"))
	assert.Equal(t, "some_special_chars_here", SanitizeFunctionName("some*special!chars#here"))
}

func TestBuildIdeRequestID(t *testing.T) {
	t.Parallel()
	reqID := BuildIdeRequestID("session-123", "gemini-2.5", 3)
	assert.Regexp(t, `^agent/[a-f0-9\-]+/\d+/[a-f0-9\-]+/\d+$`, reqID)
}
