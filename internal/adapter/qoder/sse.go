package qoder

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// Qoder wraps each SSE frame as:
//   data: {"statusCodeValue":200,"body":"<inner OpenAI chunk JSON or [DONE]>"}
// The inner body is an OpenAI streaming chunk. finish_reason often arrives on
// delta.finish_reason, and usage on a later choices:[] frame — downstream
// clients expect usage on the finish chunk, so we coalesce those.

var billingCodeRe = regexp.MustCompile(`"code"\s*:\s*"(112|10605)"`)

// isBillingBlock reports whether an inner Qoder body indicates quota/billing block.
func isBillingBlock(inner string) bool {
	if inner == "" {
		return false
	}
	if billingCodeRe.MatchString(inner) {
		return true
	}
	return strings.Contains(strings.ToLower(inner), "pricingurl")
}

type flusher interface{ Flush() }

// relayQoderStream reads the Qoder envelope SSE stream from body and relays it to
// w as OpenAI chat.completion.chunk SSE. Returns bytes written and any write error.
// billingBlocked is set true when the first frame is a billing/quota block, so the
// caller can classify it as a credential error (Layer 1) rather than success.
func relayQoderStream(ctx context.Context, w http.ResponseWriter, body io.Reader, publicModel string) (written int64, billingBlocked bool, err error) {
	reader := bufio.NewReaderSize(body, 32*1024)

	// Peek the first data frame to detect a billing block before committing headers.
	firstInner, firstStatus, consumed, peekErr := peekFirstFrame(reader)
	if peekErr != nil && peekErr != io.EOF {
		return 0, false, peekErr
	}
	if firstStatus != 200 && isBillingBlock(firstInner) {
		return 0, true, nil
	}

	f, _ := w.(flusher)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if f != nil {
		f.Flush()
	}

	co := &coalescer{
		w:       w,
		f:       f,
		model:   publicModel,
		id:      "chatcmpl-qoder-" + shortID(),
		created: time.Now().Unix(),
	}
	co.emitRole()

	// Process the peeked frame(s) first.
	for _, line := range consumed {
		if done := co.processLine(line); done {
			co.finish()
			return co.written, false, co.writeErr
		}
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 32*1024), 8<<20)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return co.written, false, ctx.Err()
		default:
		}
		if done := co.processLine(scanner.Text()); done {
			co.finish()
			return co.written, false, co.writeErr
		}
	}
	if serr := scanner.Err(); serr != nil && co.writeErr == nil {
		co.writeErr = serr
	}
	co.finish()
	return co.written, false, co.writeErr
}

// peekFirstFrame reads lines until the first `data:` frame with a parseable
// envelope, returning its inner body + status. All lines read are returned in
// `consumed` so the caller can re-process them (nothing is dropped).
func peekFirstFrame(r *bufio.Reader) (inner string, status int, consumed []string, err error) {
	status = 200
	for {
		line, readErr := r.ReadString('\n')
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed != "" {
			consumed = append(consumed, trimmed)
			if strings.HasPrefix(trimmed, "data:") {
				data := strings.TrimSpace(trimmed[len("data:"):])
				if data == "[DONE]" {
					return "", status, consumed, readErr
				}
				if gjson.Valid(data) {
					env := gjson.Parse(data)
					status = 200
					if s := env.Get("statusCodeValue"); s.Exists() {
						status = int(s.Int())
					}
					inner = env.Get("body").String()
					return inner, status, consumed, readErr
				}
				return "", status, consumed, readErr
			}
		}
		if readErr != nil {
			return "", status, consumed, readErr
		}
	}
}

// coalescer converts Qoder envelope frames into OpenAI SSE, holding empty
// finish + usage-only frames to emit a single include_usage-style terminal chunk.
type coalescer struct {
	w        http.ResponseWriter
	f        flusher
	model    string
	id       string
	created  int64
	written  int64
	writeErr error

	pendingFinish string
	pendingUsage  json.RawMessage
	finishFwd     bool
	done          bool
}

func (c *coalescer) writeRaw(s string) {
	if c.writeErr != nil {
		return
	}
	n, err := io.WriteString(c.w, s)
	c.written += int64(n)
	if err != nil {
		c.writeErr = err
		return
	}
	if c.f != nil {
		c.f.Flush()
	}
}

