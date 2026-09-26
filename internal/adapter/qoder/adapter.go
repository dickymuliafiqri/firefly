// Package qoder provides an upstream adapter for the Qoder IDE inference API,
// translating OpenAI-compatible chat/completions requests into Qoder's
// COSY-signed, WAF-encoded agent_chat_generation protocol on api3/api2.qoder.sh
// and translating the {statusCodeValue, body} SSE envelope back to OpenAI SSE.
package qoder

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

	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/dickymuliafiqri/firefly/internal/transport/httpx"
	"github.com/dickymuliafiqri/firefly/internal/transport/upstream"
	"github.com/tidwall/gjson"
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

// QoderIdentity carries the COSY-signing identity for a credential: the access
// token plus the stable userId and machineId the signature is bound to.
type QoderIdentity struct {
	Token     string
	UserID    string
	MachineID string
	Email     string
	Name      string
}

// IdentityLookup resolves a QoderIdentity for a key slot. Implementations pull
// userId/machineId from api_keys.account_metadata (harvester path). Return ok
// false to fall back to secret-embedded metadata.
type IdentityLookup func(ref string, apiKeyID int64) (QoderIdentity, bool)

// Config tunes the Qoder adapter.
type Config struct {
	TokenResolver    TokenResolver
	SecretLookup     func(ref string) (string, bool)
	IdentityLookup   IdentityLookup
	Retry            openai.RetryPolicy
	Logger           *slog.Logger
	MaxBufferedBytes int64
	Metrics          KeyMetricsObserver
	Notifier         ports.KeyActionNotifier
}

// Adapter implements ports.UpstreamAdapter for the Qoder protocol.
type Adapter struct {
	pool    ClientPool
	breaker BreakerLookup
	cfg     Config
	logger  *slog.Logger
	metrics KeyMetricsObserver
	models  *modelCache
	pats    *patCache
}

// NewAdapter constructs a Qoder adapter.
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
		models:  newModelCache(),
		pats:    newPATCache(),
	}
}

// Protocol identifies the wire dialect this adapter speaks.
func (a *Adapter) Protocol() domain.Protocol { return domain.ProtocolQoder }

// ListModels returns the routable Qoder models for a credential (best-effort),
// used by model-discovery probes. The model set is dynamic per account, so it is
// fetched live from the Qoder catalog rather than a static list.
func (a *Adapter) ListModels(ctx context.Context, u *domain.Upstream, t *domain.Target) ([]QoderModelInfo, error) {
	if u == nil {
		return nil, errors.New("qoder adapter: nil upstream")
	}
	id, ok := a.resolveIdentity(ctx, a.pool.Client(u), u, t)
	if !ok {
		return nil, errors.New("qoder adapter: could not resolve signing identity")
	}
	creds := cosyCreds{UserID: id.UserID, AuthToken: id.Token, MachineID: id.MachineID, Email: id.Email, Name: id.Name}
	return a.models.listModels(ctx, a.pool.Client(u), ResolveBaseURL(u.BaseURL, id.Token), creds)
}

type attemptResult struct {
	status   int
	headers  http.Header
	streamed bool
	body     []byte
	err      error
}

