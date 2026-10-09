package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"sync"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/observability/trace"

	"github.com/dickymuliafiqri/firefly/internal/observability/metrics"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/dickymuliafiqri/firefly/internal/storage/turso"
	"github.com/dickymuliafiqri/firefly/internal/tokensaver"
	"github.com/dickymuliafiqri/firefly/internal/transport/httpx"
	"github.com/dickymuliafiqri/firefly/internal/transport/upstream"
	"github.com/tidwall/gjson"
)

// maxRequestBodyBytes caps how much of a client body we buffer. Chat payloads
// with large context windows can be several MiB; 32 MiB is a generous ceiling
// that still prevents a memory-exhaustion vector.
const (
	maxRequestBodyBytes   = 32 << 20
	initialRequestBodyCap = 16 * 1024  // 16 KiB pre-allocation covers most chat payloads
	maxRecycleBufferSize  = 512 * 1024 // 512 KiB; larger buffers are discarded to GC
)

var (
	requestBufferPool = sync.Pool{
		New: func() any {
			return bytes.NewBuffer(make([]byte, 0, initialRequestBodyCap))
		},
	}
	copyBufferPool = sync.Pool{
		New: func() any {
			b := make([]byte, 32*1024)
			return &b
		},
	}
)

const (
	maxTrackerBodyCap = 64 * 1024 // 64 KiB
	maxTrackerTailCap = 8 * 1024  // 8 KiB rolling window
)

type responseTracker struct {
	http.ResponseWriter
	bytesWritten int64
	linesWritten int64
	bodyBuf      []byte
	tailBuf      []byte
}

func (t *responseTracker) Write(p []byte) (int, error) {
	n, err := t.ResponseWriter.Write(p)
	t.bytesWritten += int64(n)
	t.linesWritten += int64(bytes.Count(p, []byte("\n")))

	if len(t.bodyBuf) < maxTrackerBodyCap {
		rem := maxTrackerBodyCap - len(t.bodyBuf)
		if len(p) <= rem {
			t.bodyBuf = append(t.bodyBuf, p...)
		} else {
			t.bodyBuf = append(t.bodyBuf, p[:rem]...)
		}
	}

	if len(p) >= maxTrackerTailCap {
		t.tailBuf = append(t.tailBuf[:0], p[len(p)-maxTrackerTailCap:]...)
	} else {
		t.tailBuf = append(t.tailBuf, p...)
		if len(t.tailBuf) > maxTrackerTailCap {
			t.tailBuf = t.tailBuf[len(t.tailBuf)-maxTrackerTailCap:]
		}
	}

	return n, err
}

