package qoder

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// --- OpenAI chat.completion(.chunk) builders ---------------------------------

type openAIChunkChoice struct {
	Index        int            `json:"index"`
	Delta        map[string]any `json:"delta"`
	FinishReason *string        `json:"finish_reason"`
}

type openAIChunk struct {
	ID      string              `json:"id"`
	Object  string              `json:"object"`
	Created int64               `json:"created"`
	Model   string              `json:"model"`
	Choices []openAIChunkChoice `json:"choices"`
	Usage   json.RawMessage     `json:"usage,omitempty"`
}

func buildChunk(id, model string, created int64, delta map[string]any, finish string, usage json.RawMessage) []byte {
	choice := openAIChunkChoice{Index: 0, Delta: delta}
	if finish != "" {
		f := finish
		choice.FinishReason = &f
	}
	out, _ := json.Marshal(openAIChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: []openAIChunkChoice{choice},
		Usage:   usage,
	})
	return out
}

func buildChunkRawUsage(id, model string, created int64, delta map[string]any, finish string, usage *json.RawMessage) []byte {
	var u json.RawMessage
	if usage != nil {
		u = *usage
	}
	return buildChunk(id, model, created, delta, finish, u)
}

// --- non-streaming aggregation ------------------------------------------------

// aggregateQoderStream consumes the Qoder envelope SSE stream and produces a
// single OpenAI chat.completion JSON body for non-streaming clients. Returns
// billingBlocked=true if the first frame is a billing/quota block.
func aggregateQoderStream(ctx context.Context, body io.Reader, publicModel string) (out []byte, billingBlocked bool, err error) {
	sc := newFrameScanner(body)
	var content strings.Builder
	var reasoning strings.Builder
	var usage json.RawMessage
	first := true

	for sc.scan() {
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		default:
		}
		status, inner := sc.frame()
		if first {
			first = false
			if status != 200 && isBillingBlock(inner) {
				return nil, true, nil
			}
		}
		if inner == "" || inner == "[DONE]" {
			continue
		}
		if !gjson.Valid(inner) {
			continue
		}
		p := gjson.Parse(inner)
		delta := p.Get("choices.0.delta")
		if s := delta.Get("content").String(); s != "" {
			content.WriteString(s)
		}
		if s := delta.Get("reasoning_content").String(); s != "" {
			reasoning.WriteString(s)
		}
		if u := p.Get("usage"); u.Exists() && u.IsObject() {
			usage = json.RawMessage(u.Raw)
		}
	}
	if serr := sc.err(); serr != nil {
		return nil, false, serr
	}

	return buildCompletion("chatcmpl-qoder-"+shortID(), publicModel, time.Now().Unix(),
		content.String(), reasoning.String(), usage), false, nil
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
}

type openAICompletion struct {
	ID      string                   `json:"id"`
	Object  string                   `json:"object"`
	Created int64                    `json:"created"`
	Model   string                   `json:"model"`
	Choices []openAICompletionChoice `json:"choices"`
	Usage   json.RawMessage          `json:"usage,omitempty"`
}

func buildCompletion(id, model string, created int64, content, reasoning string, usage json.RawMessage) []byte {
	if usage == nil {
		est := (len(content) + 3) / 4
		usage = json.RawMessage(`{"prompt_tokens":` + itoa(est) + `,"completion_tokens":` + itoa(est) + `,"total_tokens":` + itoa(est*2) + `}`)
	}
	out, _ := json.Marshal(openAICompletion{
		ID:      id,
		Object:  "chat.completion",
		Created: created,
		Model:   model,
		Choices: []openAICompletionChoice{{
			Index:        0,
			Message:      openAIMessage{Role: "assistant", Content: content, ReasoningContent: reasoning},
			FinishReason: "stop",
		}},
		Usage: usage,
	})
	return out
}

// frameScanner reads Qoder envelope SSE frames from a reader.
type frameScanner struct {
	sc      *bufio.Scanner
	status  int
	inner   string
	scanErr error
}

func newFrameScanner(r io.Reader) *frameScanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 32*1024), 8<<20)
	return &frameScanner{sc: sc}
}

func (f *frameScanner) scan() bool {
	for {
		if !f.sc.Scan() {
			f.scanErr = f.sc.Err()
			return false
		}
		line := f.sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || !strings.HasPrefix(trimmed, "data:") {
			continue
		}
		data := strings.TrimSpace(trimmed[len("data:"):])
		if data == "[DONE]" {
			f.status, f.inner = 200, "[DONE]"
			return true
		}
		if !gjson.Valid(data) {
			continue
		}
		env := gjson.Parse(data)
		f.status = 200
		if s := env.Get("statusCodeValue"); s.Exists() {
			f.status = int(s.Int())
		}
		f.inner = env.Get("body").String()
		return true
	}
}

func (f *frameScanner) frame() (int, string) { return f.status, f.inner }
func (f *frameScanner) err() error           { return f.scanErr }
