package upstream

import (
	"log/slog"
	"time"
)

// ReleaseUpstreamKeyPenalties releases both penalties that the 429 behind a
// successful automatic WARP rotation charged to the upstream's key slots, and
// reports how many slots were affected: those serving a live cooldown, and those
// carrying a non-zero consecutive-error counter. It is the counterpart of the
// 429-driven rotation in attempt.go — the 429 was charged to the key, but the
// quota it reflects belongs to the egress IP that the rotation just replaced.
// Keeping the penalties would idle a healthy identity for up to
// MaxCooldownDuration (measured: 302s of a 300s cap in the e2e sandbox) and let an
// IP-bound 429 storm arm the KeyErrorThreshold deactivate/delete action against a
// healthy key.
//
// A nil provider, an empty name, a missing upstream and a nil ring are no-ops.
// Revocation is deliberately not released; see domain.KeyRing.ClearCooldowns and
// domain.KeyRing.ResetConsecutiveErrors.
func ReleaseUpstreamKeyPenalties(provider SnapshotProvider, upstreamName string, logger *slog.Logger) (cooldownsCleared, countersReset int) {
	if isNil(provider) || upstreamName == "" {
		return 0, 0
	}
	snap := provider.Current()
	if snap == nil {
		return 0, 0
	}
	u, ok := snap.Upstream(upstreamName)
	if !ok || u == nil || u.KeyRing == nil {
		return 0, 0
	}
	cooldownsCleared = u.KeyRing.ClearCooldowns(time.Now().UnixNano())
	countersReset = u.KeyRing.ResetConsecutiveErrors()
	if (cooldownsCleared > 0 || countersReset > 0) && logger != nil {
		logger.Info("released key penalties after warp rotation",
			"upstream", upstreamName,
			"cooldowns_cleared", cooldownsCleared,
			"counters_reset", countersReset)
	}
	return cooldownsCleared, countersReset
}
