package openai

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
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/dickymuliafiqri/firefly/internal/upstream"
)

// ClientPool is the outbound client cache the adapter dials through.
type ClientPool interface {
	// Client returns the http.Client for an upstream.
	Client(u *domain.Upstream) *http.Client
}

// BreakerLookup yields a breaker for an upstream name.
type BreakerLookup interface {
	// Allow reports ErrCircuitOpen (or nil) for the named upstream.
	Allow(name string) error
	// Report records the pre-response outcome for the named upstream.
	Report(name string, ok bool)
}

// RetryPolicy abstracts the transport retry timing policy. The concrete value
// is supplied by the wiring layer (upstream.RetryPolicy).
type RetryPolicy interface {
	// Attempts is the maximum number of tries (>=1).
	Attempts() int
	// Wait blocks for the backoff before attempt n, returning false if ctx dies.
	Wait(ctx context.Context, attempt int) bool
}

// KeyMetricsObserver receives notifications of key requests and cooldown events.
type KeyMetricsObserver interface {
	ObserveKeyCooldown(upstream, keyRef string)
	ObserveKeyRequest(upstream, keyRef string, status int)
}

// Config tunes the adapter.
type Config struct {
	// SecretLookup resolves a credential ref (ENV var name) to its value.
	SecretLookup func(ref string) (string, bool)
	// Retry policy for pre-first-byte failures.
	Retry RetryPolicy
	// Logger for per-attempt diagnostics.
	Logger *slog.Logger
	// MaxBufferedBytes caps non-streaming response buffering.
	MaxBufferedBytes int64
	// Metrics receives key health and saturation updates. Nil is safe.
	Metrics KeyMetricsObserver
	// Notifier receives automated key lifecycle actions (deactivate/delete). Nil is safe.
	Notifier ports.KeyActionNotifier
}

// Adapter implements ports.UpstreamAdapter for the OpenAI (and compatible)
// wire format. It is a near-transparent proxy with three jobs:
//
//  1. Rewrite the "model" field from the tenant-facing public name to the
//     upstream's private model name.
//  2. Scrub inbound headers (Authorization, hop-by-hop) and inject the
//     upstream secret from ENV.
//  3. Relay the response, streaming SSE chunk-wise or buffering JSON, while
//     mapping failures to OpenAI-shaped errors and driving the breaker.
type Adapter struct {
	pool    ClientPool
	breaker BreakerLookup
	cfg     Config
	logger  *slog.Logger
	metrics KeyMetricsObserver
}

// NewAdapter constructs an adapter. pool and breaker are required.
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

// Protocol identifies the dialect this adapter speaks.
func (a *Adapter) Protocol() domain.Protocol { return domain.ProtocolOpenAI }

// ErrUpstream is returned when the upstream could not be reached or returned an
// unusable response. It wraps the classification for the caller's error mapper.
type ErrUpstream struct {
	Status  int         // upstream status if any; 0 for transport errors
	Retried bool        // whether any retry was attempted
	Cause   error       // underlying transport error, if any
	Body    []byte      // buffered upstream response body for error statuses
	Header  http.Header // response headers from upstream (e.g. Retry-After)
}

func (e *ErrUpstream) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("upstream error: %v", e.Cause)
	}
	return fmt.Sprintf("upstream returned status %d", e.Status)
}
func (e *ErrUpstream) Unwrap() error { return e.Cause }

type attemptResult struct {
	status   int
	headers  http.Header
	streamed bool
	body     []byte
	err      error
}

