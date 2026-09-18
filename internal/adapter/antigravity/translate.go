package antigravity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

var (
	// ErrMalformedJSON indicates an empty or syntactically invalid JSON payload.
	ErrMalformedJSON = errors.New("antigravity: invalid json payload")
	// ErrEmptyMessages indicates an OpenAI request missing messages.
	ErrEmptyMessages = errors.New("antigravity: messages array is empty")
)

var (
	claudeBrandRegex   = regexp.MustCompile(`(?i)You are a Claude agent, built on Anthropic's Claude Agent SDK\.`)
	opencodeBrandRegex = regexp.MustCompile(`(?i)opencode`)
	funcSanitizeRegex  = regexp.MustCompile(`[^a-zA-Z0-9_.:\-]`)
)

const defaultDailyEndpoint = "https://daily-cloudcode-pa.googleapis.com"

// StreamState maintains state across streaming SSE chunks for Antigravity to OpenAI translation.
type StreamState struct {
	MessageID            string
	Model                string
	FunctionIndex        int
	GeminiToolCallCount  int
	PendingThoughtSig    string
	FirstChunkEmitted    bool
}

// GeminiPart represents one part of Gemini content.
type GeminiPart struct {
	Text             string          `json:"text,omitempty"`
	Thought          bool            `json:"thought,omitempty"`
	ThoughtSignature string          `json:"thoughtSignature,omitempty"`
	FunctionCall     *FunctionCall   `json:"functionCall,omitempty"`
	FunctionResponse *FunctionResponse `json:"functionResponse,omitempty"`
}

// FunctionCall describes an invocation of a tool function.
type FunctionCall struct {
	ID   string         `json:"id,omitempty"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

// FunctionResponse describes a tool function execution result.
type FunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

// GeminiContent represents one turn in Gemini contents array.
type GeminiContent struct {
	Role  string       `json:"role"`
	Parts []GeminiPart `json:"parts"`
}

// GeminiToolDeclaration describes a tool exposed to the model.
type GeminiToolDeclaration struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// GeminiToolGroup groups function declarations.
type GeminiToolGroup struct {
	FunctionDeclarations []GeminiToolDeclaration `json:"functionDeclarations"`
}

// GeminiGenerationConfig controls decoding parameters.
type GeminiGenerationConfig struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"topP,omitempty"`
	TopK            *int     `json:"topK,omitempty"`
	MaxOutputTokens *int     `json:"maxOutputTokens,omitempty"`
}

// CloudCodeRequest is the inner request field of the Antigravity envelope.
type CloudCodeRequest struct {
	Contents          []GeminiContent         `json:"contents"`
	SystemInstruction *GeminiContent          `json:"systemInstruction,omitempty"`
	GenerationConfig  *GeminiGenerationConfig `json:"generationConfig,omitempty"`
	Tools             []GeminiToolGroup       `json:"tools,omitempty"`
	SessionID         string                  `json:"sessionId,omitempty"`
}

// AntigravityEnvelope is the outer JSON envelope required by Google Cloud Code internal APIs.
type AntigravityEnvelope struct {
	Project     string           `json:"project"`
	Model       string           `json:"model"`
	UserAgent   string           `json:"userAgent"`
	RequestType string           `json:"requestType"`
	RequestID   string           `json:"requestId"`
	Request     CloudCodeRequest `json:"request"`
}

