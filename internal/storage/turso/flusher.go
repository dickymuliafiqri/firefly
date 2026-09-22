package turso

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/ports"
)

var _ ports.UsageRecorder = (*UsageFlusher)(nil)
var _ ports.KeyActionNotifier = (*UsageFlusher)(nil)

type usageEvent struct {
	ref   string
	count int64
	at    int64
}

type keyActionEvent struct {
	action       ports.KeyAction
	upstreamName string
	ref          string
	keyID        int64
	reason       string
	at           int64
}

type tenantUsageEvent struct {
	key    string
	tokens int64
}

// defaultMeteringPushInterval throttles cloud pushes for pure usage metering.
// Counters are analytics, so a delayed push is harmless; key lifecycle changes
// bypass the throttle entirely because a stale cloud row can resurrect a dead
// credential on the next pull. Every push also costs Turso sync quota, which is
// why metering updates ride along instead of pushing on every flush tick.
const defaultMeteringPushInterval = 5 * time.Minute

// UsageFlusher records usage, aggregates request counts per key, and persists
// metrics and key updates into Turso, pushing updates to the primary cloud.
type UsageFlusher struct {
	store         *Store
	inner         ports.UsageRecorder
	events        chan usageEvent
	revocations   chan string
	actions       chan keyActionEvent
	tenantEvents  chan tenantUsageEvent
	flushInterval time.Duration
	// meteringPushInterval is the minimum quiet period between two cloud pushes
	// that carry only usage counters. It is a field so tests can shrink it.
	meteringPushInterval time.Duration
	lastMeteringPush     time.Time
	logger               *slog.Logger
	mu                   sync.Mutex
}

// NewUsageFlusher creates a new UsageFlusher.
func NewUsageFlusher(store *Store, inner ports.UsageRecorder, flushInterval time.Duration, logger *slog.Logger) *UsageFlusher {
	if flushInterval <= 0 {
		flushInterval = 30 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &UsageFlusher{
		store:                store,
		inner:                inner,
		events:               make(chan usageEvent, 10000),
		revocations:          make(chan string, 1000),
		actions:              make(chan keyActionEvent, 1000),
		tenantEvents:         make(chan tenantUsageEvent, 10000),
		flushInterval:        flushInterval,
		meteringPushInterval: defaultMeteringPushInterval,
		logger:               logger,
	}
}

// RecordTenantTokens queues tenant token usage to be flushed in batch to Turso/SQLite.
func (f *UsageFlusher) RecordTenantTokens(apiKey string, tokens int64) {
	if f == nil || apiKey == "" || tokens <= 0 {
		return
	}
	select {
	case f.tenantEvents <- tenantUsageEvent{key: apiKey, tokens: tokens}:
	default:
	}
}

// Record implements ports.UsageRecorder. It updates memory counters immediately
// and queues the key usage event without blocking the hot path.
func (f *UsageFlusher) Record(credentialRef, tenantName, model string, reqCount int64) {
	if f.inner != nil {
		f.inner.Record(credentialRef, tenantName, model, reqCount)
	}

	if credentialRef == "" {
		return
	}

	select {
	case f.events <- usageEvent{ref: credentialRef, count: reqCount, at: time.Now().UnixMilli()}:
	default:
		// Queue full; discard to preserve data plane zero-latency invariant
	}
}

// Snapshot implements ports.UsageRecorder.
func (f *UsageFlusher) Snapshot() []ports.UsageCounter {
	if f.inner != nil {
		return f.inner.Snapshot()
	}
	return nil
}

// MarkRevoked queues a key revocation to be persisted in Turso.
func (f *UsageFlusher) MarkRevoked(ref string) {
	select {
	case f.revocations <- ref:
	default:
	}
}

// NotifyKeyAction queues an automated key lifecycle action (deactivate or delete) to be persisted in Turso.
func (f *UsageFlusher) NotifyKeyAction(action ports.KeyAction, upstreamName, ref string, keyID int64, reason string) {
	if keyID <= 0 {
		keyID = extractAPIKeyID(ref)
	}
	f.logger.Warn("key action triggered by error policy",
		"action", string(action),
		"upstream", upstreamName,
		"ref", ref,
		"key_id", keyID,
		"reason", reason,
	)
	select {
	case f.actions <- keyActionEvent{
		action:       action,
		upstreamName: upstreamName,
		ref:          ref,
		keyID:        keyID,
		reason:       reason,
		at:           time.Now().UnixMilli(),
	}:
	default:
		// Queue full; execute directly in background worker
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if action == ports.KeyActionDelete {
				_ = f.store.DeleteKey(ctx, keyID)
			} else {
				_ = f.store.DeactivateKey(ctx, keyID, reason)
			}
		}()
	}
}