// Forward sends req to the upstream named by t and writes the response to w.
//
// Retry safety: we retry ONLY when no byte has reached the client yet. The
// moment RelaySSE starts writing, retries stop. For non-streaming requests we
// buffer the upstream body first, so a 5xx can still be retried before we
// commit a status to the client.
//
// w MUST be an http.ResponseWriter (not just io.Writer) so streaming can flush;
// the ports interface widens it to io.Writer, so we accept it and require the
// concrete form here.
func (a *Adapter) Forward(ctx context.Context, t *domain.Target, req ports.ForwardRequest, w io.Writer) error {
	if t == nil || t.Upstream == nil {
		return errors.New("openai adapter: nil target")
	}
	respW, ok := w.(http.ResponseWriter)
	if !ok {
		return errors.New("openai adapter: writer is not an http.ResponseWriter")
	}
	u := t.Upstream

	// 1. Build the upstream body once (rewrite model), reused across retries.
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
			if a.cfg.Retry != nil {
				if !a.cfg.Retry.Wait(ctx, attempt) {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					return lastErr // client gone or policy says stop
				}
			}
			if a.cfg.Logger != nil {
				a.cfg.Logger.Warn("retrying upstream", "upstream", u.Name, "attempt", attempt, "key", t.CredentialRef)
			}
		}

		// Breaker gate: fail fast when the upstream is known-bad. A nil breaker
		// (mis-wire) means "always allow" rather than a nil-deref panic.
		if a.breaker != nil {
			if err := a.breaker.Allow(u.Name); err != nil {
				return &ErrUpstream{Status: http.StatusServiceUnavailable, Cause: err}
			}
		}

		res := a.attempt(ctx, u, req, t, body, respW, attempt)

		// Centralized classification: breaker reporting, key error policy,
		// metrics, and failover selection all live in the upstream package so
		// every adapter shares one correct implementation.
		decision := upstream.ProcessAttemptOutcome(u, t, upstream.AttemptOutcome{
			Status:    res.status,
			Err:       res.err,
			Headers:   res.headers,
			Committed: res.streamed || headerCommitted(respW),
		}, a.breaker, a.metrics, a.cfg.Notifier, a.logger)

		switch {
		case decision.Success:
			return nil
		case decision.StopCommitted:
			return res.err
		case decision.Failover:
			lastErr = &ErrUpstream{Status: res.status, Retried: true, Body: res.body, Header: res.headers}
			continue
		case decision.Relay:
			relayBufferedError(respW, &ErrUpstream{Status: res.status, Body: res.body, Header: res.headers})
			return nil
		default: // decision.Fail
			lastErr = &ErrUpstream{Status: res.status, Retried: attempt > 1, Cause: res.err, Body: res.body, Header: res.headers}
			if !decision.IsHostFailure {
				return lastErr
			}
		}
	}

	// Exhausted attempts: relay the last upstream error body if we have one,
	// otherwise let the caller map the transport error.
	var lastUE *ErrUpstream
	if errors.As(lastErr, &lastUE) && len(lastUE.Body) > 0 {
		if lastUE.Header != nil {
			if ra := lastUE.Header.Get("Retry-After"); ra != "" {
				respW.Header().Set("Retry-After", ra)
			}
		}
		writeUpstreamError(respW, lastUE.Status, lastUE.Body)
		return nil
	}
	return lastErr
}

// headerCommitted reports whether a status header has already been sent to client.
func headerCommitted(w http.ResponseWriter) bool {
	if rec, ok := w.(interface{ Committed() bool }); ok {
		return rec.Committed()
	}
	return false
}

// relayBufferedError writes a non-retryable upstream error verbatim.
func relayBufferedError(w http.ResponseWriter, ue *ErrUpstream) {
	if ue == nil || len(ue.Body) == 0 {
		WriteError(w, http.StatusBadGateway, TypeAPI, "upstream request failed")
		return
	}
	if ue.Header != nil {
		if ra := ue.Header.Get("Retry-After"); ra != "" {
			w.Header().Set("Retry-After", ra)
		}
	}
	writeUpstreamError(w, ue.Status, ue.Body)
}