func (t *responseTracker) Flush() {
	if f, ok := t.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// usageCounts carries the token counts parsed out of an upstream usage
// object. Cached tokens are reported separately by both wire shapes: OpenAI
// nests the read count under prompt_tokens_details, Anthropic uses flat
// cache_read_input_tokens / cache_creation_input_tokens fields.
type usageCounts struct {
	promptToks int
	compToks   int
	cachedRead int
	cacheWrite int
	// cachedInPrompt records whether the provider's prompt figure already
	// contains the cached reads. OpenAI does (prompt_tokens includes
	// cached_tokens); Anthropic does not (input_tokens excludes both cache
	// counters). Cost estimation subtracts the cached counts only for the
	// former, so a cache hit is never billed twice.
	cachedInPrompt bool
}

// parseUsageObject reads token counts from a gjson usage result, accepting
// both the OpenAI and the Anthropic field names. It reports ok only when at
// least one of prompt/completion is non-zero, so a usage object that carries
// nothing but cache counters never fabricates a billed request.
func parseUsageObject(usage gjson.Result) (usageCounts, bool) {
	var c usageCounts
	pt := usage.Get("prompt_tokens")
	// The field name is the wire shape: prompt_tokens is the OpenAI spelling
	// and already includes the cached reads, input_tokens is the Anthropic
	// one and excludes them.
	c.cachedInPrompt = pt.Exists()
	if !pt.Exists() {
		pt = usage.Get("input_tokens")
	}
	ct := usage.Get("completion_tokens")
	if !ct.Exists() {
		ct = usage.Get("output_tokens")
	}
	c.promptToks = int(pt.Int())
	c.compToks = int(ct.Int())

	// OpenAI: usage.prompt_tokens_details.cached_tokens
	if d := usage.Get("prompt_tokens_details.cached_tokens"); d.Exists() {
		c.cachedRead = int(d.Int())
	}
	// Anthropic: usage.cache_read_input_tokens / cache_creation_input_tokens
	if v := usage.Get("cache_read_input_tokens"); v.Exists() {
		c.cachedRead = int(v.Int())
	}
	if v := usage.Get("cache_creation_input_tokens"); v.Exists() {
		c.cacheWrite = int(v.Int())
	}

	if c.promptToks > 0 || c.compToks > 0 {
		return c, true
	}
	return usageCounts{}, false
}

func parseUsageFromBytes(data []byte) (usageCounts, bool) {
	if len(data) == 0 {
		return usageCounts{}, false
	}
	// Case 1: Standard JSON object with top-level "usage"
	if gjson.GetBytes(data, "usage").Exists() {
		if c, ok := parseUsageObject(gjson.GetBytes(data, "usage")); ok {
			return c, true
		}
	}

	// Case 2: SSE stream chunks with data: {...}
	lines := bytes.Split(data, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		if gjson.GetBytes(payload, "usage").Exists() {
			if c, ok := parseUsageObject(gjson.GetBytes(payload, "usage")); ok {
				return c, true
			}
		}
	}

	// Case 3: Embedded "usage" substring anywhere in chunk (e.g. Anthropic message_delta)
	idx := bytes.LastIndex(data, []byte(`"usage"`))
	if idx >= 0 {
		sub := data[idx:]
		usageBlock := append([]byte("{"), sub...)
		if gjson.GetBytes(usageBlock, "usage").Exists() {
			if c, ok := parseUsageObject(gjson.GetBytes(usageBlock, "usage")); ok {
				return c, true
			}
		}
	}

	return usageCounts{}, false
}

func (t *responseTracker) extractUsage() (usageCounts, bool) {
	if t == nil {
		return usageCounts{}, false
	}
	if c, found := parseUsageFromBytes(t.tailBuf); found {
		return c, true
	}
	if c, found := parseUsageFromBytes(t.bodyBuf); found {
		return c, true
	}
	return usageCounts{}, false
}

// tokenSaverDetail summarizes one pre-forward rewrite for the visualizer's
// Token Saver node: which pass ran and how many bytes it took out of the body.
// The operator guard can only grow a body (it appends a directive), so a
// non-positive delta is reported as marks alone instead of negative savings.
func tokenSaverDetail(guard, compress bool, saved int) string {
	var marks string
	switch {
	case guard && compress:
		marks = "guard+compress"
	case guard:
		marks = "guard"
	case compress:
		marks = "compress"
	default:
		return ""
	}
	if saved <= 0 {
		return marks
	}
	return marks + " -" + savedBytesText(saved)
}

func savedBytesText(n int) string {
	switch {
	case n < 1024:
		return strconv.Itoa(n) + "B"
	case n < 1<<20:
		return strconv.FormatFloat(float64(n)/1024, 'f', 1, 64) + "kB"
	default:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 2, 64) + "MB"
	}
}

func estimateInputTokens(body []byte) int {
	if len(body) == 0 {
		return 0
	}
	msgs := gjson.GetBytes(body, "messages")
	if msgs.IsArray() {
		totalChars := 0
		msgs.ForEach(func(_, val gjson.Result) bool {
			content := val.Get("content")
			if content.Type == gjson.String {
				totalChars += len(content.Str)
			} else if content.IsArray() {
				content.ForEach(func(_, part gjson.Result) bool {
					totalChars += len(part.Get("text").String())
					return true
				})
			}
			return true
		})
		if totalChars > 0 {
			toks := totalChars / 4
			if toks < 1 {
				toks = 1
			}
			return toks
		}
	}
	prompt := gjson.GetBytes(body, "prompt").String()
	if len(prompt) > 0 {
		toks := len(prompt) / 4
		if toks < 1 {
			toks = 1
		}
		return toks
	}
	input := gjson.GetBytes(body, "input")
	if input.Exists() {
		totalChars := 0
		if input.Type == gjson.String {
			totalChars = len(input.Str)
		} else if input.IsArray() {
			input.ForEach(func(_, part gjson.Result) bool {
				totalChars += len(part.String())
				return true
			})
		}
		if totalChars > 0 {
			toks := totalChars / 4
			if toks < 1 {
				toks = 1
			}
			return toks
		}
	}
	toks := len(body) / 6
	if toks < 1 && len(body) > 0 {
		toks = 1
	}
	return toks
}

