package anthropic

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// This file carries the reverse wire translation: an upstream that speaks the
// OpenAI dialect is relayed to a client that speaks Anthropic. Every function
// here is pure — no clocks beyond ID synthesis, no I/O, no shared state beyond
// the caller-owned stream state — so the whole surface is table-testable.

// anthropicTextBlock is a plain assistant text block.
type anthropicTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// anthropicToolUseBlock is a tool invocation the client is expected to answer
// with a tool_result block on the next turn.
type anthropicToolUseBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// anthropicResponseUsage mirrors the Anthropic usage object, including the
// cache counters Claude Code reads to render cost.
type anthropicResponseUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

// anthropicStartUsage is the usage object carried by message_start: only the
// prompt size is known at that point.
type anthropicStartUsage struct {
	InputTokens int `json:"input_tokens"`
}

// anthropicMessageResponse is the non-streaming Anthropic messages response.
type anthropicMessageResponse struct {
	ID           string                 `json:"id"`
	Type         string                 `json:"type"`
	Role         string                 `json:"role"`
	Content      json.RawMessage        `json:"content"`
	Model        string                 `json:"model"`
	StopReason   string                 `json:"stop_reason"`
	StopSequence *string                `json:"stop_sequence"`
	Usage        anthropicResponseUsage `json:"usage"`
}

// mapFinishReasonToAnthropic converts an OpenAI finish_reason into the
// Anthropic stop_reason vocabulary. An empty or unrecognised reason becomes
// end_turn: Anthropic clients switch on a closed set of values and an unknown
// string would leave them waiting for a stop that already happened.
func mapFinishReasonToAnthropic(reason string) string {
	switch reason {
	case "stop", "end_turn", "":
		return "end_turn"
	case "length", "max_tokens":
		return "max_tokens"
	case "tool_calls", "function_call", "tool_use":
		return "tool_use"
	case "content_filter":
		return "refusal"
	default:
		return "end_turn"
	}
}

// anthropicMessageID rewrites an OpenAI completion id into the msg_ namespace.
func anthropicMessageID(openaiID string) string {
	trimmed := strings.TrimPrefix(openaiID, "chatcmpl-")
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return fmt.Sprintf("msg_%d", time.Now().UnixNano())
	}
	return "msg_" + trimmed
}

// toolUseBlock converts one OpenAI tool_call object into an Anthropic tool_use
// content block. The arguments arrive as a JSON string and must be re-encoded
// as the object Anthropic clients expect; a malformed argument string is a
// broken upstream, not something to paper over with an empty object.
func toolUseBlock(tc gjson.Result, position int) (json.RawMessage, error) {
	id := tc.Get("id").String()
	if id == "" {
		id = fmt.Sprintf("toolu_%d_%d", time.Now().UnixNano(), position)
	}
	name := tc.Get("function.name").String()
	if name == "" {
		return nil, fmt.Errorf("tool_call %d: missing function.name", position)
	}

	rawArgs := tc.Get("function.arguments").String()
	input := json.RawMessage(`{}`)
	if strings.TrimSpace(rawArgs) != "" {
		var probe any
		if err := json.Unmarshal([]byte(rawArgs), &probe); err != nil {
			return nil, fmt.Errorf("tool_call %d (%s): invalid arguments json: %w", position, name, err)
		}
		input = json.RawMessage(rawArgs)
	}

	return json.Marshal(anthropicToolUseBlock{Type: "tool_use", ID: id, Name: name, Input: input})
}

// readOpenAIUsage accepts either the OpenAI prompt/completion spelling or the
// Anthropic input/output spelling, because several OpenAI-compatible gateways
// emit the latter. Cache counters are picked up from both shapes too.
func readOpenAIUsage(root gjson.Result) anthropicResponseUsage {
	u := root.Get("usage")
	if !u.Exists() {
		return anthropicResponseUsage{}
	}
	out := anthropicResponseUsage{
		InputTokens:  int(u.Get("prompt_tokens").Int()),
		OutputTokens: int(u.Get("completion_tokens").Int()),
	}
	if out.InputTokens == 0 {
		out.InputTokens = int(u.Get("input_tokens").Int())
	}
	if out.OutputTokens == 0 {
		out.OutputTokens = int(u.Get("output_tokens").Int())
	}
	out.CacheReadInputTokens = int(u.Get("prompt_tokens_details.cached_tokens").Int())
	if out.CacheReadInputTokens == 0 {
		out.CacheReadInputTokens = int(u.Get("cache_read_input_tokens").Int())
	}
	out.CacheCreationInputTokens = int(u.Get("completion_tokens_details.cache_creation_input_tokens").Int())
	if out.CacheCreationInputTokens == 0 {
		out.CacheCreationInputTokens = int(u.Get("cache_creation_input_tokens").Int())
	}
	return out
}