func (c *coalescer) emitRole() {
	c.writeRaw("data: " + string(buildChunk(c.id, c.model, c.created, map[string]any{"role": "assistant"}, "", nil)) + "\n\n")
}

// processLine handles one raw SSE line; returns true when the stream is terminal.
func (c *coalescer) processLine(line string) bool {
	if c.done {
		return true
	}
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || !strings.HasPrefix(trimmed, "data:") {
		return false
	}
	data := strings.TrimSpace(trimmed[len("data:"):])
	if data == "[DONE]" {
		return true
	}
	if !gjson.Valid(data) {
		return false
	}
	env := gjson.Parse(data)
	status := 200
	if s := env.Get("statusCodeValue"); s.Exists() {
		status = int(s.Int())
	}
	inner := env.Get("body").String()
	if status != 200 {
		msg := inner
		if msg == "" {
			msg = "upstream status error"
		}
		c.writeRaw("data: " + string(buildChunk(c.id, c.model, c.created,
			map[string]any{"content": "\n[qoder error " + itoa(status) + ": " + truncate(msg, 200) + "]"}, "stop", nil)) + "\n\n")
		c.done = true
		return true
	}
	if inner == "" {
		return false
	}
	return c.handleInner(inner)
}

// handleInner processes the inner OpenAI chunk JSON. Returns true when terminal.
func (c *coalescer) handleInner(inner string) bool {
	if inner == "[DONE]" {
		return true
	}
	if !gjson.Valid(inner) {
		// Forward raw non-JSON content defensively.
		c.writeRaw("data: " + strings.ReplaceAll(inner, "\n", "") + "\n\n")
		return false
	}
	p := gjson.Parse(inner)
	choice := p.Get("choices.0")
	finish := choice.Get("finish_reason").String()
	if finish == "" {
		finish = choice.Get("delta.finish_reason").String()
	}
	if finish == "" {
		finish = p.Get("finish_reason").String()
	}
	if u := p.Get("usage"); u.Exists() && u.IsObject() {
		c.pendingUsage = json.RawMessage(u.Raw)
	}

	if hasValuableDelta(choice.Get("delta")) {
		c.writeRaw("data: " + strings.ReplaceAll(inner, "\n", "") + "\n\n")
		if finish != "" {
			c.finishFwd = true
			if c.pendingUsage != nil {
				c.pendingFinish = finish
			} else {
				c.pendingFinish = ""
			}
		}
		if c.pendingFinish != "" && c.pendingUsage != nil {
			c.emitTerminal()
			return true
		}
		return false
	}

	if finish != "" {
		c.pendingFinish = finish
	}
	if (c.pendingFinish != "" || c.finishFwd) && c.pendingUsage != nil {
		if c.pendingFinish == "" {
			c.pendingFinish = "stop"
		}
		c.emitTerminal()
		return true
	}
	return false
}

// emitTerminal writes the coalesced finish+usage chunk.
func (c *coalescer) emitTerminal() {
	if c.pendingFinish == "" && c.pendingUsage == nil {
		return
	}
	finish := c.pendingFinish
	if finish == "" {
		finish = "stop"
	}
	var usage *json.RawMessage
	if c.pendingUsage != nil {
		usage = &c.pendingUsage
	}
	c.writeRaw("data: " + string(buildChunkRawUsage(c.id, c.model, c.created, map[string]any{}, finish, usage)) + "\n\n")
	c.pendingFinish = ""
	c.pendingUsage = nil
	c.done = true
}

// finish flushes any pending terminal chunk then [DONE].
func (c *coalescer) finish() {
	if !c.done && (c.pendingUsage != nil || (c.pendingFinish != "" && !c.finishFwd)) {
		c.emitTerminal()
	}
	if c.writeErr == nil {
		c.writeRaw("data: [DONE]\n\n")
	}
}

func hasValuableDelta(delta gjson.Result) bool {
	if !delta.Exists() || !delta.IsObject() {
		return false
	}
	if delta.Get("content").String() != "" {
		return true
	}
	if delta.Get("reasoning_content").String() != "" {
		return true
	}
	if tc := delta.Get("tool_calls"); tc.IsArray() && len(tc.Array()) > 0 {
		return true
	}
	if delta.Get("role").Exists() {
		return true
	}
	return false
}
