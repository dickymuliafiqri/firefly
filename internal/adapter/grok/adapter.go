// Package grok provides an upstream adapter for Grok CLI / Grok Build, translating
// OpenAI-compatible chat/completions requests to the xAI Grok CLI OpenAI Responses API
// (cli-chat-proxy.grok.com) using an xAI OAuth bearer token, and translating the
// Responses SSE stream back to OpenAI chat.completion format.
package grok

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/dickymuliafiqri/firefly/internal/transport/httpx"
	"github.com/dickymuliafiqri/firefly/internal/transport/upstream"
)

// ClientPool is the outbound client cache the adapter dials through.
type ClientPool interface {
	Client(u *domain.Upstream) *http.Client
}

// BreakerLookup yields a breaker for an upstream name.
type BreakerLookup interface {
	Allow(name string) error
	Report(name string, ok bool)
}

// KeyMetricsObserver receives notifications of key requests and cooldown events.
type KeyMetricsObserver interface {
	ObserveKeyCooldown(upstream, keyRef string)
	ObserveKeyRequest(upstream, keyRef string, status int)
}

// TokenResolver resolves dynamic credential references (e.g. OAuth connections).
type TokenResolver func(ctx context.Context, ref string) (string, error)

// Config tunes the Grok CLI adapter.
type Config struct {
	TokenResolver    TokenResolver
	SecretLookup     func(ref string) (string, bool)
	Retry            openai.RetryPolicy
	Logger           *slog.Logger
	MaxBufferedBytes int64
	Metrics          KeyMetricsObserver
	IdleTimeout      time.Duration
	Notifier         ports.KeyActionNotifier
}

// Adapter implements ports.UpstreamAdapter for the Grok CLI (Responses API) protocol.
type Adapter struct {
	pool    ClientPool
	breaker BreakerLookup
	cfg     Config
	logger  *slog.Logger
	metrics KeyMetricsObserver
}

// NewAdapter constructs a Grok CLI adapter.
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

// Protocol identifies the wire dialect this adapter speaks.
func (a *Adapter) Protocol() domain.Protocol {
	return domain.ProtocolGrokCLI
}

type attemptResult struct {
	status   int
	headers  http.Header
	streamed bool
	body     []byte
	err      error
}

// Forward translates an OpenAI request to the Grok CLI Responses API, sends it with
// the account's OAuth bearer token, and translates the response back to OpenAI format.
func (a *Adapter) Forward(ctx context.Context, t *domain.Target, req ports.ForwardRequest, w io.Writer) error {
	if t == nil || t.Upstream == nil {
		return errors.New("grok adapter: nil target")
	}
	respW, ok := w.(http.ResponseWriter)
	if !ok {
		return errors.New("grok adapter: writer is not an http.ResponseWriter")
	}
	u := t.Upstream

	bodyBytes := req.BodyBytes
	if bodyBytes == nil && req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return fmt.Errorf("read request body: %w", err)
		}
		bodyBytes = b
	}

	grokBody, plan, err := TranslateOpenAIToGrokCLI(bodyBytes, t.UpstreamModel)
	if err != nil {
		openai.WriteError(respW, http.StatusBadRequest, openai.TypeInvalidRequest, err.Error())
		return nil
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
			if a.cfg.Retry != nil && !a.cfg.Retry.Wait(ctx, attempt-1) {
				return ctx.Err()
			}
		}

		if a.breaker != nil {
			if bErr := a.breaker.Allow(u.Name); bErr != nil {
				return bErr
			}
		}

		res := a.attempt(ctx, u, req, t, grokBody, plan, respW)

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