// TranslateOpenAIToAnthropicResponse converts an OpenAI chat completion
// response into an Anthropic messages response. Assistant text becomes a text
// block, each tool_call becomes a tool_use block whose arguments are decoded
// from their JSON string, and finish_reason is mapped onto stop_reason.
//
// The Anthropic messages API returns exactly one message, so a provider that
// honoured n>1 is truncated to its first choice — the only one an Anthropic
// client can render.
func TranslateOpenAIToAnthropicResponse(body []byte, publicModel string) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return nil, ErrMalformedJSON
	}
	root := gjson.ParseBytes(body)

	choices := root.Get("choices").Array()
	if len(choices) == 0 {
		return nil, errors.New("openai response has no choices")
	}
	msg := choices[0].Get("message")

	blocks := make([]json.RawMessage, 0, 2)
	if text := msg.Get("content").String(); strings.TrimSpace(text) != "" {
		b, err := json.Marshal(anthropicTextBlock{Type: "text", Text: text})
		if err != nil {
			return nil, fmt.Errorf("marshal text block: %w", err)
		}
		blocks = append(blocks, b)
	}
	for i, tc := range msg.Get("tool_calls").Array() {
		block, err := toolUseBlock(tc, i)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}
	if len(blocks) == 0 {
		// Anthropic requires at least one content block; an empty text block
		// keeps the response schema valid for a provider that returned none.
		empty, err := json.Marshal(anthropicTextBlock{Type: "text", Text: ""})
		if err != nil {
			return nil, fmt.Errorf("marshal empty text block: %w", err)
		}
		blocks = append(blocks, empty)
	}

	content, err := json.Marshal(blocks)
	if err != nil {
		return nil, fmt.Errorf("marshal content blocks: %w", err)
	}

	model := publicModel
	if model == "" {
		model = root.Get("model").String()
	}

	resp := anthropicMessageResponse{
		ID:           anthropicMessageID(root.Get("id").String()),
		Type:         "message",
		Role:         "assistant",
		Content:      content,
		Model:        model,
		StopReason:   mapFinishReasonToAnthropic(choices[0].Get("finish_reason").String()),
		StopSequence: nil,
		Usage:        readOpenAIUsage(root),
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic response: %w", err)
	}
	return out, nil
}

// AnthropicStreamEvent is one Anthropic SSE event: the name that follows the
// `event:` line plus the JSON payload of the `data:` line.
type AnthropicStreamEvent struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// AnthropicStreamState carries the bookkeeping a chunk-by-chunk translation
// needs across calls. It is owned by the caller and must never be shared
// between concurrent streams.
type AnthropicStreamState struct {
	// PublicModel is the model name the client asked for; it is echoed on
	// every event so the client never sees the upstream's private name.
	PublicModel string
	// MessageID is the Anthropic message id once known.
	MessageID string
	// InputTokens is the prompt size reported so far. Callers may pre-seed it
	// with their own estimate so message_start carries a real number even when
	// the upstream only reports usage on its closing chunk.
	InputTokens int
	// OutputTokens is the completion size reported so far.
	OutputTokens int

	started      bool
	nextIndex    int
	textOpen     bool
	textIndex    int
	toolBlocks   map[int]int
	openBlocks   []int
	stopReason   string
	finishSeen   bool
	usageSeen    bool
	deltaEmitted bool
	stopped      bool
}

// NewAnthropicStreamState creates the state for one translated stream.
func NewAnthropicStreamState(publicModel string, inputTokens int) *AnthropicStreamState {
	return &AnthropicStreamState{
		PublicModel: publicModel,
		InputTokens: inputTokens,
		toolBlocks:  make(map[int]int),
	}
}

