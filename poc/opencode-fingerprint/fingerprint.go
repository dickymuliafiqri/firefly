// Package opencodefingerprint is a proof-of-concept refresh of Firefly's
// pinned OpenCode free-tier client fingerprint, lifted from the v1.18.31 era
// to the current v2 line.
//
// It is a standalone module on purpose: nothing under internal/ imports it and
// it imports nothing from internal/. Every value here is transcribed from the
// upstream v2.0.29 sources cited inline, so the delta can be reviewed and
// tested before anything is ported into the adapter.
//
// The problem it solves: OpenCode's free tier is gated server-side on a client
// fingerprint. The gate lives in a closed service (the zen routes in
// packages/console/app are 191-byte stubs that proxy to
// Resource.ConsoleMigration.inferenceUrl), so its rules can change without any
// public commit. Firefly pins the v1.18.31 fingerprint, and every upstream
// release advances at least one validated attribute — which is why the 403
// FreeTierError comes back "setiap kali OpenCode ada update".
package opencodefingerprint

import (
	"crypto/rand"
	_ "embed"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Client identity — packages/core/src/app.ts
// ---------------------------------------------------------------------------

// V2Channel is the build channel string a stable release ships with.
// packages/script/src/index.ts: CHANNEL === "latest" unless OPENCODE_CHANNEL
// overrides it, or the build is a preview (IS_PREVIEW = CHANNEL !== "latest").
const V2Channel = "latest"

// V2Version is the current stable release, from
// https://opencode.ai/update/api/latest/cli/opencode
// → {"channel":"latest","name":"cli","distribution":"opencode","version":"2.0.29"}.
const V2Version = "2.0.29"

// V2ClientName is app.name, which is also the value v2 puts in
// x-opencode-client. packages/core/src/app.ts make() defaults
// name:"opencode", version:"unknown", channel:"unknown".
const V2ClientName = "opencode"

// AppInfo mirrors packages/core/src/app.ts
//
//	Info = { name, version, channel }
type AppInfo struct {
	Name    string
	Version string
	Channel string
}

// DefaultAppInfo returns the identity a stable v2 release reports.
func DefaultAppInfo() AppInfo {
	return AppInfo{Name: V2ClientName, Version: V2Version, Channel: V2Channel}
}

// UserAgent mirrors packages/core/src/app.ts verbatim:
//
//	export function useragent(app: Info) {
//	  return `opencode/${app.channel}/${app.version}/${app.name}`
//	}
//
// The v1 string Firefly pins today
// ("opencode/1.18.31 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14")
// exists in no v2 form — the ai-sdk/provider-utils and runtime/bun segments
// were dropped when the harness moved off the AI-SDK transport. A preview
// build reports opencode/beta/<version>/opencode instead.
func UserAgent(app AppInfo) string {
	return "opencode/" + app.Channel + "/" + app.Version + "/" + app.Name
}

// ---------------------------------------------------------------------------
// Request headers — packages/core/src/session/model-request.ts
// ---------------------------------------------------------------------------

// Header is one name/value pair. Kept ordered because v2 emits a fixed set in
// a fixed order, and two of the names use non-canonical casing.
type Header struct {
	Name  string
	Value string
}

// V2Headers mirrors the header block of
// packages/core/src/session/model-request.ts (v2.0.29), verbatim:
//
//	"x-opencode-session-id": session.id,
//	...(session.parentID ? { "x-opencode-parent-session-id": session.parentID } : {}),
//	"x-session-affinity": affinity,
//	"X-Session-Id": affinity,
//	...(session.parentID ? { "x-parent-session-id": session.parentID } : {}),
//	"User-Agent": App.useragent(app),
//	"x-opencode-project": session.projectID,
//	"x-opencode-session": affinity,
//	"x-opencode-client": app.name,
//
// affinity = session.parentID ?? session.fork?.sessionID ?? session.id
// (packages/core/src/session/affinity.ts), so a root session sends the same
// value in all three session-ish headers.
//
// v2 sends no x-opencode-request at all. Firefly currently does, plus
// x-opencode-client: desktop — both are v1-era artifacts.
func V2Headers(app AppInfo, sessionID, projectID, parentID string) []Header {
	affinity := sessionID
	if parentID != "" {
		affinity = parentID
	}

	h := make([]Header, 0, 9)
	h = append(h, Header{"x-opencode-session-id", sessionID})
	if parentID != "" {
		h = append(h, Header{"x-opencode-parent-session-id", parentID})
	}
	h = append(h,
		Header{"x-session-affinity", affinity},
		Header{"X-Session-Id", affinity},
	)
	if parentID != "" {
		h = append(h, Header{"x-parent-session-id", parentID})
	}
	h = append(h,
		Header{"User-Agent", UserAgent(app)},
		Header{"x-opencode-project", projectID},
		Header{"x-opencode-session", affinity},
		Header{"x-opencode-client", app.Name},
	)
	return h
}

// RootHeaderCount is how many headers v2 sends for a root session. Asserted by
// packages/core/test/session-title.test.ts:220 and
// packages/core/test/session-runner.test.ts:4727.
const RootHeaderCount = 7

// ---------------------------------------------------------------------------
// System prompt — packages/core/src/session/runner/prompt/system.txt
// ---------------------------------------------------------------------------

// officialSystemPrompt is the v2 template, embedded verbatim.
//
//go:embed system.txt
var officialSystemPrompt string

// toolGuidancePlaceholder is the token render() substitutes.
const toolGuidancePlaceholder = "${OPENCODE_TOOL_GUIDANCE}"

// Tool guidance lines, verbatim from
// packages/core/src/session/system-prompt.ts render().
var (
	guidanceShell = []string{
		"- Prefer dedicated tools over shell commands; fall back to the shell when a tool cannot do what you need.",
		"- Do not chain shell commands with separators like `echo \"====\";` or `printf '---'`; the output becomes noisy in a way that makes the user's side of the conversation worse.",
	}
	guidanceWrite = []string{
		"- Use the write tool to create files or completely replace their content. Prefer using the edit tool for targeted changes.",
	}
	guidanceEdit = []string{
		"- Use the edit tool for targeted changes to existing text files. It replaces the exact text in `oldString` with `newString`, and the values must differ. By default, `oldString` must occur exactly once. If it occurs multiple times, include more surrounding context to make it unique or set `replaceAll` to true to replace every occurrence.",
	}
)

// OfficialSystemPrompt returns the raw v2 template, placeholder intact.
func OfficialSystemPrompt() string { return officialSystemPrompt }

// RenderSystemPrompt substitutes the tool-guidance block exactly like
// packages/core/src/session/system-prompt.ts render().
//
// Note what is missing compared with Firefly's pinned free_prompt.txt: v2 has
// no "You are powered by the model named big-pickle" tail. That line is a
// v1-only artifact, so OfficialFreePromptFor's big-pickle substitution has
// nothing left to rewrite once the prompt is refreshed.
func RenderSystemPrompt(tools []string) string {
	var instructions []string
	for _, name := range tools {
		switch name {
		case "shell":
			instructions = append(instructions, guidanceShell...)
		case "write":
			instructions = append(instructions, guidanceWrite...)
		case "edit":
			instructions = append(instructions, guidanceEdit...)
		}
	}
	return strings.Replace(officialSystemPrompt, toolGuidancePlaceholder, strings.Join(instructions, "\n"), 1)
}

// ---------------------------------------------------------------------------
// Tool schemas — packages/core/src/tool/plugin/{shell,read}.ts
// ---------------------------------------------------------------------------

// Tool is one OpenAI-shaped tool definition.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction is the function payload of a Tool.
type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// shellDescription is assembled the way
// packages/core/src/tool/plugin/shell.ts description() assembles it, with the
// shell resolved to bash on Linux and no shell-specific line.
const shellDescription = "Execute a shell command and return its output." +
	" Quote file paths containing spaces or special characters." +
	" Prefer dedicated tools over shell commands when possible." +
	" When output is large, the full result is saved to a file and a truncated preview is returned." +
	" Rely on automatic truncation unless filtering the output is more useful." +
	" Commands accept an optional timeout, background commands have no timeout by default." +
	" Background commands return immediately, and you will be notified when they complete."

// readDescription is verbatim from packages/core/src/tool/plugin/read.ts.
const readDescription = "Read the contents of a file or directory. Supports text files, images, and PDFs." +
	" Images and PDFs are presented directly to the model." +
	" Each text line is prefixed by its 1-based line number as <line>: <content>." +
	" The prefix is for reference and is not part of the file content." +
	" Directory entries are returned one per line." +
	" Use offset and limit to read large files or directories in sections." +
	" Prefer one larger read over many small slices, and use grep to find specific content in large files."

// VerificationTools returns the shell + read pair v2 ships. The names are the
// load-bearing part: v2 renamed bash → shell, and Firefly's
// EnforceFreeSessionPayload still injects a tool literally named "bash".
func VerificationTools() []Tool {
	return []Tool{
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "shell",
				Description: shellDescription,
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{
							"type":        "string",
							"description": "Shell command string to execute",
						},
						"workdir": map[string]any{
							"type":        "string",
							"description": "Working directory to execute the command in. Defaults to the current working directory. When possible, avoid changing directories in the command and set the working directory here instead.",
						},
						"timeout": map[string]any{
							"type":        "integer",
							"description": "Timeout in milliseconds. Set to 0 to disable the timeout. Defaults to 120000 for foreground commands. Background commands have no timeout by default.",
						},
						"background": map[string]any{
							"type":        "boolean",
							"description": "Run the command in the background and return immediately (useful for dev servers and long-running builds). You do not need to use '&' at the end of the command when using this parameter. You will be notified when it completes. DO NOT poll for completion.",
						},
					},
					"required": []string{"command"},
				},
			},
		},
		{
			Type: "function",
			Function: ToolFunction{
				Name:        "read",
				Description: readDescription,
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "File or directory to read",
						},
						"offset": map[string]any{
							"type":        "integer",
							"description": "The line or directory entry to start reading from (1-based)",
						},
						"limit": map[string]any{
							"type":        "integer",
							"description": "The maximum number of lines or directory entries to read (defaults to and capped at 2000)",
						},
					},
					"required": []string{"path"},
				},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Free-model aliases — mirrors upstream.MapOpenCodeFreeModel
