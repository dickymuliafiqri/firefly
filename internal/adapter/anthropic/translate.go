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

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicRequest struct {
	Model         string             `json:"model"`
	System        string             `json:"system,omitempty"`
	Messages      []anthropicMessage `json:"messages"`
	MaxTokens     int                `json:"max_tokens"`
	Temperature   *float64           `json:"temperature,omitempty"`
	Stream        bool               `json:"stream,omitempty"`
	StopSequences []string           `json:"stop_sequences,omitempty"`
}

// TranslateOpenAIToAnthropic converts an OpenAI chat completion request into
// an Anthropic messages API request body using gjson for zero-AST message extraction.
func TranslateOpenAIToAnthropic(body []byte, upstreamModel string) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return nil, ErrMalformedJSON
	}

	model := upstreamModel
	if model == "" {
		model = gjson.GetBytes(body, "model").String()
	}

	msgs := gjson.GetBytes(body, "messages").Array()
	aMsgs := make([]anthropicMessage, 0, len(msgs))
	var systems []string

	for _, msg := range msgs {
		contentStr := extractContent(msg.Get("content"))
		switch strings.ToLower(msg.Get("role").String()) {
		case "system":
			if contentStr != "" {
				systems = append(systems, contentStr)
			}
		case "user":
			aMsgs = append(aMsgs, anthropicMessage{Role: "user", Content: contentStr})
		case "assistant":
			aMsgs = append(aMsgs, anthropicMessage{Role: "assistant", Content: contentStr})
		default:
			aMsgs = append(aMsgs, anthropicMessage{Role: "user", Content: contentStr})
		}
	}

	maxTokens := 4096
	if mt := gjson.GetBytes(body, "max_tokens"); mt.Exists() && mt.Int() > 0 {
		maxTokens = int(mt.Int())
	}

	var temperature *float64
	if temp := gjson.GetBytes(body, "temperature"); temp.Exists() {
		v := temp.Float()
		temperature = &v
	}

	stream := gjson.GetBytes(body, "stream").Bool()

	var stopSeqs []string
	if stopRes := gjson.GetBytes(body, "stop"); stopRes.Exists() {
		if stopRes.IsArray() {
			arr := stopRes.Array()
			stopSeqs = make([]string, 0, len(arr))
			for _, item := range arr {
				s := item.String()
				if s != "" {
					stopSeqs = append(stopSeqs, s)
				}
			}
		} else if s := stopRes.String(); s != "" {
			stopSeqs = []string{s}
		}
	}

	aReq := anthropicRequest{
		Model:         model,
		System:        strings.Join(systems, "\n\n"),
		Messages:      aMsgs,
		MaxTokens:     maxTokens,
		Temperature:   temperature,
		Stream:        stream,
		StopSequences: stopSeqs,
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