func (st *AnthropicStreamState) event(name string, payload any) (AnthropicStreamEvent, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return AnthropicStreamEvent{}, fmt.Errorf("marshal %s: %w", name, err)
	}
	return AnthropicStreamEvent{Event: name, Data: data}, nil
}

// startEvent emits message_start. It is deferred until the first real payload
// so an upstream that reports usage on its opening chunk still lands a true
// input_tokens figure on the event clients read it from.
func (st *AnthropicStreamState) startEvent() (AnthropicStreamEvent, error) {
	if st.MessageID == "" {
		st.MessageID = fmt.Sprintf("msg_%d", time.Now().UnixNano())
	}
	payload := struct {
		Type    string `json:"type"`
		Message struct {
			ID           string              `json:"id"`
			Type         string              `json:"type"`
			Role         string              `json:"role"`
			Content      []json.RawMessage   `json:"content"`
			Model        string              `json:"model"`
			StopReason   *string             `json:"stop_reason"`
			StopSequence *string             `json:"stop_sequence"`
			Usage        anthropicStartUsage `json:"usage"`
		} `json:"message"`
	}{Type: "message_start"}
	payload.Message.ID = st.MessageID
	payload.Message.Type = "message"
	payload.Message.Role = "assistant"
	payload.Message.Content = []json.RawMessage{}
	payload.Message.Model = st.PublicModel
	// message_start reports the prompt size only; the completion size is not
	// known yet and belongs on message_delta.
	payload.Message.Usage = anthropicStartUsage{InputTokens: st.InputTokens}
	st.started = true
	return st.event("message_start", payload)
}

// closeOpenBlocks emits content_block_stop for every block still open, in the
// order they were opened. Anthropic clients assume blocks are sequential, so a
// new block never opens while another is streaming.
func (st *AnthropicStreamState) closeOpenBlocks() []AnthropicStreamEvent {
	if len(st.openBlocks) == 0 {
		return nil
	}
	events := make([]AnthropicStreamEvent, 0, len(st.openBlocks))
	for _, idx := range st.openBlocks {
		ev, err := st.event("content_block_stop", struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
		}{Type: "content_block_stop", Index: idx})
		if err != nil {
			continue
		}
		events = append(events, ev)
	}
	st.openBlocks = nil
	st.textOpen = false
	return events
}

func (st *AnthropicStreamState) openTextBlock() []AnthropicStreamEvent {
	st.textIndex = st.nextIndex
	st.nextIndex++
	st.textOpen = true
	st.openBlocks = append(st.openBlocks, st.textIndex)
	payload := struct {
		Type         string             `json:"type"`
		Index        int                `json:"index"`
		ContentBlock anthropicTextBlock `json:"content_block"`
	}{Type: "content_block_start", Index: st.textIndex, ContentBlock: anthropicTextBlock{Type: "text", Text: ""}}
	ev, err := st.event("content_block_start", payload)
	if err != nil {
		return nil
	}
	return []AnthropicStreamEvent{ev}
}

func (st *AnthropicStreamState) textDelta(text string) []AnthropicStreamEvent {
	payload := struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}{Type: "content_block_delta", Index: st.textIndex}
	payload.Delta.Type = "text_delta"
	payload.Delta.Text = text
	ev, err := st.event("content_block_delta", payload)
	if err != nil {
		return nil
	}
	return []AnthropicStreamEvent{ev}
}