// forwardEndpoint is the shared handler for /v1/chat/completions,
// /v1/completions, /v1/embeddings, and /v1/messages. The four share identical
// proxying semantics and differ only in the upstream path and the client wire
// protocol, so one handler serves all and the ingress carries the edges.
func (deps RouterDeps) forwardEndpoint(upstreamPath string, ing ingress) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenant, ok := httpx.RequireTenant(w, r)
		if !ok {
			return
		}
		snap := deps.currentSnapshot()
		if snap == nil {
			ing.WriteError(w, http.StatusServiceUnavailable, "config not loaded")
			return
		}

		// 0. Fast-fail if Content-Length explicitly exceeds maximum allowable body size.
		if r.ContentLength > maxRequestBodyBytes {
			ing.WriteError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}

		// 1. Read bounded body using pooled buffers to prevent GC churn under high concurrency.
		buf := requestBufferPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer func() {
			if buf.Cap() <= maxRecycleBufferSize {
				buf.Reset()
				requestBufferPool.Put(buf)
			}
		}()

		copyBuf := copyBufferPool.Get().(*[]byte)
		_, err := io.CopyBuffer(buf, io.LimitReader(r.Body, maxRequestBodyBytes+1), *copyBuf)
		copyBufferPool.Put(copyBuf)

		if err != nil {
			ing.WriteError(w, http.StatusBadRequest, "failed to read request body")
			return
		}
		if int64(buf.Len()) > maxRequestBodyBytes {
			ing.WriteError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		body := buf.Bytes()

		// 2. Minimal decode for routing (zero-allocation fast path via gjson).
		// A malformed body is a client error, not a 500: fail with the surface's
		// own invalid-request envelope.
		if len(body) > 0 && !gjson.ValidBytes(body) {
			ing.WriteError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		openAIBody, model, stream, tokensIn, parseErr := ing.ParseBody(body)
		if parseErr != nil {
			ing.WriteError(w, http.StatusBadRequest, parseErr.Error())
			return
		}
		body = openAIBody

		capture := deps.Traces.Start(trace.Meta{
			ID:     httpx.RequestIDFrom(r.Context()),
			Method: r.Method,
			Path:   r.URL.Path,
			Model:  model,
			Tenant: tenant.Name,
			Stream: stream,
		})
		capture.Stage(trace.StageReceived, strconv.Itoa(len(body))+" bytes")
		defer capture.Finish(0, nil)

		// 3a. Budget guard. A tenant that has already spent its cap is refused
		// before any upstream work happens, so an exhausted budget costs nothing
		// and the spend counter is untouched by the rejection itself. The check
		// sits after the trace starts so the refusal is still visible in the
		// request visualiser.
		if tenant.BudgetExceeded() {
			if deps.Logger != nil {
				deps.Logger.Warn("tenant budget exceeded",
					"request_id", httpx.RequestIDFrom(r.Context()),
					"tenant", tenant.Name,
					"budget_micros", tenant.BudgetMicros,
					"spent_micros", tenant.SpentMicros.Load())
			}
			deps.recordLog(LiveLog{
				ID:        httpx.RequestIDFrom(r.Context()),
				Timestamp: time.Now().UnixMilli(),
				Method:    r.Method,
				Path:      r.URL.Path,
				Status:    http.StatusTooManyRequests,
				Model:     model,
				Tenant:    tenant.Name,
				Stream:    stream,
				TokensIn:  tokensIn,
				Tokens:    tokensIn,
				Error:     "tenant budget exceeded",
			})
			capture.Fail(http.StatusTooManyRequests, nil)
			w.Header().Set("Retry-After", "60")
			ing.WriteError(w, http.StatusTooManyRequests,
				"tenant budget exceeded; top up or raise the budget to continue")
			return
		}

		// 3. Resolve the routing target (model -> upstream + credential).
		var canUseUpstream func(string) bool
		if deps.Breakers != nil {
			canUseUpstream = func(name string) bool {
				return deps.Breakers.Allow(name) == nil
			}
		}
		var quotaResetAt time.Time
		quotaNow := time.Now()
		canUseCandidate := func(c domain.TargetCandidate) bool {
			reset, blocked := deps.quotaBlockedCandidate(c, quotaNow)
			if blocked {
				if quotaResetAt.IsZero() {
					quotaResetAt = reset
				}
				return false
			}
			return true
		}
		target, fallbackUsed, err := snap.ResolveTargetWithCandidates(tenant, model, canUseUpstream, canUseCandidate)
		if err != nil {
			if errors.Is(err, domain.ErrProviderQuotaExhausted) {
				// Every candidate was vetoed for quota and nothing else: answer
				// with the provider's own reset time, not a misleading 503.
				retryAfter := quotaRetryAfter(quotaResetAt)
				if deps.Logger != nil {
					deps.Logger.Warn("no route: provider quota exhausted",
						"request_id", httpx.RequestIDFrom(r.Context()),
						"model", model, "retry_after_s", retryAfter)
				}
				deps.recordLog(LiveLog{
					ID:            httpx.RequestIDFrom(r.Context()),
					Timestamp:     time.Now().UnixMilli(),
					Method:        r.Method,
					Path:          r.URL.Path,
					Status:        http.StatusTooManyRequests,
					DurationMs:    0,
					Model:         model,
					Upstream:      "",
					KeyRef:        "",
					Tenant:        tenant.Name,
					Stream:        stream,
					TokensIn:      tokensIn,
					TokensOut:     0,
					Tokens:        tokensIn,
					EstimatedCost: microsToUSD(estimateRequestCost(snap, model, "", tokensIn, 0, 0, 0)),
					Error:         "provider quota exhausted for every candidate",
				})
				capture.Fail(http.StatusTooManyRequests, nil)
				w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
				ing.WriteError(w, http.StatusTooManyRequests,
					"provider quota exhausted for every upstream serving model "+model+
						"; retry after the window resets")
				return
			}
			status := http.StatusInternalServerError
			var re *domain.ResolveError
			if errors.As(err, &re) {
				switch re.Kind {
				case domain.ResolveModelNotFound:
					status = http.StatusNotFound
				case domain.ResolveModelForbidden:
					status = http.StatusForbidden
				case domain.ResolveTenantInvalid:
					status = http.StatusUnauthorized
				case domain.ResolveUpstreamUnavailable:
					status = http.StatusServiceUnavailable
				}
			} else if errors.Is(err, domain.ErrAllKeysExhausted) {
				status = http.StatusTooManyRequests
			}
			deps.recordLog(LiveLog{
				ID:            httpx.RequestIDFrom(r.Context()),
				Timestamp:     time.Now().UnixMilli(),
				Method:        r.Method,
				Path:          r.URL.Path,
				Status:        status,
				DurationMs:    0,
				Model:         model,
				Upstream:      "",
				Tenant:        tenant.Name,
				Stream:        stream,
				TokensIn:      tokensIn,
				TokensOut:     0,
				Tokens:        tokensIn,
				EstimatedCost: microsToUSD(estimateRequestCost(snap, model, "", tokensIn, 0, 0, 0)),
				Error:         err.Error(),
			})
			ing.WriteResolveError(w, err)
			return
		}
		capture.Stage(trace.StageResolve, model)
		if target.Upstream != nil {
			note := "primary"
			if fallbackUsed {
				note = "breaker fallback"
			}
			capture.Candidate(target.Upstream.Name, "", trace.CandidateChosen, note)
		}
		if target.Upstream != nil {
			target.Upstream.Inflight.Add(1)
			defer target.Upstream.Inflight.Add(-1)
		}
		if fallbackUsed && deps.Logger != nil {
			fromUpstream := ""
			if combo, isCombo := snap.Combo(model); isCombo && len(combo.Models) > 0 {
				if firstModel, ok := snap.Model(combo.Models[0]); ok {
					fromUpstream = firstModel.Upstream
				}
			}
			deps.Logger.Warn("upstream fallback triggered",
				"request_id", httpx.RequestIDFrom(r.Context()),
				"from_upstream", fromUpstream,
				"to_upstream", target.Upstream.Name,
				"model", model,
			)
		}
		if target.Upstream != nil {
			capture.Stage(trace.StageRoute, target.Upstream.Name)
		}
		if combo, isCombo := snap.Combo(model); isCombo {
			for _, member := range combo.Models {
				me, ok := snap.Model(member)
				if !ok || me.Upstream == "" {
					continue
				}
				if target.Upstream == nil || me.Upstream != target.Upstream.Name {
					capture.Candidate(me.Upstream, "", trace.CandidateSkipped, "combo member "+member)
				}
			}
		}

		// 4. Per-credential aggregate limit. AdmissionMiddleware already took
		//    the tenant slot; here we bound the shared upstream key. This runs
		//    AFTER routing precisely so the credential ref is exact.
		var releaseCred limits.ReleaseFunc
		keyRef := target.CredentialRef
		if target.KeySlot != nil && target.KeySlot.Ref != "" {
			keyRef = target.KeySlot.Ref
			releaseCred, err = deps.Limiter.AcquireKeySlot(target.KeySlot)
		} else {
			releaseCred, err = deps.Limiter.AcquireCredential(
				target.CredentialRef,
				target.Upstream.CredentialRPS,
				target.Upstream.CredentialMaxConcurrent,
			)
		}
		capture.SetTarget(target.Upstream.Name, string(target.Upstream.Protocol), keyRef)
		capture.Stage(trace.StageKey, trace.MaskRef(keyRef))

		if err != nil {
			if errors.Is(err, limits.ErrRateLimited) {
				deps.Metrics.ObserveKeyCooldown(target.Upstream.Name, keyRef)
				deps.recordLog(LiveLog{
					ID:            httpx.RequestIDFrom(r.Context()),
					Timestamp:     time.Now().UnixMilli(),
					Method:        r.Method,
					Path:          r.URL.Path,
					Status:        http.StatusTooManyRequests,
					DurationMs:    0,
					Model:         model,
					Upstream:      target.Upstream.Name,
					KeyRef:        keyRef,
					Tenant:        tenant.Name,
					Stream:        stream,
					TokensIn:      tokensIn,
					TokensOut:     0,
					Tokens:        tokensIn,
					EstimatedCost: microsToUSD(estimateRequestCost(snap, model, target.UpstreamModel, tokensIn, 0, 0, 0)),
					Error:         "upstream credential capacity exhausted",
				})
				capture.Fail(http.StatusTooManyRequests, nil)
				w.Header().Set("Retry-After", "1")
				ing.WriteError(w, http.StatusTooManyRequests,
					"upstream credential capacity exhausted; retry shortly")
				return
			}
			capture.Fail(http.StatusInternalServerError, nil)

			ing.WriteError(w, http.StatusInternalServerError, "admission control error")
			return
		}
		deps.Metrics.IncKeyInflight(target.Upstream.Name, keyRef)
		defer func() {
			deps.Metrics.DecKeyInflight(target.Upstream.Name, keyRef)
			releaseCred()
		}()

		// 4c. Prompt guard + Token Saver. Both rewrite the chat body, so the
		// input-token estimate is recomputed once after the final shape settles.
		//
		// Operator system prompt (Settings -> Token Saver -> System Prompt):
		// injected independently of the Token Saver master switch so the guard
		// stays active even when compression is off, and only for endpoints that
		// actually carry a messages array. It rewrites the OpenAI-shaped body
		// before adapter.Forward, so every protocol is covered — the anthropic
		// adapter folds the system block into its own schema the same way it
		// already does for persona directives.
		// The rewrite is reported to the visualizer as trace.StageTokenSaver, and
		// only when a pass actually changed the body: the Line canvas draws a
		// Token Saver node on the request path and uses that stage to tell a
		// rewritten request from a pass-through one. Nothing is allocated when no
		// pass rewrites anything.
		bodyModified := false
		if upstreamPath == "/chat/completions" {
			ts := snap.TokenSaver()
			bodyLenBefore := len(body)
			guardApplied := false
			// Per-model directive first: it specializes behavior for the routed
			// model (for a combo, the member that was actually selected). The
			// global operator guard is appended afterwards so it keeps the final
			// word inside the system block. Both share InjectSystemPrompt, so the
			// rewrite is idempotent and multimodal-safe.
			if target.SystemPrompt != "" {
				if transformed, modified := tokensaver.InjectSystemPrompt(body, target.SystemPrompt); modified {
					body = transformed
					bodyModified = true
					guardApplied = true
				}
			}
			if ts.SystemPrompt != "" {
				if transformed, modified := tokensaver.InjectSystemPrompt(body, ts.SystemPrompt); modified {
					body = transformed
					bodyModified = true
					guardApplied = true
				}
			}
			compressApplied := false
			if ts.Enabled {
				if transformed, modified := tokensaver.Process(body, ts); modified {
					body = transformed
					bodyModified = true
					compressApplied = true
				}
			}
			if bodyModified {
				capture.Stage(trace.StageTokenSaver,
					tokenSaverDetail(guardApplied, compressApplied, bodyLenBefore-len(body)))
				tokensIn = estimateInputTokens(body)
			}
		}

		// 5. Forward. The adapter scrubs headers, injects the secret, rewrites
		//    the model, relays the response, and drives the breaker/retry.
		fwdReq := ports.ForwardRequest{
			Method:    http.MethodPost,
			Path:      upstreamPath,
			BodyBytes: body,
			Headers:   r.Header.Clone(),
			Stream:    stream,
		}

		adapter, err := deps.adapterFor(target.Upstream.Protocol)
		if err != nil {
			ing.WriteError(w, http.StatusNotImplemented, err.Error())
			capture.Fail(http.StatusNotImplemented, err)

			return
		}
		capture.Stage(trace.StageAttempt, string(target.Upstream.Protocol))
		start := time.Now()
		reqID := httpx.RequestIDFrom(r.Context())
		upstreamName := ""
		if target.Upstream != nil {
			upstreamName = target.Upstream.Name
		}
		deps.recordLog(LiveLog{
			ID:            reqID,
			Timestamp:     start.UnixMilli(),
			Method:        r.Method,
			Path:          r.URL.Path,
			Status:        0, // In-flight / Active
			DurationMs:    0,
			Model:         model,
			Upstream:      upstreamName,
			KeyRef:        keyRef,
			Tenant:        tenant.Name,
			Stream:        stream,
			TokensIn:      tokensIn,
			TokensOut:     0,
			Tokens:        tokensIn,
			EstimatedCost: microsToUSD(estimateRequestCost(snap, model, target.UpstreamModel, tokensIn, 0, 0, 0)),
		})

		tracker := &responseTracker{ResponseWriter: ing.WrapWriter(w, model, stream)}
		fwdErr := adapter.Forward(trace.WithCapture(r.Context(), capture), target, fwdReq, tracker)
		if finalizer, ok := tracker.ResponseWriter.(interface{ finalize(error) }); ok {
			finalizer.finalize(fwdErr)
		}
		elapsed := time.Since(start)

		// 6. Record usage regardless of outcome (a failed call still consumed
		//    a credential slot and upstream capacity).
		deps.Usage.Record(target.CredentialRef, tenant.Name, model, 1)

		// 6b. Record upstream outcome/latency for operators.
		deps.Metrics.ObserveUpstream(target.Upstream.Name, model, upstreamOutcome(fwdErr), elapsed)

		// 6c. Record key-level requests and status for operator visibility.
		keyStatus := http.StatusOK
		var ue *openai.ErrUpstream
		if fwdErr == nil {
			keyStatus = http.StatusOK
		} else if errors.As(fwdErr, &ue) && ue.Status > 0 {
			keyStatus = ue.Status
			// An upstream rate limit is the ground truth about the account: when
			// it contradicts a positive quota reading, the cached picture is
			// stale and gets dropped so the next read is real. Layer 1 only —
			// the breaker and key policy already saw this 429.
			if ue.Status == http.StatusTooManyRequests || ue.Status == http.StatusConflict {
				deps.noteQuotaRateLimit(keyRefOrTarget(target))
			}
		} else if errors.Is(fwdErr, context.Canceled) {
			keyStatus = 499
		} else {
			keyStatus = http.StatusBadGateway
		}
		deps.Metrics.ObserveKeyRequest(target.Upstream.Name, keyRef, keyStatus)

		var errStr string
		if fwdErr != nil {
			errStr = fwdErr.Error()
		}
		tokensOut := 0
		cachedRead, cacheWrite := 0, 0
		cachedInPrompt := false
		if u, ok := tracker.extractUsage(); ok {
			if u.promptToks > 0 {
				tokensIn = u.promptToks
			}
			if u.compToks > 0 {
				tokensOut = u.compToks
			}
			cachedRead, cacheWrite = u.cachedRead, u.cacheWrite
			cachedInPrompt = u.cachedInPrompt
		}
		if tokensOut <= 0 && keyStatus == http.StatusOK && tracker.bytesWritten > 0 {
			if stream {
				tokensOut = int(tracker.linesWritten / 2)
				if tokensOut <= 0 {
					tokensOut = int(tracker.bytesWritten / 60)
				}
			} else {
				tokensOut = int(tracker.bytesWritten / 4)
			}
			if tokensOut < 1 {
				tokensOut = 1
			}
		}
		totTokens := tokensIn + tokensOut
		billableIn := billableInputTokens(tokensIn, cachedRead, cacheWrite, cachedInPrompt)
		costMicros := estimateRequestCost(snap, model, target.UpstreamModel, billableIn, tokensOut, cachedRead, cacheWrite)
		totCost := microsToUSD(costMicros)

		if totTokens > 0 && tenant != nil && tenant.UsedTokens != nil {
			tenant.UsedTokens.Add(int64(totTokens))
			if flusher, ok := deps.Usage.(*turso.UsageFlusher); ok && flusher != nil {
				key := tenant.APIKey
				if key == "" {
					key = tenant.KeyHash
				}
				flusher.RecordTenantTokens(key, int64(totTokens), int64(cachedRead), int64(cacheWrite))
			}
		}

		// Spend is recorded even when the upstream produced no token counts:
		// a request that returned an error still consumed quota, and dropping it
		// would let a failing upstream be used to drain a budget for free.
		if tenant != nil {
			if crossed := tenant.AddSpend(costMicros); crossed {
				deps.emitTenantBudgetWarning(tenant)
			}
		}

		deps.recordLog(LiveLog{
			ID:               reqID,
			Timestamp:        start.UnixMilli(),
			Method:           r.Method,
			Path:             r.URL.Path,
			Status:           keyStatus,
			DurationMs:       elapsed.Milliseconds(),
			Model:            model,
			Upstream:         upstreamName,
			KeyRef:           keyRef,
			Tenant:           tenant.Name,
			Stream:           stream,
			TokensIn:         tokensIn,
			TokensOut:        tokensOut,
			Tokens:           totTokens,
			EstimatedCost:    totCost,
			CostMicros:       costMicros,
			CachedReadTokens: cachedRead,
			CacheWriteTokens: cacheWrite,
			Error:            errStr,
		})

		capture.SetUsage(tokensIn, tokensOut)
		capture.Finish(keyStatus, fwdErr)

		if fwdErr != nil {
			deps.logForwardFailure(r.Context(), target, fwdErr, elapsed)
			// If nothing was written yet we can still emit an error envelope;
			// if streaming already started, the only honest action is to stop
			// (the client sees a truncated stream).
			if !httpx.HeaderCommitted(w) {
				writeUpstreamFailure(w, fwdErr)
			}
		}
	}
}

