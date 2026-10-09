package anthropic

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// ErrMalformedJSON is returned when an incoming request body is empty or invalid JSON.
var ErrMalformedJSON = errors.New("invalid request json: malformed json")

// anthropicMessage content is either a plain string or an array of blocks.
// Tool turns need the array form, so the field is typed as any and the request
// builder decides per message.
type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// anthropicToolResultBlock answers a tool_use block from the previous
// assistant turn.
type anthropicToolResultBlock struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error,omitempty"`
}

// anthropicImageBlock carries an image the client attached. OpenAI spells the
// source as image_url.url; Anthropic wants an explicit source type, and a
// data: URL has to be split into its media type and payload.
type anthropicImageBlock struct {
	Type   string `json:"type"`
	Source struct {
		Type      string `json:"type"`
		URL       string `json:"url,omitempty"`
		MediaType string `json:"media_type,omitempty"`
		Data      string `json:"data,omitempty"`
	} `json:"source"`
}

// anthropicTool is one entry of the Anthropic tools array.
type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// anthropicToolChoice selects how the model may call tools.
type anthropicToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type anthropicRequest struct {
	Model         string               `json:"model"`
	System        string               `json:"system,omitempty"`
	Messages      []anthropicMessage   `json:"messages"`
	MaxTokens     int                  `json:"max_tokens"`
	Temperature   *float64             `json:"temperature,omitempty"`
	TopP          *float64             `json:"top_p,omitempty"`
	Stream        bool                 `json:"stream,omitempty"`
	StopSequences []string             `json:"stop_sequences,omitempty"`
	Tools         []anthropicTool      `json:"tools,omitempty"`
	ToolChoice    *anthropicToolChoice `json:"tool_choice,omitempty"`
}

