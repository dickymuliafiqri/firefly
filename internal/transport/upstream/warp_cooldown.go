package upstream

import "log/slog"

// ClearUpstreamKeyCooldowns releases the cooldown penalties held by every key
// slot of the named upstream and reports how many slots were released. It is the
// counterpart of the 429-driven WARP rotation in attempt.go: the 429 was charged
// to the key slot, but the quota it reflects belongs to the egress IP that the
// rotation just replaced, so keeping the penalty would idle a healthy identity
// for up to MaxCooldownDuration (measured: 302s of a 300s cap in the e2e sandbox).
//
// A nil provider, an empty name, a missing upstream and a nil ring are no-ops.
// Revocation is deliberately not cleared; see domain.KeyRing.ClearCooldowns.
func ClearUpstreamKeyCooldowns(provider SnapshotProvider, upstreamName string, logger *slog.Logger) int {
	if isNil(provider) || upstreamName == "" {
		return 0
	}
	snap := provider.Current()
	if snap == nil {
		return 0
	}
	u, ok := snap.Upstream(upstreamName)
	if !ok || u == nil || u.KeyRing == nil {
		return 0
	}
	cleared := u.KeyRing.ClearCooldowns()
	if cleared > 0 && logger != nil {
		logger.Info("cleared key cooldowns after warp rotation",
			"upstream", upstreamName, "keys_cleared", cleared)
	}
	return cleared
}