// SanitizeFunctionName ensures a function name matches Google Gemini's naming requirements:
// starts with [a-zA-Z_], followed by [a-zA-Z0-9_.:\-], max 64 characters.
func SanitizeFunctionName(name string) string {
	if name == "" {
		return "_unknown"
	}
	s := funcSanitizeRegex.ReplaceAllString(name, "_")
	if !((s[0] >= 'a' && s[0] <= 'z') || (s[0] >= 'A' && s[0] <= 'Z') || s[0] == '_') {
		s = "_" + s
	}
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

// BuildIdeRequestID generates the canonical Antigravity IDE agent request identifier:
// agent/<conversationId>/<timestamp>/<trajectoryId>/<step>
func BuildIdeRequestID(sessionID, model string, contentCount int) string {
	convUUID := uuidFromSeed("antigravity:conversation:" + sessionID)
	trajUUID := uuidFromSeed("antigravity:trajectory:" + sessionID + ":" + model)
	step := contentCount*2 - 1
	if step < 1 {
		step = 1
	}
	return fmt.Sprintf("agent/%s/%d/%s/%d", convUUID, time.Now().UnixMilli(), trajUUID, step)
}

func uuidFromSeed(seed string) string {
	h := sha256.Sum256([]byte(seed))
	b := h[:16]
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	s := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", s[0:8], s[8:12], s[12:16], s[16:20], s[20:32])
}

// GenerateProjectID generates a randomized Google Cloud companion project ID.
func GenerateProjectID() string {
	adjectives := []string{"useful", "bright", "swift", "calm", "bold"}
	nouns := []string{"fuze", "wave", "spark", "flow", "core"}
	adjIdx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(adjectives))))
	nounIdx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(nouns))))
	suffixBytes := make([]byte, 3)
	_, _ = rand.Read(suffixBytes)
	return fmt.Sprintf("%s-%s-%s", adjectives[adjIdx.Int64()], nouns[nounIdx.Int64()], hex.EncodeToString(suffixBytes)[:5])
}

func rewriteSystemPrompts(text string) string {
	// Strip Claude Agent SDK branding which triggers 429 quota exhaustion flags from Google backend
	text = claudeBrandRegex.ReplaceAllString(text, "")
	// Replace opencode references with antigravity
	text = opencodeBrandRegex.ReplaceAllStringFunc(text, func(m string) string {
		switch m {
		case "OpenCode":
			return "Antigravity"
		case "OPENCODE":
			return "ANTIGRAVITY"
		default:
			return "antigravity"
		}
	})
	return strings.TrimSpace(text)
}

