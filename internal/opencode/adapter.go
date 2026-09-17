// Package opencode provides an upstream adapter for OpenCode (opencode.ai),
// supporting both OpenCode Free (Bearer public with client fingerprinting)
// and OpenCode Go (paid subscription with session isolation and dual-route
// Chat Completions vs Responses API dispatching).
package opencode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/httpx"
	"github.com/dickymuliafiqri/firefly/internal/openai"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/dickymuliafiqri/firefly/internal/upstream"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	DefaultFreeBaseURL = "https://opencode.ai/zen/v1"
	DefaultGoBaseURL   = "https://opencode.ai/zen/go/v1"
	OpenCodeUserAgent  = "opencode"
)

type flusher interface {
	Flush()
}

// ClientPool is the outbound client cache the adapter dials through.
type ClientPool interface {
	Client(u *domain.Upstream) *http.Client
}

// BreakerLookup yields a circuit breaker for an upstream name.
type BreakerLookup interface {
	Allow(name string) error
	Report(name string, ok bool)
}

// KeyMetricsObserver receives notifications of key requests and cooldown events.
type KeyMetricsObserver interface {
	ObserveKeyCooldown(upstream, keyRef string)
	ObserveKeyRequest(upstream, keyRef string, status int)
}

// Config tunes the OpenCode adapter.
type Config struct {
	SecretLookup     func(ref string) (string, bool)
	Retry            openai.RetryPolicy
	Logger           *slog.Logger
	MaxBufferedBytes int64
	Metrics          KeyMetricsObserver
	IdleTimeout      time.Duration
	Notifier         ports.KeyActionNotifier
}

// Adapter implements ports.UpstreamAdapter for the OpenCode protocol.
type Adapter struct {
	pool    ClientPool
	breaker BreakerLookup
	cfg     Config
	logger  *slog.Logger
	metrics KeyMetricsObserver
}

// NewAdapter constructs an OpenCode adapter.
func NewAdapter(pool ClientPool, breaker BreakerLookup, cfg Config) *Adapter {
	l := cfg.Logger
	if l == nil {
		l = slog.Default()
	}
	return &Adapter{
		pool:    pool,
		breaker: breaker,
		cfg:     cfg,
		logger:  l,
		metrics: cfg.Metrics,
	}
}

// Protocol identifies this adapter as speaking OpenCode wire protocol.
func (a *Adapter) Protocol() domain.Protocol {
	return domain.ProtocolOpenCode
}

func (a *Adapter) maxBytes() int64 {
	if a.cfg.MaxBufferedBytes > 0 {
		return a.cfg.MaxBufferedBytes
	}
	return 32 << 20 // 32 MB default
}

type attemptResult struct {
	status   int
	headers  http.Header
	streamed bool
	body     []byte
	err      error
}

