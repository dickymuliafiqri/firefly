package server

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/observability/trace"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/dickymuliafiqri/firefly/internal/storage/turso"
	"github.com/dickymuliafiqri/firefly/internal/tokensaver"
	"github.com/dickymuliafiqri/firefly/internal/transport/httpx"
	"github.com/dickymuliafiqri/firefly/internal/transport/upstream"
)

// comboAttemptTTFB bounds how long one combo attempt may wait for its first
// byte before the next member is tried. It is a watchdog rather than a context
// deadline on purpose: a deadline would also abort a healthy long SSE stream
// once it expired, which is exactly what AGENTS.md forbids on the data plane.
//
// It is a variable only so tests can shrink it; nothing mutates it at runtime.
var comboAttemptTTFB = 20 * time.Second

// comboAttemptMinRunway is how much of the client's remaining deadline must be
// left before another member is worth contacting. Below it a retry would only
// burn a provider request that the gateway can no longer deliver.
const comboAttemptMinRunway = 2 * time.Second

// comboAttemptContext carries the request-scoped state one forward attempt
// needs. It exists so the attempt's defers — the credential ticket, the key and
// upstream in-flight counters — run when that attempt ends rather than when the
// whole request ends. Holding a credential ticket until handler return would
// deadlock the second attempt of an upstream configured with MaxConcurrent 1.
type comboAttemptContext struct {
	deps         RouterDeps
	ing          ingress
	r            *http.Request
	snap         *domain.CatalogSnapshot
	tenant       *domain.Tenant
	capture      *trace.Capture
	model        string
	upstreamPath string
	stream       bool

	// body and tokensIn are the current (possibly rewritten) request shape. The
	// loop resets them from the original bytes before each retry, because the
	// prompt guard specializes the body for the member actually used.
	body     []byte
	tokensIn int

	attempt int
}

// comboAttemptOutcome is what one attempt produced, in the form the accounting
// block in forwardEndpoint consumes. It mirrors the locals that used to live
// inline, so the metric/log/cost code stays unchanged.
type comboAttemptOutcome struct {
	keyRef   string
	tracker  *responseTracker
	err      error
	start    time.Time
	elapsed  time.Duration
	tokensIn int

	// logID is this attempt's console row. The live-log hub keys rows by ID, so
	// a retry needs its own or it would overwrite the record of the member that
	// failed before it.
	logID string
	// forwarded reports whether the adapter was actually reached. An attempt
	// refused by admission control never touched a provider, so it must not be
	// counted as inbound usage or as an upstream latency sample.
	forwarded bool
	// ttfbTimeout reports that the per-attempt first-byte watchdog cancelled
	// this attempt. It is tracked separately because the cancellation reaches
	// the adapter as context.Canceled, which is otherwise the signature of a
	// client that walked away — a case that must never be retried. Without this
	// distinction a hung member would be treated as an abandoned request and the
	// combo would give up instead of moving on.
	ttfbTimeout bool
}

// resolvedMember is a member the loop has picked but not attempted yet.
type resolvedMember struct {
	target       *domain.Target
	fallbackUsed bool
}