// ---------------------------------------------------------------------------

// FreeModelAlias maps a public model id to the upstream free-tier id. The
// first three are what Firefly already pins; the last is the new one that
// triggered the report.
var FreeModelAlias = map[string]string{
	"muse-spark-1.3":      "muse-spark-1.3-contributor-free",
	"muse-spark-1.2":      "muse-spark-1.2-contributor-free",
	"union-alpha-free":    "union-alpha",
	"deepseek-v4.1-flash": "deepseek-v4.1-flash-free",
}

// MapFreeModel resolves a public id to its upstream free-tier id, passing
// through anything that is not aliased.
func MapFreeModel(public string) string {
	if upstream, ok := FreeModelAlias[public]; ok {
		return upstream
	}
	return public
}

// ---------------------------------------------------------------------------
// Identifier generation — packages/schema/src/identifier.ts
// ---------------------------------------------------------------------------

// OpenCodeIDAlphabet is the base62 alphabet v2 uses. Identical to
// upstream.OpenCodeIDAlphabet.
const OpenCodeIDAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// idCounter mirrors v2's per-millisecond sequence. packages/schema/src/
// identifier.ts keeps lastTimestamp/counter module state and resets the
// counter each millisecond, clamping to 12 bits. Firefly's
// nextOpenCodeIDCounter already does exactly this (commit ad68205 fixed the
// same 403 by mirroring it), so the logic is reproduced here only so the dry
// run can mint a well-formed id and the test can prove the format still
// matches v2.
var (
	idMu            sync.Mutex
	idLastTimestamp int64
	idCounter       uint16
)

