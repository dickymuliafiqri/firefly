// Package cline provides an upstream adapter for Cline OAuth endpoints,
// supporting chat completions forwarding and SSE streaming response relays.
package cline

import (
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
)

const defaultClineBaseURL = "https://api.cline.bot/api/v1"

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

// Config tunes the Cline adapter.
type Config struct {
	TokenResolver    TokenResolver
	SecretLookup     func(ref string) (string, bool)
	Retry            openai.RetryPolicy
	Logger           *slog.Logger
	MaxBufferedBytes int64
	Metrics          KeyMetricsObserver
}

// Adapter implements ports.UpstreamAdapter for the Cline API.
type Adapter struct {
	pool    ClientPool
	breaker BreakerLookup
	cfg     Config
	logger  *slog.Logger
	metrics KeyMetricsObserver
}

// NewAdapter constructs a new Cline adapter.
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
	return domain.ProtocolCline
}

type attemptResult struct {
	status   int
	headers  http.Header
	streamed bool
	body     []byte
	err      error
}

// Forward handles request rewriting, credential injection, outbound forwarding to Cline,
// and unwrapping non-stream responses or streaming SSE.
func (a *Adapter) Forward(ctx context.Context, t *domain.Target, req ports.ForwardRequest, w io.Writer) error {
	if t == nil || t.Upstream == nil {
		return errors.New("cline adapter: nil target")
	}
	respW, ok := w.(http.ResponseWriter)
	if !ok {
		return errors.New("cline adapter: writer is not an http.ResponseWriter")
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
		if res.streamed {
			return res.err
		}

		if a.metrics != nil && t.KeySlot != nil {
			a.metrics.ObserveKeyRequest(u.Name, t.KeySlot.Ref, res.status)
		}

		if res.status > 0 && res.status < 400 && res.err == nil {
			if a.breaker != nil {
				a.breaker.Report(u.Name, true)
			}
			return nil
		}

		isUpstreamFailure := res.status >= 500 || (res.status == 0 && res.err != nil)
		if a.breaker != nil && isUpstreamFailure {
			a.breaker.Report(u.Name, false)
		}

		// Handle 429 Too Many Requests
		if res.status == http.StatusTooManyRequests {
			ra := ""
			if res.headers != nil {
				ra = res.headers.Get("Retry-After")
			}
			if u.KeyRing != nil && t.KeySlot != nil {
				upstream.FromDomain(u.KeyRing).Handle429(t.KeySlot.Ref, ra)
			}
			if a.metrics != nil && t.KeySlot != nil {
				a.metrics.ObserveKeyCooldown(u.Name, t.KeySlot.Ref)
				a.metrics.ObserveKeyRequest(u.Name, t.KeySlot.Ref, http.StatusTooManyRequests)
			}
			if u.KeyRing != nil && len(u.KeyRing.Slots) > 1 {
				if nextSlot, selErr := u.KeyRing.SelectKey(time.Now().UnixNano()); selErr == nil && nextSlot != nil {
					t.KeySlot = nextSlot
					t.CredentialRef = nextSlot.Ref
					lastErr = &openai.ErrUpstream{Status: res.status, Retried: true, Body: res.body, Header: res.headers}
					continue
				}
			}
			relayError(respW, res.status, res.headers, res.body)
			return nil
		}

		// Handle 401 Unauthorized
		if res.status == http.StatusUnauthorized {
			if u.KeyRing != nil && t.KeySlot != nil {
				upstream.FromDomain(u.KeyRing).Handle401(t.KeySlot.Ref)
			}
			if a.metrics != nil && t.KeySlot != nil {
				a.metrics.ObserveKeyRequest(u.Name, t.KeySlot.Ref, http.StatusUnauthorized)
			}
			if u.KeyRing != nil && len(u.KeyRing.Slots) > 1 {
				if nextSlot, selErr := u.KeyRing.SelectKey(time.Now().UnixNano()); selErr == nil && nextSlot != nil {
					t.KeySlot = nextSlot
					t.CredentialRef = nextSlot.Ref
					lastErr = &openai.ErrUpstream{Status: res.status, Retried: true, Body: res.body, Header: res.headers}
					continue
				}
			}
			relayError(respW, res.status, res.headers, res.body)
			return nil
		}

		if res.status >= 400 && res.status < 500 {
			relayError(respW, res.status, res.headers, res.body)
			return nil
		}

		if u.KeyRing != nil && len(u.KeyRing.Slots) > 1 {
			if nextSlot, selErr := u.KeyRing.SelectKey(time.Now().UnixNano()); selErr == nil && nextSlot != nil {
				t.KeySlot = nextSlot
				t.CredentialRef = nextSlot.Ref
			}
		}

		lastErr = &openai.ErrUpstream{Status: res.status, Retried: attempt > 1, Cause: res.err, Body: res.body, Header: res.headers}
		if !isUpstreamFailure {
			return lastErr
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

func (a *Adapter) attempt(ctx context.Context, u *domain.Upstream, req ports.ForwardRequest, t *domain.Target, body []byte, w http.ResponseWriter, attempt int) attemptResult {
	if a.pool == nil {
		return attemptResult{err: errors.New("cline adapter: nil client pool")}
	}

	baseURL := defaultClineBaseURL
	if u.BaseURL != "" {
		baseURL = strings.TrimRight(u.BaseURL, "/")
	}

	endpointURL := baseURL
	if !strings.HasSuffix(endpointURL, "/chat/completions") {
		endpointURL += "/chat/completions"
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(body))
	if err != nil {
		return attemptResult{err: err}
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("HTTP-Referer", "https://cline.bot")
	httpReq.Header.Set("X-Title", "Cline")

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
		idle := time.Duration(u.StreamIdleTimeoutMs) * time.Millisecond
		n, relayErr := openai.RelaySSE(ctx, w, resp.Body, idle)
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

	// Unwrap Cline non-streaming envelope if present: {"success":true,"data":{...}}
	unwrapped := UnwrapEnvelope(buf)

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	n, werr := w.Write(unwrapped)
	return attemptResult{
		status:   resp.StatusCode,
		headers:  resp.Header.Clone(),
		streamed: n > 0,
		body:     unwrapped,
		err:      werr,
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
		return []byte("{}"), nil
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("invalid JSON body: %w", err)
	}

	if t.UpstreamModel != "" {
		mb, _ := json.Marshal(t.UpstreamModel)
		obj["model"] = mb
	}
	return json.Marshal(obj)
}

// UnwrapEnvelope extracts the inner data object if Cline wraps the completion in
// {"success":true, "data":{...choices...}}. Otherwise, it returns body untouched.
func UnwrapEnvelope(body []byte) []byte {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}
	if gjson.GetBytes(body, "success").Bool() {
		data := gjson.GetBytes(body, "data")
		if data.Exists() && data.IsObject() {
			return []byte(data.Raw)
		}
	}
	return body
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