// forward runs one attempt: upstream in-flight accounting, credential
// acquisition, the prompt guard and Token Saver rewrite, then the adapter call.
// Every terminal condition writes into aw instead of the real writer, so a
// failure can still be discarded and another combo member tried.
func (c *comboAttemptContext) forward(aw *comboAttemptWriter, target *domain.Target) comboAttemptOutcome {
	deps, r, ing, capture := c.deps, c.r, c.ing, c.capture
	out := comboAttemptOutcome{start: time.Now(), tokensIn: c.tokensIn}

	if target.Upstream != nil {
		target.Upstream.Inflight.Add(1)
		defer target.Upstream.Inflight.Add(-1)
	}

	// 4. Per-credential aggregate limit. AdmissionMiddleware already took the
	//    tenant slot; here we bound the shared upstream key. This runs AFTER
	//    routing precisely so the credential ref is exact, and it is released
	//    when this attempt ends so the next member can acquire its own.
	var releaseCred limits.ReleaseFunc
	keyRef := target.CredentialRef
	var acquireErr error
	out.logID = attemptLogID(httpx.RequestIDFrom(r.Context()), c.attempt)
	if target.KeySlot != nil && target.KeySlot.Ref != "" {
		keyRef = target.KeySlot.Ref
		// An AlwaysAvailable upstream is exempt from the slot's revocation and
		// cooldown state (see domain.Upstream.AlwaysAvailable): reserving
		// capacity must not re-introduce the rejection the resolver just
		// bypassed, or the request dies here with a 429 that no upstream ever
		// saw. RPS and concurrency still apply.
		if target.Upstream.AlwaysAvailable {
			releaseCred, acquireErr = deps.Limiter.AcquireKeySlotLenient(target.KeySlot)
		} else {
			releaseCred, acquireErr = deps.Limiter.AcquireKeySlot(target.KeySlot)
		}
	} else {
		releaseCred, acquireErr = deps.Limiter.AcquireCredential(
			target.CredentialRef,
			target.Upstream.CredentialRPS,
			target.Upstream.CredentialMaxConcurrent,
		)
	}
	out.keyRef = keyRef
	capture.SetTarget(target.Upstream.Name, string(target.Upstream.Protocol), keyRef)
	capture.Stage(trace.StageKey, trace.MaskRef(keyRef))

	if acquireErr != nil {
		out.err = acquireErr
		if errors.Is(acquireErr, limits.ErrRateLimited) {
			deps.Metrics.ObserveKeyCooldown(target.Upstream.Name, keyRef)
			c.recordAttemptFailure(http.StatusTooManyRequests, keyRef, "", "upstream credential capacity exhausted")
			aw.Header().Set("Retry-After", "1")
			ing.WriteError(aw, http.StatusTooManyRequests,
				"upstream credential capacity exhausted; retry shortly")
			return out
		}
		c.recordAttemptFailure(http.StatusInternalServerError, keyRef, "", "admission control error")
		ing.WriteError(aw, http.StatusInternalServerError, "admission control error")
		return out
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
	//
	// The body is reset from the original before every attempt (see the loop),
	// because the per-model directive belongs to the member actually used: a
	// combo whose first member was tried with its own directive must not send
	// that directive to the second member.
	body := c.body
	bodyModified := false
	if c.upstreamPath == "/chat/completions" {
		ts := c.snap.TokenSaver()
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
			out.tokensIn = estimateInputTokens(body)
		}
	}

	// 5. Forward. The adapter scrubs headers, injects the secret, rewrites
	//    the model, relays the response, and drives the breaker/retry.
	fwdReq := ports.ForwardRequest{
		Method:    http.MethodPost,
		Path:      c.upstreamPath,
		BodyBytes: body,
		Headers:   r.Header.Clone(),
		Stream:    c.stream,
	}

	adapter, adapterErr := deps.adapterFor(target.Upstream.Protocol)
	if adapterErr != nil {
		out.err = adapterErr
		c.recordAttemptFailure(http.StatusNotImplemented, keyRef, target.ResolvedModel, adapterErr.Error())
		ing.WriteError(aw, http.StatusNotImplemented, adapterErr.Error())
		return out
	}
	capture.Stage(trace.StageAttempt, string(target.Upstream.Protocol))

	upstreamName := ""
	if target.Upstream != nil {
		upstreamName = target.Upstream.Name
	}
	deps.recordLog(LiveLog{
		ID:            out.logID,
		Timestamp:     out.start.UnixMilli(),
		Method:        r.Method,
		Path:          r.URL.Path,
		Status:        0, // In-flight / Active
		DurationMs:    0,
		Model:         c.model,
		ResolvedModel: target.ResolvedModel,
		Upstream:      upstreamName,
		KeyRef:        keyRef,
		Tenant:        c.tenant.Name,
		Stream:        c.stream,
		TokensIn:      out.tokensIn,
		TokensOut:     0,
		Tokens:        out.tokensIn,
		Attempt:       c.attempt,
		EstimatedCost: microsToUSD(estimateRequestCost(c.snap, c.model, target.ResolvedModel, target.UpstreamModel, out.tokensIn, 0, 0, 0)),
	})

	tracker := &responseTracker{ResponseWriter: ing.WrapWriter(aw, c.model, c.stream)}
	out.tracker = tracker

	// A per-attempt TTFB watchdog: an attempt that never produces a first byte
	// is cancelled so the next member can be tried, while an attempt that has
	// already committed is left alone to stream for as long as it needs.
	attemptCtx, cancelAttempt := context.WithCancel(r.Context())
	defer cancelAttempt()
	var ttfbExpired atomic.Bool
	ttfbTimer := time.AfterFunc(comboAttemptTTFB, func() {
		if !aw.Committed() {
			ttfbExpired.Store(true)
			cancelAttempt()
		}
	})
	defer ttfbTimer.Stop()

	out.forwarded = true
	fwdErr := adapter.Forward(trace.WithCapture(attemptCtx, c.capture), target, fwdReq, tracker)
	if finalizer, ok := tracker.ResponseWriter.(interface{ finalize(error) }); ok {
		finalizer.finalize(fwdErr)
	}
	out.err = fwdErr
	out.elapsed = time.Since(out.start)
	// Read the watchdog verdict after disarming it, so "the timer fired" and
	// "the flag was set" cannot disagree: a Stop that returns false means the
	// callback already ran or is running, and the flag is written before the
	// cancellation the callback performs.
	timerFired := !ttfbTimer.Stop()
	out.ttfbTimeout = timerFired && ttfbExpired.Load() && !aw.Committed()
	return out
}

