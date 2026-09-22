// Package codebuddy provides upstream adapters for CodeBuddy (China & International)
// endpoints, handling device authorization flows, completion forwarding, and SSE relays.
package codebuddy

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
	"regexp"
	"strings"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/dickymuliafiqri/firefly/internal/transport/httpx"
	"github.com/dickymuliafiqri/firefly/internal/transport/upstream"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	defaultBaseURLCN   = "https://copilot.tencent.com/v2/chat/completions"
	defaultBaseURLIntl = "https://www.codebuddy.ai/v2/chat/completions"

	neutralPrompt = "You are a helpful AI assistant that helps with software engineering tasks."
)

var agentPattern = regexp.MustCompile(`(?i)you are claude code|claude.?code.+official.+cli|anthropic.+official.+cli|anxthxropic.+official.+cli|you are (?:cursor|windsurf|cline|aider|continue|copilot|cody)|you are an? (?:ai )?(?:coding |code )?agent|cc_entrypoint\s*=\s*(?:cli|vscode|jetbrains|gui)|claude.?code.+issues|give feedback.+claude.?code|you are .{0,30}(?:powerful )?ai agent|orchestration capabilities|OhMyOpenCode|<agent-identity>|<Role>|<Behavior_Instructions>`)

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

// TokenResolver resolves dynamic tokens, such as OAuth connection access tokens.
type TokenResolver func(ctx context.Context, ref string) (string, error)

// Config tunes the CodeBuddy adapter.
type Config struct {
	TokenResolver    TokenResolver
	SecretLookup     func(ref string) (string, bool)
	Retry            openai.RetryPolicy
	Logger           *slog.Logger
	MaxBufferedBytes int64
	Metrics          KeyMetricsObserver
	Notifier         ports.KeyActionNotifier
}

// Adapter implements ports.UpstreamAdapter for Tencent CodeBuddy (CN and International).
type Adapter struct {
	pool    ClientPool
	breaker BreakerLookup
	cfg     Config
	logger  *slog.Logger
	metrics KeyMetricsObserver
}

// NewAdapter constructs a new CodeBuddy adapter.
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

// Protocol returns domain.ProtocolCodeBuddyCN.
func (a *Adapter) Protocol() domain.Protocol {
	return domain.ProtocolCodeBuddyCN
}

type attemptResult struct {
	status   int
	headers  http.Header
	streamed bool
	body     []byte
	err      error
}

