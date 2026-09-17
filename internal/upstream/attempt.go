package upstream

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
)

// AttemptMetrics is the subset of key-level metrics the shared outcome
// processor emits. A nil value is safe (all calls become no-ops).
type AttemptMetrics interface {
	ObserveKeyCooldown(upstream, keyRef string)
	ObserveKeyRequest(upstream, keyRef string, status int)
}

// BreakerReporter records the pre-response outcome for an upstream host. A nil
// value is safe.
type BreakerReporter interface {
	Report(name string, ok bool)
}

// WarpRotator triggers asynchronous IP rotation for Cloudflare WARP.
type WarpRotator interface {
	RotateAsync(upstreamName string)
}

var globalWarpRotator atomic.Pointer[WarpRotator]

// SetGlobalWarpRotator sets the package-level WARP rotator for automated 429 IP rotations.
func SetGlobalWarpRotator(wr WarpRotator) {
	if wr == nil {
		globalWarpRotator.Store(nil)
		return
	}
	globalWarpRotator.Store(&wr)
}

// AttemptOutcome describes the result of a single upstream attempt, expressed
// with the facts every adapter already has: the HTTP status (0 for transport
// errors), the transport/relay error, the response headers, and whether any
// byte was already committed to the client.
type AttemptOutcome struct {
	// Status is the upstream HTTP status, or 0 for a transport-level error.
	Status int
	// Err is the transport or relay error, if any.
	Err error
	// Headers are the upstream response headers (used to read Retry-After).
	Headers http.Header
	// Committed reports whether a byte/header already reached the client, which
	// makes a retry impossible without corrupting the response.
	Committed bool
}

// AttemptDecision tells the adapter what to do next after an attempt. Exactly
// one of the boolean fields is meaningful at a time, checked in this order:
// Success, StopCommitted, Failover, Relay, Fail.
type AttemptDecision struct {
	// Success: the attempt succeeded; return nil.
	Success bool
	// StopCommitted: a byte already reached the client, so we cannot retry;
	// return the attempt error as-is.
	StopCommitted bool
	// Failover: a new key slot was selected; the adapter should `continue` the
	// retry loop with the already-updated Target.
	Failover bool
	// Relay: this is a terminal client-facing error (4xx that we do not retry, or
	// a rate/auth error with no remaining key). The adapter should relay the
	// buffered error body verbatim and return nil.
	Relay bool
	// Fail: a transport error or 5xx that is not retryable here (single key or
	// selection failed). The adapter should record lastErr and either retry the
	// loop (host failure) or return immediately.
	Fail bool
	// IsHostFailure reports whether the outcome counts as an upstream host
	// failure for the circuit breaker (transport error or 5xx). Only meaningful
	// with Fail.
	IsHostFailure bool
}

// isHostFailure reports whether an outcome is an upstream host failure for the
// circuit breaker. Only transport errors (status 0 with a non-cancel error) and
// 5xx responses count. Credential/quota errors (401/403/429) and other 4xx MUST
// NOT trip the breaker — that is the Layer 1 (key) vs Layer 2 (host) separation.
//
// This is the single source of truth for breaker classification, replacing the
// per-adapter variants that previously diverged (some counted any non-nil error,
// including wrapped 4xx, as a host failure).
func isHostFailure(o AttemptOutcome) bool {
	if o.Status >= 500 {
		return true
	}
	if o.Status == 0 && o.Err != nil && !errors.Is(o.Err, context.Canceled) {
		return true
	}
	return false
}

// ProcessAttemptOutcome centralizes the post-attempt decision shared by every
// upstream adapter: report the breaker, evaluate the key error policy, emit key
// metrics, and select a failover key when eligible. It mutates target.KeySlot
// and target.CredentialRef in place when it selects a new key.
//
// The adapter remains responsible for the mechanics that legitimately differ
// between protocols (buffering the body, relaying the error envelope in the
// adapter's own wire format, and building lastErr), driven by the returned
// AttemptDecision.
func ProcessAttemptOutcome(
	u *domain.Upstream,
	target *domain.Target,
	outcome AttemptOutcome,
	breaker BreakerReporter,
	metrics AttemptMetrics,
	notifier ports.KeyActionNotifier,
	logger *slog.Logger,
) AttemptDecision {
	hostFailure := isHostFailure(outcome)

	// Report the breaker on every attempt: success or host failure. Key/quota
	// errors report success (ok=true) so they never trip the host breaker.
	if breaker != nil {
		breaker.Report(u.Name, !hostFailure)
	}

	// Success path.
	if outcome.Err == nil && outcome.Status > 0 && outcome.Status < 400 {
		if target.KeySlot != nil {
			HandleKeyOutcome(u, target.KeySlot, outcome.Status, "", notifier, logger)
		}
		return AttemptDecision{Success: true}
	}

	// A response already reached the client; retrying would corrupt it.
	if outcome.Committed {
		return AttemptDecision{StopCommitted: true}
	}

	// Evaluate the key error policy (429, 401, 402, 403).
	retryAfter := ""
	if outcome.Headers != nil {
		retryAfter = outcome.Headers.Get("Retry-After")
	}
	_, failoverEligible := HandleKeyOutcome(u, target.KeySlot, outcome.Status, retryAfter, notifier, logger)

	if metrics != nil && target.KeySlot != nil {
		if outcome.Status == http.StatusTooManyRequests {
			metrics.ObserveKeyCooldown(u.Name, target.KeySlot.Ref)
		}
		metrics.ObserveKeyRequest(u.Name, target.KeySlot.Ref, outcome.Status)
	}

	// Auto-rotate Cloudflare WARP IP if enabled for this upstream
	if outcome.Status == http.StatusTooManyRequests && u != nil && u.WarpAutoRotateOn429 {
		if ptr := globalWarpRotator.Load(); ptr != nil && *ptr != nil {
			(*ptr).RotateAsync(u.Name)
		}
	}

	// Failover: rotate to another available key and retry.
	if failoverEligible && selectNextKey(u, target) {
		return AttemptDecision{Failover: true}
	}

	// Terminal credential/quota error with no remaining key, or any other 4xx:
	// relay verbatim without retrying.
	if outcome.Status >= 400 && outcome.Status < 500 {
		return AttemptDecision{Relay: true}
	}

	// Transport error or 5xx: rotate key for the next attempt if possible, then
	// let the adapter decide whether to retry (host failure) or stop.
	selectNextKey(u, target)
	return AttemptDecision{Fail: true, IsHostFailure: hostFailure}
}

// selectNextKey picks another available key slot when the ring has more than one
// slot, updating target in place. It reports whether a new slot was selected.
func selectNextKey(u *domain.Upstream, target *domain.Target) bool {
	if u.KeyRing == nil || len(u.KeyRing.Slots) <= 1 {
		return false
	}
	nextSlot, err := u.KeyRing.SelectKey(time.Now().UnixNano())
	if err != nil || nextSlot == nil {
		return false
	}
	target.KeySlot = nextSlot
	target.CredentialRef = nextSlot.Ref
	return true
}
