package tokensaver

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// InjectSystemPrompt adds an operator-authored directive (e.g. a prohibition on
// promotional messages) to an OpenAI chat completion payload.
//
// Placement: the directive is appended to the tail of the first system message
// when its content is a plain string, so the operator's prohibition carries the
// last word inside the system block. Otherwise (no system message, or structured
// multimodal content that must not be flattened into a string) a new system
// message is inserted at index 0 — the same mechanics ApplyPersonas uses.
//
// It is idempotent: a directive already present in any system message is never
// injected twice, so a client that echoes the same text cannot stack copies.
// Returns the updated body and whether a change was made.
func InjectSystemPrompt(body []byte, directive string) ([]byte, bool) {
	directive = strings.TrimSpace(directive)
	if directive == "" {
		return body, false
	}

	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body, false
	}
	msgArr := messages.Array()
	if len(msgArr) == 0 {
		return body, false
	}
	if systemPromptPresent(msgArr, directive) {
		return body, false
	}

	// Case 1: first message is a system message with string content -> append.
	// Structured content (multimodal blocks, Anthropic-style content parts) is
	// deliberately not touched: writing raw JSON back through sjson as a string
	// would destroy the blocks. Those payloads fall through to Case 2.
	if msgArr[0].Get("role").String() == "system" {
		content := msgArr[0].Get("content")
		if content.Type == gjson.String {
			newContent := strings.TrimRight(content.String(), "\n") + "\n\n" + directive
			updated, err := sjson.SetBytes(body, "messages.0.content", newContent)
			if err != nil {
				return body, false
			}
			return updated, true
		}
	}

	// Case 2: insert a new system message at index 0.
	origRaw := strings.TrimSpace(messages.Raw)
	if !strings.HasPrefix(origRaw, "[") || !strings.HasSuffix(origRaw, "]") {
		return body, false
	}
	sysObj, err := json.Marshal(map[string]string{
		"role":    "system",
		"content": directive,
	})
	if err != nil {
		return body, false
	}

	var newRaw strings.Builder
	newRaw.Grow(len(origRaw) + len(sysObj) + 2)
	newRaw.WriteByte('[')
	newRaw.Write(sysObj)
	newRaw.WriteByte(',')
	newRaw.WriteString(origRaw[1:]) // rest without the original leading '['

	updated, err := sjson.SetRawBytes(body, "messages", []byte(newRaw.String()))
	if err != nil {
		return body, false
	}
	return updated, true
}

// systemPromptPresent reports whether any system message already carries the
// directive. Matched against the decoded string content rather than raw JSON so
// escaped non-ASCII characters cannot defeat the check.
func systemPromptPresent(msgArr []gjson.Result, directive string) bool {
	for _, m := range msgArr {
		if m.Get("role").String() != "system" {
			continue
		}
		if c := m.Get("content"); c.Type == gjson.String && strings.Contains(c.String(), directive) {
			return true
		}
	}
	return false
}