// keyRefOrTarget resolves the credential reference a request would use, before
// the concurrency slot is acquired. The quota gate needs it for metrics and
// logging; the authoritative value is recomputed just after.
func keyRefOrTarget(target *domain.Target) string {
	if target == nil {
		return ""
	}
	if target.KeySlot != nil && target.KeySlot.Ref != "" {
		return target.KeySlot.Ref
	}
	return target.CredentialRef
}

// recordLog dispatches the request log to LiveLogs (and persistent storage) or directly to Analytics.
func (deps RouterDeps) recordLog(log LiveLog) {
	if deps.LiveLogs != nil {
		deps.LiveLogs.Publish(log)
		return
	}
	if deps.Analytics != nil {
		_ = deps.Analytics.Record(context.Background(), log)
	}
}

// writeResolveError maps a domain.ResolveError to the correct OpenAI status.
func writeResolveError(w http.ResponseWriter, err error) {
	if errors.Is(err, domain.ErrAllKeysExhausted) {
		w.Header().Set("Retry-After", "1")
		openai.WriteError(w, http.StatusTooManyRequests, openai.TypeRateLimit,
			"all upstream credentials exhausted; retry shortly")
		return
	}
	var re *domain.ResolveError
	if errors.As(err, &re) {
		switch re.Kind {
		case domain.ResolveModelNotFound:
			openai.WriteError(w, http.StatusNotFound, openai.TypeNotFound,
				"the model '"+re.Model+"' does not exist or is not enabled")
			return
		case domain.ResolveModelForbidden:
			openai.WriteError(w, http.StatusForbidden, openai.TypePermission,
				"tenant is not permitted to use model '"+re.Model+"'")
			return
		case domain.ResolveTenantInvalid:
			openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthenticated")
			return
		case domain.ResolveUpstreamUnavailable:
			openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI,
				"no available upstream for model '"+re.Model+"' (upstream disabled or circuit open)")
			return
		}
	}
	openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, "model resolution failed")
}

