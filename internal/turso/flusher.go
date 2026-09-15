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

// UsageFlusher records usage, aggregates request counts per key, and persists
// metrics and key updates into Turso, pushing updates to the primary cloud.
type UsageFlusher struct {
	store         *Store
	inner         ports.UsageRecorder
	events        chan usageEvent
	revocations   chan string
	actions       chan keyActionEvent
	flushInterval time.Duration
	logger        *slog.Logger
	mu            sync.Mutex
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
		store:         store,
		inner:         inner,
		events:        make(chan usageEvent, 10000),
		revocations:   make(chan string, 1000),
		actions:       make(chan keyActionEvent, 1000),
		flushInterval: flushInterval,
		logger:        logger,
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
			_ = f.Flush(flushCtx)
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
func (f *UsageFlusher) Flush(ctx context.Context) error {
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

	if len(usageAgg) == 0 && len(revocations) == 0 && len(actions) == 0 {
		return nil
	}

	if f.store == nil || f.store.DB() == nil {
		return nil
	}

	tx, err := f.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin usage tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	now := time.Now().UnixMilli()
	updatedAny := false

	// Update api_keys usage
	for ref, ev := range usageAgg {
		keyID := extractAPIKeyID(ref)
		if keyID > 0 {
			res, err := tx.ExecContext(ctx, `
				UPDATE api_keys
				SET total_requests = total_requests + ?,
				    last_used_at = ?,
				    updated_at = ?
				WHERE id = ?
			`, ev.count, ev.at, now, keyID)
			if err == nil {
				if r, _ := res.RowsAffected(); r > 0 {
					updatedAny = true
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
					updatedAny = true
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
						updatedAny = true
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
						updatedAny = true
					}
				}
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit usage tx: %w", err)
	}

	// Push usage updates to Turso Cloud
	if updatedAny && f.store.Client() != nil {
		_ = f.store.Client().Push(ctx)
	}

	return nil
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