func nextIDCounter(nowMs int64) uint16 {
	idMu.Lock()
	defer idMu.Unlock()
	if nowMs != idLastTimestamp {
		idLastTimestamp = nowMs
		idCounter = 0
	} else {
		idCounter = (idCounter + 1) & 0x0FFF
	}
	return idCounter
}

// GenerateTimestampID reproduces packages/schema/src/identifier.ts:
//
//	length = 26, chars = base62
//	current = BigInt(timestamp) * 0x1000n + BigInt(counter)
//	value   = descending ? ~current : current
//	12 hex chars from value & 0xFFFFFFFFFFFF, then 14 base62 random
//
// The 12-bit counter in the low bits is what the free-tier gate decodes to
// reject implausible sequences — a random counter is what caused the original
// 403 that commit ad68205 fixed.
func GenerateTimestampID(descending bool) string {
	now := time.Now().UnixMilli()
	counter := nextIDCounter(now)

	var value int64
	if descending {
		value = ^(now*0x1000 + int64(counter))
	} else {
		value = now*0x1000 + int64(counter)
	}

	var randBytes [14]byte
	_, _ = rand.Read(randBytes[:])
	base62 := make([]byte, 14)
	for i := range base62 {
		base62[i] = OpenCodeIDAlphabet[int(randBytes[i])%len(OpenCodeIDAlphabet)]
	}
	return fmt.Sprintf("%012x%s", value&0xFFFFFFFFFFFF, string(base62))
}

// GenerateSessionID creates a canonical OpenCode session id (ses_<26 chars>),
// matching packages/schema/src/session-id.ts.
func GenerateSessionID() string { return "ses_" + GenerateTimestampID(true) }