// writeUpstreamFailure maps a forwarding error to an OpenAI-shaped response.
func writeUpstreamFailure(w http.ResponseWriter, err error) {
	var ue *openai.ErrUpstream
	if errors.As(err, &ue) {
		switch {
		case errors.Is(ue.Cause, context.Canceled):
			// Client went away; nothing useful to write.
			return
		case ue.Status >= 400 && ue.Status < 500:
			// We already relayed the upstream 4xx body; avoid double-writing.
			return
		case ue.Status == http.StatusServiceUnavailable:
			openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI,
				"upstream temporarily unavailable (circuit open)")
			return
		default:
			openai.WriteError(w, http.StatusBadGateway, openai.TypeAPI,
				"upstream request failed")
			return
		}
	}
	openai.WriteError(w, http.StatusBadGateway, openai.TypeAPI, "upstream request failed")
}

// upstreamOutcome classifies a forwarding error into a low-cardinality metric
// label. A nil error is a success; otherwise we distinguish client cancels,
// circuit-open rejections, and generic upstream failures.
func upstreamOutcome(err error) string {
	switch {
	case err == nil:
		return metrics.OutcomeOK
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return metrics.OutcomeClientCancel
	case errors.Is(err, upstream.ErrCircuitOpen):
		return metrics.OutcomeCircuitOpen
	default:
		return metrics.OutcomeUpstreamError
	}
}

