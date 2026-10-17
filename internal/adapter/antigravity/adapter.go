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

	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/dickymuliafiqri/firefly/internal/transport/httpx"
	"github.com/dickymuliafiqri/firefly/internal/transport/upstream"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
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

// ConnectionLookup yields the OAuth connection metadata matching a reference.
type ConnectionLookup func(ctx context.Context, ref string) (*domain.OAuthConnection, error)

// Config tunes the Antigravity adapter.
type Config struct {
	TokenResolver    TokenResolver
	ConnectionLookup ConnectionLookup
	SecretLookup     func(ref string) (string, bool)
	Retry            openai.RetryPolicy
	Logger           *slog.Logger
	MaxBufferedBytes int64
	Metrics          KeyMetricsObserver
	IdleTimeout      time.Duration
	Notifier         ports.KeyActionNotifier
	// Signatures is the Gemini 3 thought signature store. Nil creates a
	// default bounded store; inject one to share state across adapters or
	// observe it in tests.
	Signatures *SignatureStore
}

// Adapter implements ports.UpstreamAdapter for the Google Antigravity Cloud Code protocol.
type Adapter struct {
	pool    ClientPool
	breaker BreakerLookup
	cfg     Config
	logger  *slog.Logger
	metrics KeyMetricsObserver
	// sigs remembers Gemini 3 thought signatures per tool call id so they can
	// be replayed when a client echoes a tool call back without the field
	// (OpenAI clients discard unknown fields).
	sigs *SignatureStore
}

// NewAdapter constructs an Antigravity adapter.
func NewAdapter(pool ClientPool, breaker BreakerLookup, cfg Config) *Adapter {
	l := cfg.Logger
	if l == nil {
		l = slog.Default()
	}
	sigs := cfg.Signatures
	if sigs == nil {
		sigs = NewSignatureStore()
	}
	return &Adapter{
		pool:    pool,
		breaker: breaker,
		cfg:     cfg,
		logger:  l,
		metrics: cfg.Metrics,
		sigs:    sigs,
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

	projectID := a.resolveProjectID(ctx, u, t)

	// Backfill Gemini 3 thought signatures the client dropped from echoed
	// tool calls, so the second turn of a tool-calling loop validates.
	bodyBytes = a.enrichThoughtSignatures(bodyBytes)

	antigravityBody, err := TranslateOpenAIToAntigravity(bodyBytes, t.UpstreamModel, projectID)
	if err != nil {
		openai.WriteError(respW, http.StatusBadRequest, openai.TypeInvalidRequest, err.Error())
		return nil
	}

	// Key failover may consume one attempt per credential (a 429 on one key is
	// worth retrying on the next), but a HOST failure is not credential-specific:
	// every slot in the ring points at the same host, so retrying it once per key
	// only multiplies the wait and the breaker's failure count. The ring size as
	// the bound did exactly that — a 4-credential antigravity upstream turned one
	// slow request into 4 consecutive 30s TTFB timeouts (121s of latency) and 4
	// breaker failures, so two such requests crossed FailureThreshold and every
	// following request answered 503 "circuit open" for the whole cooldown. Host
	// retries are therefore bounded by the retry policy; key rotation is not.
	hostAttempts := 1
	if a.cfg.Retry != nil {
		hostAttempts = a.cfg.Retry.Attempts()
	}
	attempts := hostAttempts
	if u.KeyRing != nil && u.KeyRing.SlotCount() > attempts {
		attempts = u.KeyRing.SlotCount()
	}

	var lastErr error
	hostFailures := 0
retryLoop:
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
			if bErr := upstream.AllowUpstream(a.breaker, u); bErr != nil {
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
			newProjectID := a.resolveProjectID(ctx, u, t)
			if newProjectID != projectID {
				projectID = newProjectID
				if updatedBody, uErr := TranslateOpenAIToAntigravity(bodyBytes, t.UpstreamModel, projectID); uErr == nil {
					antigravityBody = updatedBody
				}
			}
			continue
		case decision.Relay:
			upstream.RelayError(respW, res.status, res.headers, res.body)
			return nil
		default: // decision.Fail
			lastErr = &openai.ErrUpstream{Status: res.status, Retried: attempt > 1, Cause: res.err, Body: res.body, Header: res.headers}
			if !decision.IsHostFailure {
				return lastErr
			}
			hostFailures++
			if hostFailures >= hostAttempts {
				// Break, do not return: the post-loop block relays the
				// upstream body (it is how a provider 5xx envelope reaches the
				// client verbatim). Returning here answered 200 with an empty
				// body instead of the upstream error.
				break retryLoop
			}
		}
	}

	var lastUE *openai.ErrUpstream
	if errors.As(lastErr, &lastUE) && len(lastUE.Body) > 0 {
		upstream.RelayError(respW, lastUE.Status, lastUE.Header, lastUE.Body)
		return nil
	}
	return lastErr
}

