package server

import (
	"bytes"
	"errors"
	"net/http"
	"strings"

	"github.com/dickymuliafiqri/firefly/internal/adapter/anthropic"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/tidwall/gjson"
)

// anthropicIngress serves POST /v1/messages. The client speaks the Anthropic
// Messages API; the gateway translates the request into the OpenAI shape the
// shared pipeline and every adapter already understand, then translates the
// response back on the way out.
type anthropicIngress struct{}

func (anthropicIngress) Name() string { return "anthropic" }

// ParseBody validates an Anthropic messages request and returns the OpenAI
// chat-completion body the pipeline expects. model and max_tokens are the two
// fields the Anthropic API makes mandatory, so an absent one is a client error
// rather than something to default.
func (anthropicIngress) ParseBody(body []byte) ([]byte, string, bool, int, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return nil, "", false, 0, errors.New("invalid JSON body")
	}
	model := gjson.GetBytes(body, "model").String()
	if strings.TrimSpace(model) == "" {
		return nil, "", false, 0, errors.New("model: field required")
	}
	maxTokens := gjson.GetBytes(body, "max_tokens")
	if !maxTokens.Exists() || maxTokens.Int() <= 0 {
		return nil, "", false, 0, errors.New("max_tokens: field required")
	}

	translated, err := anthropic.TranslateAnthropicRequestToOpenAI(body)
	if err != nil {
		return nil, "", false, 0, err
	}
	return translated, model, gjson.GetBytes(body, "stream").Bool(),
		estimateInputTokens(translated), nil
}

func (anthropicIngress) WriteError(w http.ResponseWriter, status int, msg string) {
	anthropic.WriteAnthropicError(w, status, []byte(`{"error":{"message":"`+msg+`"}}`))
}

func (anthropicIngress) WriteResolveError(w http.ResponseWriter, err error) {
	status, msg := resolveErrorStatus(err)
	anthropic.WriteAnthropicError(w, status, []byte(`{"error":{"message":"`+msg+`"}}`))
}

// WrapWriter installs the response translator for this surface.
func (anthropicIngress) WrapWriter(w http.ResponseWriter, model string, stream bool) http.ResponseWriter {
	return &anthropicResponseWriter{
		ResponseWriter: w,
		model:          model,
		stream:         stream,
		state:          anthropic.NewAnthropicStreamState(model, 0),
	}
}

// resolveErrorStatus maps a routing failure onto (status, message) so either
// surface can render it in its own envelope.
func resolveErrorStatus(err error) (int, string) {
	if errors.Is(err, domain.ErrAllKeysExhausted) {
		return http.StatusTooManyRequests, "all upstream credentials exhausted; retry shortly"
	}
	var re *domain.ResolveError
	if errors.As(err, &re) {
		switch re.Kind {
		case domain.ResolveModelNotFound:
			return http.StatusNotFound, "the model '" + re.Model + "' does not exist or is not enabled"
		case domain.ResolveModelForbidden:
			return http.StatusForbidden, "tenant is not permitted to use model '" + re.Model + "'"
		case domain.ResolveTenantInvalid:
			return http.StatusUnauthorized, "unauthenticated"
		case domain.ResolveUpstreamUnavailable:
			return http.StatusServiceUnavailable, "no available upstream for model '" + re.Model + "' (upstream disabled or circuit open)"
		}
	}
	return http.StatusInternalServerError, err.Error()
}

// anthropicResponseWriter rewrites the upstream's OpenAI-shaped response into
// the Anthropic wire format before the client sees a byte.
//
// Non-streaming bodies are buffered whole and translated once the adapter
// returns, because a JSON document cannot be translated incrementally. A
// streaming body is translated frame by frame: the upstream emits
// "data: {...}\n\n" and this writer emits the Anthropic event sequence, so the client still
// sees tokens as they arrive.
type anthropicResponseWriter struct {
	http.ResponseWriter
	model  string
	stream bool
	state  *anthropic.AnthropicStreamState

	status      int
	header      http.Header
	buf         bytes.Buffer
	pending     []byte
	wroteHeader bool
	committed   bool
	failed      bool
}

func (a *anthropicResponseWriter) Header() http.Header {
	if a.header == nil {
		a.header = make(http.Header)
	}
	return a.header
}

func (a *anthropicResponseWriter) WriteHeader(status int) {
	if a.wroteHeader {
		return
	}
	a.status = status
	a.wroteHeader = true
}

func (a *anthropicResponseWriter) Write(p []byte) (int, error) {
	if !a.wroteHeader {
		a.WriteHeader(http.StatusOK)
	}
	if a.failed {
		// The upstream already failed; pass the remainder through untouched so
		// the client sees the provider's own error body.
		return a.ResponseWriter.Write(p)
	}
	if a.stream {
		return a.writeStreamChunk(p)
	}
	return a.buf.Write(p)
}

