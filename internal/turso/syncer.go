package turso

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/metrics"
	"github.com/dickymuliafiqri/firefly/internal/registry"
)

// SyncerConfig holds dependencies and configuration for the Turso background syncer.
type SyncerConfig struct {
	Interval  time.Duration
	EnvLookup func(string) (string, bool)
	Logger    *slog.Logger
	Metrics   *metrics.Metrics
}

// Syncer periodically pulls updates from Turso Cloud into the local SQLite replica
// and hot-swaps the CatalogSnapshot when catalog revisions change.
type Syncer struct {
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
		cfg.Interval = 15 * time.Second
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
	s.lastKnownRevision = rev
}

// Run executes the periodic pull and reload loop until ctx is canceled.
func (s *Syncer) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()

	s.cfg.Logger.Info("turso cloud syncer started", "interval", s.cfg.Interval)

	for {
		select {
		case <-ctx.Done():
			s.cfg.Logger.Info("turso syncer stopping on context cancellation")
			return ctx.Err()
		case <-ticker.C:
			if err := s.SyncOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				s.cfg.Logger.Warn("turso background sync check encountered warning", "err", err)
			}
		}
	}
}

// SyncOnce performs a single pull and checks if the catalog revision was updated.
func (s *Syncer) SyncOnce(ctx context.Context) error {
	// 1. Pull changes from Turso Cloud
	if s.client != nil {
		if err := s.client.Pull(ctx); err != nil {
			return fmt.Errorf("pull turso updates: %w", err)
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
		return fmt.Errorf("read catalog revision: %w", err)
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
		return nil
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
		return fmt.Errorf("compile snapshot for revision %d: %w", currentRev, err)
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

	return nil
}