// TranslateOpenAIToAntigravity converts an OpenAI /v1/chat/completions payload into the
// Google Cloud Code internal envelope structure.
func TranslateOpenAIToAntigravity(body []byte, upstreamModel, projectID string) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return nil, ErrMalformedJSON
	}

	model := upstreamModel
	if model == "" {
		model = gjson.GetBytes(body, "model").String()
	}
	if projectID == "" {
		projectID = GenerateProjectID()
	}

	msgs := gjson.GetBytes(body, "messages").Array()
	if len(msgs) == 0 {
		return nil, ErrEmptyMessages
	}

	var contents []GeminiContent
	var systemParts []GeminiPart

	for _, msg := range msgs {
		role := strings.ToLower(msg.Get("role").String())
		contentRaw := msg.Get("content")

		switch role {
		case "system":
			var text string
			if contentRaw.IsArray() {
				var sb strings.Builder
				for _, item := range contentRaw.Array() {
					if item.Get("type").String() == "text" {
						sb.WriteString(item.Get("text").String())
					}
				}
				text = sb.String()
			} else {
				text = contentRaw.String()
			}
			text = rewriteSystemPrompts(text)
			if text != "" {
				systemParts = append(systemParts, GeminiPart{Text: text})
			}

		case "user":
			var parts []GeminiPart
			if contentRaw.IsArray() {
				for _, item := range contentRaw.Array() {
					if item.Get("type").String() == "text" {
						parts = append(parts, GeminiPart{Text: item.Get("text").String()})
					}
				}
			} else if text := contentRaw.String(); text != "" {
				parts = append(parts, GeminiPart{Text: text})
			}
			if len(parts) > 0 {
				contents = append(contents, GeminiContent{Role: "user", Parts: parts})
			}

		case "assistant":
			var parts []GeminiPart
			// Check reasoning content / thinking
			if reasoning := msg.Get("reasoning_content").String(); reasoning != "" {
				parts = append(parts, GeminiPart{Text: reasoning, Thought: true})
			}
			if text := contentRaw.String(); text != "" {
				parts = append(parts, GeminiPart{Text: text})
			}
			// Check tool calls
			if toolCalls := msg.Get("tool_calls").Array(); len(toolCalls) > 0 {
				for _, tc := range toolCalls {
					fn := tc.Get("function")
					fnName := SanitizeFunctionName(fn.Get("name").String())
					argsMap := make(map[string]any)
					if argsStr := fn.Get("arguments").String(); argsStr != "" {
						_ = json.Unmarshal([]byte(argsStr), &argsMap)
					}
					parts = append(parts, GeminiPart{
						FunctionCall: &FunctionCall{
							ID:   tc.Get("id").String(),
							Name: fnName,
							Args: argsMap,
						},
					})
				}
			}
			if len(parts) > 0 {
				contents = append(contents, GeminiContent{Role: "model", Parts: parts})
			}

		case "tool":
			fnName := msg.Get("name").String()
			if fnName == "" {
				fnName = "tool_response"
			}
			respMap := map[string]any{"response": contentRaw.String()}
			contents = append(contents, GeminiContent{
				Role: "user",
				Parts: []GeminiPart{
					{
						FunctionResponse: &FunctionResponse{
							Name:     SanitizeFunctionName(fnName),
							Response: respMap,
						},
					},
				},
			})
		}
	}

	// Generation configuration
	var genConfig *GeminiGenerationConfig
	if temp := gjson.GetBytes(body, "temperature"); temp.Exists() {
		v := temp.Float()
		if genConfig == nil {
			genConfig = &GeminiGenerationConfig{}
		}
		genConfig.Temperature = &v
	}
	if topP := gjson.GetBytes(body, "top_p"); topP.Exists() {
		v := topP.Float()
		if genConfig == nil {
			genConfig = &GeminiGenerationConfig{}
		}
		genConfig.TopP = &v
	}
	if maxTok := gjson.GetBytes(body, "max_tokens"); maxTok.Exists() && maxTok.Int() > 0 {
		v := int(maxTok.Int())
		if v > 64000 {
			v = 64000
		}
		if genConfig == nil {
			genConfig = &GeminiGenerationConfig{}
		}
		genConfig.MaxOutputTokens = &v
	}

	// Tools
	var toolGroups []GeminiToolGroup
	if tools := gjson.GetBytes(body, "tools").Array(); len(tools) > 0 {
		var decls []GeminiToolDeclaration
		for _, tool := range tools {
			if tool.Get("type").String() == "function" {
				fn := tool.Get("function")
				name := SanitizeFunctionName(fn.Get("name").String())
				desc := fn.Get("description").String()
				var params map[string]any
				if p := fn.Get("parameters"); p.Exists() {
					_ = json.Unmarshal([]byte(p.Raw), &params)
				}
				decls = append(decls, GeminiToolDeclaration{
					Name:        name,
					Description: desc,
					Parameters:  params,
				})
			}
		}
		if len(decls) > 0 {
			toolGroups = append(toolGroups, GeminiToolGroup{FunctionDeclarations: decls})
		}
	}

	sessionID := fmt.Sprintf("%d", time.Now().UnixNano())
	reqID := BuildIdeRequestID(sessionID, model, len(contents))

	ccReq := CloudCodeRequest{
		Contents:         contents,
		GenerationConfig: genConfig,
		Tools:            toolGroups,
		SessionID:        sessionID,
	}
	if len(systemParts) > 0 {
		ccReq.SystemInstruction = &GeminiContent{
			Role:  "user",
			Parts: systemParts,
		}
	}

	envelope := AntigravityEnvelope{
		Project:     projectID,
		Model:       model,
		UserAgent:   "antigravity",
		RequestType: "agent",
		RequestID:   reqID,
		Request:     ccReq,
	}

	return json.Marshal(envelope)
}