// Flush is called by the SSE relay after every event. For a stream it must
// forward the translated frames immediately, which is the whole point of
// streaming; for a buffered body there is nothing to flush yet.
func (a *anthropicResponseWriter) Flush() {
	if a.stream && !a.failed {
		if flusher, ok := a.ResponseWriter.(http.Flusher); ok {
			flusher.Flush()
		}
		return
	}
	if flusher, ok := a.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// writeStreamChunk translates one slice of upstream SSE bytes. Frames are
// accumulated until a blank line terminates them, so a chunk that splits a
// frame in half is held back rather than mis-parsed.
func (a *anthropicResponseWriter) writeStreamChunk(p []byte) (int, error) {
	a.pending = append(a.pending, p...)
	written := 0

	for {
		idx := bytes.Index(a.pending, []byte("\n\n"))
		if idx < 0 {
			break
		}
		frame := a.pending[:idx]
		a.pending = a.pending[idx+2:]

		payload, ok := sseDataPayload(frame)
		if !ok {
			// Comments, event-only lines and blank frames carry no payload.
			continue
		}
		if string(payload) == "[DONE]" {
			n, err := a.writeEvents(anthropic.FlushAnthropicStream(a.state))
			written += n
			if err != nil {
				return written, err
			}
			continue
		}

		events, err := anthropic.TranslateOpenAIChunkToAnthropic(payload, a.state)
		if err != nil {
			// A chunk we cannot parse is not worth killing the stream over:
			// skip it and keep relaying what follows.
			continue
		}
		n, err := a.writeEvents(events)
		written += n
		if err != nil {
			return written, err
		}
	}
	return len(p), nil
}

// sseDataPayload extracts the JSON payload of one SSE frame, ignoring the
// event:/id:/retry: lines that may precede it.
func sseDataPayload(frame []byte) ([]byte, bool) {
	for _, line := range bytes.Split(frame, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 {
			continue
		}
		return payload, true
	}
	return nil, false
}

// writeEvents emits translated Anthropic frames, committing the response
// headers on the first write.
func (a *anthropicResponseWriter) writeEvents(events []anthropic.AnthropicStreamEvent) (int, error) {
	if len(events) == 0 {
		return 0, nil
	}
	if !a.wroteHeader {
		a.WriteHeader(http.StatusOK)
	}
	if !a.committed {
		a.commitHeaders()
	}
	n, err := a.ResponseWriter.Write(anthropic.FormatAnthropicSSE(events))
	return n, err
}

// commitHeaders copies the buffered header set onto the real writer with the
// Anthropic content type, replacing whatever the OpenAI relay had staged.
func (a *anthropicResponseWriter) commitHeaders() {
	header := a.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	for k, vs := range header {
		for _, v := range vs {
			a.ResponseWriter.Header().Add(k, v)
		}
	}
	a.ResponseWriter.WriteHeader(a.status)
	a.committed = true
}

// finalize is called by the handler once the adapter returns. For a buffered
// body it translates the JSON and writes it; for a stream it makes sure the
// event sequence was terminated.
func (a *anthropicResponseWriter) finalize(fwdErr error) {
	if a.failed || fwdErr != nil {
		// The upstream failed before producing a usable body. Pass whatever it
		// did produce through untouched so the client sees the real error.
		if a.buf.Len() > 0 {
			if !a.committed {
				header := a.Header()
				if header.Get("Content-Type") == "" {
					header.Set("Content-Type", "application/json")
				}
				for k, vs := range header {
					for _, v := range vs {
						a.ResponseWriter.Header().Add(k, v)
					}
				}
				a.ResponseWriter.WriteHeader(a.status)
				a.committed = true
			}
			_, _ = a.ResponseWriter.Write(a.buf.Bytes())
		}
		return
	}

	if a.stream {
		if len(a.pending) > 0 {
			_, _ = a.writeStreamChunk(nil)
		}
		_, _ = a.writeEvents(anthropic.FlushAnthropicStream(a.state))
		return
	}

	body := a.buf.Bytes()
	if len(bytes.TrimSpace(body)) == 0 {
		return
	}
	if a.status != http.StatusOK {
		// A non-200 body is an error envelope from the upstream; translate the
		// vocabulary but keep the status.
		if !a.committed {
			header := a.Header()
			header.Set("Content-Type", "application/json")
			for k, vs := range header {
				for _, v := range vs {
					a.ResponseWriter.Header().Add(k, v)
				}
			}
			a.ResponseWriter.WriteHeader(a.status)
			a.committed = true
		}
		_, _ = a.ResponseWriter.Write(anthropic.TranslateOpenAIErrorToAnthropic(a.status, body))
		return
	}

	translated, err := anthropic.TranslateOpenAIToAnthropicResponse(body, a.model)
	if err != nil {
		// The upstream body is not a chat completion we can map. Fall back to
		// relaying it verbatim rather than inventing a response.
		if !a.committed {
			header := a.Header()
			if header.Get("Content-Type") == "" {
				header.Set("Content-Type", "application/json")
			}
			for k, vs := range header {
				for _, v := range vs {
					a.ResponseWriter.Header().Add(k, v)
				}
			}
			a.ResponseWriter.WriteHeader(a.status)
			a.committed = true
		}
		_, _ = a.ResponseWriter.Write(body)
		return
	}

	if !a.committed {
		header := a.Header()
		header.Set("Content-Type", "application/json")
		for k, vs := range header {
			for _, v := range vs {
				a.ResponseWriter.Header().Add(k, v)
			}
		}
		a.ResponseWriter.WriteHeader(a.status)
		a.committed = true
	}
	_, _ = a.ResponseWriter.Write(translated)
}