// Forward sends req to the OpenCode upstream and relays the response to w.
func (a *Adapter) Forward(ctx context.Context, t *domain.Target, req ports.ForwardRequest, w io.Writer) error {
	if t == nil || t.Upstream == nil {
		return errors.New("opencode adapter: nil target")
	}
	respW, ok := w.(http.ResponseWriter)
	if !ok {
		return errors.New("opencode adapter: writer is not an http.ResponseWriter")
	}
	u := t.Upstream

	bodyBytes := req.BodyBytes
	if len(bodyBytes) == 0 && req.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(req.Body)
		if err != nil {
			return fmt.Errorf("opencode: read body: %w", err)
		}
	}

	attempts := 1
	if a.cfg.Retry != nil {
		attempts = a.cfg.Retry.Attempts()
	}
	if u.KeyRing != nil && len(u.KeyRing.Slots) > attempts {
		attempts = len(u.KeyRing.Slots)
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if a.cfg.Retry != nil {
				if !a.cfg.Retry.Wait(ctx, attempt) {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					return lastErr
				}
			}
			if a.logger != nil {
				a.logger.Warn("retrying opencode upstream", "upstream", u.Name, "attempt", attempt, "key", t.CredentialRef)
			}
		}

		// Circuit Breaker check
		if a.breaker != nil {
			if bErr := a.breaker.Allow(u.Name); bErr != nil {
				return &openai.ErrUpstream{Status: http.StatusServiceUnavailable, Cause: bErr}
			}
		}

		res := a.attempt(ctx, u, req, t, bodyBytes, respW)

		decision := upstream.ProcessAttemptOutcome(u, t, upstream.AttemptOutcome{
			Status:    res.status,
			Err:       res.err,
			Headers:   res.headers,
			Committed: res.streamed,
		}, a.breaker, a.metrics, a.cfg.Notifier, a.logger)

		switch {
		case decision.Success:
			return nil
		case decision.StopCommitted:
			return res.err
		case decision.Failover:
			lastErr = &openai.ErrUpstream{Status: res.status, Retried: true, Body: res.body, Header: res.headers}
			continue
		case decision.Relay:
			relayError(respW, res.status, res.headers, res.body)
			return nil
		default: // decision.Fail
			lastErr = &openai.ErrUpstream{Status: res.status, Retried: attempt > 1, Cause: res.err, Body: res.body, Header: res.headers}
			if !decision.IsHostFailure {
				return lastErr
			}
		}
	}

	var lastUE *openai.ErrUpstream
	if errors.As(lastErr, &lastUE) && len(lastUE.Body) > 0 {
		relayError(respW, lastUE.Status, lastUE.Header, lastUE.Body)
		return nil
	}
	return lastErr
}

// resolveSecret gets the API key secret, or returns empty string if unauthenticated/free.
func (a *Adapter) resolveSecret(u *domain.Upstream, t *domain.Target) string {
	ref := u.CredentialRef
	if t != nil && t.CredentialRef != "" {
		ref = t.CredentialRef
	}

	if t != nil && t.KeySlot != nil && t.KeySlot.Secret != "" {
		return t.KeySlot.Secret
	}

	if a.cfg.SecretLookup != nil {
		if ref != "" {
			if val, ok := a.cfg.SecretLookup(ref); ok && val != "" {
				return val
			}
		}
		if t != nil && t.KeySlot != nil && t.KeySlot.Ref != "" {
			if val, ok := a.cfg.SecretLookup(t.KeySlot.Ref); ok && val != "" {
				return val
			}
		}
	}

	if ref != "" && !strings.Contains(ref, "ENV") && len(ref) > 8 {
		return ref
	}

	return ""
}

// buildEndpointURL calculates the full target URL based on whether it's free vs go,
// and whether the target model requires the Responses API.
func buildEndpointURL(u *domain.Upstream, isResponses, isFree bool) string {
	base := u.BaseURL
	if base == "" && len(u.BaseURLs) > 0 {
		base = u.BaseURLs[0]
	}
	if base == "" {
		if isFree {
			base = DefaultFreeBaseURL
		} else {
			base = DefaultGoBaseURL
		}
	}
	base = strings.TrimRight(base, "/")

	// Auto-correct endpoint tier mismatch if user misconfigured base URL:
	// Free requests (Bearer public) must hit /zen/v1, whereas Go requests (Subscription key) must hit /zen/go/v1.
	if isFree && strings.Contains(base, "/zen/go/v1") {
		base = strings.Replace(base, "/zen/go/v1", "/zen/v1", 1)
	} else if !isFree && strings.Contains(base, "/zen/v1") && !strings.Contains(base, "/zen/go/v1") {
		base = strings.Replace(base, "/zen/v1", "/zen/go/v1", 1)
	}

	if isResponses {
		if strings.HasSuffix(base, "/responses") {
			return base
		}
		if strings.HasSuffix(base, "/chat/completions") {
			return strings.TrimSuffix(base, "/chat/completions") + "/responses"
		}
		return base + "/responses"
	}

	// Standard chat completions
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	if strings.HasSuffix(base, "/responses") {
		return strings.TrimSuffix(base, "/responses") + "/chat/completions"
	}
	return base + "/chat/completions"
}

