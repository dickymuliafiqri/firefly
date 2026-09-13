package limits

import (
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// percentile returns the p-th percentile (0..1) of a sorted slice.
func percentile(ns []time.Duration, p float64) time.Duration {
	if len(ns) == 0 {
		return 0
	}
	idx := int(p * float64(len(ns)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(ns) {
		idx = len(ns) - 1
	}
	return ns[idx]
}

// TestLimiterHoldsCapUnderLoad drives many concurrent workers through a limiter
// with a hard concurrency cap and asserts the cap is NEVER exceeded, even though
// the offered load vastly exceeds it. This is the load-based counterpart to the
// single-shot TestReloadDoesNotOverAdmitInFlight: here we also record tail
// latencies and the in-flight high-water mark so the numbers can be calibrated
// against a deployment.
func TestLimiterHoldsCapUnderLoad(t *testing.T) {
	const (
		cap      = 8
		workers  = 128
		perWork  = 200
		holdTime = 200 * time.Microsecond
	)
	l := New()
	in := AcquireInput{TenantName: "load", RPS: 0, MaxConcurrent: cap} // RPS off: isolate the cap

	var (
		admitted  atomic.Int64
		rejected  atomic.Int64
		flight    atomic.Int64
		maxFlight atomic.Int64
	)
	bumpMax := func(v int64) {
		for {
			cur := maxFlight.Load()
			if v <= cur || maxFlight.CompareAndSwap(cur, v) {
				return
			}
		}
	}

	durations := make([]time.Duration, 0, workers*perWork)
	var durMu sync.Mutex

	start := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWork; i++ {
				t0 := time.Now()
				rel, err := l.Acquire(in)
				if err != nil {
					if err != ErrRateLimited {
						t.Errorf("unexpected error: %v", err)
						return
					}
					rejected.Add(1)
					continue
				}
				admitted.Add(1)
				bumpMax(flight.Add(1))
				time.Sleep(holdTime)
				flight.Add(-1)
				rel()
				durMu.Lock()
				durations = append(durations, time.Since(t0))
				durMu.Unlock()
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	// SAFETY INVARIANT: the observed high-water mark must never exceed the cap.
	if got := maxFlight.Load(); got > cap {
		t.Fatalf("over-admission: observed %d concurrent > cap %d", got, cap)
	}
	// The load must actually have exercised the cap, else the test is vacuous.
	if got := maxFlight.Load(); got < int64(cap) {
		t.Fatalf("cap never saturated (max=%d, want %d); load too light to be meaningful", got, cap)
	}

	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	t.Logf("load: workers=%d attempts=%d admitted=%d rejected=%d cap=%d maxFlight=%d",
		workers, workers*perWork, admitted.Load(), rejected.Load(), cap, maxFlight.Load())
	t.Logf("latency: p50=%s p95=%s p99=%s elapsed=%s",
		percentile(durations, 0.50), percentile(durations, 0.95),
		percentile(durations, 0.99), elapsed)

	// Admission + rejection must account for every attempt (no silent drops).
	if total := admitted.Load() + rejected.Load(); total != int64(workers*perWork) {
		t.Fatalf("lost attempts: admitted+rejected=%d, want %d", total, workers*perWork)
	}
}

// TestLimiterRPSAdmitsNearBudget calibrates the RPS bucket under light load: over
// a window, the number of admissions should be close to (but not exceed) the
// token budget. This is a sanity band, not a tight timing assertion, so it is
// robust on a loaded CI box.
func TestLimiterRPSAdmitsNearBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("timing-sensitive calibration skipped in -short")
	}
	const rps = 200.0
	l := New()
	in := AcquireInput{TenantName: "rps", RPS: rps, Burst: int(rps) / 4, MaxConcurrent: 0}

	window := 500 * time.Millisecond
	deadline := time.Now().Add(window)
	var admitted int64
	for time.Now().Before(deadline) {
		rel, err := l.Acquire(in)
		if err == nil {
			admitted++
			rel()
		} else if err != ErrRateLimited {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	budget := int64(rps * window.Seconds())
	ceiling := budget + int64(in.Burst) + 1
	if admitted > ceiling {
		t.Fatalf("RPS over-admission: admitted %d > ceiling %d (budget %d + burst %d)",
			admitted, ceiling, budget, in.Burst)
	}
	if admitted < budget/2 {
		t.Fatalf("RPS under-admission: admitted %d, expected near budget %d", admitted, budget)
	}
	t.Logf("rps calibration: admitted=%d budget=%d ceiling=%d", admitted, budget, ceiling)
}

// BenchmarkLimiterAcquireRelease measures the uncontended hot path.
func BenchmarkLimiterAcquireRelease(b *testing.B) {
	l := New()
	in := AcquireInput{TenantName: "bench", RPS: 1e9, MaxConcurrent: 1 << 20}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rel, err := l.Acquire(in)
		if err != nil {
			b.Fatal(err)
		}
		rel()
	}
}

// BenchmarkLimiterAcquireReleaseParallel measures the hot path under contention
// across all cores — the case that matters at high RPS. Run with -cpu 1,2,4,8 to
// see how the CAS loop in inflight.tryAcquire scales.
func BenchmarkLimiterAcquireReleaseParallel(b *testing.B) {
	l := New()
	in := AcquireInput{TenantName: "bench-p", RPS: 1e9, MaxConcurrent: 1 << 20}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			rel, err := l.Acquire(in)
			if err != nil {
				b.Fatal(err)
			}
			rel()
		}
	})
}

// BenchmarkLimiterRejectedPath measures the cost of a rejected admission when
// the concurrency cap is saturated — the hot path a flood of excess traffic
// hits, so it must stay cheap (no allocation, no lock).
func BenchmarkLimiterRejectedPath(b *testing.B) {
	l := New()
	in := AcquireInput{TenantName: "bench-rej", MaxConcurrent: 1}
	rel, err := l.Acquire(in) // hold the only slot forever
	if err != nil {
		b.Fatal(err)
	}
	defer rel()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := l.Acquire(in); err != ErrRateLimited {
			b.Fatalf("expected ErrRateLimited, got %v", err)
		}
	}
}