// recordAttemptFailure records an attempt that failed before the adapter ran.
// The final record for a request still comes from the accounting block; this one
// exists so a discarded attempt is visible in the console.
func (c *comboAttemptContext) recordAttemptFailure(status int, keyRef, resolvedModel, message string) {
	c.deps.recordLog(LiveLog{
		ID:            attemptLogID(httpx.RequestIDFrom(c.r.Context()), c.attempt),
		Timestamp:     time.Now().UnixMilli(),
		Method:        c.r.Method,
		Path:          c.r.URL.Path,
		Status:        status,
		DurationMs:    0,
		Model:         c.model,
		ResolvedModel: resolvedModel,
		KeyRef:        keyRef,
		Attempt:       c.attempt,
		Stream:        c.stream,
		TokensIn:      c.tokensIn,
		Tokens:        c.tokensIn,
		Error:         message,
	})
}

// attemptLogID namespaces the console row for a retry. The live-log hub keeps
// one row per ID, so a second attempt on the same request would otherwise
// overwrite the record of the member that failed before it.
func attemptLogID(reqID string, attempt int) string {
	if attempt <= 1 || reqID == "" {
		return reqID
	}
	return reqID + "#" + strconv.Itoa(attempt)
}

// deadlineSoon reports whether ctx has less than d of runway left. A context
// with no deadline never counts as soon.
func deadlineSoon(ctx context.Context, d time.Duration) bool {
	deadline, ok := ctx.Deadline()
	return ok && time.Until(deadline) < d
}

// traceRoute records the routing decision for the visualizer. A retry re-runs
// it, so each member attempt contributes its own resolve/route/candidate stages
// and the Line canvas shows the hop the request actually took.
func (deps RouterDeps) traceRoute(
	capture *trace.Capture,
	snap *domain.CatalogSnapshot,
	model string,
	target *domain.Target,
	fallbackUsed bool,
) {
	capture.Stage(trace.StageResolve, model)
	if target.ResolvedModel != "" {
		capture.SetResolvedModel(target.ResolvedModel)
	}
	if target.Upstream != nil {
		note := "primary"
		if fallbackUsed {
			note = "breaker fallback"
		}
		if target.ResolvedModel != "" && target.ResolvedModel != model {
			note += " (" + target.ResolvedModel + ")"
		}
		capture.Candidate(target.Upstream.Name, "", trace.CandidateChosen, note)
		capture.Stage(trace.StageRoute, target.Upstream.Name)
	}
	if fallbackUsed && deps.Logger != nil {
		fromUpstream := ""
		// The skipped primary is the first concrete model the combo graph
		// reaches, which may sit one or more nested combos deep.
		if combo, isCombo := snap.Combo(model); isCombo {
			if members := snap.ResolveMemberCandidates(combo.Name); len(members) > 0 {
				if firstModel, ok := snap.Model(members[0]); ok {
					fromUpstream = firstModel.Upstream
				}
			}
		}
		deps.Logger.Warn("upstream fallback triggered",
			"from_upstream", fromUpstream,
			"to_upstream", target.Upstream.Name,
			"model", model,
		)
	}
	if combo, isCombo := snap.Combo(model); isCombo {
		// Expand nested combos so the trace names real skipped upstreams
		// instead of a combo that never reaches a host by itself.
		for _, member := range snap.ResolveMemberCandidates(combo.Name) {
			me, ok := snap.Model(member)
			if !ok || me.Upstream == "" {
				continue
			}
			if target.Upstream == nil || me.Upstream != target.Upstream.Name {
				capture.Candidate(me.Upstream, "", trace.CandidateSkipped, "combo member "+member)
			}
		}
	}
}

