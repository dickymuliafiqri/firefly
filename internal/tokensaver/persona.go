package tokensaver

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	// CavemanDirective forces the LLM to output concise, terse answers without conversational filler.
	CavemanDirective = "CRITICAL INSTRUCTION (Brevity): Be extremely terse. Eliminate conversational filler, pleasantries, preambles, and post-summaries. Provide direct answers, code, or data immediately. Do not explain obvious things."

	// PonytailDirective instructs the model to follow YAGNI, prefer standard library, and minimize added abstractions.
	PonytailDirective = "ENGINEERING MANDATE (Minimal Code): Follow YAGNI (You Aren't Gonna Need It). Write minimal, clean code. Prefer the standard library over external dependencies. Prefer modifying or deleting code over adding new abstractions. Keep solutions simple, direct, and maintainable without premature generalization."
)

// BuildPersonaDirective combines enabled persona directives into a single prompt injection.
func BuildPersonaDirective(terse, minimalCode bool) string {
	if !terse && !minimalCode {
		return ""
	}
	if terse && minimalCode {
		return CavemanDirective + "\n\n" + PonytailDirective
	}
	if terse {
		return CavemanDirective
	}
	return PonytailDirective
}

// containsDirective reports whether the persona directives are already present in
// a system message, so a hot-reloaded payload is never injected twice. The raw
// JSON of non-string content is checked as text, which is sufficient because the
// directive markers are plain ASCII.
func containsDirective(s string) bool {
	return strings.Contains(s, "CRITICAL INSTRUCTION (Brevity)") ||
		strings.Contains(s, "ENGINEERING MANDATE (Minimal Code)")
}

// ApplyPersonas injects the combined persona directive into the chat payload.
// If the first message is a system message with plain string content, it prepends
// the directive. Otherwise it inserts a new system message at index 0.
func ApplyPersonas(body []byte, terse, minimalCode bool) ([]byte, bool) {
	directive := BuildPersonaDirective(terse, minimalCode)
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

	firstRole := msgArr[0].Get("role").String()

	// Case 1: First message is a system message with string content -> Prepend
	// the directive to it. Structured content (multimodal blocks, Anthropic-style
	// content parts) is deliberately not touched: gjson renders non-string values
	// as raw JSON, and writing that back would flatten the array into a string and
	// destroy the blocks. Those payloads fall through to Case 2 instead.
	if firstRole == "system" {
		content := msgArr[0].Get("content")
		if content.Type == gjson.String {
			origContent := content.String()
			if containsDirective(origContent) {
				return body, false
			}
			newContent := directive + "\n\n" + origContent
			updated, err := sjson.SetBytes(body, "messages.0.content", newContent)
			if err != nil {
				return body, false
			}
			return updated, true
		}
		if containsDirective(content.Raw) {
			return body, false
		}
	}

	// Case 2: First message is user or assistant -> Insert new system message at index 0
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
	newRaw.WriteString(origRaw[1:]) // append the rest without original leading '['

	updated, err := sjson.SetRawBytes(body, "messages", []byte(newRaw.String()))
	if err != nil {
		return body, false
	}

	return updated, true
}
