package qoder

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

var (
	// ErrMalformedJSON indicates an empty or syntactically invalid JSON payload.
	ErrMalformedJSON = errors.New("qoder: invalid json payload")
)

// buildQoderPayload maps an OpenAI-style chat/completions body into the exact
// shape Qoder's agent_chat_generation endpoint expects.
//
// modelConfig is the live per-model config block fetched from /model/list; it is
// embedded verbatim (Qoder silently downgrades to a different model when the
// wrong block is sent). userID scopes the stable session id.
func buildQoderPayload(openaiBody []byte, qoderKey, userID string, modelConfig json.RawMessage) ([]byte, error) {
	if len(openaiBody) == 0 || !gjson.ValidBytes(openaiBody) {
		return nil, ErrMalformedJSON
	}
	root := gjson.ParseBytes(openaiBody)

	messages, systemText := normalizeMessages(root.Get("messages"))
	lastUser := lastUserText(messages)

	// max_tokens: prefer model config ceiling, then request overrides if smaller.
	maxTokens := 32768
	if mc := gjson.ParseBytes(modelConfig); mc.Exists() {
		if v := mc.Get("max_output_tokens").Int(); v > 0 {
			maxTokens = int(v)
		}
	}
	if v := root.Get("max_tokens").Int(); v > 0 && int(v) < maxTokens {
		maxTokens = int(v)
	}
	if v := root.Get("max_completion_tokens").Int(); v > 0 && int(v) < maxTokens {
		maxTokens = int(v)
	}

	isReasoning := false
	if mc := gjson.ParseBytes(modelConfig); mc.Exists() {
		isReasoning = mc.Get("is_reasoning").Bool()
	}

	// Tools passthrough (array or empty).
	var tools json.RawMessage = []byte("[]")
	if t := root.Get("tools"); t.IsArray() {
		tools = json.RawMessage(t.Raw)
	}

	sessionID := stableHash16("qoder-session", userID, qoderKey)
	recordID := stableChatRecordID(qoderKey, messages, tools, maxTokens)

	payload := map[string]any{
		"request_id":       newUUID(),
		"request_set_id":   recordID,
		"chat_record_id":   recordID,
		"session_id":       sessionID,
		"stream":           true,
		"chat_task":        "FREE_INPUT",
		"is_reply":         true,
		"is_retry":         false,
		"source":           1,
		"version":          "3",
		"session_type":     "qodercli",
		"agent_id":         "agent_common",
		"task_id":          "common",
		"code_language":    "",
		"chat_prompt":      "",
		"image_urls":       nil,
		"aliyun_user_type": "",
		"system":           systemText,
		"messages":         messages,
		"tools":            tools,
		"parameters":       map[string]any{"max_tokens": maxTokens},
		"chat_context": map[string]any{
			"chatPrompt": "",
			"imageUrls":  nil,
			"extra": map[string]any{
				"context":         []any{},
				"modelConfig":     map[string]any{"key": qoderKey, "is_reasoning": isReasoning},
				"originalContent": lastUser,
			},
			"features": []any{},
			"text":     lastUser,
		},
		"model_config": modelConfig,
		"business": map[string]any{
			"product":  "cli",
			"version":  "1.0.0",
			"type":     "agent",
			"stage":    "start",
			"id":       newUUID(),
			"name":     truncate(lastUser, 30),
			"begin_at": time.Now().UnixMilli(),
		},
	}

	return json.Marshal(payload)
}

// qoderMessage is a normalized chat message (role + flattened text content).
type qoderMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// normalizeMessages hoists role:system messages out (Qoder rejects system in the
// messages array) and flattens multipart content to plain text. Returns the
// remaining messages plus the joined system text.
func normalizeMessages(messages gjson.Result) ([]qoderMessage, string) {
	var out []qoderMessage
	var systemParts []string
	if !messages.IsArray() {
		return out, ""
	}
	messages.ForEach(func(_, msg gjson.Result) bool {
		role := msg.Get("role").String()
		text := extractText(msg.Get("content"))
		if role == "system" || role == "developer" {
			if text != "" {
				systemParts = append(systemParts, text)
			}
			return true
		}
		if role == "" {
			role = "user"
		}
		out = append(out, qoderMessage{Role: role, Content: text})
		return true
	})
	return out, strings.Join(systemParts, "\n\n")
}

// extractText flattens a chat message content (string or content-part array).
func extractText(content gjson.Result) string {
	switch {
	case content.Type == gjson.String:
		return content.String()
	case content.IsArray():
		var parts []string
		content.ForEach(func(_, part gjson.Result) bool {
			t := part.Get("type").String()
			if t == "text" || t == "input_text" || t == "output_text" || t == "" {
				if s := part.Get("text").String(); s != "" {
					parts = append(parts, s)
				}
			}
			return true
		})
		return strings.Join(parts, "\n")
	}
	return ""
}

// lastUserText returns the content of the last user message.
func lastUserText(messages []qoderMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return messages[i].Content
		}
	}
	return ""
}

// stableChatRecordID derives a deterministic 16-hex id from the request so the
// same prompt reuses the same chat_record_id (mirrors 9router).
func stableChatRecordID(model string, messages []qoderMessage, tools json.RawMessage, maxTokens int) string {
	parts := []string{model}
	for _, m := range messages {
		parts = append(parts, m.Role, m.Content)
	}
	parts = append(parts, string(tools), "mt="+itoa(maxTokens))
	return stableHash16("qoder-record", parts...)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
