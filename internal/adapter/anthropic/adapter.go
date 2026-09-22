package anthropic

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

// Config tunes the Anthropic adapter.
type Config struct {
	SecretLookup     func(ref string) (string, bool)
	Retry            openai.RetryPolicy
	Logger           *slog.Logger
	MaxBufferedBytes int64
	// Metrics receives key health and saturation updates. Nil is safe.
	Metrics KeyMetricsObserver
	// Notifier receives automated key lifecycle actions (deactivate/delete). Nil is safe.
	Notifier ports.KeyActionNotifier
}

// Adapter implements ports.UpstreamAdapter for the Anthropic Messages API.
type Adapter struct {
	pool    ClientPool
	breaker BreakerLookup
	cfg     Config
	metrics KeyMetricsObserver
}

// NewAdapter constructs an Anthropic adapter.
func NewAdapter(pool ClientPool, breaker BreakerLookup, cfg Config) *Adapter {
	return &Adapter{
		pool:    pool,
		breaker: breaker,
		cfg:     cfg,
		metrics: cfg.Metrics,
	}
}

// Protocol identifies the Anthropic Messages wire format.
func (a *Adapter) Protocol() domain.Protocol {
	return domain.ProtocolAnthropic
}

type attemptResult struct {
	status   int
	headers  http.Header
	streamed bool
	body     []byte
	err      error
}

// Forward translates an OpenAI request to Anthropic Messages, forwards it,
// and translates the response back to OpenAI format.
func (a *Adapter) Forward(ctx context.Context, t *domain.Target, req ports.ForwardRequest, w io.Writer) error {
	if t == nil || t.Upstream == nil {
		return errors.New("anthropic adapter: nil target")
	}
	respW, ok := w.(http.ResponseWriter)
	if !ok {
		return errors.New("anthropic adapter: writer is not an http.ResponseWriter")
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

	anthropicBody, err := TranslateOpenAIToAnthropic(bodyBytes, t.UpstreamModel)
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
			if a.cfg.Retry != nil {
				if !a.cfg.Retry.Wait(ctx, attempt) {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					return lastErr
				}
			}
			if a.cfg.Logger != nil {
				a.cfg.Logger.Warn("retrying anthropic upstream", "upstream", u.Name, "attempt", attempt, "key", t.CredentialRef)
			}
		}

		if a.breaker != nil {
			if err := a.breaker.Allow(u.Name); err != nil {
				return &openai.ErrUpstream{Status: http.StatusServiceUnavailable, Cause: err}
			}
		}

		res := a.attempt(ctx, u, req, t, anthropicBody, respW, attempt)

		decision := upstream.ProcessAttemptOutcome(u, t, upstream.AttemptOutcome{
			Status:    res.status,
			Err:       res.err,
			Headers:   res.headers,
			Committed: res.streamed || headerCommitted(respW),
		}, a.breaker, a.metrics, a.cfg.Notifier, a.cfg.Logger)

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

func (a *Adapter) attempt(ctx context.Context, u *domain.Upstream, req ports.ForwardRequest, t *domain.Target, body []byte, w http.ResponseWriter, attempt int) attemptResult {
	if a.pool == nil {
		return attemptResult{err: errors.New("anthropic adapter: nil client pool")}
	}

	path := "/v1/messages"
	trimmed := u.URLForAttempt(attempt)
	if strings.HasSuffix(trimmed, "/v1") {
		path = "/messages"
	}
	url := trimmed + path

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return attemptResult{err: err}
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	secret, ok := a.secret(u, t)
	if !ok {
		ref := u.CredentialRef
		if t != nil && t.CredentialRef != "" {
			ref = t.CredentialRef
		}
		return attemptResult{err: fmt.Errorf("credential %q is not set in the environment", ref)}
	}
	httpReq.Header.Set("x-api-key", secret)

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

	if req.Stream && resp.StatusCode < 400 &&
		strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		n, relayErr := a.relayAnthropicSSE(ctx, w, resp.Body, t.UpstreamModel)
		return attemptResult{
			status:   resp.StatusCode,
			headers:  resp.Header.Clone(),
			streamed: n > 0,
			err:      relayErr,
		}
	}

	maxBytes := a.cfg.MaxBufferedBytes
	if maxBytes <= 0 {
		maxBytes = 32 << 20
	}
	buf, rerr := openai.RelayBuffered(resp.Body, maxBytes)
	if rerr != nil {
		return attemptResult{status: resp.StatusCode, headers: resp.Header.Clone(), body: buf, err: rerr}
	}

	if resp.StatusCode >= 400 {
		return attemptResult{
			status:  resp.StatusCode,
			headers: resp.Header.Clone(),
			body:    buf,
			err:     &openai.ErrUpstream{Status: resp.StatusCode, Body: buf, Header: resp.Header.Clone()},
		}
	}

	// Translate non-streaming Anthropic response to OpenAI response
	openAIRespBytes, err := TranslateAnthropicToOpenAI(buf, t.UpstreamModel)
	if err != nil {
		return attemptResult{status: http.StatusInternalServerError, err: err}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	n, werr := w.Write(openAIRespBytes)
	return attemptResult{
		status:   resp.StatusCode,
		headers:  resp.Header.Clone(),
		streamed: n > 0,
		body:     openAIRespBytes,
		err:      werr,
	}
}

type flusher interface {
	Flush()
}

func (a *Adapter) relayAnthropicSSE(ctx context.Context, w http.ResponseWriter, body io.Reader, publicModel string) (int64, error) {
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

	scanner := bufio.NewScanner(body)
	var written int64
	var currentEvent string
	var msgID string

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return written, ctx.Err()
		default:
		}

		line := scanner.Text()
		if strings.HasPrefix(line, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			chunk, isDone, err := TranslateAnthropicChunkToOpenAI(currentEvent, []byte(dataStr), publicModel, &msgID)
			if err != nil {
				if a.cfg.Logger != nil {
					a.cfg.Logger.Warn("anthropic chunk translation error", "event", currentEvent, "err", err)
				}
				continue
			}
			if isDone {
				n, werr := io.WriteString(w, "data: [DONE]\n\n")
				written += int64(n)
				if werr != nil {
					return written, werr
				}
				if f != nil {
					f.Flush()
				}
				continue
			}
			if len(chunk) > 0 {
				n, werr := io.WriteString(w, "data: "+string(chunk)+"\n\n")
				written += int64(n)
				if werr != nil {
					return written, werr
				}
				if f != nil {
					f.Flush()
				}
			}
		}
	}

	return written, scanner.Err()
}

