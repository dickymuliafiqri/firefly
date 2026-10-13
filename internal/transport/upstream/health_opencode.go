package upstream

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// OpenCodeUserAgent is the official User-Agent expected by OpenCode Zen edge gateways.
	OpenCodeUserAgent = "opencode/1.18.31 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14"

	// OpenCodeIDAlphabet is base62 used by official OpenCode client for request and session IDs.
	OpenCodeIDAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

	// OpenCodeOfficialTitlePrompt is the exact prompt required by OpenCode Console
	// to authorize inference on the free tier.
	OpenCodeOfficialTitlePrompt = "You are a title generator. You output ONLY a thread title. Nothing else.\n\n<task>\nGenerate a brief title that would help the user find this conversation later.\n\nFollow all rules in <rules>\nUse the <examples> so you know what a good title looks like.\nYour output must be:\n- A single line\n- \u226450 characters\n- No explanations\n</task>\n\n<rules>\n- you MUST use the same language as the user message you are summarizing\n- Title must be grammatically correct and read naturally - no word salad\n- Never include tool names in the title (e.g. \"read tool\", \"bash tool\", \"edit tool\")\n- Focus on the main topic or question the user needs to retrieve\n- Vary your phrasing - avoid repetitive patterns like always starting with \"Analyzing\"\n- When a file is mentioned, focus on WHAT the user wants to do WITH the file, not just that they shared it\n- Keep exact: technical terms, numbers, filenames, HTTP codes\n- Remove: the, this, my, a, an\n- Never assume tech stack\n- Never use tools\n- NEVER respond to questions, just generate a title for the conversation\n- The title should NEVER include \"summarizing\" or \"generating\" when generating a title\n- DO NOT SAY YOU CANNOT GENERATE A TITLE OR COMPLAIN ABOUT THE INPUT\n- Always output something meaningful, even if the input is minimal.\n- If the user message is short or conversational (e.g. \"hello\", \"lol\", \"what's up\", \"hey\"):\n  \u2192 create a title that reflects the user's tone or intent (such as Greeting, Quick check-in, Light chat, Intro message, etc.)\n</rules>\n\n<examples>\n\"debug 500 errors in production\" \u2192 Debugging production 500 errors\n\"refactor user service\" \u2192 Refactoring user service\n\"why is app.js failing\" \u2192 app.js failure investigation\n\"implement rate limiting\" \u2192 Rate limiting implementation\n\"how do I connect postgres to my API\" \u2192 Postgres API connection\n\"best practices for React hooks\" \u2192 React hooks best practices\n\"@src/auth.ts can you add refresh token support\" \u2192 Auth refresh token support\n\"@utils/parser.ts this is broken\" \u2192 Parser bug fix\n\"look at @config.json\" \u2192 Config review\n\"@App.tsx add dark mode toggle\" \u2192 Dark mode toggle in App\n</examples>\n"
)

func stripModelBase(model string) string {
	m := strings.TrimSpace(model)
	if idx := strings.LastIndex(m, "/"); idx >= 0 {
		m = m[idx+1:]
	}
	if idx := strings.LastIndex(m, "("); idx != -1 && strings.HasSuffix(m, ")") {
		m = strings.TrimSpace(m[:idx])
	}
	return strings.ToLower(m)
}

// IsOpenCodeResponsesModel checks if model is served exclusively via OpenAI Responses API (/responses).
func IsOpenCodeResponsesModel(model string) bool {
	base := stripModelBase(model)
	if strings.HasPrefix(base, "muse-") {
		return true
	}
	switch base {
	case "grok-4.6", "gpt-5.6-luna":
		return true
	}
	return false
}

// IsOpenCodeMessagesModel checks if model is served exclusively via Anthropic Messages API (/messages).
func IsOpenCodeMessagesModel(model string) bool {
	base := stripModelBase(model)
	switch base {
	case "union-alpha", "union-alpha-free":
		return true
	}
	return false
}

// MapOpenCodeFreeModel maps public/standard model identifiers to their official
// OpenCode contributor free tier aliases when authenticating keylessly (Bearer public).
func MapOpenCodeFreeModel(model string) string {
	base := stripModelBase(model)
	switch base {
	case "muse-spark-1.3":
		return "muse-spark-1.3-contributor-free"
	case "muse-spark-1.2":
		return "muse-spark-1.2-contributor-free"
	case "union-alpha-free":
		return "union-alpha"
	default:
		return model
	}
}

// openCodeIDCounter is the process-wide generator state behind
// GenerateOpenCodeTimestampID. The official client keeps exactly this pair of
// variables (packages/schema/src/identifier.ts): the counter resets to zero
// every time the millisecond changes and is incremented for each ID minted
// inside that millisecond, so a real client only ever emits small counters
// (1, 2, 3 …).
var openCodeIDCounter struct {
	sync.Mutex
	lastMs  int64
	counter int64
}