func (deps RouterDeps) logForwardFailure(ctx context.Context, t *domain.Target, err error, elapsed time.Duration) {
	if deps.Logger == nil {
		return
	}
	attrs := []any{
		"upstream", t.Upstream.Name,
		"upstream_model", t.UpstreamModel,
		"duration_ms", elapsed.Milliseconds(),
		"err", err.Error(),
	}
	if errors.Is(err, context.Canceled) {
		deps.Logger.Debug("client disconnected during forward", attrs...)
		return
	}
	deps.Logger.Error("upstream forward failed", attrs...)
}

// handleOptionsCompress handles preflight CORS for the compression endpoint.
func (deps RouterDeps) handleOptionsCompress(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
	w.WriteHeader(http.StatusNoContent)
}

// handleCompress provides a standalone API for compressing prompts and tool outputs.
func (deps RouterDeps) handleCompress(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	defer r.Body.Close()

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		// Report an oversized body as 413 rather than a generic 400 so clients
		// can distinguish "shrink the payload" from "fix the JSON".
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			openai.WriteError(w, http.StatusRequestEntityTooLarge, openai.TypeInvalidRequest, "request body too large")
			return
		}
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "read body: "+err.Error())
		return
	}

	prompt := gjson.GetBytes(bodyBytes, "prompt").String()
	maxChars := int(gjson.GetBytes(bodyBytes, "max_chars").Int())
	if maxChars <= 0 {
		maxChars = 12000
	}

	if prompt != "" {
		res := tokensaver.CompressText(prompt, maxChars)
		_ = json.NewEncoder(w).Encode(res)
		return
	}

	// If messages array was supplied, run Process directly on the payload
	if gjson.GetBytes(bodyBytes, "messages").Exists() {
		cfg := domain.TokenSaverConfig{
			Enabled:            true,
			CompressToolOutput: true,
			TerseOutput:        gjson.GetBytes(bodyBytes, "terse").Bool(),
			MinimalCode:        gjson.GetBytes(bodyBytes, "minimal_code").Bool(),
			CompressContext:    true,
			MaxToolOutputChars: maxChars,
		}
		transformed, _ := tokensaver.Process(bodyBytes, cfg)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(transformed)
		return
	}

	openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "provide either prompt or messages in payload")
}