func (a *Adapter) secret(u *domain.Upstream, t *domain.Target) (string, bool) {
	if a.cfg.SecretLookup != nil {
		if t != nil && t.CredentialRef != "" {
			if s, ok := a.cfg.SecretLookup(t.CredentialRef); ok && s != "" {
				return s, true
			}
		}
		if t != nil && t.KeySlot != nil && t.KeySlot.Ref != "" {
			if s, ok := a.cfg.SecretLookup(t.KeySlot.Ref); ok && s != "" {
				return s, true
			}
		}
		if u != nil && u.CredentialRef != "" {
			if s, ok := a.cfg.SecretLookup(u.CredentialRef); ok && s != "" {
				return s, true
			}
		}
	}
	if t != nil && t.KeySlot != nil && t.KeySlot.Secret != "" {
		return t.KeySlot.Secret, true
	}
	return "", false
}

func relayError(w http.ResponseWriter, status int, headers http.Header, body []byte) {
	if headers != nil {
		if ra := headers.Get("Retry-After"); ra != "" {
			w.Header().Set("Retry-After", ra)
		}
	}
	outStatus, outBody := TranslateAnthropicError(status, body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(outStatus)
	_, _ = w.Write(outBody)
}

func headerCommitted(w http.ResponseWriter) bool {
	if rec, ok := w.(interface{ Committed() bool }); ok {
		return rec.Committed()
	}
	return false
}