// resolveToken resolves the xAI OAuth bearer token for this target. Mirrors the
// three-tier resolution of the other adapters and additionally falls back to the
// key slot's stored secret (the harvester path, where the access token is stored
// directly in KeySlot.Secret).
func (a *Adapter) resolveToken(ctx context.Context, u *domain.Upstream, t *domain.Target) (string, bool) {
	ref := u.CredentialRef
	if t != nil && t.CredentialRef != "" {
		ref = t.CredentialRef
	}

	// 1. Dynamic resolver (OAuth connection references, e.g. "oauth:...").
	if a.cfg.TokenResolver != nil && ref != "" {
		if tok, err := a.cfg.TokenResolver(ctx, ref); err == nil && tok != "" {
			return tok, true
		}
	}

	// 2. Environment / secret lookup by ref (and by key-slot ref).
	if a.cfg.SecretLookup != nil {
		if ref != "" {
			if tok, ok := a.cfg.SecretLookup(ref); ok && tok != "" {
				return tok, true
			}
		}
		if t != nil && t.KeySlot != nil && t.KeySlot.Ref != "" {
			if tok, ok := a.cfg.SecretLookup(t.KeySlot.Ref); ok && tok != "" {
				return tok, true
			}
		}
	}

	// 3. The credential value stored directly on the key slot. This is the harvester
	//    path: the xAI OAuth access token is stored in KeySlot.Secret, while the ref
	//    is only an identifier (e.g. "grok-key-1156").
	if t != nil && t.KeySlot != nil && t.KeySlot.Secret != "" {
		return t.KeySlot.Secret, true
	}

	// 4. Fallback: the ref is itself a bearer token (JWTs start with "ey").
	if strings.HasPrefix(ref, "ey") || len(ref) > 40 {
		return ref, true
	}

	return "", false
}

