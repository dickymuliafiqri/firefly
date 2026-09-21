package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/transport/httpx"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/observability/metrics"
	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/dickymuliafiqri/firefly/internal/storage/turso"
	"github.com/dickymuliafiqri/firefly/internal/tokensaver"
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

func parseUsageFromBytes(data []byte) (promptToks int, compToks int, ok bool) {
	if len(data) == 0 {
		return 0, 0, false
	}
	// Case 1: Standard JSON object with top-level "usage"
	if gjson.GetBytes(data, "usage").Exists() {
		usage := gjson.GetBytes(data, "usage")
		pt := usage.Get("prompt_tokens")
		if !pt.Exists() {
			pt = usage.Get("input_tokens")
		}
		ct := usage.Get("completion_tokens")
		if !ct.Exists() {
			ct = usage.Get("output_tokens")
		}
		pVal := int(pt.Int())
		cVal := int(ct.Int())
		if pVal > 0 || cVal > 0 {
			return pVal, cVal, true
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
			usage := gjson.GetBytes(payload, "usage")
			pt := usage.Get("prompt_tokens")
			if !pt.Exists() {
				pt = usage.Get("input_tokens")
			}
			ct := usage.Get("completion_tokens")
			if !ct.Exists() {
				ct = usage.Get("output_tokens")
			}
			pVal := int(pt.Int())
			cVal := int(ct.Int())
			if pVal > 0 || cVal > 0 {
				return pVal, cVal, true
			}
		}
	}

	// Case 3: Embedded "usage" substring anywhere in chunk (e.g. Anthropic message_delta)
	idx := bytes.LastIndex(data, []byte(`"usage"`))
	if idx >= 0 {
		sub := data[idx:]
		usageBlock := append([]byte("{"), sub...)
		if gjson.GetBytes(usageBlock, "usage").Exists() {
			usage := gjson.GetBytes(usageBlock, "usage")
			pt := usage.Get("prompt_tokens")
			if !pt.Exists() {
				pt = usage.Get("input_tokens")
			}
			ct := usage.Get("completion_tokens")
			if !ct.Exists() {
				ct = usage.Get("output_tokens")
			}
			pVal := int(pt.Int())
			cVal := int(ct.Int())
			if pVal > 0 || cVal > 0 {
				return pVal, cVal, true
			}
		}
	}

	return 0, 0, false
}

