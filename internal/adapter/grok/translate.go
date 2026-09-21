package grok

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dickymuliafiqri/firefly/internal/idgen"
	"github.com/tidwall/gjson"
)

var (
	// ErrMalformedJSON indicates an empty or syntactically invalid JSON payload.
	ErrMalformedJSON = errors.New("grok: invalid json payload")
	// ErrEmptyMessages indicates an OpenAI request missing usable messages/input.
	ErrEmptyMessages = errors.New("grok: messages/input produced no content")
)

// Wire constants for Grok CLI / Grok Build (cli-chat-proxy.grok.com).
// Source of truth: 9router open-sse grok-cli executor + registry.
const (
	// GrokCLIBaseURL is the Grok CLI inference API base (OpenAI Responses API).
	GrokCLIBaseURL = "https://cli-chat-proxy.grok.com/v1"
	// GrokCLIResponsesPath is the Responses API endpoint path.
	GrokCLIResponsesPath = "/responses"
	// GrokCLIVersion mirrors the official grok CLI client version.
	GrokCLIVersion = "0.2.99"
	// GrokCLIClientIdentifier is the x-grok-client-identifier value.
	GrokCLIClientIdentifier = "grok-shell"
	// GrokCLIUserAgent mirrors the official grok CLI User-Agent.
	GrokCLIUserAgent = "grok-shell/" + GrokCLIVersion + " (linux; x86_64)"
	// GrokCLITokenAuth is the x-xai-token-auth value for grok CLI OAuth.
	GrokCLITokenAuth = "xai-grok-cli"
	// grokCLIDefaultModel is the base Grok Build model id.
	grokCLIDefaultModel = "grok-build"
)

// SupportedModels returns the public model ids this adapter routes, in a stable
// order. It is the single source of truth for model discovery (the dashboard
// "Add Route" probe), since the model set is curated/static.
func SupportedModels() []string {
	return []string{
		"grok-build",
		"grok-4.5",
		"grok-4.5-high",
		"grok-4.5-medium",
		"grok-4.5-low",
	}
}

// modelUpstreamID maps a public model id to the upstream (grok) model id.
// Effort-suffixed variants collapse onto the base grok-4.5 model.
var modelUpstreamID = map[string]string{
	"grok-build":      "grok-build",
	"grok-4.5":        "grok-4.5",
	"grok-4.5-high":   "grok-4.5",
	"grok-4.5-medium": "grok-4.5",
	"grok-4.5-low":    "grok-4.5",
}

// SupportsModel reports whether m is a recognized public model id.
func SupportsModel(m string) bool {
	_, ok := modelUpstreamID[m]
	return ok
}

// effortLevels are the reasoning-effort suffixes recognized on grok-4.5 models.
var effortLevels = []string{"low", "medium", "high", "xhigh"}

// resolveModelPlan describes how a requested public model maps to the grok wire
// request: the upstream model id and the reasoning effort (if any).
type resolveModelPlan struct {
	UpstreamModel  string // model id sent to grok
	Effort         string // reasoning effort ("" = none/default)
	SupportsEffort bool   // whether grok accepts a reasoning.effort for this model
}

// resolveModel maps a public model id (or an explicit UpstreamModel override) to a
// wire plan. Unknown ids are passed through verbatim so custom/future grok models
// still route.
func resolveModel(publicModel string) resolveModelPlan {
	model := strings.TrimSpace(publicModel)
	if model == "" {
		model = grokCLIDefaultModel
	}

	// Detect a trailing effort suffix (e.g. grok-4.5-high).
	effort := ""
	base := model
	for _, lvl := range effortLevels {
		if strings.HasSuffix(model, "-"+lvl) {
			effort = lvl
			base = strings.TrimSuffix(model, "-"+lvl)
			break
		}
	}

	upstream := base
	if mapped, ok := modelUpstreamID[model]; ok {
		upstream = mapped
	} else if mapped, ok := modelUpstreamID[base]; ok {
		upstream = mapped
	}

	return resolveModelPlan{
		UpstreamModel:  upstream,
		Effort:         effort,
		SupportsEffort: supportsReasoningEffort(upstream),
	}
}

// supportsReasoningEffort reports whether the model accepts a reasoning.effort
// field (grok-4.5 family). Mirrors 9router supportsGrokCliReasoningEffort.
func supportsReasoningEffort(model string) bool {
	return strings.HasPrefix(model, "grok-4.5")
}

// normalizeEffort clamps an effort value to a recognized level, defaulting high.
func normalizeEffort(v string) string {
	e := strings.ToLower(strings.TrimSpace(v))
	if e == "max" {
		return "xhigh"
	}
	for _, lvl := range effortLevels {
		if e == lvl {
			return e
		}
	}
	return "high"
}

