package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

// This file carries the inbound direction: a client speaking the Anthropic
// Messages API is relayed to an upstream that speaks OpenAI. It is the mirror
// of TranslateOpenAIToAnthropic and must stay its inverse — anything the
// outbound path can emit, this path has to accept.

// openAIToolCall is one entry of the OpenAI assistant tool_calls array.
type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// openAIChatMessage is one OpenAI chat message. Content is typed as any so a
// tool answer can carry a plain string while a multimodal turn carries parts.
type openAIChatMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
}

// openAIFunctionTool is the OpenAI tools[] entry.
type openAIFunctionTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

// openAIToolChoice is the OpenAI tool_choice shape. Function is a pointer so a
// bare "auto"/"required" choice does not serialise an empty function object.
type openAIToolChoice struct {
	Type     string `json:"type"`
	Function *struct {
		Name string `json:"name"`
	} `json:"function,omitempty"`
}

// systemText flattens the Anthropic system field, which may be a string or an
// array of text blocks, into one string.
func systemText(root gjson.Result) string {
	sys := root.Get("system")
	if !sys.Exists() {
		return ""
	}
	if sys.Type == gjson.String {
		return sys.String()
	}
	var parts []string
	for _, block := range sys.Array() {
		if block.Get("type").String() == "text" || block.Get("type").String() == "" {
			if t := block.Get("text").String(); t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

// imagePartToOpenAI rewrites an Anthropic image block into the OpenAI image_url
// part. A base64 source is reassembled into a data: URL because that is the
// only form OpenAI accepts for inline bytes.
func imagePartToOpenAI(block gjson.Result) (map[string]any, bool) {
	src := block.Get("source")
	if !src.Exists() {
		return nil, false
	}
	url := ""
	switch src.Get("type").String() {
	case "url":
		url = src.Get("url").String()
	case "base64":
		mediaType := src.Get("media_type").String()
		data := src.Get("data").String()
		if mediaType == "" || data == "" {
			return nil, false
		}
		url = "data:" + mediaType + ";base64," + data
	default:
		return nil, false
	}
	if url == "" {
		return nil, false
	}
	return map[string]any{
		"type":      "image_url",
		"image_url": map[string]any{"url": url},
	}, true
}

// toolResultText flattens a tool_result block's content, which may itself be a
// string or an array of text blocks.
func toolResultText(block gjson.Result) string {
	content := block.Get("content")
	if !content.Exists() {
		return ""
	}
	if content.Type == gjson.String {
		return content.String()
	}
	var parts []string
	for _, inner := range content.Array() {
		if t := inner.Get("text").String(); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n")
}

// userContentToOpenAI maps an Anthropic user turn. A plain string stays a
// string; a block array becomes OpenAI parts, and any tool_result block is
// returned separately because OpenAI models it as its own tool role message.
func userContentToOpenAI(content gjson.Result) (any, []openAIChatMessage) {
	if !content.IsArray() {
		return content.String(), nil
	}

	var toolResults []openAIChatMessage
	parts := make([]map[string]any, 0, len(content.Array()))
	for _, block := range content.Array() {
		switch block.Get("type").String() {
		case "text", "":
			if t := block.Get("text").String(); t != "" {
				parts = append(parts, map[string]any{"type": "text", "text": t})
			}
		case "image":
			if part, ok := imagePartToOpenAI(block); ok {
				parts = append(parts, part)
			}
		case "tool_result":
			// OpenAI reports a tool answer as a standalone message, so it is
			// pulled out of the content array and emitted after this turn.
			toolResults = append(toolResults, openAIChatMessage{
				Role:       "tool",
				Content:    toolResultText(block),
				ToolCallID: block.Get("tool_use_id").String(),
			})
		}
	}
	if len(parts) == 0 {
		return "", toolResults
	}
	return parts, toolResults
}

// assistantContentToOpenAI maps an Anthropic assistant turn. Text blocks fold
// into the message content and tool_use blocks become tool_calls entries whose
// input object is re-serialised into the JSON string OpenAI expects.
func assistantContentToOpenAI(content gjson.Result) (any, []openAIToolCall, error) {
	if !content.IsArray() {
		return content.String(), nil, nil
	}

	var texts []string
	var calls []openAIToolCall
	for _, block := range content.Array() {
		switch block.Get("type").String() {
		case "text", "":
			if t := block.Get("text").String(); t != "" {
				texts = append(texts, t)
			}
		case "tool_use":
			var call openAIToolCall
			call.ID = block.Get("id").String()
			call.Type = "function"
			call.Function.Name = block.Get("name").String()
			input := block.Get("input")
			if input.Exists() {
				args, err := json.Marshal(json.RawMessage(input.Raw))
				if err != nil {
					return nil, nil, fmt.Errorf("tool_use %s: marshal input: %w", call.ID, err)
				}
				call.Function.Arguments = string(args)
			} else {
				call.Function.Arguments = "{}"
			}
			calls = append(calls, call)
		}
	}
	return strings.Join(texts, "\n"), calls, nil
}

// translateToolsInbound maps the Anthropic tools array onto the OpenAI shape.
func translateToolsInbound(root gjson.Result) ([]openAIFunctionTool, error) {
	raw := root.Get("tools").Array()
	if len(raw) == 0 {
		return nil, nil
	}
	tools := make([]openAIFunctionTool, 0, len(raw))
	for i, t := range raw {
		name := t.Get("name").String()
		if name == "" {
			return nil, fmt.Errorf("tools[%d]: missing name", i)
		}
		var tool openAIFunctionTool
		tool.Type = "function"
		tool.Function.Name = name
		tool.Function.Description = t.Get("description").String()
		schema := t.Get("input_schema")
		if !schema.Exists() {
			return nil, fmt.Errorf("tools[%d] (%s): missing input_schema", i, name)
		}
		tool.Function.Parameters = json.RawMessage(schema.Raw)
		tools = append(tools, tool)
	}
	return tools, nil
}

// translateToolChoiceInbound maps the Anthropic tool_choice onto the OpenAI
// vocabulary. "any" becomes "required"; "auto" and a named tool map directly.
func translateToolChoiceInbound(root gjson.Result) *openAIToolChoice {
	tc := root.Get("tool_choice")
	if !tc.Exists() {
		return nil
	}
	if tc.Type == gjson.String {
		switch tc.String() {
		case "auto":
			return &openAIToolChoice{Type: "auto"}
		case "any":
			return &openAIToolChoice{Type: "required"}
		default:
			return nil
		}
	}
	switch tc.Get("type").String() {
	case "auto":
		return &openAIToolChoice{Type: "auto"}
	case "any":
		return &openAIToolChoice{Type: "required"}
	case "tool":
		var choice openAIToolChoice
		choice.Type = "function"
		name := tc.Get("name").String()
		if name == "" {
			return nil
		}
		choice.Function = &struct {
			Name string `json:"name"`
		}{Name: name}
		return &choice
	default:
		return nil
	}
}

// TranslateAnthropicRequestToOpenAI converts an Anthropic messages request into
// an OpenAI chat-completions request. It is the exact inverse of
// TranslateOpenAIToAnthropic: the system field becomes a leading system
// message, tool_use blocks become tool_calls, tool_result blocks become tool
// role messages, and the Anthropic tools/tool_choice vocabulary is mapped onto
// OpenAI's.
func TranslateAnthropicRequestToOpenAI(body []byte) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return nil, ErrMalformedJSON
	}
	root := gjson.ParseBytes(body)

	msgs := make([]openAIChatMessage, 0, 8)
	if sys := systemText(root); sys != "" {
		msgs = append(msgs, openAIChatMessage{Role: "system", Content: sys})
	}

	for _, msg := range root.Get("messages").Array() {
		role := strings.ToLower(msg.Get("role").String())
		content := msg.Get("content")

		switch role {
		case "assistant":
			text, calls, err := assistantContentToOpenAI(content)
			if err != nil {
				return nil, err
			}
			msgs = append(msgs, openAIChatMessage{Role: "assistant", Content: text, ToolCalls: calls})

		case "user", "":
			text, toolResults := userContentToOpenAI(content)
			// A turn that carried nothing but tool_result blocks must not
			// become an empty user message: OpenAI rejects those, and the tool
			// message alone already answers the assistant's call.
			if _, isEmpty := text.(string); !isEmpty || text != "" || len(toolResults) == 0 {
				msgs = append(msgs, openAIChatMessage{Role: "user", Content: text})
			}
			msgs = append(msgs, toolResults...)

		default:
			// An unknown role is preserved verbatim rather than dropped: the
			// upstream is better placed to reject it than the gateway is to
			// silently discard a turn.
			msgs = append(msgs, openAIChatMessage{Role: role, Content: content.String()})
		}
	}

	req := map[string]any{
		"model":    root.Get("model").String(),
		"messages": msgs,
	}
	if mt := root.Get("max_tokens"); mt.Exists() {
		req["max_tokens"] = mt.Int()
	}
	if v := root.Get("temperature"); v.Exists() {
		req["temperature"] = v.Float()
	}
	if v := root.Get("top_p"); v.Exists() {
		req["top_p"] = v.Float()
	}
	if v := root.Get("stream"); v.Exists() {
		req["stream"] = v.Bool()
	}
	if stops := root.Get("stop_sequences").Array(); len(stops) > 0 {
		seq := make([]string, 0, len(stops))
		for _, s := range stops {
			if s.String() != "" {
				seq = append(seq, s.String())
			}
		}
		if len(seq) > 0 {
			req["stop"] = seq
		}
	}
	tools, err := translateToolsInbound(root)
	if err != nil {
		return nil, err
	}
	if len(tools) > 0 {
		req["tools"] = tools
	}
	if choice := translateToolChoiceInbound(root); choice != nil {
		req["tool_choice"] = choice
	}

	out, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal openai request: %w", err)
	}
	return out, nil
}