// assistantBlocks builds the content array of an assistant turn: the text (if
// any) followed by one tool_use block per tool call. Dropping the tool calls
// here would silently break every agentic client, so they are never optional.
func assistantBlocks(msg gjson.Result) (any, error) {
	calls := msg.Get("tool_calls").Array()
	if len(calls) == 0 {
		return extractContent(msg.Get("content")), nil
	}

	blocks := make([]json.RawMessage, 0, len(calls)+1)
	if text := extractContent(msg.Get("content")); strings.TrimSpace(text) != "" {
		b, err := json.Marshal(anthropicTextBlock{Type: "text", Text: text})
		if err != nil {
			return nil, fmt.Errorf("marshal assistant text: %w", err)
		}
		blocks = append(blocks, b)
	}
	for i, tc := range calls {
		rawArgs := tc.Get("function.arguments").String()
		input := json.RawMessage(`{}`)
		if strings.TrimSpace(rawArgs) != "" {
			var probe any
			if err := json.Unmarshal([]byte(rawArgs), &probe); err != nil {
				return nil, fmt.Errorf("tool_call %d (%s): invalid arguments json: %w", i, tc.Get("function.name").String(), err)
			}
			input = json.RawMessage(rawArgs)
		}
		b, err := json.Marshal(anthropicToolUseBlock{
			Type:  "tool_use",
			ID:    tc.Get("id").String(),
			Name:  tc.Get("function.name").String(),
			Input: input,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal tool_use %d: %w", i, err)
		}
		blocks = append(blocks, b)
	}
	return blocks, nil
}

// userBlocks builds the content of a user turn. A plain string stays a string.
// An OpenAI multimodal array collapses back to a joined string when it holds
// nothing but text — that keeps the wire body small and matches what Anthropic
// itself emits — and becomes a block array as soon as an image (or any other
// non-text part) appears, because only the block form can carry a source.
func userBlocks(msg gjson.Result) (any, error) {
	content := msg.Get("content")
	if !content.IsArray() {
		return extractContent(content), nil
	}

	parts := content.Array()
	hasNonText := false
	for _, part := range parts {
		switch part.Get("type").String() {
		case "text", "":
		default:
			hasNonText = true
		}
		if hasNonText {
			break
		}
	}
	if !hasNonText {
		return extractContent(content), nil
	}

	blocks := make([]json.RawMessage, 0, len(parts))
	for _, part := range parts {
		switch part.Get("type").String() {
		case "text", "":
			if txt := part.Get("text").String(); txt != "" {
				b, err := json.Marshal(anthropicTextBlock{Type: "text", Text: txt})
				if err != nil {
					return nil, fmt.Errorf("marshal user text: %w", err)
				}
				blocks = append(blocks, b)
			}
		case "image_url":
			raw := part.Get("image_url.url").String()
			if raw == "" {
				continue
			}
			var img anthropicImageBlock
			img.Type = "image"
			if strings.HasPrefix(raw, "data:") {
				header, payload, found := strings.Cut(strings.TrimPrefix(raw, "data:"), ",")
				if !found {
					continue
				}
				img.Source.Type = "base64"
				img.Source.MediaType = strings.TrimSuffix(header, ";base64")
				img.Source.Data = payload
			} else {
				img.Source.Type = "url"
				img.Source.URL = raw
			}
			b, err := json.Marshal(img)
			if err != nil {
				return nil, fmt.Errorf("marshal image block: %w", err)
			}
			blocks = append(blocks, b)
		}
	}
	if len(blocks) == 0 {
		return "", nil
	}
	return blocks, nil
}

// translateTools maps the OpenAI tools array onto the Anthropic shape.
func translateTools(root gjson.Result) ([]anthropicTool, error) {
	raw := root.Get("tools").Array()
	if len(raw) == 0 {
		return nil, nil
	}
	tools := make([]anthropicTool, 0, len(raw))
	for i, t := range raw {
		fn := t.Get("function")
		name := fn.Get("name").String()
		if name == "" {
			return nil, fmt.Errorf("tools[%d]: missing function.name", i)
		}
		schema := fn.Get("parameters")
		if !schema.Exists() {
			return nil, fmt.Errorf("tools[%d] (%s): missing parameters", i, name)
		}
		tools = append(tools, anthropicTool{
			Name:        name,
			Description: fn.Get("description").String(),
			InputSchema: json.RawMessage(schema.Raw),
		})
	}
	return tools, nil
}

// translateToolChoice maps the OpenAI tool_choice vocabulary. "none" has no
// Anthropic equivalent, so it is dropped and the model decides on its own.
func translateToolChoice(root gjson.Result) *anthropicToolChoice {
	tc := root.Get("tool_choice")
	if !tc.Exists() {
		return nil
	}
	if tc.Type == gjson.String {
		switch tc.String() {
		case "auto":
			return &anthropicToolChoice{Type: "auto"}
		case "required", "any":
			return &anthropicToolChoice{Type: "any"}
		default:
			return nil
		}
	}
	switch tc.Get("type").String() {
	case "function":
		name := tc.Get("function.name").String()
		if name == "" {
			return nil
		}
		return &anthropicToolChoice{Type: "tool", Name: name}
	case "auto", "":
		return &anthropicToolChoice{Type: "auto"}
	case "required", "any":
		return &anthropicToolChoice{Type: "any"}
	default:
		return nil
	}
}

// TranslateOpenAIToAnthropic converts an OpenAI chat completion request into
// an Anthropic messages API request body using gjson for zero-AST message
// extraction. System and developer messages fold into the top-level system
// string, assistant tool_calls become tool_use blocks, and tool role messages
// become tool_result blocks merged into a single following user turn - which
// is the only shape Anthropic accepts.
func TranslateOpenAIToAnthropic(body []byte, upstreamModel string) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return nil, ErrMalformedJSON
	}
	root := gjson.ParseBytes(body)

	model := upstreamModel
	if model == "" {
		model = root.Get("model").String()
	}

	msgs := root.Get("messages").Array()
	aMsgs := make([]anthropicMessage, 0, len(msgs))
	var systems []string
	var pendingResults []json.RawMessage

	flushResults := func() {
		if len(pendingResults) == 0 {
			return
		}
		aMsgs = append(aMsgs, anthropicMessage{Role: "user", Content: pendingResults})
		pendingResults = nil
	}

	for _, msg := range msgs {
		switch strings.ToLower(msg.Get("role").String()) {
		case "system", "developer":
			flushResults()
			if contentStr := extractContent(msg.Get("content")); contentStr != "" {
				systems = append(systems, contentStr)
			}
		case "tool", "function":
			// Consecutive tool answers accumulate so they land in one user turn.
			block := anthropicToolResultBlock{
				Type:      "tool_result",
				ToolUseID: msg.Get("tool_call_id").String(),
				Content:   extractContent(msg.Get("content")),
			}
			b, err := json.Marshal(block)
			if err != nil {
				return nil, fmt.Errorf("marshal tool_result: %w", err)
			}
			pendingResults = append(pendingResults, b)
		case "assistant":
			flushResults()
			blocks, err := assistantBlocks(msg)
			if err != nil {
				return nil, err
			}
			aMsgs = append(aMsgs, anthropicMessage{Role: "assistant", Content: blocks})
		case "user":
			flushResults()
			blocks, err := userBlocks(msg)
			if err != nil {
				return nil, err
			}
			aMsgs = append(aMsgs, anthropicMessage{Role: "user", Content: blocks})
		default:
			flushResults()
			aMsgs = append(aMsgs, anthropicMessage{Role: "user", Content: extractContent(msg.Get("content"))})
		}
	}
	flushResults()

	maxTokens := 4096
	if mt := root.Get("max_tokens"); mt.Exists() && mt.Int() > 0 {
		maxTokens = int(mt.Int())
	}

	var temperature *float64
	if temp := root.Get("temperature"); temp.Exists() {
		v := temp.Float()
		temperature = &v
	}

	var topP *float64
	if tp := root.Get("top_p"); tp.Exists() {
		v := tp.Float()
		topP = &v
	}

	stream := root.Get("stream").Bool()

	var stopSeqs []string
	if stopRes := root.Get("stop"); stopRes.Exists() {
		if stopRes.IsArray() {
			arr := stopRes.Array()
			stopSeqs = make([]string, 0, len(arr))
			for _, item := range arr {
				if s := item.String(); s != "" {
					stopSeqs = append(stopSeqs, s)
				}
			}
		} else if s := stopRes.String(); s != "" {
			stopSeqs = []string{s}
		}
	}

	tools, err := translateTools(root)
	if err != nil {
		return nil, err
	}

	aReq := anthropicRequest{
		Model:         model,
		System:        strings.Join(systems, "\n\n"),
		Messages:      aMsgs,
		MaxTokens:     maxTokens,
		Temperature:   temperature,
		TopP:          topP,
		Stream:        stream,
		StopSequences: stopSeqs,
		Tools:         tools,
		ToolChoice:    translateToolChoice(root),
	}

	return json.Marshal(aReq)
}