// responsesInputItem is one item of the Responses API `input` array.
type responsesInputItem struct {
	Type    string                  `json:"type"`
	Role    string                  `json:"role"`
	Content []responsesContentBlock `json:"content"`
}

type responsesContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesReasoning struct {
	Summary string `json:"summary"`
	Effort  string `json:"effort,omitempty"`
}

// grokResponsesRequest is the Responses API body Grok CLI expects.
type grokResponsesRequest struct {
	Model     string               `json:"model"`
	Input     []responsesInputItem `json:"input"`
	Stream    bool                 `json:"stream"`
	Store     bool                 `json:"store"`
	Reasoning responsesReasoning   `json:"reasoning"`
	Include   []string             `json:"include,omitempty"`
}

// messagesToResponsesInput converts an OpenAI chat `messages` array into the
// Responses API `input` array. Assistant turns use output_text; other roles use
// input_text (matching the Responses schema).
func messagesToResponsesInput(messages gjson.Result) []responsesInputItem {
	var items []responsesInputItem
	messages.ForEach(func(_, msg gjson.Result) bool {
		role := msg.Get("role").String()
		if role == "" {
			role = "user"
		}
		if role == "developer" {
			role = "system"
		}

		text := extractMessageText(msg.Get("content"))
		if strings.TrimSpace(text) == "" {
			return true
		}

		blockType := "input_text"
		if role == "assistant" {
			blockType = "output_text"
		}
		items = append(items, responsesInputItem{
			Type: "message",
			Role: role,
			Content: []responsesContentBlock{
				{Type: blockType, Text: text},
			},
		})
		return true
	})
	return items
}

// extractMessageText flattens a chat message content (string or content-part array).
func extractMessageText(content gjson.Result) string {
	switch {
	case content.Type == gjson.String:
		return content.String()
	case content.IsArray():
		var parts []string
		content.ForEach(func(_, part gjson.Result) bool {
			t := part.Get("type").String()
			if t == "text" || t == "input_text" || t == "output_text" {
				if s := part.Get("text").String(); s != "" {
					parts = append(parts, s)
				}
			}
			return true
		})
		return strings.Join(parts, " ")
	}
	return ""
}

// TranslateOpenAIToGrokCLI converts an OpenAI chat/completions (or Responses) request
// body into the Grok CLI Responses API payload. It returns the payload bytes plus the
// resolved model plan so the caller can set the correct upstream model + headers.
func TranslateOpenAIToGrokCLI(openaiBody []byte, upstreamModel string) ([]byte, resolveModelPlan, error) {
	if len(openaiBody) == 0 || !gjson.ValidBytes(openaiBody) {
		return nil, resolveModelPlan{}, ErrMalformedJSON
	}
	root := gjson.ParseBytes(openaiBody)

	// The public model requested by the client (before mapping). Prefer the target's
	// resolved upstream model when provided; else the body's model field.
	requested := upstreamModel
	if requested == "" {
		requested = root.Get("model").String()
	}
	plan := resolveModel(requested)

	// Build the Responses input array from chat messages, or from an existing
	// Responses `input` if the client already speaks Responses.
	var input []responsesInputItem
	if msgs := root.Get("messages"); msgs.IsArray() && len(msgs.Array()) > 0 {
		input = messagesToResponsesInput(msgs)
	} else if in := root.Get("input"); in.Type == gjson.String {
		if txt := in.String(); strings.TrimSpace(txt) != "" {
			input = []responsesInputItem{{Type: "message", Role: "user", Content: []responsesContentBlock{{Type: "input_text", Text: txt}}}}
		}
	}
	if len(input) == 0 {
		return nil, resolveModelPlan{}, ErrEmptyMessages
	}

	reasoning := responsesReasoning{Summary: "concise"}
	var include []string
	if plan.SupportsEffort {
		// Reasoning effort priority: explicit reasoning_effort > model suffix > default high.
		effort := root.Get("reasoning_effort").String()
		if effort == "" {
			effort = plan.Effort
		}
		reasoning.Effort = normalizeEffort(effort)
		// Encrypted reasoning continuity (CLI always requests this when effort != none).
		include = []string{"reasoning.encrypted_content"}
	}

	reqBody := grokResponsesRequest{
		Model:     plan.UpstreamModel,
		Input:     input,
		Stream:    true,
		Store:     false,
		Reasoning: reasoning,
		Include:   include,
	}

	out, err := json.Marshal(reqBody)
	if err != nil {
		return nil, resolveModelPlan{}, fmt.Errorf("grok: marshal responses payload: %w", err)
	}
	return out, plan, nil
}

