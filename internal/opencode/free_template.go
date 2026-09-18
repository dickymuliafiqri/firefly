package opencode

import (
	_ "embed"
	"encoding/json"
	"strings"

	"github.com/dickymuliafiqri/firefly/internal/upstream"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

//go:embed free_prompt.txt
var officialFreePrompt string

//go:embed free_tools.json
var officialFreeToolsJSON []byte

var parsedFreeTools []any

func init() {
	if len(officialFreeToolsJSON) > 0 {
		_ = json.Unmarshal(officialFreeToolsJSON, &parsedFreeTools)
	}
}

// OfficialFreePromptFor returns the official prompt adapted for the given model ID.
func OfficialFreePromptFor(model string) string {
	base := BaseModelID(model)
	if base == "" {
		base = "muse-spark-1.3-contributor-free"
	}
	return strings.ReplaceAll(officialFreePrompt, "big-pickle", base)
}

// OfficialTitlePrompt is the exact prompt used by the official OpenCode CLI
// for session initialization and title generation. OpenCode Console verifies
// this prompt on the free tier to authorize model invocation.
const OfficialTitlePrompt = upstream.OpenCodeOfficialTitlePrompt

// OfficialTitleProbePayload generates an official OpenCode session title probe request
// for active health checking and model reachability verification on Chat Completions.
func OfficialTitleProbePayload(model string) []byte {
	return upstream.OpenCodeOfficialTitleProbePayload(model)
}

// OfficialResponsesProbePayload generates an official OpenCode session title probe request
// targeting the OpenAI Responses API (/responses) for active health checking and model reachability.
func OfficialResponsesProbePayload(model string) []byte {
	return upstream.OpenCodeOfficialResponsesProbePayload(model)
}

// EnforceFreeSessionPayload enriches an arbitrary Chat Completions payload with the
// official OpenCode client session structure (system prompt preamble and official tools
// schema) required by OpenCode Console to permit free tier model inference.
func EnforceFreeSessionPayload(body []byte) []byte {
	out := body

	// Always require stream and include_usage for free tier console validation
	out, _ = sjson.SetBytes(out, "stream", true)
	out, _ = sjson.SetBytes(out, "stream_options.include_usage", true)

	if !gjson.GetBytes(out, "max_tokens").Exists() && !gjson.GetBytes(out, "max_completion_tokens").Exists() {
		out, _ = sjson.SetBytes(out, "max_tokens", 32000)
	}

	model := gjson.GetBytes(out, "model").String()
	basePrompt := OfficialFreePromptFor(model)

	// Ensure system prompt contains the official OpenCode instructions preamble
	msgsRes := gjson.GetBytes(out, "messages")
	if msgsRes.IsArray() {
		msgs := msgsRes.Array()
		if len(msgs) > 0 && msgs[0].Get("role").String() == "system" {
			currContent := msgs[0].Get("content").String()
			if !strings.HasPrefix(currContent, "You are opencode") && !strings.Contains(currContent, "You are a title generator") {
				combined := basePrompt + "\n\n# User Instructions\n" + currContent
				out, _ = sjson.SetBytes(out, "messages.0.content", combined)
			}
		} else {
			// Insert official system prompt at beginning of messages array
			var existingMsgs []any
			_ = json.Unmarshal([]byte(msgsRes.Raw), &existingMsgs)
			newMsgs := append([]any{
				map[string]string{
					"role":    "system",
					"content": basePrompt,
				},
			}, existingMsgs...)
			out, _ = sjson.SetBytes(out, "messages", newMsgs)
		}
	}

	toolsRes := gjson.GetBytes(out, "tools")
	if !toolsRes.Exists() || !toolsRes.IsArray() || len(toolsRes.Array()) == 0 {
		if len(parsedFreeTools) > 0 {
			out, _ = sjson.SetBytes(out, "tools", parsedFreeTools)
			out, _ = sjson.SetBytes(out, "tool_choice", "auto")
		}
	}

	return out
}