// settle publishes the attempt whose response the client keeps: the recorded
// status and body are committed to the real writer, usage and cost are
// accounted, and the terminal record replaces the in-flight row. It is the only
// place an attempt becomes visible, which is what keeps a discarded attempt from
// inflating usage.
func (c *comboAttemptContext) settle(
	w http.ResponseWriter,
	aw *comboAttemptWriter,
	target *domain.Target,
	out comboAttemptOutcome,
	fallbackFrom string,
) {
	deps, r, capture := c.deps, c.r, c.capture
	status := aw.Status()

	// Publish whatever the attempt recorded. A transport failure recorded no
	// status at all, so there is nothing to publish and the error envelope below
	// is written against the real writer instead.
	if status != 0 {
		aw.Permit()
	}

	keyRef, tracker, fwdErr := out.keyRef, out.tracker, out.err
	tokensIn := out.tokensIn
	elapsed := out.elapsed
	upstreamName := ""
	if target.Upstream != nil {
		upstreamName = target.Upstream.Name
	}

	// 6. Record usage regardless of outcome (a failed call still consumed a
	//    credential slot and upstream capacity) — but only when a provider was
	//    actually contacted. An attempt refused by admission control never was.
	if out.forwarded {
		deps.Usage.Record(target.CredentialRef, c.tenant.Name, c.model, 1)
		observedModel := c.model
		if target.ResolvedModel != "" {
			observedModel = target.ResolvedModel
		}
		deps.Metrics.ObserveUpstream(upstreamName, observedModel, upstreamOutcome(fwdErr), elapsed)
	}

	// 6b. Record key-level requests and status for operator visibility.
	keyStatus := http.StatusOK
	var ue *openai.ErrUpstream
	switch {
	case fwdErr == nil:
		keyStatus = http.StatusOK
	case errors.As(fwdErr, &ue) && ue.Status > 0:
		keyStatus = ue.Status
		// An upstream rate limit is the ground truth about the account: when it
		// contradicts a positive quota reading, the cached picture is stale and
		// gets dropped so the next read is real. Layer 1 only — the breaker and
		// key policy already saw this 429.
		if ue.Status == http.StatusTooManyRequests || ue.Status == http.StatusConflict {
			deps.noteQuotaRateLimit(keyRefOrTarget(target))
		}
	case errors.Is(fwdErr, context.Canceled):
		keyStatus = 499
	default:
		keyStatus = http.StatusBadGateway
	}
	// An attempt that answered with a status of its own — an admission refusal, a
	// missing adapter, an adapter-synthesized error body — is reported with that
	// status: the client received it, so the console must not contradict the
	// response with a generic 502.
	if keyStatus == http.StatusBadGateway && status >= 400 {
		keyStatus = status
	}
	if out.forwarded {
		deps.Metrics.ObserveKeyRequest(upstreamName, keyRef, keyStatus)
	}

	var errStr string
	if fwdErr != nil {
		errStr = sanitizeUpstreamError(fwdErr)
	}
	tokensOut := 0
	cachedRead, cacheWrite := 0, 0
	cachedInPrompt := false
	if tracker != nil {
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
	}
	if tokensOut <= 0 && keyStatus == http.StatusOK && tracker != nil && tracker.bytesWritten > 0 {
		if c.stream {
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
	costMicros := estimateRequestCost(c.snap, c.model, target.ResolvedModel, target.UpstreamModel, billableIn, tokensOut, cachedRead, cacheWrite)
	totCost := microsToUSD(costMicros)

	if totTokens > 0 && c.tenant != nil && c.tenant.UsedTokens != nil {
		c.tenant.UsedTokens.Add(int64(totTokens))
		if flusher, ok := deps.Usage.(*turso.UsageFlusher); ok && flusher != nil {
			key := c.tenant.APIKey
			if key == "" {
				key = c.tenant.KeyHash
			}
			flusher.RecordTenantTokens(key, int64(totTokens), int64(cachedRead), int64(cacheWrite))
		}
	}

	// Spend is recorded even when the upstream produced no token counts: a
	// request that returned an error still consumed quota, and dropping it would
	// let a failing upstream be used to drain a budget for free.
	if c.tenant != nil {
		if crossed := c.tenant.AddSpend(costMicros); crossed {
			deps.emitTenantBudgetWarning(c.tenant)
		}
	}

	deps.recordLog(LiveLog{
		ID:               out.logID,
		Timestamp:        out.start.UnixMilli(),
		Method:           r.Method,
		Path:             r.URL.Path,
		Status:           keyStatus,
		DurationMs:       elapsed.Milliseconds(),
		Model:            c.model,
		ResolvedModel:    target.ResolvedModel,
		Upstream:         upstreamName,
		KeyRef:           keyRef,
		Tenant:           c.tenant.Name,
		Stream:           c.stream,
		TokensIn:         tokensIn,
		TokensOut:        tokensOut,
		Tokens:           totTokens,
		EstimatedCost:    totCost,
		CostMicros:       costMicros,
		CachedReadTokens: cachedRead,
		CacheWriteTokens: cacheWrite,
		Attempt:          c.attempt,
		FallbackFrom:     fallbackFrom,
		Error:            errStr,
	})

	capture.SetUsage(tokensIn, tokensOut)
	capture.Finish(keyStatus, fwdErr)

	if fwdErr != nil {
		deps.logForwardFailure(r.Context(), target, fwdErr, elapsed)
		// If nothing was written yet we can still emit an error envelope; if
		// streaming already started, the only honest action is to stop (the
		// client sees a truncated stream).
		if !httpx.HeaderCommitted(w) {
			writeUpstreamFailure(w, fwdErr)
		}
	}
}

// comboAttemptBudget is how many top-level combo members one client request may
// contact. A direct model gets exactly one, which is what keeps the non-combo
// path byte-identical to the single pass it always was.
func comboAttemptBudget(snap *domain.CatalogSnapshot, model string) int {
	combo, ok := snap.Combo(model)
	if !ok {
		return 1
	}
	switch n := len(combo.Models); {
	case n > domain.MaxComboFailoverAttempts:
		return domain.MaxComboFailoverAttempts
	case n >= 1:
		return n
	default:
		return 1
	}
}

// retryableAcrossMembers decides whether the next combo member may be tried.
// The rules are the ones documented on the feature: a transport failure, a
// timeout, any 5xx (including an adapter-synthesized one), circuit-open, a
// credential error and a hard 429 are worth another member, while a client
// cancellation and a request-shaped 4xx are not — every member would answer the
// same way, and retrying a cancelled request only bills a provider for output
// nobody reads.
func retryableAcrossMembers(err error, status int, hdr http.Header, ctx context.Context) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var ue *openai.ErrUpstream
	if errors.As(err, &ue) {
		if ue.Cause != nil && errors.Is(ue.Cause, context.Canceled) {
			return false
		}
		if status == 0 {
			status = ue.Status
		}
	}
	if errors.Is(err, upstream.ErrCircuitOpen) {
		return true
	}
	switch {
	case status == 0:
		// No status at all: a transport failure (dial, reset, DNS, TLS) or the
		// TTFB watchdog. The member was never answered, which is the case the
		// combo fallback exists for.
		return err != nil
	case status == http.StatusTooManyRequests:
		// Only worth another member when this one is limited for longer than the
		// request can wait; otherwise waiting is cheaper than a second provider.
		wait, ok := retryAfterDuration(hdr)
		if !ok {
			return true
		}
		deadline, hasDeadline := ctx.Deadline()
		if !hasDeadline {
			return true
		}
		return wait > time.Until(deadline)
	case status == http.StatusUnauthorized,
		status == http.StatusPaymentRequired,
		status == http.StatusForbidden,
		status == http.StatusNotFound:
		return true
	case status >= 500:
		return true
	}
	// 400, 409, 413, 422 and every other 4xx: the request itself is at fault.
	return false
}

// retryAfterDuration reads an explicit Retry-After header as a duration, and
// reports whether the upstream sent one at all. Parsing is delegated to the
// keyring's RFC 7231 reader (integer seconds, decimal seconds, HTTP-date), but
// the value is deliberately NOT capped: the question here is whether the wait
// outlasts this request, not how long a key should cool, so a ten-minute
// Retry-After must read as ten minutes rather than as the cooldown ceiling.
func retryAfterDuration(hdr http.Header) (time.Duration, bool) {
	if hdr == nil {
		return 0, false
	}
	raw := strings.TrimSpace(hdr.Get("Retry-After"))
	if raw == "" {
		return 0, false
	}
	return upstream.ParseRetryAfter(raw, time.Second, math.MaxInt64), true
}
