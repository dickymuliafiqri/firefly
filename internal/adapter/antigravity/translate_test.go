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

	t.Run("tools with unsupported JSON Schema fields", func(t *testing.T) {
		t.Parallel()
		inbound := []byte(`{
			"model": "gemini-3.8-flash",
			"messages": [
				{"role": "user", "content": "Search for files"}
			],
			"tools": [
				{
					"type": "function",
					"function": {
						"name": "search_files",
						"description": "Search files by criteria",
						"parameters": {
							"$schema": "https://json-schema.org/draft/2020-12/schema",
							"type": "object",
							"properties": {
								"query": {
									"type": "string",
									"minLength": 1,
									"maxLength": 500
								},
								"count": {
									"type": "integer",
									"exclusiveMinimum": 0,
									"maximum": 100
								},
								"filters": {
									"type": "array",
									"items": {
										"type": "object",
										"properties": {
											"field": {"type": "string"},
											"value": {
												"any_of": [
													{"type": "string"},
													{"type": "integer", "exclusiveMinimum": -1}
												]
											}
										}
									}
								}
							},
							"required": ["query"],
							"additionalProperties": false
						}
					}
				},
				{
					"type": "function",
					"function": {
						"name": "no_params_tool",
						"description": "A tool with schema-level meta",
						"parameters": {
							"$schema": "http://json-schema.org/draft-07/schema#",
							"$id": "https://example.com/schema.json",
							"type": "object",
							"properties": {
								"x": {"type": "number", "exclusiveMaximum": 10}
							},
							"default": {},
							"examples": [{"x": 5}]
						}
					}
				}
			]
		}`)

		out, err := TranslateOpenAIToAntigravity(inbound, "gemini-3.8-flash", "proj-abc")
		require.NoError(t, err)
		require.True(t, gjson.ValidBytes(out))

		tools := gjson.GetBytes(out, "request.tools.0.functionDeclarations").Array()
		require.Len(t, tools, 2)

		// Tool 0: verify $schema removed, exclusiveMinimum converted, any_of normalized
		params0 := tools[0].Get("parameters")
		assert.False(t, params0.Get("$schema").Exists(), "$schema should be stripped")
		assert.False(t, params0.Get("additionalProperties").Exists(), "additionalProperties should be stripped")
		assert.Equal(t, "object", params0.Get("type").String())

		// exclusiveMinimum: 0 should become minimum: 0
		countProp := params0.Get("properties.count")
		assert.False(t, countProp.Get("exclusiveMinimum").Exists(), "exclusiveMinimum should be removed")
		assert.Equal(t, float64(0), countProp.Get("minimum").Float(), "exclusiveMinimum should convert to minimum")
		assert.Equal(t, float64(100), countProp.Get("maximum").Float())

		// Nested: items.properties.value.any_of should be normalized to anyOf
		// and exclusiveMinimum inside should be converted
		valueSchema := params0.Get("properties.filters.items.properties.value")
		assert.False(t, valueSchema.Get("any_of").Exists(), "any_of should be normalized to anyOf")
		anyOfArr := valueSchema.Get("anyOf").Array()
		require.Len(t, anyOfArr, 2)
		assert.Equal(t, "string", anyOfArr[0].Get("type").String())
		assert.Equal(t, "integer", anyOfArr[1].Get("type").String())
		assert.False(t, anyOfArr[1].Get("exclusiveMinimum").Exists(), "nested exclusiveMinimum should be removed")
		assert.Equal(t, float64(-1), anyOfArr[1].Get("minimum").Float(), "nested exclusiveMinimum should convert to minimum")

		// Tool 1: verify $schema, $id, default, examples all stripped
		params1 := tools[1].Get("parameters")
		assert.False(t, params1.Get("$schema").Exists(), "$schema should be stripped")
		assert.False(t, params1.Get("$id").Exists(), "$id should be stripped")
		assert.False(t, params1.Get("default").Exists(), "default should be stripped")
		assert.False(t, params1.Get("examples").Exists(), "examples should be stripped")

		// exclusiveMaximum: 10 should become maximum: 10
		xProp := params1.Get("properties.x")
		assert.False(t, xProp.Get("exclusiveMaximum").Exists(), "exclusiveMaximum should be removed")
		assert.Equal(t, float64(10), xProp.Get("maximum").Float(), "exclusiveMaximum should convert to maximum")
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

// Gemini 3 rejects functionCall parts echoed back without the thoughtSignature
// that was returned with the original call. This is the request-side half of
// the round trip: the signature the client preserved must be replayed verbatim
// onto the Gemini part.
func TestTranslateOpenAIToAntigravity_ThoughtSignatureRoundTrip(t *testing.T) {
	t.Parallel()

	inbound := []byte(`{
		"model": "gemini-3.8-flash",
		"messages": [
			{"role": "user", "content": "Search for TODO"},
			{
				"role": "assistant",
				"content": null,
				"tool_calls": [
					{
						"id": "call_grep_1",
						"type": "function",
						"thought_signature": "CvwBAAdFCly7abc123",
						"function": {"name": "grep", "arguments": "{\"pattern\": \"TODO\"}"}
					}
				]
			},
			{"role": "tool", "tool_call_id": "call_grep_1", "name": "grep", "content": "main.go:10: // TODO"}
		]
	}`)

	out, err := TranslateOpenAIToAntigravity(inbound, "gemini-3.8-flash", "proj-1")
	require.NoError(t, err)

	// The model turn carries the functionCall part with its signature.
	fc := gjson.GetBytes(out, "request.contents.1.parts.0.functionCall")
	require.True(t, fc.Exists())
	assert.Equal(t, "grep", fc.Get("name").String())
	assert.Equal(t, "CvwBAAdFCly7abc123", gjson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String())
}

func TestTranslateOpenAIToAntigravity_ThoughtSignatureFromFunctionObject(t *testing.T) {
	t.Parallel()

	// Some clients stash custom fields inside the function object instead.
	inbound := []byte(`{
		"model": "gemini-3.8-flash",
		"messages": [
			{"role": "user", "content": "hi"},
			{
				"role": "assistant",
				"tool_calls": [
					{
						"id": "call_1",
						"type": "function",
						"function": {
							"name": "bash",
							"arguments": "{}",
							"thought_signature": "sig-inside-function"
						}
					}
				]
			}
		]
	}`)

	out, err := TranslateOpenAIToAntigravity(inbound, "gemini-3.8-flash", "proj-1")
	require.NoError(t, err)
	assert.Equal(t, "sig-inside-function", gjson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String())
}

// gemini-2.5 clients never carry the field, and the translation must stay a
// no-op (no empty key emitted).
func TestTranslateOpenAIToAntigravity_NoThoughtSignature(t *testing.T) {
	t.Parallel()

	inbound := []byte(`{
		"model": "gemini-2.5-flash",
		"messages": [
			{"role": "user", "content": "hi"},
			{
				"role": "assistant",
				"tool_calls": [
					{"id": "call_1", "type": "function", "function": {"name": "bash", "arguments": "{}"}}
				]
			}
		]
	}`)

	out, err := TranslateOpenAIToAntigravity(inbound, "gemini-2.5-flash", "proj-1")
	require.NoError(t, err)
	assert.False(t, gjson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").Exists())
}

func TestTranslateAntigravityChunkToOpenAI_ThoughtSignatureOnFunctionCall(t *testing.T) {
	t.Parallel()

	state := &StreamState{Model: "gemini-3.8-flash"}

	chunk := []byte(`{
		"response": {
			"candidates": [
				{
					"content": {
						"role": "model",
						"parts": [
							{
								"functionCall": {"id": "call_grep_1", "name": "grep", "args": {"pattern": "TODO"}},
								"thoughtSignature": "CvwBAAdFCly7xyz789"
							}
						]
					}
				}
			]
		}
	}`)

	chunks, isDone, err := TranslateAntigravityChunkToOpenAI(chunk, "gemini-3.8-flash", state)
	require.NoError(t, err)
	assert.False(t, isDone)

	// chunks[0] is the role chunk; chunks[1] is the tool call chunk.
	require.Len(t, chunks, 2)
	tc := gjson.GetBytes(chunks[1], "choices.0.delta.tool_calls.0")
	assert.Equal(t, "call_grep_1", tc.Get("id").String())
	assert.Equal(t, "CvwBAAdFCly7xyz789", tc.Get("thought_signature").String())

	// The signature is queued for the adapter to drain into the store.
	require.Len(t, state.ToolSignatures, 1)
	assert.Equal(t, "call_grep_1", state.ToolSignatures[0].ID)
	assert.Equal(t, "CvwBAAdFCly7xyz789", state.ToolSignatures[0].Sig)
}

// Gemini 3 usually puts the signature on a separate thought part that precedes
// the functionCall; the pending signature must be replayed onto the call.
func TestTranslateAntigravityChunkToOpenAI_PendingThoughtSignature(t *testing.T) {
	t.Parallel()

	state := &StreamState{Model: "gemini-3.8-flash"}

	thought := []byte(`{
		"response": {
			"candidates": [
				{
					"content": {
						"role": "model",
						"parts": [
							{"text": "planning...", "thought": true, "thoughtSignature": "pending-sig-42"}
						]
					}
				}
			]
		}
	}`)
	chunks, _, err := TranslateAntigravityChunkToOpenAI(thought, "gemini-3.8-flash", state)
	require.NoError(t, err)
	require.Len(t, chunks, 2)
	assert.Equal(t, "planning...", gjson.GetBytes(chunks[1], "choices.0.delta.reasoning_content").String())
	assert.Equal(t, "pending-sig-42", state.PendingThoughtSig)

	fnChunk := []byte(`{
		"response": {
			"candidates": [
				{
					"content": {
						"role": "model",
						"parts": [
							{"functionCall": {"id": "call_bash_2", "name": "bash", "args": {"cmd": "ls"}}}
						]
					}
				}
			]
		}
	}`)
	chunks, _, err = TranslateAntigravityChunkToOpenAI(fnChunk, "gemini-3.8-flash", state)
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	assert.Equal(t, "pending-sig-42", gjson.GetBytes(chunks[0], "choices.0.delta.tool_calls.0.thought_signature").String())

	require.Len(t, state.ToolSignatures, 1)
	assert.Equal(t, "call_bash_2", state.ToolSignatures[0].ID)
	assert.Equal(t, "pending-sig-42", state.ToolSignatures[0].Sig)
}

func TestTranslateAntigravityToOpenAI_ThoughtSignature(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"response": {
			"candidates": [
				{
					"content": {
						"role": "model",
						"parts": [
							{"text": "thinking", "thought": true, "thoughtSignature": "pending-sig-9"},
							{"functionCall": {"id": "call_grep_9", "name": "grep", "args": {"pattern": "TODO"}}}
						]
					},
					"finishReason": "STOP"
				}
			]
		}
	}`)

	out, err := TranslateAntigravityToOpenAI(raw, "gemini-3.8-flash")
	require.NoError(t, err)

	assert.Equal(t, "call_grep_9", gjson.GetBytes(out, "choices.0.message.tool_calls.0.id").String())
	assert.Equal(t, "pending-sig-9", gjson.GetBytes(out, "choices.0.message.tool_calls.0.thought_signature").String())
	assert.Equal(t, "tool_calls", gjson.GetBytes(out, "choices.0.finish_reason").String())

	// And the store helper picks the pair up from the translated body.
	store := NewSignatureStore()
	recordToolSignaturesFromOpenAI(store, out)
	assert.Equal(t, "pending-sig-9", store.Lookup("call_grep_9"))
}
