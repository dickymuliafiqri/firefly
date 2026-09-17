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

// ApplyPersonas injects the combined persona directive into the chat payload.
// If the first message is a system message, it prepends the directive.
// Otherwise, it inserts a new system message at index 0.
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

	// Case 1: First message is already a system message -> Prepend directive to content
	if firstRole == "system" {
		origContent := msgArr[0].Get("content").String()
		// Avoid duplicate injection if already present
		if strings.Contains(origContent, "CRITICAL INSTRUCTION (Brevity)") || strings.Contains(origContent, "ENGINEERING MANDATE (Minimal Code)") {
			return body, false
		}
		newContent := directive + "\n\n" + origContent
		updated, err := sjson.SetBytes(body, "messages.0.content", newContent)
		if err != nil {
			return body, false
		}
		return updated, true
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
