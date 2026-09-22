package turso

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/observability/metrics"
	"github.com/dickymuliafiqri/firefly/internal/registry"
)

// Default pull pacing. Every cycle costs Turso sync quota (a wait_changes
// handshake plus any frames), so idle gateways back off to MaxInterval instead of
// polling at the base rate forever. Writes originate locally (harvester, operator
// CRUD) and trigger an immediate sync, so the base interval only governs how fast
// changes written by *another* instance become visible here.
const (
	DefaultSyncInterval    = 60 * time.Second
	DefaultSyncMaxInterval = 5 * time.Minute
)

// SyncerConfig holds dependencies and configuration for the Turso background syncer.
type SyncerConfig struct {
	// Interval is the base pull period, used right after a cycle that observed a
	// change. Values <= 0 fall back to DefaultSyncInterval.
	Interval time.Duration
	// MaxInterval caps the idle backoff. Consecutive change-free cycles double the
	// delay until it reaches this ceiling. Values below Interval are raised to it.
	MaxInterval time.Duration
	EnvLookup   func(string) (string, bool)
	Logger      *slog.Logger
	Metrics     *metrics.Metrics
}

// Syncer periodically pulls updates from Turso Cloud into the local SQLite replica
// and hot-swaps the CatalogSnapshot when catalog revisions change.
type Syncer struct {
	// mu serializes sync cycles: the periodic loop and on-demand callers (the
	// harvester sync endpoint) share the lastKnown* cursors below, so letting two
	// cycles overlap would race on them and could double-apply a reload.
	mu                 sync.Mutex
	client             *Client
	store              *Store
	reg                *registry.Registry
	cfg                SyncerConfig
	lastKnownRevision  int64
	lastKnownKeyUpdate int64
	lastKnownKeyCount  int64
}

// NewSyncer creates a new Syncer instance.
func NewSyncer(client *Client, store *Store, reg *registry.Registry, cfg SyncerConfig) *Syncer {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultSyncInterval
	}
	if cfg.MaxInterval <= 0 {
		cfg.MaxInterval = DefaultSyncMaxInterval
	}
	if cfg.MaxInterval < cfg.Interval {
		cfg.MaxInterval = cfg.Interval
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Syncer{
		client: client,
		store:  store,
		reg:    reg,
		cfg:    cfg,
	}
}

// SetInitialRevision records the initial revision so the syncer knows the starting point.
func (s *Syncer) SetInitialRevision(rev int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastKnownRevision = rev
}

// Run executes the periodic pull and reload loop until ctx is canceled. The
// delay between cycles adapts to observed activity: a change resets it to the
// configured base interval, while consecutive idle cycles double it up to
// MaxInterval. That keeps idle sync quota proportional to actual traffic.
func (s *Syncer) Run(ctx context.Context) error {
	interval := s.cfg.Interval
	timer := time.NewTimer(interval)
	defer timer.Stop()

	s.cfg.Logger.Info("turso cloud syncer started",
		"interval", interval,
		"max_interval", s.cfg.MaxInterval)

	for {
		select {
		case <-ctx.Done():
			s.cfg.Logger.Info("turso syncer stopping on context cancellation")
			return ctx.Err()
		case <-timer.C:
			changed, err := s.syncOnce(ctx)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					s.cfg.Logger.Warn("turso background sync check encountered warning", "err", err)
				}
				// Keep the current cadence: the interval already validated as
				// reachable, and a failing pull must not stretch into a longer gap.
			} else {
				interval = nextSyncInterval(interval, s.cfg.Interval, s.cfg.MaxInterval, changed)
			}
			// Operators tune sync quota by reading this line: it shows both what
			// the cycle observed and the delay the loop settled on.
			s.cfg.Logger.Debug("turso sync cycle complete",
				"changed", changed,
				"errored", err != nil,
				"next_interval", interval)
			timer.Reset(interval)
		}
	}
}

