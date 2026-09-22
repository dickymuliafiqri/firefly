package upstream

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
)

// HandleKeyOutcome evaluates a key attempt's status code against the upstream's
// key error policy, tracking consecutive errors atomically, applying cooldown or
// revocation/deletion, and notifying database sinks if configured.
//
// Returns:
// - actionTaken: true if an automated threshold action (deactivate/delete/cooldown) was executed.
// - failoverEligible: true if the key is now unavailable and rotation should fail over to another key.
//
// notifier may be a typed-nil pointer — in file-config mode the usage flusher
// does not exist but is still injected as an interface — so every guard uses
// the package-local isNil helper (the same check as httpx.IsNil; httpx cannot
// be imported here because it transitively depends on the adapters). A bare
// `notifier != nil` check lets the typed nil through and the first
// NotifyKeyAction call panics into a recovered 500.
func HandleKeyOutcome(
	u *domain.Upstream,
	slot *domain.KeySlot,
	statusCode int,
	retryAfterHeader string,
	notifier ports.KeyActionNotifier,
	logger *slog.Logger,
) (actionTaken bool, failoverEligible bool) {
	if u == nil || slot == nil {
		return false, false
	}

	// 1. Classify key-level error: only 429, 401, 402, 403 are credential/quota
	// errors that indicate the key itself is failing. Everything else — success,
	// client cancel (499), transport drops, and 5xx host errors — is NOT the
	// key's fault and MUST reset the consecutive-error counter. Resetting on any
	// non-key-error is what keeps transient 429s from slowly accumulating to the
	// deactivate/delete threshold on a perfectly healthy key.
	isKeyError := (statusCode == http.StatusTooManyRequests ||
		statusCode == http.StatusUnauthorized ||
		statusCode == http.StatusPaymentRequired ||
		statusCode == http.StatusForbidden)

	if !isKeyError {
		slot.ConsecutiveErrors.Store(0)
		return false, false
	}

	// 2. Dynamic cooldown for 429 always applies immediately to prevent loop hammering
	if statusCode == http.StatusTooManyRequests && u.KeyRing != nil {
		FromDomain(u.KeyRing).Handle429(slot.Ref, retryAfterHeader)
	}

	// 3. Increment consecutive error counter
	consec := slot.ConsecutiveErrors.Add(1)

	// 4. Check threshold
	threshold := u.KeyErrorThreshold
	if threshold > 0 && consec >= int64(threshold) {
		reason := fmt.Sprintf("reached consecutive error threshold of %d (last status: %d)", threshold, statusCode)
		return applyKeyAction(u, slot, notifier, logger, reason)
	}

	// 5. If threshold is not reached, handle standard single-error behaviors:
	// - 401: Key is invalid/unauthorized, revoke in-memory to prevent retrying
	if statusCode == http.StatusUnauthorized {
		if u.KeyRing != nil {
			FromDomain(u.KeyRing).Handle401(slot.Ref)
		}
		// If threshold was 0 (unlimited/unconfigured) or 1, trigger deactivation in notifier as well
		if threshold <= 1 && !isNil(notifier) {
			notifier.NotifyKeyAction(ports.KeyActionDeactivate, u.Name, slot.Ref, slot.APIKeyID, "HTTP 401 Unauthorized")
		}
		return false, true
	}

	// - 429: In cooldown, eligible for failover
	if statusCode == http.StatusTooManyRequests {
		return false, true
	}

	return false, false
}

func applyKeyAction(
	u *domain.Upstream,
	slot *domain.KeySlot,
	notifier ports.KeyActionNotifier,
	logger *slog.Logger,
	reason string,
) (actionTaken bool, failoverEligible bool) {
	action := u.KeyErrorAction
	if action == "" {
		action = string(ports.KeyActionDeactivate)
	}

	switch ports.KeyAction(action) {
	case ports.KeyActionDelete:
		slot.Revoked.Store(true)
		if logger != nil {
			logger.Warn("key error threshold reached: deleting key",
				"upstream", u.Name,
				"ref", slot.Ref,
				"consecutive_errors", slot.ConsecutiveErrors.Load(),
				"reason", reason,
			)
		}
		if !isNil(notifier) {
			notifier.NotifyKeyAction(ports.KeyActionDelete, u.Name, slot.Ref, slot.APIKeyID, reason)
		}
		return true, true

	case ports.KeyActionCooldown:
		durationMs := u.KeyCooldownDurationMs
		if durationMs <= 0 {
			durationMs = 300000 // 5 minutes default
		}
		dur := time.Duration(durationMs) * time.Millisecond
		if u.KeyRing != nil {
			FromDomain(u.KeyRing).MarkCooldown(slot.Ref, dur)
		}
		// Reset counter so after cooldown expires, key starts with a clean slate
		slot.ConsecutiveErrors.Store(0)
		if logger != nil {
			logger.Warn("key error threshold reached: placing key in extended cooldown",
				"upstream", u.Name,
				"ref", slot.Ref,
				"duration", dur.String(),
				"reason", reason,
			)
		}
		return true, true

	default: // Deactivate
		slot.Revoked.Store(true)
		if logger != nil {
			logger.Warn("key error threshold reached: deactivating key",
				"upstream", u.Name,
				"ref", slot.Ref,
				"consecutive_errors", slot.ConsecutiveErrors.Load(),
				"reason", reason,
			)
		}
		if !isNil(notifier) {
			notifier.NotifyKeyAction(ports.KeyActionDeactivate, u.Name, slot.Ref, slot.APIKeyID, reason)
		}
		return true, true
	}
}