// Forward translates an OpenAI request to the Qoder protocol, COSY-signs it, and
// relays the response back as OpenAI SSE/JSON.
func (a *Adapter) Forward(ctx context.Context, t *domain.Target, req ports.ForwardRequest, w io.Writer) error {
	if t == nil || t.Upstream == nil {
		return errors.New("qoder adapter: nil target")
	}
	respW, ok := w.(http.ResponseWriter)
	if !ok {
		return errors.New("qoder adapter: writer is not an http.ResponseWriter")
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
	if len(bodyBytes) == 0 || !gjson.ValidBytes(bodyBytes) {
		openai.WriteError(respW, http.StatusBadRequest, openai.TypeInvalidRequest, "invalid json payload")
		return nil
	}

	attempts := 1
	if a.cfg.Retry != nil {
		attempts = a.cfg.Retry.Attempts()
	}
	if u.KeyRing != nil && u.KeyRing.SlotCount() > attempts {
		attempts = u.KeyRing.SlotCount()
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

// resolveIdentity resolves the COSY-signing identity for this target: the token
// plus userId/machineId. PAT tokens (pt-) are exchanged for job tokens and their
// userId is resolved via /userinfo. Non-PAT tokens use an explicit identity
// lookup or a JSON identity blob for userId/machineId.
func (a *Adapter) resolveIdentity(ctx context.Context, client *http.Client, u *domain.Upstream, t *domain.Target) (QoderIdentity, bool) {
	ref := u.CredentialRef
	var apiKeyID int64
	if t != nil && t.CredentialRef != "" {
		ref = t.CredentialRef
	}
	if t != nil && t.KeySlot != nil {
		apiKeyID = t.KeySlot.APIKeyID
		if t.KeySlot.Ref != "" {
			ref = t.KeySlot.Ref
		}
	}

	// 1. Explicit identity lookup (harvester account_metadata with userId).
	if a.cfg.IdentityLookup != nil {
		if id, ok := a.cfg.IdentityLookup(ref, apiKeyID); ok && id.Token != "" && id.UserID != "" {
			if id.MachineID == "" {
				id.MachineID = newUUID()
			}
			return id, true
		}
	}

	// Gather the raw secret/token from the layered sources.
	rawSecret := ""
	if t != nil && t.KeySlot != nil && t.KeySlot.Secret != "" {
		rawSecret = t.KeySlot.Secret
	}
	if rawSecret == "" && a.cfg.SecretLookup != nil && ref != "" {
		if s, ok := a.cfg.SecretLookup(ref); ok {
			rawSecret = s
		}
	}
	if rawSecret == "" && a.cfg.TokenResolver != nil && ref != "" {
		if tok, err := a.cfg.TokenResolver(ctx, ref); err == nil {
			rawSecret = tok
		}
	}
	if rawSecret == "" {
		return QoderIdentity{}, false
	}

	// 2. JSON identity blob {"token","userId","machineId",...}.
	if id, ok := parseIdentityBlob(rawSecret); ok {
		if id.MachineID == "" {
			id.MachineID = newUUID()
		}
		return id, true
	}

	token := strings.TrimSpace(rawSecret)

	// 3. PAT (pt-): exchange for a job token + resolve userId at runtime.
	if isPAT(token) {
		res, err := a.pats.resolve(ctx, client, token)
		if err != nil {
			a.logger.Warn("qoder PAT exchange failed", "ref", ref, "err", err)
			return QoderIdentity{}, false
		}
		return QoderIdentity{Token: res.jobToken, UserID: res.userID, MachineID: res.machineID}, true
	}

	// 4. Bare device/job token with no metadata — cannot COSY-sign without userId.
	return QoderIdentity{Token: token}, false
}

// parseIdentityBlob parses a JSON identity blob stored as the key secret.
func parseIdentityBlob(s string) (QoderIdentity, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") || !gjson.Valid(s) {
		return QoderIdentity{}, false
	}
	r := gjson.Parse(s)
	id := QoderIdentity{
		Token:     firstNonEmpty(r.Get("token").String(), r.Get("access_token").String(), r.Get("accessToken").String()),
		UserID:    firstNonEmpty(r.Get("userId").String(), r.Get("user_id").String(), r.Get("uid").String()),
		MachineID: firstNonEmpty(r.Get("machineId").String(), r.Get("machine_id").String()),
		Email:     r.Get("email").String(),
		Name:      firstNonEmpty(r.Get("name").String(), r.Get("displayName").String()),
	}
	if id.Token == "" || id.UserID == "" {
		return QoderIdentity{}, false
	}
	return id, true
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func (a *Adapter) attempt(ctx context.Context, u *domain.Upstream, req ports.ForwardRequest, t *domain.Target, bodyBytes []byte, w http.ResponseWriter) attemptResult {
	if a.pool == nil {
		return attemptResult{err: errors.New("qoder adapter: nil client pool")}
	}
	client := a.pool.Client(u)

	id, ok := a.resolveIdentity(ctx, client, u, t)
	if !ok {
		// Missing userId/token — surface a clean 401 so the operator reconnects.
		msg := "qoder credential missing token or userId; reconnect the account"
		buf := openaiErrorJSON(msg)
		return attemptResult{status: http.StatusUnauthorized, headers: jsonHeader(), body: buf,
			err: &openai.ErrUpstream{Status: http.StatusUnauthorized, Body: buf, Header: jsonHeader()}}
	}

	creds := cosyCreds{UserID: id.UserID, AuthToken: id.Token, MachineID: id.MachineID, Email: id.Email, Name: id.Name}

	// Base host: honor a custom host (tests + self-hosted deployments), but dial
	// the token-derived host whenever the configured value is a managed qoder
	// host (api3/api2) or empty, so a pinned base_url can never route a token to
	// the wrong host. See ResolveBaseURL.
	base := ResolveBaseURL(u.BaseURL, id.Token)

	// Resolve the public model to the qoder key + live model_config.
	qoderKey := strings.TrimPrefix(t.UpstreamModel, "qoder/")
	if qoderKey == "" {
		qoderKey = gjson.GetBytes(bodyBytes, "model").String()
	}
	// Free-tier alias: Qwen3.8-Flash lives at "qfmodel" (enable=true,
	// is_free=true, price 0). The legacy "qmodel_latest" placeholder is
	// disabled server-side, so normalize it (and common client-side
	// spellings) to "qfmodel" before the catalog lookup.
	switch strings.ToLower(qoderKey) {
	case "qmodel_latest", "qoder-latest", "qwen-3.8-flash", "qwen3.8-flash", "qwen-flash", "qwen":
		qoderKey = "qfmodel"
	}
	modelConfig, err := a.models.getModelConfig(ctx, client, base, creds, qoderKey)
	if err != nil {
		buf := openaiErrorJSON(err.Error())
		return attemptResult{status: http.StatusBadRequest, headers: jsonHeader(), body: buf,
			err: &openai.ErrUpstream{Status: http.StatusBadRequest, Body: buf, Header: jsonHeader()}}
	}

	payload, err := buildQoderPayload(bodyBytes, qoderKey, id.UserID, modelConfig)
	if err != nil {
		buf := openaiErrorJSON(err.Error())
		return attemptResult{status: http.StatusBadRequest, headers: jsonHeader(), body: buf,
			err: &openai.ErrUpstream{Status: http.StatusBadRequest, Body: buf, Header: jsonHeader()}}
	}

	// Encode (WAF bypass) then COSY-sign the ENCODED body.
	encoded := qoderEncodeBody(payload)
	url := base + "/algo" + QoderChatSigPath + QoderChatQuery + "&Encode=1"

	cosyHeaders, err := buildCosyHeaders(encoded, url, creds)
	if err != nil {
		buf := openaiErrorJSON("qoder cosy signing failed: " + err.Error())
		return attemptResult{status: http.StatusUnauthorized, headers: jsonHeader(), body: buf,
			err: &openai.ErrUpstream{Status: http.StatusUnauthorized, Body: buf, Header: jsonHeader()}}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return attemptResult{err: err}
	}
	for k, v := range cosyHeaders {
		httpReq.Header.Set(k, v)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	httpReq.Header.Set("X-Model-Key", qoderKey)
	httpReq.Header.Set("X-Model-Source", "system")
	// gzip triggers signature validation on Qoder's CDN; force identity.
	httpReq.Header.Set("Accept-Encoding", "identity")
	for k, v := range u.ExtraHeaders {
		httpReq.Header.Set(k, v)
	}
	if rid := httpx.RequestIDFrom(ctx); rid != "" {
		httpReq.Header.Set("X-Request-Id", rid)
	}

	resp, err := client.Do(httpReq)
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

	if req.Stream {
		n, billing, relayErr := relayQoderStream(ctx, w, resp.Body, "qoder/"+qoderKey)
		if billing {
			// Billing/quota block: classify as a credential error (Layer 1), no bytes sent.
			buf := openaiErrorJSON("qoder quota/billing block")
			return attemptResult{status: http.StatusTooManyRequests, headers: jsonHeader(), body: buf,
				err: &openai.ErrUpstream{Status: http.StatusTooManyRequests, Body: buf, Header: jsonHeader()}}
		}
		return attemptResult{status: http.StatusOK, headers: resp.Header.Clone(), streamed: n > 0, err: relayErr}
	}

	respBytes, billing, aggErr := aggregateQoderStream(ctx, resp.Body, "qoder/"+qoderKey)
	if billing {
		buf := openaiErrorJSON("qoder quota/billing block")
		return attemptResult{status: http.StatusTooManyRequests, headers: jsonHeader(), body: buf,
			err: &openai.ErrUpstream{Status: http.StatusTooManyRequests, Body: buf, Header: jsonHeader()}}
	}
	if aggErr != nil {
		return attemptResult{status: http.StatusBadGateway, headers: resp.Header.Clone(), err: aggErr}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	n, werr := w.Write(respBytes)
	return attemptResult{status: http.StatusOK, headers: resp.Header.Clone(), streamed: n > 0, body: respBytes, err: werr}
}

func (a *Adapter) maxBytes() int64 {
	if a.cfg.MaxBufferedBytes > 0 {
		return a.cfg.MaxBufferedBytes
	}
	return 32 << 20
}

func jsonHeader() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	return h
}

func openaiErrorJSON(msg string) []byte {
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "upstream_error"}})
	return b
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
	if status == 0 {
		status = http.StatusBadGateway
	}
	w.WriteHeader(status)
	if len(body) > 0 {
		_, _ = w.Write(body)
	}
}