func (a *Adapter) attempt(ctx context.Context, u *domain.Upstream, req ports.ForwardRequest, t *domain.Target, body []byte, w http.ResponseWriter) attemptResult {
	if a.pool == nil {
		return attemptResult{err: errors.New("opencode adapter: nil client pool")}
	}

	secret := a.resolveSecret(u, t)
	isFree := secret == "" || secret == "public"

	targetModel := t.UpstreamModel
	if targetModel == "" {
		targetModel = gjson.GetBytes(body, "model").String()
	}

	isResponses := IsResponsesModel(targetModel)
	endpoint := buildEndpointURL(u, isResponses, isFree)

	var outboundBody []byte
	if isResponses {
		transformed, err := TransformChatToResponses(body, targetModel)
		if err != nil {
			return attemptResult{err: fmt.Errorf("opencode: transform to responses: %w", err)}
		}
		outboundBody = transformed
	} else {
		// Standard chat completions: rewrite model if necessary, sanitize tools and reasoning
		out := body
		if t.UpstreamModel != "" {
			out, _ = sjson.SetBytes(out, "model", t.UpstreamModel)
		}
		out, _ = SanitizeTools(out)
		out, _ = NormalizeReasoning(out)
		outboundBody = out
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(outboundBody))
	if err != nil {
		return attemptResult{err: err}
	}

	// Session resolution & headers
	tenant := ""
	if ten := httpx.TenantFrom(ctx); ten != nil {
		tenant = ten.Name
	}
	reqID := httpx.RequestIDFrom(ctx)
	clientHeaders := make(http.Header)
	for k, vv := range req.Headers {
		for _, v := range vv {
			clientHeaders.Add(k, v)
		}
	}

	sessionID := ResolveSessionID(clientHeaders, tenant, reqID)

	httpReq.Header.Set("Content-Type", "application/json")
	if req.Stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	} else {
		httpReq.Header.Set("Accept", "application/json")
	}

	httpReq.Header.Set(HeaderSession, sessionID)
	httpReq.Header.Set(HeaderProject, "global")
	httpReq.Header.Set(HeaderClient, "desktop")

	if isFree {
		httpReq.Header.Set("Authorization", "Bearer public")
		httpReq.Header.Set(HeaderRequest, GenerateRequestID())
		clientUA := clientHeaders.Get("User-Agent")
		if strings.Contains(strings.ToLower(clientUA), "opencode") {
			httpReq.Header.Set("User-Agent", clientUA)
		} else {
			httpReq.Header.Set("User-Agent", OpenCodeUserAgent)
		}
	} else {
		httpReq.Header.Set("Authorization", "Bearer "+secret)
	}

	if reqID != "" {
		httpReq.Header.Set("X-Request-Id", reqID)
	}

	resp, err := a.pool.Client(u).Do(httpReq)
	if err != nil {
		return attemptResult{err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= 400 {
		buf, _ := openai.RelayBuffered(resp.Body, a.maxBytes())
		return attemptResult{
			status:  resp.StatusCode,
			headers: resp.Header.Clone(),
			body:    buf,
			err:     &openai.ErrUpstream{Status: resp.StatusCode, Body: buf, Header: resp.Header.Clone()},
		}
	}

	// Transparent decompression if upstream sent gzip or flate
	reader := resp.Body

	publicModel := gjson.GetBytes(body, "model").String()
	if publicModel == "" {
		publicModel = targetModel
	}

	if isResponses {
		if req.Stream {
			written, rErr := a.relayResponsesStream(ctx, w, reader, publicModel)
			return attemptResult{
				status:   resp.StatusCode,
				headers:  resp.Header.Clone(),
				streamed: written > 0,
				err:      rErr,
			}
		}

		aggregated, aErr := a.aggregateResponsesStream(ctx, reader, publicModel)
		if aErr != nil {
			return attemptResult{status: http.StatusInternalServerError, err: aErr}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(aggregated)
		return attemptResult{
			status:   http.StatusOK,
			headers:  resp.Header.Clone(),
			streamed: true,
		}
	}

	// Standard chat completions relay
	if req.Stream {
		written, sErr := openai.RelaySSE(ctx, w, resp.Body, a.cfg.IdleTimeout)
		return attemptResult{
			status:   resp.StatusCode,
			headers:  resp.Header.Clone(),
			streamed: written > 0,
			err:      sErr,
		}
	}

	buf, rErr := openai.RelayBuffered(reader, a.maxBytes())
	if rErr != nil {
		return attemptResult{status: resp.StatusCode, err: rErr}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf)
	return attemptResult{
		status:   resp.StatusCode,
		headers:  resp.Header.Clone(),
		streamed: true,
	}
}

// Normalized Responses API event for SSE streaming
type responsesEvent struct {
	Delta     string
	Reasoning string
	Error     string
	Done      bool
	InputTok  int
	OutputTok int
}

func parseResponsesEvent(eventType string, data []byte) (responsesEvent, bool) {
	if !gjson.ValidBytes(data) {
		return responsesEvent{}, false
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
			return responsesEvent{}, false
		}
		return responsesEvent{Delta: d}, true

	case "response.reasoning_summary_text.delta":
		d := root.Get("delta").String()
		if d == "" {
			return responsesEvent{}, false
		}
		return responsesEvent{Reasoning: d}, true

	case "response.completed", "response.done":
		ev := responsesEvent{Done: true}
		if u := root.Get("response.usage"); u.Exists() {
			in := int(u.Get("input_tokens").Int())
			if in == 0 {
				in = int(u.Get("prompt_tokens").Int())
			}
			out := int(u.Get("output_tokens").Int())
			if out == 0 {
				out = int(u.Get("completion_tokens").Int())
			}
			ev.InputTok = in
			ev.OutputTok = out
		}
		return ev, true

	case "response.failed", "error":
		errMsg := root.Get("response.error.message").String()
		if errMsg == "" {
			errMsg = root.Get("message").String()
		}
		if errMsg == "" {
			errMsg = "opencode upstream error"
		}
		return responsesEvent{Error: errMsg, Done: true}, true
	}

	return responsesEvent{}, false
}

func scanSSE(ctx context.Context, body io.Reader, fn func(ev responsesEvent) (bool, error)) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8<<20)

	var eventType string
	var dataBuf strings.Builder

	dispatch := func() (bool, error) {
		if dataBuf.Len() == 0 {
			eventType = ""
			return false, nil
		}
		data := strings.TrimSpace(dataBuf.String())
		dataBuf.Reset()
		et := eventType
		eventType = ""
		if data == "" || data == "[DONE]" {
			return false, nil
		}
		ev, ok := parseResponsesEvent(et, []byte(data))
		if !ok {
			return false, nil
		}
		return fn(ev)
	}

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := scanner.Text()
		if line == "" {
			stop, err := dispatch()
			if err != nil || stop {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(line[len("event:"):])
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if dataBuf.Len() > 0 {
				dataBuf.WriteByte('\n')
			}
			dataBuf.WriteString(strings.TrimSpace(line[len("data:"):]))
			continue
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	_, err := dispatch()
	return err
}

func (a *Adapter) relayResponsesStream(ctx context.Context, w http.ResponseWriter, body io.Reader, publicModel string) (int64, error) {
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

	id := "chatcmpl-opencode-" + GenerateRequestID()
	created := time.Now().Unix()
	var written int64

	writeChunk := func(delta map[string]any, finish string, inTok, outTok int) error {
		chunk := map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   publicModel,
			"choices": []map[string]any{
				{
					"index":         0,
					"delta":         delta,
					"finish_reason": nil,
				},
			},
		}
		if finish != "" {
			chunk["choices"].([]map[string]any)[0]["finish_reason"] = finish
		}
		if inTok > 0 || outTok > 0 {
			chunk["usage"] = map[string]int{
				"prompt_tokens":     inTok,
				"completion_tokens": outTok,
				"total_tokens":      inTok + outTok,
			}
		}
		raw, err := json.Marshal(chunk)
		if err != nil {
			return err
		}
		n, werr := io.WriteString(w, "data: "+string(raw)+"\n\n")
		written += int64(n)
		if f != nil {
			f.Flush()
		}
		return werr
	}

	// Initial role chunk
	if err := writeChunk(map[string]any{"role": "assistant"}, "", 0, 0); err != nil {
		return written, err
	}

	var finalIn, finalOut int
	var writeErr error
	scanErr := scanSSE(ctx, body, func(ev responsesEvent) (bool, error) {
		switch {
		case ev.Error != "":
			_ = writeChunk(map[string]any{"content": "[Error: " + ev.Error + "]"}, "", 0, 0)
			return true, nil
		case ev.Reasoning != "":
			if err := writeChunk(map[string]any{"reasoning_content": ev.Reasoning}, "", 0, 0); err != nil {
				writeErr = err
				return true, err
			}
		case ev.Delta != "":
			if err := writeChunk(map[string]any{"content": ev.Delta}, "", 0, 0); err != nil {
				writeErr = err
				return true, err
			}
		}
		if ev.Done {
			finalIn = ev.InputTok
			finalOut = ev.OutputTok
			return true, nil
		}
		return false, nil
	})

	if writeErr != nil {
		return written, writeErr
	}
	if scanErr != nil {
		return written, scanErr
	}

	// Terminal stop chunk + [DONE]
	if err := writeChunk(map[string]any{}, "stop", finalIn, finalOut); err != nil {
		return written, err
	}
	n, werr := io.WriteString(w, "data: [DONE]\n\n")
	written += int64(n)
	if f != nil {
		f.Flush()
	}
	return written, werr
}