// toolCallEvents translates one OpenAI tool_calls delta entry. The first
// fragment of a call carries id and name and opens a tool_use block; every
// fragment's arguments become an input_json_delta.
func (st *AnthropicStreamState) toolCallEvents(tc gjson.Result) []AnthropicStreamEvent {
	idx := int(tc.Get("index").Int())
	if idx < 0 {
		idx = 0
	}

	var events []AnthropicStreamEvent
	blockIdx, open := st.toolBlocks[idx]
	if !open {
		events = append(events, st.closeOpenBlocks()...)

		id := tc.Get("id").String()
		if id == "" {
			id = fmt.Sprintf("toolu_%d", time.Now().UnixNano())
		}
		blockIdx = st.nextIndex
		st.nextIndex++
		st.toolBlocks[idx] = blockIdx
		st.openBlocks = append(st.openBlocks, blockIdx)

		payload := struct {
			Type         string                `json:"type"`
			Index        int                   `json:"index"`
			ContentBlock anthropicToolUseBlock `json:"content_block"`
		}{
			Type:         "content_block_start",
			Index:        blockIdx,
			ContentBlock: anthropicToolUseBlock{Type: "tool_use", ID: id, Name: tc.Get("function.name").String(), Input: json.RawMessage(`{}`)},
		}
		ev, err := st.event("content_block_start", payload)
		if err == nil {
			events = append(events, ev)
		}
	}

	if args := tc.Get("function.arguments").String(); args != "" {
		payload := struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
			Delta struct {
				Type        string `json:"type"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}{Type: "content_block_delta", Index: blockIdx}
		payload.Delta.Type = "input_json_delta"
		payload.Delta.PartialJSON = args
		ev, err := st.event("content_block_delta", payload)
		if err == nil {
			events = append(events, ev)
		}
	}
	return events
}

func (st *AnthropicStreamState) messageDeltaEvent() []AnthropicStreamEvent {
	reason := st.stopReason
	if reason == "" {
		reason = "end_turn"
	}
	payload := struct {
		Type  string `json:"type"`
		Delta struct {
			StopReason   string  `json:"stop_reason"`
			StopSequence *string `json:"stop_sequence"`
		} `json:"delta"`
		Usage anthropicResponseUsage `json:"usage"`
	}{Type: "message_delta"}
	payload.Delta.StopReason = reason
	payload.Usage = anthropicResponseUsage{InputTokens: st.InputTokens, OutputTokens: st.OutputTokens}
	ev, err := st.event("message_delta", payload)
	if err != nil {
		return nil
	}
	return []AnthropicStreamEvent{ev}
}

// TranslateOpenAIChunkToAnthropic converts one OpenAI SSE chunk into the
// Anthropic events it implies. A single chunk can yield several events (a
// tool-call fragment opens a block and streams arguments), and the returned
// slice is always in wire order. message_stop is never emitted here: the
// caller flushes it with FlushAnthropicStream once the upstream stream ends.
func TranslateOpenAIChunkToAnthropic(chunk []byte, st *AnthropicStreamState) ([]AnthropicStreamEvent, error) {
	if st == nil {
		return nil, errors.New("nil anthropic stream state")
	}
	if len(chunk) == 0 || !gjson.ValidBytes(chunk) {
		return nil, ErrMalformedJSON
	}
	root := gjson.ParseBytes(chunk)

	if id := root.Get("id").String(); id != "" && st.MessageID == "" {
		st.MessageID = anthropicMessageID(id)
	}
	if u := root.Get("usage"); u.Exists() {
		st.usageSeen = true
		usage := readOpenAIUsage(root)
		if usage.InputTokens > 0 {
			st.InputTokens = usage.InputTokens
		}
		if usage.OutputTokens > 0 {
			st.OutputTokens = usage.OutputTokens
		}
	}

	var events []AnthropicStreamEvent
	if !st.started && (root.Get("choices").Exists() || st.usageSeen) {
		ev, err := st.startEvent()
		if err != nil {
			return nil, err
		}
		events = append(events, ev)
	}

	for _, choice := range root.Get("choices").Array() {
		delta := choice.Get("delta")
		if text := delta.Get("content").String(); text != "" {
			// A text block that is already streaming stays open across deltas;
			// only a block belonging to something else (a tool_use) is closed
			// first, and text resuming afterwards opens a fresh block.
			if !st.textOpen {
				events = append(events, st.closeOpenBlocks()...)
				events = append(events, st.openTextBlock()...)
			}
			events = append(events, st.textDelta(text)...)
		}
		for _, tc := range delta.Get("tool_calls").Array() {
			events = append(events, st.toolCallEvents(tc)...)
		}
		if fr := choice.Get("finish_reason"); fr.Exists() && fr.String() != "" {
			st.stopReason = mapFinishReasonToAnthropic(fr.String())
			st.finishSeen = true
		}
	}

	// message_delta waits for the usage chunk: OpenAI reports completion tokens
	// on the final chunk, after the one carrying finish_reason, so emitting
	// early would report zero output tokens.
	if st.finishSeen && !st.deltaEmitted && st.usageSeen {
		events = append(events, st.closeOpenBlocks()...)
		events = append(events, st.messageDeltaEvent()...)
		st.deltaEmitted = true
	}
	return events, nil
}

// FlushAnthropicStream closes a stream that ended without a finish_reason or
// without a usage chunk. It is idempotent and always terminates the event
// sequence with message_stop.
func FlushAnthropicStream(st *AnthropicStreamState) []AnthropicStreamEvent {
	if st == nil || st.stopped {
		return nil
	}
	var events []AnthropicStreamEvent
	if !st.started {
		ev, err := st.startEvent()
		if err != nil {
			return nil
		}
		events = append(events, ev)
	}
	events = append(events, st.closeOpenBlocks()...)
	if !st.deltaEmitted {
		events = append(events, st.messageDeltaEvent()...)
		st.deltaEmitted = true
	}
	ev, err := st.event("message_stop", struct {
		Type string `json:"type"`
	}{Type: "message_stop"})
	if err != nil {
		return events
	}
	events = append(events, ev)
	st.stopped = true
	return events
}

// FormatAnthropicSSE renders events as the SSE frames Anthropic clients parse:
// an `event:` line, a `data:` line, and the blank separator.
func FormatAnthropicSSE(events []AnthropicStreamEvent) []byte {
	var sb strings.Builder
	for _, ev := range events {
		sb.WriteString("event: ")
		sb.WriteString(ev.Event)
		sb.WriteString("\ndata: ")
		sb.Write(ev.Data)
		sb.WriteString("\n\n")
	}
	return []byte(sb.String())
}

// anthropicErrorType maps an OpenAI error vocabulary entry, or a bare HTTP
// status, onto the Anthropic error type clients branch on.
func anthropicErrorType(openaiType string, status int) string {
	switch openaiType {
	case "invalid_request_error":
		return "invalid_request_error"
	case "authentication_error":
		return "authentication_error"
	case "permission_error":
		return "permission_error"
	case "not_found_error":
		return "not_found_error"
	case "rate_limit_exceeded":
		return "rate_limit_error"
	case "overloaded_error":
		return "overloaded_error"
	case "api_error", "server_error":
		return "api_error"
	}
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return "overloaded_error"
	default:
		return "api_error"
	}
}

// anthropicErrorEnvelope is the Anthropic error wire shape.
type anthropicErrorEnvelope struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// TranslateOpenAIErrorToAnthropic converts an OpenAI error envelope — or a
// bare status with a plain-text body — into the Anthropic error envelope. The
// upstream message is preserved verbatim; only the type vocabulary changes.
func TranslateOpenAIErrorToAnthropic(status int, body []byte) []byte {
	msg := ""
	openaiType := ""
	if len(body) > 0 && gjson.ValidBytes(body) {
		root := gjson.ParseBytes(body)
		msg = root.Get("error.message").String()
		openaiType = root.Get("error.type").String()
		if msg == "" {
			msg = root.Get("message").String()
		}
	}
	if strings.TrimSpace(msg) == "" {
		msg = strings.TrimSpace(string(body))
	}
	if strings.TrimSpace(msg) == "" {
		msg = http.StatusText(status)
	}
	if strings.TrimSpace(msg) == "" {
		msg = "upstream request failed"
	}

	var env anthropicErrorEnvelope
	env.Type = "error"
	env.Error.Type = anthropicErrorType(openaiType, status)
	env.Error.Message = msg
	out, err := json.Marshal(env)
	if err != nil {
		return []byte(`{"type":"error","error":{"type":"api_error","message":"upstream request failed"}}`)
	}
	return out
}

// AnthropicErrorSSE renders an error as an Anthropic `error` SSE event, for
// streams that fail after the response headers are already committed.
func AnthropicErrorSSE(status int, body []byte) []byte {
	var sb strings.Builder
	sb.WriteString("event: error\ndata: ")
	sb.Write(TranslateOpenAIErrorToAnthropic(status, body))
	sb.WriteString("\n\n")
	return []byte(sb.String())
}

// WriteAnthropicError writes the Anthropic error envelope with the given
// status. It is the single place the /v1/messages surface reports failures.
func WriteAnthropicError(w http.ResponseWriter, status int, body []byte) {
	if status <= 0 {
		status = http.StatusBadGateway
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(TranslateOpenAIErrorToAnthropic(status, body))
}
