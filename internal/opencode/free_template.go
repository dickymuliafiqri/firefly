package opencode

import (
	_ "embed"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

//go:embed free_prompt.txt
var officialFreePrompt string

//go:embed free_tools.json
var officialFreeToolsJSON []byte

var parsedFreeTools []any
var parsedFreeResponsesTools []map[string]any

func init() {
	if len(officialFreeToolsJSON) > 0 {
		_ = json.Unmarshal(officialFreeToolsJSON, &parsedFreeTools)
		for _, t := range parsedFreeTools {
			m, ok := t.(map[string]any)
			if !ok {
				continue
			}
			fn, ok := m["function"].(map[string]any)
			if !ok {
				continue
			}
			parsedFreeResponsesTools = append(parsedFreeResponsesTools, map[string]any{
				"type":        "function",
				"name":        fn["name"],
				"description": fn["description"],
				"parameters":  fn["parameters"],
			})
		}
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

// OfficialResponsesTools returns the official tool definitions in OpenAI Responses format.
func OfficialResponsesTools() []map[string]any {
	return parsedFreeResponsesTools
}

// OfficialTitlePrompt is the exact prompt used by the official OpenCode CLI
// for session initialization and title generation. OpenCode Console verifies
// this prompt on the free tier to authorize model invocation.
const OfficialTitlePrompt = "You are a title generator. You output ONLY a thread title. Nothing else.\n\n<task>\nGenerate a brief title that would help the user find this conversation later.\n\nFollow all rules in <rules>\nUse the <examples> so you know what a good title looks like.\nYour output must be:\n- A single line\n- \u226450 characters\n- No explanations\n</task>\n\n<rules>\n- you MUST use the same language as the user message you are summarizing\n- Title must be grammatically correct and read naturally - no word salad\n- Never include tool names in the title (e.g. \"read tool\", \"bash tool\", \"edit tool\")\n- Focus on the main topic or question the user needs to retrieve\n- Vary your phrasing - avoid repetitive patterns like always starting with \"Analyzing\"\n- When a file is mentioned, focus on WHAT the user wants to do WITH the file, not just that they shared it\n- Keep exact: technical terms, numbers, filenames, HTTP codes\n- Remove: the, this, my, a, an\n- Never assume tech stack\n- Never use tools\n- NEVER respond to questions, just generate a title for the conversation\n- The title should NEVER include \"summarizing\" or \"generating\" when generating a title\n- DO NOT SAY YOU CANNOT GENERATE A TITLE OR COMPLAIN ABOUT THE INPUT\n- Always output something meaningful, even if the input is minimal.\n- If the user message is short or conversational (e.g. \"hello\", \"lol\", \"what's up\", \"hey\"):\n  \u2192 create a title that reflects the user's tone or intent (such as Greeting, Quick check-in, Light chat, Intro message, etc.)\n</rules>\n\n<examples>\n\"debug 500 errors in production\" \u2192 Debugging production 500 errors\n\"refactor user service\" \u2192 Refactoring user service\n\"why is app.js failing\" \u2192 app.js failure investigation\n\"implement rate limiting\" \u2192 Rate limiting implementation\n\"how do I connect postgres to my API\" \u2192 Postgres API connection\n\"best practices for React hooks\" \u2192 React hooks best practices\n\"@src/auth.ts can you add refresh token support\" \u2192 Auth refresh token support\n\"@utils/parser.ts this is broken\" \u2192 Parser bug fix\n\"look at @config.json\" \u2192 Config review\n\"@App.tsx add dark mode toggle\" \u2192 Dark mode toggle in App\n</examples>\n"

// OfficialTitleProbePayload generates an official OpenCode session title probe request
// for active health checking and model reachability verification on Chat Completions.
func OfficialTitleProbePayload(model string) []byte {
	probe := map[string]any{
		"model":       model,
		"max_tokens":  32000,
		"temperature": 0.5,
		"messages": []map[string]string{
			{"role": "system", "content": OfficialTitlePrompt},
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

// OfficialResponsesProbePayload generates an official OpenCode session title probe request
// targeting the OpenAI Responses API (/responses) for active health checking and model reachability.
func OfficialResponsesProbePayload(model string) []byte {
	probe := map[string]any{
		"model":        model,
		"instructions": OfficialTitlePrompt,
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

	// If no tools are provided, supply the official OpenCode CLI tool declarations
	toolsRes := gjson.GetBytes(out, "tools")
	if !toolsRes.Exists() || !toolsRes.IsArray() || len(toolsRes.Array()) == 0 {
		if len(parsedFreeTools) > 0 {
			out, _ = sjson.SetBytes(out, "tools", parsedFreeTools)
			out, _ = sjson.SetBytes(out, "tool_choice", "auto")
		}
	}

	return out
}