// OpenAIChunkChoice is a single choice inside an SSE chunk response.
type OpenAIChunkChoice struct {
	Index        int               `json:"index"`
	Delta        map[string]any    `json:"delta"`
	FinishReason *string           `json:"finish_reason"`
}

// OpenAIChunk is an SSE chunk formatted for OpenAI API clients.
type OpenAIChunk struct {
	ID      string              `json:"id"`
	Object  string              `json:"object"`
	Created int64               `json:"created"`
	Model   string              `json:"model"`
	Choices []OpenAIChunkChoice `json:"choices"`
}

// TranslateAntigravityChunkToOpenAI translates a raw Google Cloud Code / Gemini SSE chunk
// into one or more OpenAI-compatible SSE chunk JSON objects (without the "data: " prefix).
// If the stream finishes, isDone is reported as true.
func TranslateAntigravityChunkToOpenAI(rawChunk []byte, publicModel string, state *StreamState) ([][]byte, bool, error) {
	if len(rawChunk) == 0 {
		return nil, false, nil
	}

	// Unpack outer "response" if present
	chunkJSON := rawChunk
	if resp := gjson.GetBytes(rawChunk, "response"); resp.Exists() {
		chunkJSON = []byte(resp.Raw)
	}

	candidates := gjson.GetBytes(chunkJSON, "candidates").Array()
	if len(candidates) == 0 {
		return nil, false, nil
	}

	if state.MessageID == "" {
		state.MessageID = fmt.Sprintf("chatcmpl-ag-%d", time.Now().UnixMilli())
	}
	if state.Model == "" {
		state.Model = publicModel
	}

	var results [][]byte
	now := time.Now().Unix()

	// Initial role chunk if first time
	if !state.FirstChunkEmitted {
		state.FirstChunkEmitted = true
		roleChunk := OpenAIChunk{
			ID:      state.MessageID,
			Object:  "chat.completion.chunk",
			Created: now,
			Model:   state.Model,
			Choices: []OpenAIChunkChoice{
				{
					Index:        0,
					Delta:        map[string]any{"role": "assistant"},
					FinishReason: nil,
				},
			},
		}
		if b, err := json.Marshal(roleChunk); err == nil {
			results = append(results, b)
		}
	}

	cand := candidates[0]
	parts := cand.Get("content.parts").Array()

	for _, part := range parts {
		isThought := part.Get("thought").Bool()
		text := part.Get("text").String()

		if text != "" {
			delta := make(map[string]any)
			if isThought {
				delta["reasoning_content"] = text
			} else {
				delta["content"] = text
			}
			chunk := OpenAIChunk{
				ID:      state.MessageID,
				Object:  "chat.completion.chunk",
				Created: now,
				Model:   state.Model,
				Choices: []OpenAIChunkChoice{
					{
						Index:        0,
						Delta:        delta,
						FinishReason: nil,
					},
				},
			}
			if b, err := json.Marshal(chunk); err == nil {
				results = append(results, b)
			}
		}

		if fc := part.Get("functionCall"); fc.Exists() {
			fnName := fc.Get("name").String()
			fnArgs := fc.Get("args").Raw
			if fnArgs == "" {
				fnArgs = "{}"
			}
			callID := fc.Get("id").String()
			if callID == "" {
				callID = fmt.Sprintf("call_%s_%d", fnName, state.FunctionIndex)
			}
			toolCall := map[string]any{
				"index": state.FunctionIndex,
				"id":    callID,
				"type":  "function",
				"function": map[string]any{
					"name":      fnName,
					"arguments": fnArgs,
				},
			}
			state.FunctionIndex++
			state.GeminiToolCallCount++

			chunk := OpenAIChunk{
				ID:      state.MessageID,
				Object:  "chat.completion.chunk",
				Created: now,
				Model:   state.Model,
				Choices: []OpenAIChunkChoice{
					{
						Index:        0,
						Delta:        map[string]any{"tool_calls": []any{toolCall}},
						FinishReason: nil,
					},
				},
			}
			if b, err := json.Marshal(chunk); err == nil {
				results = append(results, b)
			}
		}
	}

	// Check finish reason
	if fr := cand.Get("finishReason"); fr.Exists() {
		rawFR := fr.String()
		openAIFinish := "stop"
		switch rawFR {
		case "MAX_TOKENS":
			openAIFinish = "length"
		case "SAFETY", "RECITATION":
			openAIFinish = "content_filter"
		default:
			if state.GeminiToolCallCount > 0 {
				openAIFinish = "tool_calls"
			} else {
				openAIFinish = "stop"
			}
		}

		chunk := OpenAIChunk{
			ID:      state.MessageID,
			Object:  "chat.completion.chunk",
			Created: now,
			Model:   state.Model,
			Choices: []OpenAIChunkChoice{
				{
					Index:        0,
					Delta:        map[string]any{},
					FinishReason: &openAIFinish,
				},
			},
		}
		if b, err := json.Marshal(chunk); err == nil {
			results = append(results, b)
		}
		return results, true, nil
	}

	return results, false, nil
}

