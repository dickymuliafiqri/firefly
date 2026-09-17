package opencode

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const maxToolNameLen = 128

// BaseModelID strips any thinking suffix such as "model(high)" or "model(medium)".
func BaseModelID(model string) string {
	m := strings.TrimSpace(model)
	if idx := strings.LastIndex(m, "("); idx != -1 && strings.HasSuffix(m, ")") {
		return strings.TrimSpace(m[:idx])
	}
	return m
}

// IsResponsesModel returns true if the model is served exclusively by the OpenAI Responses API
// (/v1/responses) rather than /chat/completions on OpenCode backends.
func IsResponsesModel(model string) bool {
	base := strings.ToLower(BaseModelID(model))
	if strings.HasPrefix(base, "muse-spark") {
		return true
	}
	switch base {
	case "grok-4.6", "gpt-5.6-luna":
		return true
	}
	return false
}

// SanitizeTools ensures tool parameters and function names adhere to OpenCode's strict schemas.
// Specifically:
// 1. Clamps tool function names to 128 characters.
// 2. Fills missing "properties": {} if parameters.type == "object" to avoid InputValidationError.
func SanitizeTools(body []byte) ([]byte, bool) {
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return body, false
	}

	toolArr := tools.Array()
	if len(toolArr) == 0 {
		return body, false
	}

	modified := false
	out := body

	for i, t := range toolArr {
		// Tool function name clamp
		fnName := t.Get("function.name").String()
		if len(fnName) > maxToolNameLen {
			path := fmt.Sprintf("tools.%d.function.name", i)
			out, _ = sjson.SetBytes(out, path, fnName[:maxToolNameLen])
			modified = true
		}

		// Tool parameters properties fix
		paramType := t.Get("function.parameters.type").String()
		if paramType == "object" && !t.Get("function.parameters.properties").Exists() {
			path := fmt.Sprintf("tools.%d.function.parameters.properties", i)
			out, _ = sjson.SetRawBytes(out, path, []byte("{}"))
			modified = true
		}
	}

	return out, modified
}

// NormalizeReasoning ensures reasoning/thinking parameters match OpenCode expectations.
// If reasoning_effort is a string and reasoning is absent, wraps it into reasoning: { effort, summary: "auto" }.
func NormalizeReasoning(body []byte) ([]byte, bool) {
	effort := gjson.GetBytes(body, "reasoning_effort")
	reasoning := gjson.GetBytes(body, "reasoning")

	if effort.Exists() && !reasoning.Exists() {
		rObj := map[string]string{
			"effort":  effort.String(),
			"summary": "auto",
		}
		raw, _ := json.Marshal(rObj)
		out, _ := sjson.SetRawBytes(body, "reasoning", raw)
		out, _ = sjson.DeleteBytes(out, "reasoning_effort")
		return out, true
	}

	if reasoning.Exists() && reasoning.IsObject() && !reasoning.Get("summary").Exists() {
		out, _ := sjson.SetBytes(body, "reasoning.summary", "auto")
		return out, true
	}

	return body, false
}