// nextSyncInterval returns the delay before the next periodic pull. A cycle that
// observed a catalog change resets to base so propagation of a burst stays
// prompt; an idle cycle doubles the current delay up to max (doubling rather
// than jumping to max keeps detection latency low right after activity settles).
func nextSyncInterval(current, base, maxInterval time.Duration, changed bool) time.Duration {
	if base <= 0 {
		base = DefaultSyncInterval
	}
	if maxInterval < base {
		maxInterval = base
	}
	if changed {
		return base
	}
	next := current * 2
	if next < base {
		next = base
	}
	if next > maxInterval {
		next = maxInterval
	}
	return next
}

// SyncOnce performs a single pull and applies any pending catalog change.
// It is safe to call concurrently with Run: cycles are serialized so the cursors
// advance one cycle at a time.
func (s *Syncer) SyncOnce(ctx context.Context) error {
	_, err := s.syncOnce(ctx)
	return err
}

// syncOnce runs one cycle and reports whether it applied a new catalog snapshot.
func (s *Syncer) syncOnce(ctx context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. Pull changes from Turso Cloud. Pull rebases any local unpushed writes on
	//    top of the remote changes, so cycles never lose buffered local updates.
	if s.client != nil {
		if err := s.client.Pull(ctx); err != nil {
			return false, fmt.Errorf("pull turso updates: %w", err)
		}
	}

	// 2. Check and deactivate expired keys automatically in local replica
	if s.store != nil {
		if deactivated, err := s.store.DeactivateExpiredKeys(ctx); err == nil && deactivated > 0 {
			s.cfg.Logger.Info("turso syncer deactivated expired keys", "count", deactivated)
			if s.client != nil {
				_ = s.client.Push(ctx)
			}
		}
	}

	// 3. Check catalog revision and api_keys changes
	currentRev, err := s.store.GetCatalogRevision(ctx)
	if err != nil {
		return false, fmt.Errorf("read catalog revision: %w", err)
	}

	keyUpdate, keyCount, _ := s.store.GetKeysState(ctx)

	revisionChanged := currentRev > s.lastKnownRevision
	keysChanged := (s.lastKnownKeyUpdate > 0 && keyUpdate > s.lastKnownKeyUpdate) ||
		(s.lastKnownKeyCount > 0 && keyCount != s.lastKnownKeyCount)

	if !revisionChanged && !keysChanged {
		if s.lastKnownKeyUpdate == 0 {
			s.lastKnownKeyUpdate = keyUpdate
		}
		if s.lastKnownKeyCount == 0 {
			s.lastKnownKeyCount = keyCount
		}
		return false, nil
	}

	s.cfg.Logger.Info("detected catalog or key updates from turso cloud",
		"previous_revision", s.lastKnownRevision,
		"new_revision", currentRev,
		"keys_count", keyCount,
	)

	// 4. Rebuild snapshot from local replica
	snap, warns, err := s.store.LoadCatalogSnapshot(ctx, s.cfg.EnvLookup)
	if err != nil {
		// Fail-closed invariant: preserve previous valid snapshot on error
		return false, fmt.Errorf("compile snapshot for revision %d: %w", currentRev, err)
	}

	for _, w := range warns {
		s.cfg.Logger.Warn("catalog warning during turso reload", "warning", w)
	}

	// 5. Atomically swap in memory
	if s.reg != nil {
		s.reg.Store(snap)
		if s.cfg.Metrics != nil {
			s.cfg.Metrics.SetConfigGeneration(s.reg.CurrentGeneration())
		}
	}
	s.lastKnownRevision = currentRev
	s.lastKnownKeyUpdate = keyUpdate
	s.lastKnownKeyCount = keyCount

	gen := uint64(0)
	if s.reg != nil {
		gen = s.reg.CurrentGeneration()
	}
	s.cfg.Logger.Info("turso catalog snapshot successfully updated and hot-swapped",
		"revision", currentRev,
		"generation", gen,
		"enabled_models", len(snap.EnabledModels()),
	)

	return true, nil
}
