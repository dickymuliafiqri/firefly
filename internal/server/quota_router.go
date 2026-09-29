package server

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

// Quota-aware routing.
//
// The provider tells us an account is out of allowance for a model; without
// this, every request for that model is forwarded anyway, fails upstream, and
// costs the operator a round trip to learn something Firefly already knew.
//
// Two deliberate limits keep this from becoming a new source of outages:
//
//   - **Positive evidence only.** A model blocks a route only when a cached
//     quota entry says `remainingFraction == 0` and its reset time has not
//     already passed. No entry, a stale entry, an unknown model, a free tier, or
//     a reset time already in the past all mean "route it" — the gateway
//     degrades to its previous behavior instead of inventing an outage.
//   - **Layer 1, never Layer 2.** A locally-detected exhaustion answers 429 to
//     the client like any other credential error. It never reports a breaker
//     failure: a Google account with no allowance left is not a broken host.

// oauthRefPrefix is how an OAuth-backed credential is referenced in the catalog.
const oauthRefPrefix = "oauth:"

// quotaConnectionID maps a credential ref onto the OAuth connection whose quota
// describes it. Returns "" for every non-OAuth credential.
func quotaConnectionID(ref string) string {
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(ref, oauthRefPrefix) {
		return ""
	}
	return strings.TrimPrefix(ref, oauthRefPrefix)
}

// quotaEntryFor returns the cached quota for a connection regardless of age.
// The routing gate applies its own freshness rule (see quotaBlocked); the
// dashboard applies the TTL.
func quotaEntryFor(id string) (QuotaProviderDTO, bool) {
	if id == "" {
		return QuotaProviderDTO{}, false
	}
	quotaCache.mu.Lock()
	defer quotaCache.mu.Unlock()
	entry, ok := quotaCache.entries[id]
	if !ok {
		return QuotaProviderDTO{}, false
	}
	return entry.dto, true
}

// quotaBlocked reports whether provider quota says this target's model is spent.
//
// The match is on the *upstream* model id (`Target.UpstreamModel`), which is
// exactly the id Cloud Code meters and exactly the id the adapter sends — not
// the public alias, so a renamed route cannot silently stop matching.
func (deps RouterDeps) quotaBlocked(target *domain.Target, now time.Time) (resetAt time.Time, blocked bool) {
	if target == nil || target.UpstreamModel == "" {
		return time.Time{}, false
	}
	ref := target.CredentialRef
	if target.KeySlot != nil && target.KeySlot.Ref != "" {
		ref = target.KeySlot.Ref
	}
	dto, ok := quotaEntryFor(quotaConnectionID(ref))
	if !ok || len(dto.Models) == 0 {
		return time.Time{}, false
	}
	for _, m := range dto.Models {
		if m.Model != target.UpstreamModel || !m.Exhausted {
			continue
		}
		if m.ResetAt == "" {
			// Exhausted with no reset time: block, but ask the client to retry
			// soon rather than pinning a long Retry-After we cannot justify.
			return time.Time{}, true
		}
		reset, err := time.Parse(time.RFC3339, m.ResetAt)
		if err != nil {
			// Exhaustion is the provider's own statement; a reset time we cannot
			// read only costs us the Retry-After, not the evidence.
			return time.Time{}, true
		}
		if !reset.After(now) {
			// A reset time already in the past contradicts the exhaustion: the
			// snapshot predates the reset, so it is not evidence.
			return time.Time{}, false
		}
		return reset, true
	}
	return time.Time{}, false
}

// quotaBlockedCandidate is the candidate-level form of quotaBlocked, used by the
// resolver's veto. It is the same decision on the same data; only the input
// shape differs (a candidate triple instead of a resolved target).
func (deps RouterDeps) quotaBlockedCandidate(
	c domain.TargetCandidate, now time.Time,
) (resetAt time.Time, blocked bool) {
	return deps.quotaBlocked(&domain.Target{
		UpstreamModel: c.UpstreamModel,
		CredentialRef: c.CredentialRef,
	}, now)
}

// quotaRetryAfter turns a provider reset time into a Retry-After in seconds.
// A zero reset time means the provider gave us no deadline, so the answer is a
// short, honest "come back soon" rather than a long promise we cannot keep.
func quotaRetryAfter(resetAt time.Time) int {
	const (
		defaultWait = 60
		maxWait     = 3600
	)
	wait := defaultWait
	if !resetAt.IsZero() {
		wait = int(time.Until(resetAt).Seconds()) + 1
	}
	if wait < 1 {
		wait = 1
	}
	if wait > maxWait {
		wait = maxWait
	}
	return wait
}

// noteQuotaRateLimit reacts to a generation-side 429/409 on an OAuth-backed
// credential. Upstream is the ground truth: a rate limit that contradicts a
// positive quota reading means our picture is stale, so the cached entry is
// dropped and the next read (dashboard poll, poller tick, or the next request)
// refetches. It deliberately does not touch the breaker or the key error
// policy — those already handled the 429 in Layer 1.
func (deps RouterDeps) noteQuotaRateLimit(ref string) {
	id := quotaConnectionID(ref)
	if id == "" {
		return
	}
	quotaCache.mu.Lock()
	_, existed := quotaCache.entries[id]
	delete(quotaCache.entries, id)
	quotaCache.mu.Unlock()
	if !existed || deps.OAuthManager == nil {
		return
	}
	if deps.Logger != nil {
		deps.Logger.Warn("quota snapshot invalidated by upstream rate limit",
			"connection_id", id, "reason", "generation 429/409 overrides positive quota data")
	}
	// Refetch in the background: the failing request must not wait on Google.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), quotaFetchTimeout)
		defer cancel()
		conn, err := deps.OAuthManager.ResolveConnection(ctx, oauthRefPrefix+id)
		if err != nil || conn == nil {
			return
		}
		deps.fetchConnectionQuota(ctx, conn, time.Now(), true)
	}()
}

// StartQuotaPoller keeps the provider-quota cache warm so the routing gate has
// fresh data even when no dashboard is open. It is deliberately dumb: the fetch
// path is idempotent and self-caching, so a tick inside the TTL costs nothing.
//
// Interval 0 disables the poller, leaving quota reads purely on demand.
func (deps RouterDeps) StartQuotaPoller(ctx context.Context, interval time.Duration) {
	if interval <= 0 || deps.OAuthManager == nil {
		return
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				deps.refreshAllQuota(ctx, logger)
			}
		}
	}()
}

// refreshAllQuota polls every connection whose provider has a quota client.
// Failures are logged, never fatal: a quota poller that dies takes the routing
// gate's freshness with it.
func (deps RouterDeps) refreshAllQuota(ctx context.Context, logger *slog.Logger) {
	connections, err := deps.OAuthManager.ListConnections(ctx)
	if err != nil {
		logger.Warn("quota poller could not list oauth connections", "err", err)
		return
	}
	now := time.Now()
	for _, conn := range connections {
		if conn == nil || !supportsProviderQuota(conn.Provider) {
			continue
		}
		// A canceled context must stop the loop, not spin through every
		// connection failing on the same dead context.
		if ctx.Err() != nil {
			return
		}
		dto := deps.fetchConnectionQuota(ctx, conn, now, false)
		if dto.Message != "" {
			logger.Debug("quota refresh reported a message",
				"connection_id", conn.ID, "provider", conn.Provider, "message", dto.Message)
		}
	}
}