// grokEvent is a normalized Responses-API SSE event.
type grokEvent struct {
	Delta     string // assistant text delta (response.output_text.delta)
	Reasoning string // reasoning summary delta (response.reasoning_summary_text.delta)
	Error     string // upstream error message
	Done      bool   // terminal marker (response.completed / response.done / response.failed)
	Usage     *usageInfo
}

type usageInfo struct {
	Prompt     int
	Completion int
	Total      int
}

// parseResponsesEvent decodes a single Responses-API SSE data line into a normalized
// event. The eventType is taken from the SSE "event:" line if present, else from the
// JSON "type" field. Returns ok=false for events we ignore.
func parseResponsesEvent(eventType string, data []byte) (grokEvent, bool) {
	if !gjson.ValidBytes(data) {
		return grokEvent{}, false
	}
	root := gjson.ParseBytes(data)
	typ := eventType
	if typ == "" {
		typ = root.Get("type").String()
	}

	switch typ {
	case "response.output_text.delta":
		d := root.Get("delta").String()
		if d == "" {
			return grokEvent{}, false
		}
		return grokEvent{Delta: d}, true

	case "response.reasoning_summary_text.delta":
		d := root.Get("delta").String()
		if d == "" {
			return grokEvent{}, false
		}
		return grokEvent{Reasoning: d}, true

	case "response.completed", "response.done":
		ev := grokEvent{Done: true}
		if u := root.Get("response.usage"); u.Exists() {
			in := int(u.Get("input_tokens").Int())
			if in == 0 {
				in = int(u.Get("prompt_tokens").Int())
			}
			out := int(u.Get("output_tokens").Int())
			if out == 0 {
				out = int(u.Get("completion_tokens").Int())
			}
			ev.Usage = &usageInfo{Prompt: in, Completion: out, Total: in + out}
		}
		return ev, true

	case "error", "response.failed":
		msg := root.Get("error.message").String()
		if msg == "" {
			msg = root.Get("response.error.message").String()
		}
		if msg == "" {
			msg = root.Get("message").String()
		}
		if msg == "" {
			msg = "grok upstream error"
		}
		return grokEvent{Error: msg, Done: true}, true
	}

	return grokEvent{}, false
}

// --- OpenAI chat.completion(.chunk) builders ---------------------------------

type openAIChunkChoice struct {
	Index        int            `json:"index"`
	Delta        map[string]any `json:"delta"`
	FinishReason *string        `json:"finish_reason"`
	Logprobs     *string        `json:"logprobs"`
}

type openAIChunk struct {
	ID                string              `json:"id"`
	Object            string              `json:"object"`
	Created           int64               `json:"created"`
	Model             string              `json:"model"`
	SystemFingerprint *string             `json:"system_fingerprint"`
	Choices           []openAIChunkChoice `json:"choices"`
	Usage             *openAIUsage        `json:"usage,omitempty"`
}

func buildChunk(id, model string, created int64, delta map[string]any, finish string, usage *openAIUsage) []byte {
	choice := openAIChunkChoice{Index: 0, Delta: delta}
	if finish != "" {
		f := finish
		choice.FinishReason = &f
	}
	chunk := openAIChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: []openAIChunkChoice{choice},
		Usage:   usage,
	}
	out, _ := json.Marshal(chunk)
	return out
}

type openAIMessage struct {
	Role             string `json:"role"`
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

type openAICompletionChoice struct {
	Index        int           `json:"index"`
	Message      openAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
	Logprobs     *string       `json:"logprobs"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type openAICompletion struct {
	ID      string                   `json:"id"`
	Object  string                   `json:"object"`
	Created int64                    `json:"created"`
	Model   string                   `json:"model"`
	Choices []openAICompletionChoice `json:"choices"`
	Usage   openAIUsage              `json:"usage"`
}

func buildCompletion(id, model string, created int64, content, reasoning string, usage *usageInfo) []byte {
	u := openAIUsage{}
	if usage != nil {
		u = openAIUsage{PromptTokens: usage.Prompt, CompletionTokens: usage.Completion, TotalTokens: usage.Total}
	} else {
		// Rough estimate when upstream omits usage.
		u.PromptTokens = (len(content) + 3) / 4
		u.CompletionTokens = (len(content) + 3) / 4
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	}
	resp := openAICompletion{
		ID:      id,
		Object:  "chat.completion",
		Created: created,
		Model:   model,
		Choices: []openAICompletionChoice{{
			Index:        0,
			Message:      openAIMessage{Role: "assistant", Content: content, ReasoningContent: reasoning},
			FinishReason: "stop",
		}},
		Usage: u,
	}
	out, _ := json.Marshal(resp)
	return out
}

// --- id helpers ---------------------------------------------------------------

func newUUID() string { return idgen.UUIDv4() }

func shortID() string { return idgen.Short(6) }