func extractContent(content gjson.Result) string {
	if !content.Exists() {
		return ""
	}
	if content.Type == gjson.String {
		return content.String()
	}
	if content.IsArray() {
		var b strings.Builder
		first := true
		for _, part := range content.Array() {
			pType := part.Get("type").String()
			if pType == "text" || pType == "" {
				if !first {
					b.WriteString("\n")
				}
				b.WriteString(part.Get("text").String())
				first = false
			}
		}
		return b.String()
	}
	return content.Raw
}

type anthropicResponse struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Model        string `json:"model"`
	StopReason   string `json:"stop_reason"`
	StopSequence string `json:"stop_sequence"`
	Usage        struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type openAIChoice struct {
	Index        int           `json:"index"`
	Message      openAIRespMsg `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type openAIRespMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type openAIResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []openAIChoice `json:"choices"`
	Usage   openAIUsage    `json:"usage"`
}

// TranslateAnthropicToOpenAI converts an Anthropic messages response into an
// OpenAI chat completion response.
func TranslateAnthropicToOpenAI(body []byte, publicModel string) ([]byte, error) {
	var aResp anthropicResponse
	if err := json.Unmarshal(body, &aResp); err != nil {
		return nil, fmt.Errorf("invalid anthropic response json: %w", err)
	}

	var sb strings.Builder
	for _, c := range aResp.Content {
		if c.Type == "text" || c.Type == "" {
			sb.WriteString(c.Text)
		}
	}

	id := aResp.ID
	if !strings.HasPrefix(id, "chatcmpl-") {
		id = "chatcmpl-" + id
	}

	model := publicModel
	if model == "" {
		model = aResp.Model
	}

	finishReason := mapStopReason(aResp.StopReason)

	oResp := openAIResponse{
		ID:      id,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []openAIChoice{
			{
				Index: 0,
				Message: openAIRespMsg{
					Role:    "assistant",
					Content: sb.String(),
				},
				FinishReason: finishReason,
			},
		},
		Usage: openAIUsage{
			PromptTokens:     aResp.Usage.InputTokens,
			CompletionTokens: aResp.Usage.OutputTokens,
			TotalTokens:      aResp.Usage.InputTokens + aResp.Usage.OutputTokens,
		},
	}

	return json.Marshal(oResp)
}

func mapStopReason(reason string) string {
	switch reason {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	case "":
		return "stop"
	default:
		return reason
	}
}

// Anthropic SSE chunk events
type anthropicMessageStartEvent struct {
	Type    string `json:"type"`
	Message struct {
		ID   string `json:"id"`
		Role string `json:"role"`
	} `json:"message"`
}

type anthropicContentBlockDeltaEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
}

type anthropicMessageDeltaEvent struct {
	Type  string `json:"type"`
	Delta struct {
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
}

type openAIChunkChoice struct {
	Index        int            `json:"index"`
	Delta        map[string]any `json:"delta"`
	FinishReason *string        `json:"finish_reason"`
}

type openAIStreamChunk struct {
	ID      string              `json:"id"`
	Object  string              `json:"object"`
	Created int64               `json:"created"`
	Model   string              `json:"model"`
	Choices []openAIChunkChoice `json:"choices"`
}

// TranslateAnthropicChunkToOpenAI converts one Anthropic SSE event into an
// OpenAI SSE event chunk. Returns (chunkBytes, isDoneSentinel, error).
func TranslateAnthropicChunkToOpenAI(eventType string, data []byte, publicModel string, msgID *string) ([]byte, bool, error) {
	switch eventType {
	case "message_start":
		var startEv anthropicMessageStartEvent
		if err := json.Unmarshal(data, &startEv); err == nil && startEv.Message.ID != "" {
			*msgID = "chatcmpl-" + startEv.Message.ID
		}
		if *msgID == "" {
			*msgID = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
		}
		chunk := openAIStreamChunk{
			ID:      *msgID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   publicModel,
			Choices: []openAIChunkChoice{
				{
					Index:        0,
					Delta:        map[string]any{"role": "assistant", "content": ""},
					FinishReason: nil,
				},
			},
		}
		b, err := json.Marshal(chunk)
		return b, false, err

	case "content_block_delta":
		var deltaEv anthropicContentBlockDeltaEvent
		if err := json.Unmarshal(data, &deltaEv); err != nil {
			return nil, false, err
		}
		if *msgID == "" {
			*msgID = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
		}
		chunk := openAIStreamChunk{
			ID:      *msgID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   publicModel,
			Choices: []openAIChunkChoice{
				{
					Index:        0,
					Delta:        map[string]any{"content": deltaEv.Delta.Text},
					FinishReason: nil,
				},
			},
		}
		b, err := json.Marshal(chunk)
		return b, false, err

	case "message_delta":
		var msgDeltaEv anthropicMessageDeltaEvent
		if err := json.Unmarshal(data, &msgDeltaEv); err != nil {
			return nil, false, err
		}
		if *msgID == "" {
			*msgID = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
		}
		reason := mapStopReason(msgDeltaEv.Delta.StopReason)
		chunk := openAIStreamChunk{
			ID:      *msgID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   publicModel,
			Choices: []openAIChunkChoice{
				{
					Index:        0,
					Delta:        map[string]any{},
					FinishReason: &reason,
				},
			},
		}
		b, err := json.Marshal(chunk)
		return b, false, err

	case "message_stop":
		return nil, true, nil

	default:
		// Other Anthropic events (ping, content_block_start, etc.) - ignored
		return nil, false, nil
	}
}

// AnthropicErrorPayload models the Anthropic error envelope.
type AnthropicErrorPayload struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// OpenAIErrorEnvelope models the OpenAI error envelope.
type OpenAIErrorEnvelope struct {
	Error OpenAIErrorDetail `json:"error"`
}

type OpenAIErrorDetail struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    string  `json:"code"`
}

// TranslateAnthropicError maps an Anthropic error HTTP status and body to an
// OpenAI-shaped error envelope.
func TranslateAnthropicError(status int, body []byte) (int, []byte) {
	var aErr AnthropicErrorPayload
	_ = json.Unmarshal(body, &aErr)

	msg := aErr.Error.Message
	if msg == "" {
		msg = string(body)
		if strings.TrimSpace(msg) == "" {
			msg = http.StatusText(status)
		}
	}

	errType := "api_error"
	code := aErr.Error.Type
	if code == "" {
		code = errType
	}

	switch aErr.Error.Type {
	case "invalid_request_error":
		errType = "invalid_request_error"
		if status == 0 {
			status = http.StatusBadRequest
		}
	case "authentication_error":
		errType = "authentication_error"
		if status == 0 {
			status = http.StatusUnauthorized
		}
	case "permission_error":
		errType = "permission_error"
		if status == 0 {
			status = http.StatusForbidden
		}
	case "not_found_error":
		errType = "invalid_request_error"
		if status == 0 {
			status = http.StatusNotFound
		}
	case "rate_limit_error":
		errType = "rate_limit_error"
		if status == 0 {
			status = http.StatusTooManyRequests
		}
	case "overloaded_error":
		errType = "api_error"
		if status == 0 {
			status = http.StatusServiceUnavailable
		}
	default:
		if status == http.StatusBadRequest {
			errType = "invalid_request_error"
		} else if status == http.StatusUnauthorized {
			errType = "authentication_error"
		} else if status == http.StatusForbidden {
			errType = "permission_error"
		} else if status == http.StatusTooManyRequests {
			errType = "rate_limit_error"
		}
	}

	if status == 0 {
		status = http.StatusInternalServerError
	}

	env := OpenAIErrorEnvelope{
		Error: OpenAIErrorDetail{
			Message: msg,
			Type:    errType,
			Code:    code,
		},
	}
	out, err := json.Marshal(env)
	if err != nil {
		return http.StatusInternalServerError, []byte(`{"error":{"message":"internal error","type":"api_error"}}`)
	}
	return status, out
}
