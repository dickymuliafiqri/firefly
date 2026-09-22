// Package registry holds the active, immutable CatalogSnapshot and rebuilds it
// on reload. Reads are lock-free via atomic.Pointer; writes happen in a single
// reload worker and are serialized.
package registry

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
)

// Registry owns the active snapshot and a monotonic generation counter.
type Registry struct {
	active     atomic.Pointer[domain.CatalogSnapshot]
	generation atomic.Uint64
}

// New returns an empty registry with no active snapshot.
func New() *Registry {
	return &Registry{}
}

// Current returns the active snapshot, or nil if none has been loaded yet.
func (r *Registry) Current() *domain.CatalogSnapshot {
	return r.active.Load()
}

// Store atomically swaps in a new snapshot and bumps the generation. It is
// called by the reload worker only.
func (r *Registry) Store(s *domain.CatalogSnapshot) {
	r.active.Store(s)
}

// NextGeneration returns the next monotonic generation id.
func (r *Registry) NextGeneration() uint64 {
	return r.generation.Add(1)
}

// CurrentGeneration returns the latest assigned generation id.
func (r *Registry) CurrentGeneration() uint64 {
	return r.generation.Load()
}

// BuildAndStore loads config via src, validates it, and stores the resulting
// snapshot. On any error the previous snapshot is left untouched (fail-closed).
// It returns the warnings produced during validation on success.
//
// The snapshot built here must carry the same options as the settings-update
// path (handleUpdateSettings): this function runs at startup and on every
// watcher reload, so an option missing here is silently dropped from the live
// snapshot the moment the process reloads.
func (r *Registry) BuildAndStore(ctx context.Context, src ports.ConfigSource, envLookup func(string) (string, bool)) ([]string, error) {
	raw, err := src.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	res, err := config.Build(config.FileSetFromMap(raw), envLookup)
	if err != nil {
		return nil, err
	}
	snap := domain.NewCatalogSnapshot(
		r.NextGeneration(),
		res.Upstreams,
		res.UpstreamOrder,
		res.Models,
		res.EnabledModelIDs,
		res.TenantsByHash,
		res.TenantOrder,
		domain.WithCombos(res.Combos, res.ComboOrder),
		domain.WithTokenSaver(res.TokenSaver),
	)
	r.Store(snap)
	return res.Warnings, nil
}