// Forward handles request transformation, outbound forwarding to CodeBuddy,
// and unwrapping non-stream SSE or streaming SSE.
func (a *Adapter) Forward(ctx context.Context, t *domain.Target, req ports.ForwardRequest, w io.Writer) error {
	if t == nil || t.Upstream == nil {
		return errors.New("codebuddy adapter: nil target")
	}
	respW, ok := w.(http.ResponseWriter)
	if !ok {
		return errors.New("codebuddy adapter: writer is not an http.ResponseWriter")
	}
	u := t.Upstream

	body, err := a.buildBody(req, t)
	if err != nil {
		return err
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

		// Breaker check before network call
		if a.breaker != nil {
			if bErr := a.breaker.Allow(u.Name); bErr != nil {
				return bErr
			}
		}

		res := a.attempt(ctx, u, req, t, body, respW, attempt)

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

func (a *Adapter) resolveToken(ctx context.Context, u *domain.Upstream, t *domain.Target) (string, bool) {
	ref := u.CredentialRef
	if t != nil && t.CredentialRef != "" {
		ref = t.CredentialRef
	}

	// 1. Try TokenResolver
	if a.cfg.TokenResolver != nil && ref != "" {
		if tok, err := a.cfg.TokenResolver(ctx, ref); err == nil && tok != "" {
			return tok, true
		}
	}

	// 2. Try SecretLookup
	if a.cfg.SecretLookup != nil && ref != "" {
		if tok, ok := a.cfg.SecretLookup(ref); ok && tok != "" {
			return tok, true
		}
	}

	// 3. Fallback direct token
	if strings.HasPrefix(ref, "ey") || len(ref) > 30 {
		return ref, true
	}

	return "", false
}

func sanitizeSystemPrompts(raw []byte) []byte {
	messages := gjson.GetBytes(raw, "messages")
	if !messages.IsArray() {
		return raw
	}

	for i, msg := range messages.Array() {
		if msg.Get("role").String() != "system" {
			continue
		}

		contentNode := msg.Get("content")
		var text string
		if contentNode.Type == gjson.String {
			text = contentNode.String()
		} else if contentNode.IsArray() {
			var sb strings.Builder
			for _, part := range contentNode.Array() {
				if t := part.Get("text").String(); t != "" {
					sb.WriteString(t)
					sb.WriteString("\n")
				}
			}
			text = sb.String()
		}

		if text == "" {
			continue
		}

		if len(text) > 2000 || agentPattern.MatchString(text) {
			path := fmt.Sprintf("messages.%d.content", i)
			if contentNode.Type == gjson.String {
				raw, _ = sjson.SetBytes(raw, path, neutralPrompt)
			} else {
				replacement := []map[string]string{{"type": "text", "text": neutralPrompt}}
				raw, _ = sjson.SetBytes(raw, path, replacement)
			}
		}
	}

	return raw
}

func (a *Adapter) attempt(ctx context.Context, u *domain.Upstream, req ports.ForwardRequest, t *domain.Target, body []byte, w http.ResponseWriter, attempt int) attemptResult {
	if a.pool == nil {
		return attemptResult{err: errors.New("codebuddy adapter: nil client pool")}
	}

	endpointURL := defaultBaseURLCN
	if u.Protocol == domain.ProtocolCodeBuddyIntl {
		endpointURL = defaultBaseURLIntl
	}
	if u.BaseURL != "" {
		endpointURL = strings.TrimRight(u.BaseURL, "/")
		if !strings.HasSuffix(endpointURL, "/chat/completions") {
			endpointURL += "/chat/completions"
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(body))
	if err != nil {
		return attemptResult{err: err}
	}

	// CodeBuddy Headers
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("X-Product", "SaaS")
	httpReq.Header.Set("x-requested-with", "XMLHttpRequest")
	httpReq.Header.Set("x-codebuddy-request", "1")

	if u.Protocol == domain.ProtocolCodeBuddyIntl {
		httpReq.Header.Set("User-Agent", "IDE/2.108.1 CodeBuddy/2.108.1")
		httpReq.Header.Set("X-Domain", "www.codebuddy.ai")
		httpReq.Header.Set("X-IDE-Type", "IDE")
		httpReq.Header.Set("X-IDE-Name", "IDE")
	} else {
		httpReq.Header.Set("User-Agent", "CLI/2.108.1 CodeBuddy/2.108.1")
		httpReq.Header.Set("X-Domain", "copilot.tencent.com")
		httpReq.Header.Set("X-IDE-Type", "CLI")
		httpReq.Header.Set("X-IDE-Name", "CLI")
	}

	token, ok := a.resolveToken(ctx, u, t)
	if !ok {
		ref := u.CredentialRef
		if t != nil && t.CredentialRef != "" {
			ref = t.CredentialRef
		}
		return attemptResult{err: fmt.Errorf("credential %q could not be resolved", ref)}
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)

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

	// If upstream returned error status (>= 400), read error buffer and return
	if resp.StatusCode >= 400 {
		maxBytes := a.cfg.MaxBufferedBytes
		if maxBytes <= 0 {
			maxBytes = 32 << 20
		}
		buf, rerr := openai.RelayBuffered(resp.Body, maxBytes)
		if rerr != nil {
			return attemptResult{status: resp.StatusCode, headers: resp.Header.Clone(), body: buf, err: rerr}
		}
		return attemptResult{
			status:  resp.StatusCode,
			headers: resp.Header.Clone(),
			body:    buf,
			err:     &openai.ErrUpstream{Status: resp.StatusCode, Body: buf, Header: resp.Header.Clone()},
		}
	}

	// 1. Client requested streaming (SSE)
	if req.Stream {
		idle := time.Duration(u.StreamIdleTimeoutMs) * time.Millisecond
		n, relayErr := openai.RelaySSE(ctx, w, resp.Body, idle)
		return attemptResult{
			status:   resp.StatusCode,
			headers:  resp.Header.Clone(),
			streamed: n > 0,
			err:      relayErr,
		}
	}

	// 2. Client requested non-streaming (JSON):
	// Since CodeBuddy forced stream: true, aggregate SSE deltas into a single JSON response.
	modelName := t.UpstreamModel
	aggregatedJSON, aggErr := AggregateSSEToJSON(resp.Body, modelName)
	if aggErr != nil {
		return attemptResult{
			status:  http.StatusBadGateway,
			headers: resp.Header.Clone(),
			err:     fmt.Errorf("failed to aggregate sse to json: %w", aggErr),
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, werr := w.Write(aggregatedJSON)
	return attemptResult{
		status:   resp.StatusCode,
		headers:  resp.Header.Clone(),
		streamed: false,
		body:     aggregatedJSON,
		err:      werr,
	}
}

func relayError(w http.ResponseWriter, status int, headers http.Header, body []byte) {
	if status == 0 {
		status = http.StatusBadGateway
	}
	if headers != nil {
		if ct := headers.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		if ra := headers.Get("Retry-After"); ra != "" {
			w.Header().Set("Retry-After", ra)
		}
	}
	w.WriteHeader(status)
	if len(body) > 0 {
		_, _ = w.Write(body)
	}
}

func (a *Adapter) buildBody(req ports.ForwardRequest, t *domain.Target) ([]byte, error) {
	raw := req.BodyBytes
	if raw == nil && req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("read request body: %w", err)
		}
		raw = b
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}

	// 1. Rewrite model name if requested
	if t.UpstreamModel != "" {
		updated, err := sjson.SetBytes(raw, "model", t.UpstreamModel)
		if err == nil {
			raw = updated
		}
	}

	// 2. Force stream = true (CodeBuddy rejects non-stream requests with 11101)
	updated, err := sjson.SetBytes(raw, "stream", true)
	if err == nil {
		raw = updated
	}

	// 3. System Prompt Sanitization (Bypass Tencent Content Filter)
	raw = sanitizeSystemPrompts(raw)

	// 4. Reasoning parameters
	// If reasoning_effort is "none" or "off", omit it.
	// If set, add reasoning_summary: "auto"
	reasoningEffort := gjson.GetBytes(raw, "reasoning_effort").String()
	if reasoningEffort == "none" || reasoningEffort == "off" {
		raw, _ = sjson.DeleteBytes(raw, "reasoning_effort")
		raw, _ = sjson.DeleteBytes(raw, "reasoning_summary")
	} else if reasoningEffort != "" {
		raw, _ = sjson.SetBytes(raw, "reasoning_summary", "auto")
	}

	return raw, nil
}

// ToolCall holds aggregated function tool call delta chunks.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// AggregateSSEToJSON reads an OpenAI-compatible SSE stream and aggregates it into
// a standard non-streaming chat.completion JSON payload.
func AggregateSSEToJSON(r io.Reader, fallbackModel string) ([]byte, error) {
	scanner := bufio.NewScanner(r)
	var (
		id               string
		created          int64
		model            string
		contentBuilder   strings.Builder
		reasoningBuilder strings.Builder
		finishReason     = "stop"
		usage            any
		toolCallsMap     = make(map[int]*ToolCall)
	)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}

		if errNode := gjson.Get(payload, "error"); errNode.Exists() {
			return nil, fmt.Errorf("upstream sse error: %s", errNode.String())
		}

		chunk := gjson.Parse(payload)
		if id == "" {
			id = chunk.Get("id").String()
		}
		if created == 0 {
			created = chunk.Get("created").Int()
		}
		if model == "" {
			model = chunk.Get("model").String()
		}

		choice := chunk.Get("choices.0")
		if choice.Exists() {
			delta := choice.Get("delta")
			if c := delta.Get("content").String(); c != "" {
				contentBuilder.WriteString(c)
			}
			if r := delta.Get("reasoning_content").String(); r != "" {
				reasoningBuilder.WriteString(r)
			}
			if fr := choice.Get("finish_reason").String(); fr != "" {
				finishReason = fr
			}

			// Aggregate tool calls
			if tcArr := delta.Get("tool_calls"); tcArr.IsArray() {
				for _, tc := range tcArr.Array() {
					idx := int(tc.Get("index").Int())
					item, exists := toolCallsMap[idx]
					if !exists {
						item = &ToolCall{
							Type: "function",
						}
						toolCallsMap[idx] = item
					}
					if tid := tc.Get("id").String(); tid != "" {
						item.ID = tid
					}
					if fnName := tc.Get("function.name").String(); fnName != "" {
						item.Function.Name += fnName
					}
					if fnArgs := tc.Get("function.arguments").String(); fnArgs != "" {
						item.Function.Arguments += fnArgs
					}
				}
			}
		}

		if u := chunk.Get("usage"); u.Exists() {
			usage = u.Value()
		}
	}

	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}

	if id == "" {
		id = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	if created == 0 {
		created = time.Now().Unix()
	}
	if model == "" {
		model = fallbackModel
	}

	message := map[string]any{
		"role": "assistant",
	}
	if contentBuilder.Len() > 0 || len(toolCallsMap) == 0 {
		message["content"] = contentBuilder.String()
	} else {
		message["content"] = nil
	}

	if reasoningBuilder.Len() > 0 {
		message["reasoning_content"] = reasoningBuilder.String()
	}

	if len(toolCallsMap) > 0 {
		toolCalls := make([]*ToolCall, len(toolCallsMap))
		for i := 0; i < len(toolCallsMap); i++ {
			toolCalls[i] = toolCallsMap[i]
		}
		message["tool_calls"] = toolCalls
		if finishReason == "stop" {
			finishReason = "tool_calls"
		}
	}

	response := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []map[string]any{
			{
				"index":         0,
				"message":       message,
				"finish_reason": finishReason,
			},
		},
	}
	if usage != nil {
		response["usage"] = usage
	}

	return json.Marshal(response)
}