func (a *Adapter) aggregateResponsesStream(ctx context.Context, body io.Reader, publicModel string) ([]byte, error) {
	id := "chatcmpl-opencode-" + GenerateRequestID()
	created := time.Now().Unix()

	var content strings.Builder
	var reasoning strings.Builder
	var inTok, outTok int
	var upstreamErr string

	err := scanSSE(ctx, body, func(ev responsesEvent) (bool, error) {
		switch {
		case ev.Error != "":
			upstreamErr = ev.Error
			return true, nil
		case ev.Reasoning != "":
			reasoning.WriteString(ev.Reasoning)
		case ev.Delta != "":
			content.WriteString(ev.Delta)
		}
		if ev.Done {
			inTok = ev.InputTok
			outTok = ev.OutputTok
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	if upstreamErr != "" {
		return nil, fmt.Errorf("opencode upstream error: %s", upstreamErr)
	}

	msg := map[string]any{
		"role":    "assistant",
		"content": content.String(),
	}
	if reasoning.Len() > 0 {
		msg["reasoning_content"] = reasoning.String()
	}

	respObj := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   publicModel,
		"choices": []map[string]any{
			{
				"index":         0,
				"message":       msg,
				"finish_reason": "stop",
			},
		},
		"usage": map[string]int{
			"prompt_tokens":     inTok,
			"completion_tokens": outTok,
			"total_tokens":      inTok + outTok,
		},
	}

	return json.Marshal(respObj)
}

func relayError(w http.ResponseWriter, status int, hdr http.Header, body []byte) {
	ct := ""
	if hdr != nil {
		ct = hdr.Get("Content-Type")
	}
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	if hdr != nil {
		if ra := hdr.Get("Retry-After"); ra != "" {
			w.Header().Set("Retry-After", ra)
		}
	}
	w.WriteHeader(status)
	if len(body) > 0 {
		_, _ = w.Write(body)
	}
}