// attempt performs ONE upstream call and relays the response. It reports the
// upstream status, headers, whether any byte reached the client, and errors.
func (a *Adapter) attempt(ctx context.Context, u *domain.Upstream, req ports.ForwardRequest, t *domain.Target, body []byte, w http.ResponseWriter, attempt int) attemptResult {
	if a.pool == nil {
		return attemptResult{err: errors.New("openai adapter: nil client pool")}
	}
	url := u.URLForAttempt(attempt) + req.Path
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, url, bytes.NewReader(body))
	if err != nil {
		return attemptResult{err: err}
	}

	// Header scrub + injection.
	copyUpstreamHeaders(httpReq.Header, req.Headers)
	httpReq.Header.Set("Content-Type", "application/json")
	secret, ok := a.secret(u, t)
	if !ok {
		ref := u.CredentialRef
		if t != nil && t.CredentialRef != "" {
			ref = t.CredentialRef
		}
		return attemptResult{err: fmt.Errorf("credential %q is not set in the environment", ref)}
	}
	httpReq.Header.Set("Authorization", "Bearer "+secret)
	for k, v := range u.ExtraHeaders {
		httpReq.Header.Set(k, v)
	}
	// Correlate with our request id for cross-system tracing.
	if rid := requestID(ctx); rid != "" {
		httpReq.Header.Set("X-Request-Id", rid)
	}

	resp, err := a.pool.Client(u).Do(httpReq)
	if err != nil {
		return attemptResult{err: err}
	}
	defer func() {
		// Drain fully + close so the connection returns cleanly to the pool (prevent socket leak).
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if req.Stream && resp.StatusCode < 400 &&
		strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		idle := time.Duration(u.StreamIdleTimeoutMs) * time.Millisecond
		// Defense in depth: if the upstream still returned a compressed body
		// (e.g. an ExtraHeaders-set Accept-Encoding, or a server that gzips
		// unconditionally), Go's transport will NOT have auto-decompressed it.
		// Relaying raw gzip/deflate bytes over SSE corrupts the stream, so we
		// decode here before framing.
		streamBody, decErr := decodeResponseBody(resp)
		if decErr != nil {
			return attemptResult{status: resp.StatusCode, headers: resp.Header.Clone(), err: decErr}
		}
		n, relayErr := RelaySSE(ctx, w, streamBody, idle)
		return attemptResult{
			status:   resp.StatusCode,
			headers:  resp.Header.Clone(),
			streamed: n > 0,
			err:      relayErr,
		}
	}

	// Non-streaming (or an upstream error while streaming was requested):
	// buffer so we can pick the status code and shape the error body. Decode any
	// compressed body first (mirrors the streaming path) so a gzip/deflate
	// response the transport did not auto-decompress is not relayed as raw bytes.
	decoded, decErr := decodeResponseBody(resp)
	if decErr != nil {
		return attemptResult{status: resp.StatusCode, headers: resp.Header.Clone(), err: decErr}
	}
	buf, rerr := RelayBuffered(decoded, a.cfg.MaxBufferedBytes)
	if rerr != nil {
		return attemptResult{status: resp.StatusCode, headers: resp.Header.Clone(), body: buf, err: rerr}
	}

	if resp.StatusCode >= 400 {
		// Do NOT write anything yet: Forward may still decide to retry.
		// Hand the buffered body and headers back so caller can inspect or relay.
		return attemptResult{
			status:  resp.StatusCode,
			headers: resp.Header.Clone(),
			body:    buf,
			err:     &ErrUpstream{Status: resp.StatusCode, Body: buf, Header: resp.Header.Clone()},
		}
	}

	// Pass through Content-Type and any useful headers.
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	n, werr := w.Write(buf)
	return attemptResult{
		status:   resp.StatusCode,
		headers:  resp.Header.Clone(),
		streamed: n > 0,
		body:     buf,
		err:      werr,
	}
}

// buildBody decodes just enough of the request to rewrite "model" to the
// upstream model name, then re-marshals. It preserves all other fields verbatim
// by decoding into a generic map — this keeps us forward-compatible with new
// OpenAI parameters without touching this code.
func (a *Adapter) buildBody(req ports.ForwardRequest, t *domain.Target) ([]byte, error) {
	// Prefer BodyBytes (already read by the handler); fall back to Body.
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
	// Overwrite only the model field; everything else is untouched.
	if t.UpstreamModel != "" {
		mb, _ := json.Marshal(t.UpstreamModel)
		obj["model"] = mb
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("re-marshal request body: %w", err)
	}
	return out, nil
}

// secret resolves the upstream credential value from SecretLookup or target KeySlot.
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