// GenerateRequestID creates a canonical OpenCode request id (msg_<26 chars>).
// v2 no longer sends it, but Firefly's health probes still do.
func GenerateRequestID() string { return "msg_" + GenerateTimestampID(false) }

// ---------------------------------------------------------------------------
// The delta
// ---------------------------------------------------------------------------

// Change is one line of the v1 → v2 fingerprint delta.
type Change struct {
	Attribute string
	V1        string
	V2        string
	Note      string
}

// Delta lists every attribute the closed free-tier gate can see that moved
// between the v1.18.31 line Firefly pins and v2.0.29. Attributes marked
// "unchanged" are listed so the review can see they were checked, not missed.
func Delta() []Change {
	return []Change{
		{
			Attribute: "User-Agent",
			V1:        "opencode/1.18.31 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14",
			V2:        UserAgent(DefaultAppInfo()),
			Note:      "Format changed entirely to opencode/<channel>/<version>/<name>. The ai-sdk and runtime/bun segments are gone.",
		},
		{
			Attribute: "x-opencode-client",
			V1:        "desktop",
			V2:        "opencode",
			Note:      "app.name, not the surface. The desktop app reports the same string as the CLI.",
		},
		{
			Attribute: "x-opencode-session-id",
			V1:        "(absent)",
			V2:        "<session.id>",
			Note:      "New in v2. Carries the same value as x-opencode-session for a root session.",
		},
		{
			Attribute: "x-session-affinity",
			V1:        "(absent)",
			V2:        "<affinity>",
			Note:      "New in v2. affinity = parentID ?? fork.sessionID ?? session.id.",
		},
		{
			Attribute: "X-Session-Id",
			V1:        "(absent)",
			V2:        "<affinity>",
			Note:      "New in v2, and note the non-canonical casing — it is sent alongside the lowercase form.",
		},
		{
			Attribute: "x-opencode-request",
			V1:        "<msg_ id>",
			V2:        "(absent)",
			Note:      "v2 stopped sending it. Harmless to keep, but it is not part of the v2 shape.",
		},
		{
			Attribute: "system prompt",
			V1:        "\"You are opencode, an interactive CLI tool...\" + \"You are powered by the model named big-pickle\"",
			V2:        "\"You are an AI agent running in OpenCode, a coding agent harness...\"",
			Note:      "Rewritten wholesale, and the model-identity tail is gone.",
		},
		{
			Attribute: "verification tools",
			V1:        "bash + read",
			V2:        "shell + read",
			Note:      "bash was renamed to shell. Firefly injects a tool literally named \"bash\".",
		},
		{
			Attribute: "session/request ID format",
			V1:        "ses_/msg_ + descending timestamp id",
			V2:        "ses_/msg_ + descending timestamp id",
			Note:      "Unchanged. packages/schema/src/identifier.ts is byte-identical to GenerateOpenCodeTimestampID.",
		},
		{
			Attribute: "x-opencode-project",
			V1:        "global",
			V2:        "global",
			Note:      "Unchanged. Project.ID.global is still the static.",
		},
	}
}

// ---------------------------------------------------------------------------
// Free-tier request assembly
// ---------------------------------------------------------------------------

// FreeRequest is the fully assembled request the refreshed fingerprint
// produces for one keyless chat completion.
type FreeRequest struct {
	Method  string
	URL     string
	Headers []Header
	Body    map[string]any
}

// BuildFreeRequest assembles a keyless /chat/completions request the way the
// refreshed adapter would. It preserves EnforceFreeSessionPayload's contract
// (stream forced on, include_usage, max_tokens floor, official prompt
// prepended, verification tools present) with the v2 values substituted.
//
// The model is expected to already be resolved through MapFreeModel.
func BuildFreeRequest(app AppInfo, sessionID, upstreamModel, userMessage string) FreeRequest {
	tools := []string{"shell", "read"}

	body := map[string]any{
		"model":      upstreamModel,
		"stream":     true,
		"max_tokens": 32000,
		"messages": []map[string]string{
			{"role": "system", "content": RenderSystemPrompt(tools)},
			{"role": "user", "content": userMessage},
		},
		"stream_options": map[string]any{"include_usage": true},
		"tools":          VerificationTools(),
	}

	headers := append(V2Headers(app, sessionID, "global", ""),
		Header{"Content-Type", "application/json"},
		Header{"Accept", "text/event-stream"},
		Header{"Authorization", "Bearer public"},
	)

	return FreeRequest{
		Method:  "POST",
		URL:     "https://opencode.ai/zen/v1/chat/completions",
		Headers: headers,
		Body:    body,
	}
}