// TranslateAntigravityToOpenAI converts a non-streaming Google Cloud Code / Gemini JSON response
// to a standard OpenAI /v1/chat/completions response.
func TranslateAntigravityToOpenAI(rawBody []byte, publicModel string) ([]byte, error) {
	if len(rawBody) == 0 || !gjson.ValidBytes(rawBody) {
		return nil, ErrMalformedJSON
	}

	body := rawBody
	if resp := gjson.GetBytes(rawBody, "response"); resp.Exists() {
		body = []byte(resp.Raw)
	}

	candidates := gjson.GetBytes(body, "candidates").Array()
	if len(candidates) == 0 {
		return nil, errors.New("antigravity: empty candidates in response")
	}

	cand := candidates[0]
	parts := cand.Get("content.parts").Array()

	var textBuilder strings.Builder
	var toolCalls []map[string]any
	idx := 0

	for _, part := range parts {
		if text := part.Get("text").String(); text != "" {
			textBuilder.WriteString(text)
		}
		if fc := part.Get("functionCall"); fc.Exists() {
			fnName := fc.Get("name").String()
			fnArgs := fc.Get("args").Raw
			if fnArgs == "" {
				fnArgs = "{}"
			}
			callID := fc.Get("id").String()
			if callID == "" {
				callID = fmt.Sprintf("call_%s_%d", fnName, idx)
			}
			toolCalls = append(toolCalls, map[string]any{
				"id":   callID,
				"type": "function",
				"function": map[string]any{
					"name":      fnName,
					"arguments": fnArgs,
				},
			})
			idx++
		}
	}

	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}
	if fr := cand.Get("finishReason"); fr.Exists() {
		switch fr.String() {
		case "MAX_TOKENS":
			finishReason = "length"
		case "SAFETY", "RECITATION":
			finishReason = "content_filter"
		}
	}

	msg := map[string]any{
		"role":    "assistant",
		"content": textBuilder.String(),
	}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}

	// Usage tokens
	promptTokens := gjson.GetBytes(body, "usageMetadata.promptTokenCount").Int()
	completionTokens := gjson.GetBytes(body, "usageMetadata.candidatesTokenCount").Int()
	totalTokens := gjson.GetBytes(body, "usageMetadata.totalTokenCount").Int()
	if totalTokens == 0 {
		totalTokens = promptTokens + completionTokens
	}

	resp := map[string]any{
		"id":      fmt.Sprintf("chatcmpl-ag-%d", time.Now().UnixMilli()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   publicModel,
		"choices": []map[string]any{
			{
				"index":         0,
				"message":       msg,
				"finish_reason": finishReason,
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      totalTokens,
		},
	}

	return json.Marshal(resp)
}