// TransformChatToResponses transforms an OpenAI Chat Completions payload into
// an OpenAI Responses API payload suitable for OpenCode's /responses endpoint.
func TransformChatToResponses(body []byte, upstreamModel string) ([]byte, error) {
	parsed := gjson.ParseBytes(body)

	// Determine output token limits
	maxOutputTokens := int64(0)
	if parsed.Get("max_output_tokens").Exists() {
		maxOutputTokens = parsed.Get("max_output_tokens").Int()
	} else if parsed.Get("max_completion_tokens").Exists() {
		maxOutputTokens = parsed.Get("max_completion_tokens").Int()
	} else if parsed.Get("max_tokens").Exists() {
		maxOutputTokens = parsed.Get("max_tokens").Int()
	}

	// Build responses input array from messages
	messages := parsed.Get("messages")
	var inputs []map[string]any

	if messages.Exists() && messages.IsArray() {
		for _, msg := range messages.Array() {
			role := msg.Get("role").String()
			content := msg.Get("content")

			// Handle tool response
			if role == "tool" {
				callID := msg.Get("tool_call_id").String()
				outputStr := content.String()
				inputs = append(inputs, map[string]any{
					"type":    "function_call_output",
					"call_id": callID,
					"output":  outputStr,
				})
				continue
			}

			// Handle assistant tool calls
			toolCalls := msg.Get("tool_calls")
			if toolCalls.Exists() && toolCalls.IsArray() && len(toolCalls.Array()) > 0 {
				for _, tc := range toolCalls.Array() {
					tcID := tc.Get("id").String()
					tcName := tc.Get("function.name").String()
					if len(tcName) > maxToolNameLen {
						tcName = tcName[:maxToolNameLen]
					}
					tcArgs := tc.Get("function.arguments").String()
					inputs = append(inputs, map[string]any{
						"type":      "function_call",
						"call_id":   tcID,
						"name":      tcName,
						"arguments": tcArgs,
					})
				}
				continue
			}

			// Handle standard text messages
			text := ""
			if content.Type == gjson.String {
				text = content.String()
			} else if content.IsArray() {
				for _, part := range content.Array() {
					if part.Get("type").String() == "text" {
						text += part.Get("text").String()
					}
				}
			}

			if text != "" {
				inputs = append(inputs, map[string]any{
					"type": "message",
					"role": role,
					"content": []map[string]any{
						{
							"type": "input_text",
							"text": text,
						},
					},
				})
			}
		}
	}

	if len(inputs) == 0 {
		inputs = append(inputs, map[string]any{
			"type": "message",
			"role": "user",
			"content": []map[string]any{
				{
					"type": "input_text",
					"text": "...",
				},
			},
		})
	}

	payload := map[string]any{
		"model":  upstreamModel,
		"input":  inputs,
		"stream": true,
		"store":  false,
	}

	if maxOutputTokens > 0 {
		payload["max_output_tokens"] = maxOutputTokens
	}

	// Reasoning
	if parsed.Get("reasoning").Exists() {
		var rObj map[string]any
		_ = json.Unmarshal([]byte(parsed.Get("reasoning").Raw), &rObj)
		if rObj != nil {
			if _, ok := rObj["summary"]; !ok {
				rObj["summary"] = "auto"
			}
			payload["reasoning"] = rObj
		}
	} else if parsed.Get("reasoning_effort").Exists() {
		payload["reasoning"] = map[string]string{
			"effort":  parsed.Get("reasoning_effort").String(),
			"summary": "auto",
		}
	}

	// Tools
	if parsed.Get("tools").Exists() && parsed.Get("tools").IsArray() {
		var rawTools []any
		_ = json.Unmarshal([]byte(parsed.Get("tools").Raw), &rawTools)
		var validTools []map[string]any
		for _, rawT := range rawTools {
			tObj, ok := rawT.(map[string]any)
			if !ok {
				continue
			}
			fn, _ := tObj["function"].(map[string]any)
			name := ""
			if fn != nil {
				name, _ = fn["name"].(string)
			}
			if name == "" {
				name, _ = tObj["name"].(string)
			}
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if len(name) > maxToolNameLen {
				name = name[:maxToolNameLen]
			}
			desc := ""
			if fn != nil {
				desc, _ = fn["description"].(string)
			}
			params, _ := fn["parameters"].(map[string]any)
			if params == nil {
				params = map[string]any{"type": "object", "properties": map[string]any{}}
			} else if params["type"] == "object" && params["properties"] == nil {
				params["properties"] = map[string]any{}
			}

			validTools = append(validTools, map[string]any{
				"type":        "function",
				"name":        name,
				"description": desc,
				"parameters":  params,
			})
		}
		if len(validTools) > 0 {
			payload["tools"] = validTools
		}
	}

	return json.Marshal(payload)
}