// enrichThoughtSignatures backfills the Gemini 3 thought signature onto echoed
// tool calls the client dropped it from, using the signature remembered when
// the original call was relayed. The tool call id is a protocol-required field
// every client round-trips, so the store lookup also covers clients (Cline,
// Roo, the OpenAI SDKs, ...) that discard unknown JSON fields entirely.
func (a *Adapter) enrichThoughtSignatures(body []byte) []byte {
	if a.sigs == nil || len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}
	for i, msg := range gjson.GetBytes(body, "messages").Array() {
		for j, tc := range msg.Get("tool_calls").Array() {
			// Client preserved the original field: TranslateOpenAIToAntigravity
			// replays it verbatim, nothing to inject.
			if tc.Get("thought_signature").Exists() || tc.Get("function.thought_signature").Exists() {
				continue
			}
			id := tc.Get("id").String()
			if id == "" {
				continue
			}
			sig := a.sigs.Lookup(id)
			if sig == "" {
				continue
			}
			path := fmt.Sprintf("messages.%d.tool_calls.%d.thought_signature", i, j)
			if updated, err := sjson.SetBytes(body, path, sig); err == nil {
				body = updated
			}
		}
	}
	return body
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

func (a *Adapter) resolveProjectID(ctx context.Context, u *domain.Upstream, t *domain.Target) string {
	if a.cfg.ConnectionLookup != nil {
		ref := u.CredentialRef
		if t != nil && t.CredentialRef != "" {
			ref = t.CredentialRef
		}
		if conn, err := a.cfg.ConnectionLookup(ctx, ref); err == nil && conn != nil {
			if pid := conn.ProviderSpecificData["project_id"]; pid != "" {
				return pid
			}
		}
	}
	return GenerateProjectID()
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
	// The two identity headers the Cloud Code unified gateway documents besides
	// Authorization and User-Agent. The live probe (NewProbeRequest) already
	// sends them; real traffic must present the same fingerprint.
	httpReq.Header.Set("X-Goog-Api-Client", googleAPIClient)
	httpReq.Header.Set("Client-Metadata", clientMetadata)

	token, ok := a.resolveToken(ctx, u, t)
	if !ok {
		ref := u.CredentialRef
		if t != nil && t.CredentialRef != "" {
			ref = t.CredentialRef
		}
		// A credential that cannot be resolved is a KEY error (Layer 1), not a
		// host failure: the upstream was never dialed. It used to be reported
		// with status 0 (a transport shape), so ProcessAttemptOutcome classified
		// it as a host failure — tripping the circuit breaker for a healthy host,
		// advancing the key's consecutive-error counter as a transport drop,
		// retrying three times, and surfacing as a generic 502 instead of the 401
		// it is. A synthetic 401 keeps it in Layer 1 and still lets the ring fail
		// over to another connection.
		cause := fmt.Errorf("credential %q could not be resolved", ref)
		return attemptResult{
			status: http.StatusUnauthorized,
			body:   openai.ErrorBody(openai.TypeAuthentication, cause.Error()),
			err:    &openai.ErrUpstream{Status: http.StatusUnauthorized, Cause: cause},
		}
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

	// Translate non-streaming Antigravity response to OpenAI format. A 2xx body
	// we cannot parse is a protocol anomaly (a new upstream error envelope, a
	// retired shape), not a host outage: answer with the reason and mark the
	// attempt committed so it neither retries nor reports a host failure to the
	// breaker. It previously returned a synthetic 500 with no body, which tripped
	// the breaker and ended as an opaque 502.
	openAIRespBytes, err := TranslateAntigravityToOpenAI(buf, t.UpstreamModel)
	if err != nil {
		openai.WriteError(w, http.StatusBadGateway, openai.TypeAPI,
			"upstream returned an unreadable response: "+err.Error())
		return attemptResult{
			status:   resp.StatusCode,
			headers:  resp.Header.Clone(),
			streamed: true,
			err:      err,
		}
	}

	// Remember Gemini 3 thought signatures keyed by tool call id so the next
	// turn can replay them when the client echoes the calls back.
	recordToolSignaturesFromOpenAI(a.sigs, openAIRespBytes)

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

	// A malformed upstream chunk must not become one log line per chunk. A bad
	// stream would otherwise emit a warning at token rate, and because each
	// message embeds the upstream's own text, every one would open its own log
	// group. Count them and report once, when the stream ends.
	var (
		translateErrs int
		firstErr      error
	)
	defer func() {
		if translateErrs > 0 && a.cfg.Logger != nil {
			a.cfg.Logger.Warn("antigravity chunk translation failed; affected chunks were dropped",
				"errors", translateErrs, "err", firstErr)
		}
	}()

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
				translateErrs++
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			// Drain Gemini 3 thought signatures surfaced by this chunk into the
			// store keyed by tool call id.
			if a.sigs != nil && len(state.ToolSignatures) > 0 {
				for _, ts := range state.ToolSignatures {
					a.sigs.Record(ts.ID, ts.Sig)
				}
				state.ToolSignatures = state.ToolSignatures[:0]
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
