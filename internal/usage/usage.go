// Package usage records per-tenant/per-credential/per-model request counters.
// Counters are lock-free on the hot path (sync.Map -> *atomic.Int64) and an
// optional background goroutine periodically exports a snapshot.
package usage

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/ports"
)

// counterKey is the composite identity of a counter row.
type counterKey struct {
	credentialRef string
	tenantName    string
	model         string
}

// Counters is a lock-free usage recorder satisfying ports.UsageRecorder.
type Counters struct {
	m sync.Map // counterKey -> *atomic.Int64
}

// NewCounters returns an empty recorder.
func NewCounters() *Counters { return &Counters{} }

// Record increments the counter for the given identity. It never blocks and is
// safe to call from the request hot path.
func (c *Counters) Record(credentialRef, tenantName, model string, reqCount int64) {
	k := counterKey{credentialRef: credentialRef, tenantName: tenantName, model: model}
	v, ok := c.m.Load(k)
	if !ok {
		v, _ = c.m.LoadOrStore(k, new(atomic.Int64))
	}
	v.(*atomic.Int64).Add(reqCount)
}

// Snapshot returns all counter rows. Order is unspecified.
//
// The type assertions use the comma-ok form: a sync.Map is untyped by nature,
// and a bare `k.(counterKey)` would panic the whole process if a future caller
// ever stored a different key shape. Since Snapshot feeds the metrics/admin
// plane, degrading to a skipped row is strictly better than a crash.
func (c *Counters) Snapshot() []ports.UsageCounter {
	var out []ports.UsageCounter
	c.m.Range(func(k, v any) bool {
		key, ok := k.(counterKey)
		if !ok {
			return true // foreign key: skip rather than panic
		}
		ctr, ok := v.(*atomic.Int64)
		if !ok || ctr == nil {
			return true
		}
		out = append(out, ports.UsageCounter{
			CredentialRef: key.credentialRef,
			TenantName:    key.tenantName,
			Model:         key.model,
			Requests:      ctr.Load(),
		})
		return true
	})
	return out
}

// Total sums all counter values. Useful for metrics.
func (c *Counters) Total() int64 {
	var total int64
	c.m.Range(func(_, v any) bool {
		if ctr, ok := v.(*atomic.Int64); ok && ctr != nil {
			total += ctr.Load()
		}
		return true
	})
	return total
}

var _ ports.UsageRecorder = (*Counters)(nil)

// Exporter periodically logs a usage snapshot and pushes DELTAS to a metrics
// sink. It runs until ctx is cancelled and is the seam where a future
// Prometheus collector or billing sink plugs in.
//
// Delta accounting: Prometheus counters are monotonic, so we must export the
// CHANGE since the last tick, not the running total (which would double-count).
// lastSeen tracks the per-row value previously exported.
type Exporter struct {
	Counters *Counters
	Interval time.Duration
	Logger   *slog.Logger
	// Metrics, when non-nil, receives usage deltas each tick.
	Metrics UsageMetrics

	// lastSeen maps counterKey -> last exported value. Guarded by run-loop
	// confinement: it is only ever touched by the single Run goroutine.
	lastSeen map[counterKey]int64
}

// UsageMetrics is the metrics sink the exporter pushes to.
type UsageMetrics interface {
	AddUsage(tenant, credentialRef, model string, n int64)
	SetConfigGeneration(gen uint64)
}

// Run blocks until ctx is done, exporting usage each interval.
func (e *Exporter) Run(ctx context.Context) {
	interval := e.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	logger := e.Logger
	if logger == nil {
		logger = slog.Default()
	}
	e.lastSeen = make(map[counterKey]int64)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			e.export(logger)
			return
		case <-t.C:
			e.export(logger)
		}
	}
}

// export logs a snapshot and pushes usage deltas to the metrics sink.
func (e *Exporter) export(logger *slog.Logger) {
	if e.Counters == nil {
		return
	}
	snap := e.Counters.Snapshot()
	logger.Info("usage snapshot", "rows", len(snap), "total_requests", e.Counters.Total())
	if e.Metrics == nil {
		return
	}
	if e.lastSeen == nil {
		e.lastSeen = make(map[counterKey]int64)
	}
	for _, row := range snap {
		k := counterKey{credentialRef: row.CredentialRef, tenantName: row.TenantName, model: row.Model}
		prev := e.lastSeen[k]
		if delta := row.Requests - prev; delta > 0 {
			e.Metrics.AddUsage(row.TenantName, row.CredentialRef, row.Model, delta)
			e.lastSeen[k] = row.Requests
		}
	}
}
