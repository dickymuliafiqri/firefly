package usage

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRecordAndSnapshot(t *testing.T) {
	c := NewCounters()
	c.Record("K", "alpha", "gpt-4o", 1)
	c.Record("K", "alpha", "gpt-4o", 2)
	c.Record("K", "beta", "gpt-4o", 1)

	snap := c.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("rows = %d, want 2", len(snap))
	}
	var alpha int64
	for _, r := range snap {
		if r.TenantName == "alpha" {
			alpha = r.Requests
		}
	}
	if alpha != 3 {
		t.Fatalf("alpha requests = %d, want 3", alpha)
	}
	if got := c.Total(); got != 4 {
		t.Fatalf("total = %d, want 4", got)
	}
}

func TestRecordConcurrent(t *testing.T) {
	c := NewCounters()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.Record("K", "t", "m", 1) }()
	}
	wg.Wait()
	if got := c.Total(); got != 100 {
		t.Fatalf("total = %d, want 100", got)
	}
}

func TestExporterStopsOnCancel(t *testing.T) {
	c := NewCounters()
	e := &Exporter{Counters: c, Interval: time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("exporter did not stop on cancel")
	}
}

// TestSnapshotToleratesForeignEntries guards the safety invariant that Snapshot
// and Total use comma-ok type assertions. A sync.Map is untyped; if a future
// change ever stores a differently-typed key or value, a bare assertion would
// panic the metrics/admin path. We inject foreign entries directly and assert
// no panic plus correct results for the well-formed rows.
func TestSnapshotToleratesForeignEntries(t *testing.T) {
	c := NewCounters()
	c.Record("K", "alpha", "gpt-4o", 3)

	// Deliberately poison the map with a wrong key type and a wrong value type.
	c.m.Store("not-a-counterKey", new(atomic.Int64))
	c.m.Store(counterKey{credentialRef: "K", tenantName: "bad", model: "m"}, "not-a-counter")

	// Neither call may panic.
	snap := c.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("Snapshot rows = %d, want 1 (foreign entries skipped)", len(snap))
	}
	if snap[0].TenantName != "alpha" || snap[0].Requests != 3 {
		t.Fatalf("row = %+v, want alpha/3", snap[0])
	}
	if got := c.Total(); got != 3 {
		t.Fatalf("Total = %d, want 3 (foreign value skipped)", got)
	}
}

// TestEmptyCounters pins the behavior on an empty recorder: Snapshot must
// return an empty slice (len 0) and Total must be 0, with no panic.
func TestEmptyCounters(t *testing.T) {
	c := NewCounters()
	if got := c.Snapshot(); len(got) != 0 {
		t.Fatalf("empty Snapshot len = %d, want 0", len(got))
	}
	if got := c.Total(); got != 0 {
		t.Fatalf("empty Total = %d, want 0", got)
	}
}

type fakeSink struct {
	mu      sync.Mutex
	byKey   map[string]int64
	genSeen uint64
}

func (f *fakeSink) AddUsage(tenant, credentialRef, model string, n int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.byKey == nil {
		f.byKey = map[string]int64{}
	}
	f.byKey[tenant+"|"+credentialRef+"|"+model] += n
}
func (f *fakeSink) SetConfigGeneration(gen uint64) { f.genSeen = gen }
func (f *fakeSink) usage(k string) int64           { f.mu.Lock(); defer f.mu.Unlock(); return f.byKey[k] }

// TestExporterEmitsUsageDeltas proves the exporter pushes the CHANGE since the
// last tick, not the running total (otherwise Prometheus counters double-count).
func TestExporterEmitsUsageDeltas(t *testing.T) {
	c := NewCounters()
	sink := &fakeSink{}
	e := &Exporter{Counters: c, Interval: 10 * time.Millisecond, Metrics: sink}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()

	// First batch, then wait for at least one tick.
	c.Record("K", "alpha", "gpt-4o", 5)
	time.Sleep(40 * time.Millisecond)

	// Second batch; the delta must be +3, not +8 cumulative.
	c.Record("K", "alpha", "gpt-4o", 3)
	time.Sleep(40 * time.Millisecond)

	cancel()
	<-done

	// The final export on cancel flushes the remaining delta.
	if got := sink.usage("alpha|K|gpt-4o"); got != 8 {
		t.Fatalf("exported usage = %d, want 8 (5+3 as deltas)", got)
	}
}
