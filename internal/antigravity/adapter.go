// Package antigravity provides an upstream adapter for Google Antigravity Cloud Code,
// translating OpenAI-compatible inference requests to Cloud Code internal protocols.
package antigravity

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

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/httpx"
	"github.com/dickymuliafiqri/firefly/internal/openai"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/dickymuliafiqri/firefly/internal/upstream"
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

// TokenResolver resolves dynamic tokens, such as OAuth connection access tokens.
type TokenResolver func(ctx context.Context, ref string) (string, error)

// Config tunes the Antigravity adapter.
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

// Adapter implements ports.UpstreamAdapter for the Google Antigravity Cloud Code protocol.
type Adapter struct {
	pool    ClientPool
	breaker BreakerLookup
	cfg     Config
	logger  *slog.Logger
	metrics KeyMetricsObserver
}

// NewAdapter constructs an Antigravity adapter.
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
	return domain.ProtocolAntigravity
}

type attemptResult struct {
	status   int
	headers  http.Header
	streamed bool
	body     []byte
	err      error
}

// Forward translates an OpenAI request to Antigravity Cloud Code, sends it to the daily host,
// and streams/translates the response back to OpenAI format.
func (a *Adapter) Forward(ctx context.Context, t *domain.Target, req ports.ForwardRequest, w io.Writer) error {
	if t == nil || t.Upstream == nil {
		return errors.New("antigravity adapter: nil target")
	}
	respW, ok := w.(http.ResponseWriter)
	if !ok {
		return errors.New("antigravity adapter: writer is not an http.ResponseWriter")
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

	projectID := GenerateProjectID()

	antigravityBody, err := TranslateOpenAIToAntigravity(bodyBytes, t.UpstreamModel, projectID)
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

		// Breaker check before network call
		if a.breaker != nil {
			if bErr := a.breaker.Allow(u.Name); bErr != nil {
				return bErr
			}
		}

		res := a.attempt(ctx, u, req, t, antigravityBody, respW, attempt)

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

	// 1. Try TokenResolver (for OAuth connections, e.g. "oauth:user@gmail.com")
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

	// 3. Fallback: if ref is already an access token itself
	if strings.HasPrefix(ref, "ya29.") {
		return ref, true
	}

	return "", false
}

func (a *Adapter) attempt(ctx context.Context, u *domain.Upstream, req ports.ForwardRequest, t *domain.Target, body []byte, w http.ResponseWriter, attempt int) attemptResult {
	if a.pool == nil {
		return attemptResult{err: errors.New("antigravity adapter: nil client pool")}
	}

	baseURL := defaultDailyEndpoint
	if u.BaseURL != "" {
		baseURL = strings.TrimSuffix(u.BaseURL, "/")
	}

	action := "generateContent"
	if req.Stream {
		action = "streamGenerateContent?alt=sse"
	}
	url := fmt.Sprintf("%s/v1internal:%s", baseURL, action)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return attemptResult{err: err}
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "antigravity/ide/2.11.0 darwin/arm64")

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

	if req.Stream && resp.StatusCode < 400 &&
		strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		n, relayErr := a.relayAntigravitySSE(ctx, w, resp.Body, t.UpstreamModel)
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

	// Translate non-streaming Antigravity response to OpenAI format
	openAIRespBytes, err := TranslateAntigravityToOpenAI(buf, t.UpstreamModel)
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

func (a *Adapter) relayAntigravitySSE(ctx context.Context, w http.ResponseWriter, body io.Reader, publicModel string) (int64, error) {
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
	state := &StreamState{Model: publicModel}

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return written, ctx.Err()
		default:
		}

		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			chunks, isDone, err := TranslateAntigravityChunkToOpenAI([]byte(dataStr), publicModel, state)
			if err != nil {
				if a.cfg.Logger != nil {
					a.cfg.Logger.Warn("antigravity chunk translation error", "err", err)
				}
				continue
			}
			for _, chunk := range chunks {
				n, werr := io.WriteString(w, "data: "+string(chunk)+"\n\n")
				written += int64(n)
				if werr != nil {
					return written, werr
				}
				if f != nil {
					f.Flush()
				}
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
				break
			}
		}
	}

	return written, scanner.Err()
}

func relayError(w http.ResponseWriter, status int, h http.Header, body []byte) {
	ct := h.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	if ra := h.Get("Retry-After"); ra != "" {
		w.Header().Set("Retry-After", ra)
	}
	w.WriteHeader(status)
	if len(body) > 0 {
		_, _ = w.Write(body)
	}
}
