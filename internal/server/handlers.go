package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/httpx"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/metrics"
	"github.com/dickymuliafiqri/firefly/internal/openai"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/dickymuliafiqri/firefly/internal/upstream"
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

type responseTracker struct {
	http.ResponseWriter
	bytesWritten int64
	linesWritten int64
}

func (t *responseTracker) Write(p []byte) (int, error) {
	n, err := t.ResponseWriter.Write(p)
	t.bytesWritten += int64(n)
	t.linesWritten += int64(bytes.Count(p, []byte("\n")))
	return n, err
}

func (t *responseTracker) Flush() {
	if f, ok := t.ResponseWriter.(http.Flusher); ok {
		f.Flush()
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
	toks := len(body) / 6
	if toks < 1 {
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

		// 3. Resolve the routing target (model -> upstream + credential).
		var canUseUpstream func(string) bool
		if deps.Breakers != nil {
			canUseUpstream = func(name string) bool {
				return deps.Breakers.Allow(name) == nil
			}
		}
		target, fallbackUsed, err := snap.ResolveTargetWithBreaker(tenant, model, canUseUpstream)
		if err != nil {
			if deps.LiveLogs != nil {
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
				deps.LiveLogs.Publish(LiveLog{
					ID:         httpx.RequestIDFrom(r.Context()),
					Timestamp:  time.Now().UnixMilli(),
					Method:     r.Method,
					Path:       r.URL.Path,
					Status:     status,
					DurationMs: 0,
					Model:      model,
					Upstream:   "",
					Tenant:     tenant.Name,
					Stream:     stream,
					Error:      err.Error(),
				})
			}
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
				if deps.LiveLogs != nil {
					deps.LiveLogs.Publish(LiveLog{
						ID:         httpx.RequestIDFrom(r.Context()),
						Timestamp:  time.Now().UnixMilli(),
						Method:     r.Method,
						Path:       r.URL.Path,
						Status:     http.StatusTooManyRequests,
						DurationMs: 0,
						Model:      model,
						Upstream:   target.Upstream.Name,
						Tenant:     tenant.Name,
						Stream:     stream,
						Error:      "upstream credential capacity exhausted",
					})
				}
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
		tokensIn := estimateInputTokens(body)
		if deps.LiveLogs != nil {
			deps.LiveLogs.Publish(LiveLog{
				ID:            reqID,
				Timestamp:     start.UnixMilli(),
				Method:        r.Method,
				Path:          r.URL.Path,
				Status:        0, // In-flight / Active
				DurationMs:    0,
				Model:         model,
				Upstream:      upstreamName,
				Tenant:        tenant.Name,
				Stream:        stream,
				TokensIn:      tokensIn,
				TokensOut:     0,
				Tokens:        tokensIn,
				EstimatedCost: float64(tokensIn) * 0.0000025,
			})
		}

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

		if deps.LiveLogs != nil {
			var errStr string
			if fwdErr != nil {
				errStr = fwdErr.Error()
			}
			tokensOut := 0
			if keyStatus == http.StatusOK && tracker.bytesWritten > 0 {
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

			deps.LiveLogs.Publish(LiveLog{
				ID:            reqID,
				Timestamp:     start.UnixMilli(),
				Method:        r.Method,
				Path:          r.URL.Path,
				Status:        keyStatus,
				DurationMs:    elapsed.Milliseconds(),
				Model:         model,
				Upstream:      upstreamName,
				Tenant:        tenant.Name,
				Stream:        stream,
				TokensIn:      tokensIn,
				TokensOut:     tokensOut,
				Tokens:        totTokens,
				EstimatedCost: totCost,
				Error:         errStr,
			})
		}

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