func (a *Adapter) attempt(ctx context.Context, u *domain.Upstream, req ports.ForwardRequest, t *domain.Target, body []byte, plan resolveModelPlan, w http.ResponseWriter) attemptResult {
	if a.pool == nil {
		return attemptResult{err: errors.New("grok adapter: nil client pool")}
	}

	endpoint := GrokCLIBaseURL + GrokCLIResponsesPath
	if u.BaseURL != "" {
		base := strings.TrimRight(u.BaseURL, "/")
		if strings.HasSuffix(base, GrokCLIResponsesPath) {
			endpoint = base
		} else {
			endpoint = base + GrokCLIResponsesPath
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return attemptResult{err: err}
	}

	token, ok := a.resolveToken(ctx, u, t)
	if !ok {
		ref := u.CredentialRef
		if t != nil && t.CredentialRef != "" {
			ref = t.CredentialRef
		}
		return attemptResult{err: fmt.Errorf("credential %q could not be resolved", ref)}
	}

	sessionID := newUUID()
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("User-Agent", GrokCLIUserAgent)
	httpReq.Header.Set("x-xai-token-auth", GrokCLITokenAuth)
	httpReq.Header.Set("x-grok-client-identifier", GrokCLIClientIdentifier)
	httpReq.Header.Set("x-grok-client-version", GrokCLIVersion)
	httpReq.Header.Set("x-grok-client-mode", "headless")
	httpReq.Header.Set("x-grok-session-id", sessionID)
	httpReq.Header.Set("x-grok-conv-id", sessionID)
	httpReq.Header.Set("x-grok-req-id", newUUID())
	httpReq.Header.Set("x-grok-turn-idx", "1")
	if plan.UpstreamModel != "" {
		httpReq.Header.Set("x-grok-model-override", plan.UpstreamModel)
	}

	for k, v := range u.ExtraHeaders {
		httpReq.Header.Set(k, v)
	}
	if rid := httpx.RequestIDFrom(ctx); rid != "" {
		httpReq.Header.Set("X-Request-Id", rid)
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

	// Grok CLI always streams a Responses-API SSE feed; branch on client intent.
	if req.Stream {
		n, relayErr := a.relayResponsesStream(ctx, w, resp.Body, t.UpstreamModel)
		return attemptResult{
			status:   resp.StatusCode,
			headers:  resp.Header.Clone(),
			streamed: n > 0,
			err:      relayErr,
		}
	}

	respBytes, aggErr := a.aggregateResponsesStream(ctx, resp.Body, t.UpstreamModel)
	if aggErr != nil {
		return attemptResult{status: http.StatusBadGateway, headers: resp.Header.Clone(), err: aggErr}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	n, werr := w.Write(respBytes)
	return attemptResult{
		status:   http.StatusOK,
		headers:  resp.Header.Clone(),
		streamed: n > 0,
		body:     respBytes,
		err:      werr,
	}
}

func (a *Adapter) maxBytes() int64 {
	if a.cfg.MaxBufferedBytes > 0 {
		return a.cfg.MaxBufferedBytes
	}
	return 32 << 20
}

type flusher interface {
	Flush()
}

// scanSSEEvents reads a Responses-API SSE stream and invokes fn for each decoded
// event. It handles multi-line events with "event:" and "data:" fields separated
// by blank lines. Returns on EOF, ctx cancellation, or a terminal event.
func scanSSEEvents(ctx context.Context, body io.Reader, fn func(ev grokEvent) (stop bool, err error)) error {
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
			// End of one SSE event.
			stop, err := dispatch()
			if err != nil || stop {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // comment / heartbeat
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
	// Flush a trailing event without a final blank line.
	_, err := dispatch()
	return err
}

// relayResponsesStream reads the Grok Responses-API SSE stream and relays it to the
// client as an OpenAI-compatible chat.completion.chunk SSE stream.
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

	id := "chatcmpl-grok-" + shortID()
	created := time.Now().Unix()
	var written int64

	write := func(delta map[string]any, finish string, usage *openAIUsage) error {
		chunk := buildChunk(id, publicModel, created, delta, finish, usage)
		n, werr := io.WriteString(w, "data: "+string(chunk)+"\n\n")
		written += int64(n)
		if f != nil {
			f.Flush()
		}
		return werr
	}

	// Initial role delta.
	if err := write(map[string]any{"role": "assistant"}, "", nil); err != nil {
		return written, err
	}

	var finalUsage *openAIUsage
	var writeErr error
	scanErr := scanSSEEvents(ctx, body, func(ev grokEvent) (bool, error) {
		switch {
		case ev.Error != "":
			_ = write(map[string]any{"content": "[Error: " + ev.Error + "]"}, "", nil)
			return true, nil
		case ev.Reasoning != "":
			if err := write(map[string]any{"reasoning_content": ev.Reasoning}, "", nil); err != nil {
				writeErr = err
				return true, err
			}
		case ev.Delta != "":
			if err := write(map[string]any{"content": ev.Delta}, "", nil); err != nil {
				writeErr = err
				return true, err
			}
		}
		if ev.Done {
			if ev.Usage != nil {
				finalUsage = &openAIUsage{PromptTokens: ev.Usage.Prompt, CompletionTokens: ev.Usage.Completion, TotalTokens: ev.Usage.Total}
			}
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

	// Terminal stop chunk + [DONE].
	if err := write(map[string]any{}, "stop", finalUsage); err != nil {
		return written, err
	}
	n, werr := io.WriteString(w, "data: [DONE]\n\n")
	written += int64(n)
	if f != nil {
		f.Flush()
	}
	return written, werr
}

// aggregateResponsesStream consumes the full Responses-API SSE stream and produces a
// single OpenAI chat.completion JSON body for non-streaming clients.
func (a *Adapter) aggregateResponsesStream(ctx context.Context, body io.Reader, publicModel string) ([]byte, error) {
	id := "chatcmpl-grok-" + shortID()
	created := time.Now().Unix()

	var content strings.Builder
	var reasoning strings.Builder
	var usage *usageInfo
	var upstreamErr string

	err := scanSSEEvents(ctx, body, func(ev grokEvent) (bool, error) {
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
			if ev.Usage != nil {
				usage = ev.Usage
			}
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	if upstreamErr != "" {
		return nil, fmt.Errorf("grok upstream error: %s", upstreamErr)
	}

	return buildCompletion(id, publicModel, created, content.String(), reasoning.String(), usage), nil
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