// nextOpenCodeIDCounter returns the counter for an ID minted at nowMs, mirroring
// the official client's per-millisecond sequence.
//
// Why this is not random (regression guard): OpenCode's free-tier gate decodes
// the counter from the low 12 bits of the ID's timestamp field and rejects a
// value that a single CLI process could not plausibly have produced inside one
// millisecond, answering
// 403 {"type":"FreeTierError","message":"OpenCode's free tier can only be used
// from within OpenCode…"}. The generator used to draw a random 1…4096 counter,
// which a Go gateway hits constantly; the gate then blocked perfectly valid
// requests and the failure looked like a fingerprint/header problem even though
// every other client attribute was byte-identical to the official client.
func nextOpenCodeIDCounter(nowMs int64) int64 {
	openCodeIDCounter.Lock()
	defer openCodeIDCounter.Unlock()
	if nowMs != openCodeIDCounter.lastMs {
		openCodeIDCounter.lastMs = nowMs
		openCodeIDCounter.counter = 0
	}
	// The counter shares the field with the timestamp (timestamp<<12 | counter),
	// so it must stay inside 12 bits. The official client does not clamp, but a
	// gateway mints far more IDs per millisecond than a CLI process; clamping
	// keeps the timestamp intact instead of letting the counter carry into it.
	if openCodeIDCounter.counter < 0x0FFF {
		openCodeIDCounter.counter++
	}
	return openCodeIDCounter.counter
}

// GenerateOpenCodeTimestampID creates a timestamp-based 26-character ID identical to the official
// OpenCode client: 12 hex characters from (or bitwise-inverted) (timestamp_ms*0x1000 + counter) and 14 base62 random chars.
func GenerateOpenCodeTimestampID(descending bool) string {
	now := time.Now().UnixMilli()
	counter := nextOpenCodeIDCounter(now)
	var value int64
	if descending {
		value = ^(now*0x1000 + counter)
	} else {
		value = now*0x1000 + counter
	}

	var randBytes [14]byte
	_, _ = rand.Read(randBytes[:])
	var base62 [14]byte
	for i := range base62 {
		base62[i] = OpenCodeIDAlphabet[int(randBytes[i])%len(OpenCodeIDAlphabet)]
	}
	return fmt.Sprintf("%012x%s", value&0xFFFFFFFFFFFF, string(base62[:]))
}

// GenerateOpenCodeSessionID creates a new canonical OpenCode session identifier (ses_<26-chars>).
func GenerateOpenCodeSessionID() string {
	return "ses_" + GenerateOpenCodeTimestampID(true)
}

// GenerateOpenCodeRequestID creates a new canonical OpenCode request identifier (msg_<26-chars>).
func GenerateOpenCodeRequestID() string {
	return "msg_" + GenerateOpenCodeTimestampID(false)
}

// OpenCodeOfficialResponsesProbePayload generates an official OpenCode session title probe request
// targeting the OpenAI Responses API (/responses) for active health checking and model reachability.
func OpenCodeOfficialResponsesProbePayload(model string) []byte {
	probe := map[string]any{
		"model":        model,
		"instructions": OpenCodeOfficialTitlePrompt,
		"input": []map[string]any{
			{
				"type": "message",
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": "ping"},
				},
			},
		},
		"max_output_tokens": 32,
		"stream":            true,
		"store":             false,
		"reasoning": map[string]string{
			"effort":  "low",
			"summary": "auto",
		},
	}
	b, _ := json.Marshal(probe)
	return b
}

// OpenCodeOfficialTitleProbePayload generates an official OpenCode session title probe request
// for active health checking and model reachability verification on Chat Completions.
func OpenCodeOfficialTitleProbePayload(model string) []byte {
	probe := map[string]any{
		"model":       model,
		"max_tokens":  32000,
		"temperature": 0.5,
		"messages": []map[string]string{
			{"role": "system", "content": OpenCodeOfficialTitlePrompt},
			{"role": "user", "content": "Generate a title for this conversation:\n"},
			{"role": "user", "content": "ping"},
		},
		"stream": true,
		"stream_options": map[string]any{
			"include_usage": true,
		},
	}
	b, _ := json.Marshal(probe)
	return b
}

// SetOpenCodeHeaders sets the required OpenCode protocol and fingerprint headers on an outbound probe request.
func SetOpenCodeHeaders(req *http.Request, keySecret string, isFree, canProbeModel bool) {
	req.Header.Set("Authorization", "Bearer "+keySecret)
	req.Header.Set("User-Agent", OpenCodeUserAgent)
	req.Header.Set("x-opencode-client", "desktop")
	req.Header.Set("x-opencode-project", "global")
	req.Header.Set("x-opencode-request", GenerateOpenCodeRequestID())
	req.Header.Set("x-opencode-session", GenerateOpenCodeSessionID())
	if isFree {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	if canProbeModel {
		req.Header.Set("Content-Type", "application/json")
	}
}