func (t *responseTracker) extractUsage() (promptToks int, compToks int, ok bool) {
	if t == nil {
		return 0, 0, false
	}
	if pIn, pOut, found := parseUsageFromBytes(t.tailBuf); found {
		return pIn, pOut, true
	}
	if pIn, pOut, found := parseUsageFromBytes(t.bodyBuf); found {
		return pIn, pOut, true
	}
	return 0, 0, false
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
// /v1/completions, and /v1/embeddings. The three share identical proxying
// semantics and differ only in the upstream path, so one handler serves all.
func (deps RouterDeps) forwardEndpoint(upstreamPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenant, ok := httpx.RequireTenant(w, r)
		if !ok {
			return
		}
		snap := deps.currentSnapshot()
		if snap == nil {
			openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI, "config not loaded")
			return
		}

		// 0. Fast-fail if Content-Length explicitly exceeds maximum allowable body size.
		if r.ContentLength > maxRequestBodyBytes {
			openai.WriteError(w, http.StatusRequestEntityTooLarge, openai.TypeInvalidRequest, "request body too large")
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
			openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "failed to read request body")
			return
		}
		if int64(buf.Len()) > maxRequestBodyBytes {
			openai.WriteError(w, http.StatusRequestEntityTooLarge, openai.TypeInvalidRequest, "request body too large")
			return
		}
		body := buf.Bytes()

		// 2. Minimal decode for routing (zero-allocation fast path via gjson).
		// A malformed body is a client error, not a 500: fail with OpenAI's invalid_request_error.
		if len(body) > 0 && !gjson.ValidBytes(body) {
			openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "invalid JSON body")
			return
		}
		model := gjson.GetBytes(body, "model").String()
		if model == "" {
			openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "you must provide a model parameter")
			return
		}
		stream := gjson.GetBytes(body, "stream").Bool()
		tokensIn := estimateInputTokens(body)

		// 3. Resolve the routing target (model -> upstream + credential).
		var canUseUpstream func(string) bool
		if deps.Breakers != nil {
			canUseUpstream = func(name string) bool {
				return deps.Breakers.Allow(name) == nil
			}
		}
		target, fallbackUsed, err := snap.ResolveTargetWithBreaker(tenant, model, canUseUpstream)
		if err != nil {
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
				EstimatedCost: float64(tokensIn) * 0.0000025,
				Error:         err.Error(),
			})
			writeResolveError(w, err)
			return
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
					EstimatedCost: float64(tokensIn) * 0.0000025,
					Error:         "upstream credential capacity exhausted",
				})
				w.Header().Set("Retry-After", "1")
				openai.WriteError(w, http.StatusTooManyRequests, openai.TypeRateLimit,
					"upstream credential capacity exhausted; retry shortly")
				return
			}
			openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, "admission control error")
			return
		}
		deps.Metrics.IncKeyInflight(target.Upstream.Name, keyRef)
		defer func() {
			deps.Metrics.DecKeyInflight(target.Upstream.Name, keyRef)
			releaseCred()
		}()

		// Apply Token Saver optimizations if enabled and routing to chat completions
		if upstreamPath == "/chat/completions" && snap.TokenSaver().Enabled {
			if transformedBody, modified := tokensaver.Process(body, snap.TokenSaver()); modified {
				body = transformedBody
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
			openai.WriteError(w, http.StatusNotImplemented, openai.TypeAPI, err.Error())
			return
		}

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
			EstimatedCost: float64(tokensIn) * 0.0000025,
		})

		tracker := &responseTracker{ResponseWriter: w}
		fwdErr := adapter.Forward(r.Context(), target, fwdReq, tracker)
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
		if pIn, pOut, ok := tracker.extractUsage(); ok {
			if pIn > 0 {
				tokensIn = pIn
			}
			if pOut > 0 {
				tokensOut = pOut
			}
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
		totCost := (float64(tokensIn) * 0.0000025) + (float64(tokensOut) * 0.0000100)

		if totTokens > 0 && tenant != nil && tenant.UsedTokens != nil {
			tenant.UsedTokens.Add(int64(totTokens))
			if flusher, ok := deps.Usage.(*turso.UsageFlusher); ok && flusher != nil {
				key := tenant.APIKey
				if key == "" {
					key = tenant.KeyHash
				}
				flusher.RecordTenantTokens(key, int64(totTokens))
			}
		}

		deps.recordLog(LiveLog{
			ID:            reqID,
			Timestamp:     start.UnixMilli(),
			Method:        r.Method,
			Path:          r.URL.Path,
			Status:        keyStatus,
			DurationMs:    elapsed.Milliseconds(),
			Model:         model,
			Upstream:      upstreamName,
			KeyRef:        keyRef,
			Tenant:        tenant.Name,
			Stream:        stream,
			TokensIn:      tokensIn,
			TokensOut:     tokensOut,
			Tokens:        totTokens,
			EstimatedCost: totCost,
			Error:         errStr,
		})

		if fwdErr != nil {
			deps.logForwardFailure(r.Context(), target, fwdErr, elapsed)
			// If nothing was written yet we can still emit an error envelope;
			// if streaming already started, the only honest action is to stop
			// (the client sees a truncated stream).
			if !headerCommitted(w) {
				writeUpstreamFailure(w, fwdErr)
			}
		}
	}
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

// headerCommitted reports whether a status line has already been sent, using the
// responseController probe when the writer exposes it (the logging middleware's
// statusRecorder does). Absent that, we assume uncommitted and let the writer
// ignore a redundant WriteHeader.
func headerCommitted(w http.ResponseWriter) bool {
	if rec, ok := w.(interface{ Committed() bool }); ok {
		return rec.Committed()
	}
	return false
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