// Run periodically flushes accumulated events to Turso until ctx is canceled.
func (f *UsageFlusher) Run(ctx context.Context) {
	ticker := time.NewTicker(f.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			// Forced: the process is going away, so inside-window metering must
			// reach the cloud now or the local-only rows are lost with the replica.
			_ = f.FlushAll(flushCtx)
			cancel()
			return
		case <-ticker.C:
			if err := f.Flush(ctx); err != nil {
				f.logger.Warn("turso usage flush encountered warning", "err", err)
			}
		}
	}
}

// Flush aggregates queued events and executes batch updates against the database.
// Cloud pushes for counter-only updates are throttled; see FlushAll to bypass.
func (f *UsageFlusher) Flush(ctx context.Context) error {
	return f.flush(ctx, false)
}

// FlushAll behaves like Flush but always pushes to the cloud, skipping the
// metering quiet window. Use it on shutdown so buffered counters are not lost.
func (f *UsageFlusher) FlushAll(ctx context.Context) error {
	return f.flush(ctx, true)
}

func (f *UsageFlusher) flush(ctx context.Context, forcePush bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	// 1. Drain queued usage events
	usageAgg := make(map[string]*usageEvent)
	for {
		select {
		case ev := <-f.events:
			existing, ok := usageAgg[ev.ref]
			if !ok {
				usageAgg[ev.ref] = &ev
			} else {
				existing.count += ev.count
				if ev.at > existing.at {
					existing.at = ev.at
				}
			}
		default:
			goto DRAINED_USAGE
		}
	}
DRAINED_USAGE:

	// 2. Drain queued revocations
	revocations := make(map[string]bool)
	for {
		select {
		case ref := <-f.revocations:
			revocations[ref] = true
		default:
			goto DRAINED_REVOCATIONS
		}
	}
DRAINED_REVOCATIONS:

	// 3. Drain queued key lifecycle actions
	actions := make([]keyActionEvent, 0)
	for {
		select {
		case act := <-f.actions:
			actions = append(actions, act)
		default:
			goto DRAINED_ACTIONS
		}
	}
DRAINED_ACTIONS:

	// 4. Drain queued tenant token usage
	tenantTokensAgg := make(map[string]int64)
	for {
		select {
		case ev := <-f.tenantEvents:
			tenantTokensAgg[ev.key] += ev.tokens
		default:
			goto DRAINED_TENANTS
		}
	}
DRAINED_TENANTS:

	if len(usageAgg) == 0 && len(revocations) == 0 && len(actions) == 0 && len(tenantTokensAgg) == 0 {
		return nil
	}

	if f.store == nil || f.store.DB() == nil {
		return nil
	}

	f.store.Lock()
	defer f.store.Unlock()

	tx, err := f.store.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("begin usage tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			_, _ = f.store.DB().ExecContext(cleanupCtx, "ROLLBACK")
			cancel()
		}
	}()

	now := time.Now().UnixMilli()
	structuralUpdated := false
	meteringUpdated := false

	// Update api_keys usage.
	//
	// IMPORTANT: pure usage metering must NOT bump updated_at. The syncer's
	// GetKeysState uses MAX(updated_at) as a "structural change" signal to
	// decide whether to hot-swap the catalog snapshot. total_requests /
	// last_used_at do not affect routing, so bumping updated_at here caused a
	// reload every sync interval whenever traffic flowed — which rebuilt every
	// KeyRing and reset key-selection rotation, collapsing load balancing onto
	// the first few keys. Revocations/deactivations below still bump updated_at
	// because those ARE structural changes the syncer must observe.
	for ref, ev := range usageAgg {
		keyID := extractAPIKeyID(ref)
		if keyID > 0 {
			res, err := tx.ExecContext(ctx, `
				UPDATE api_keys
				SET total_requests = total_requests + ?,
				    last_used_at = ?
				WHERE id = ?
			`, ev.count, ev.at, keyID)
			if err == nil {
				if r, _ := res.RowsAffected(); r > 0 {
					meteringUpdated = true
				}
			}
		}
	}

	// Update revoked keys
	for ref := range revocations {
		keyID := extractAPIKeyID(ref)
		if keyID > 0 {
			res, err := tx.ExecContext(ctx, `
				UPDATE api_keys
				SET status = 'revoked',
				    is_active = 0,
				    updated_at = ?
				WHERE id = ?
			`, now, keyID)
			if err == nil {
				if r, _ := res.RowsAffected(); r > 0 {
					structuralUpdated = true
				}
			}
		}
	}

	// Process key actions (deactivate / delete)
	for _, act := range actions {
		if act.keyID <= 0 {
			act.keyID = extractAPIKeyID(act.ref)
		}
		if act.keyID > 0 {
			if act.action == ports.KeyActionDelete {
				_, _ = tx.ExecContext(ctx, `DELETE FROM upstream_credentials WHERE api_key_id = ?`, act.keyID)
				res, err := tx.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ?`, act.keyID)
				if err == nil {
					if r, _ := res.RowsAffected(); r > 0 {
						structuralUpdated = true
					}
				}
			} else {
				// Deactivate
				_, _ = tx.ExecContext(ctx, `
					UPDATE upstream_credentials
					SET status = 'deactivated', is_active = 0, updated_at = ?
					WHERE api_key_id = ?
				`, now, act.keyID)
				res, err := tx.ExecContext(ctx, `
					UPDATE api_keys
					SET status = 'deactivated',
					    is_active = 0,
					    updated_at = ?
					WHERE id = ?
				`, now, act.keyID)
				if err == nil {
					if r, _ := res.RowsAffected(); r > 0 {
						structuralUpdated = true
					}
				}
			}
		}
	}

	// 4. Update tenant token usage
	for key, delta := range tenantTokensAgg {
		if delta <= 0 {
			continue
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE tenants
			SET used_tokens = used_tokens + ?,
			    updated_at = ?
			WHERE api_key = ? OR key_hash = ?
		`, delta, now, key, key)
		if err == nil {
			if r, _ := res.RowsAffected(); r > 0 {
				meteringUpdated = true
			}
		}
	}

	if err := tx.Commit(); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		_, _ = f.store.DB().ExecContext(cleanupCtx, "ROLLBACK")
		cancel()
		return fmt.Errorf("commit usage tx: %w", err)
	}
	committed = true

	// Push updates to Turso Cloud. Unpushed local rows are rebased on top of
	// remote changes by the next Pull, so a deferred counter push loses nothing.
	if f.store.Client() == nil {
		return nil
	}
	due := forcePush || structuralUpdated || f.meteringPushDue()
	if (structuralUpdated || meteringUpdated) && due {
		if f.pushLocked(ctx) {
			f.lastMeteringPush = time.Now()
		}
	}

	return nil
}

// meteringPushDue reports whether the counter-only push quiet window has elapsed.
// It must be called with f.mu held.
func (f *UsageFlusher) meteringPushDue() bool {
	if f.meteringPushInterval <= 0 {
		return true
	}
	return time.Since(f.lastMeteringPush) >= f.meteringPushInterval
}

// pushLocked pushes local changes to the cloud and reports success. A failed push
// leaves the rows local, so the caller must not start a fresh quiet window.
func (f *UsageFlusher) pushLocked(ctx context.Context) bool {
	if err := f.store.Client().PushLocked(ctx); err != nil {
		f.logger.Warn("turso usage push encountered warning", "err", err)
		return false
	}
	return true
}

// extractAPIKeyID extracts the numeric key ID from a reference string formatted
// like "<upstream>-key-<id>". Returns 0 if not matching this format.
func extractAPIKeyID(ref string) int64 {
	idx := strings.LastIndex(ref, "-key-")
	if idx < 0 {
		return 0
	}
	numStr := ref[idx+len("-key-"):]
	id, err := strconv.ParseInt(numStr, 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}
