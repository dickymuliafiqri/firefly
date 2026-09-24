package upstream

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

func TestKeyRing_RoundRobin_Rotation(t *testing.T) {
	k1 := &domain.KeySlot{Ref: "K1"}
	k2 := &domain.KeySlot{Ref: "K2"}
	k3 := &domain.KeySlot{Ref: "K3"}

	kr := NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{k1, k2, k3})

	// The ring's starting cursor is seeded from a process-wide rotation counter
	// (so rotation survives snapshot rebuilds), therefore the absolute first
	// slot is not fixed across test runs. The invariant under test is strict
	// round-robin rotation: 6 consecutive selections must cycle through all 3
	// distinct slots twice in a stable order with no repeats within a cycle.
	refOrder := []string{"K1", "K2", "K3"}
	indexOf := map[string]int{"K1": 0, "K2": 1, "K3": 2}

	first, err := kr.SelectKey()
	if err != nil {
		t.Fatalf("initial select failed: %v", err)
	}
	startIdx := indexOf[first.Ref]

	// Verify the full cycle continues in order from wherever it started.
	got := []string{first.Ref}
	for i := 1; i < 6; i++ {
		slot, err := kr.SelectKey()
		if err != nil {
			t.Fatalf("call %d failed: %v", i, err)
		}
		got = append(got, slot.Ref)
	}
	for i, ref := range got {
		want := refOrder[(startIdx+i)%len(refOrder)]
		if ref != want {
			t.Fatalf("call %d: expected %s, got %s (full sequence %v)", i, want, ref, got)
		}
	}
}

func TestKeyRing_RoundRobin_SkipsCooldownAndRevoked(t *testing.T) {
	k1 := &domain.KeySlot{Ref: "K1"}
	k2 := &domain.KeySlot{Ref: "K2"}
	k3 := &domain.KeySlot{Ref: "K3"}

	kr := NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{k1, k2, k3})
	now := time.Now().UnixNano()

	// Put K2 into cooldown
	kr.MarkCooldownAt("K2", 30*time.Second, now)
	// Revoke K3
	kr.MarkRevoked("K3")

	// Only K1 should be available
	for i := 0; i < 5; i++ {
		slot, err := kr.SelectKeyAt(now)
		if err != nil {
			t.Fatalf("call %d: unexpected error %v", i, err)
		}
		if slot.Ref != "K1" {
			t.Fatalf("call %d: expected K1, got %s", i, slot.Ref)
		}
	}
}

func TestKeyRing_LeastInflight(t *testing.T) {
	k1 := &domain.KeySlot{Ref: "K1"}
	k2 := &domain.KeySlot{Ref: "K2"}
	k3 := &domain.KeySlot{Ref: "K3"}

	kr := NewKeyRing(domain.KeyStrategyLeastInflight, []*domain.KeySlot{k1, k2, k3})
	now := time.Now().UnixNano()

	// Set inflight: K1=3, K2=1, K3=2
	k1.Inflight.Store(3)
	k2.Inflight.Store(1)
	k3.Inflight.Store(2)

	// Should choose K2 (inflight=1)
	slot, err := kr.SelectKeyAt(now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if slot.Ref != "K2" {
		t.Fatalf("expected K2, got %s", slot.Ref)
	}

	// Update K2 inflight to 4 -> now K3 is lowest (inflight=2)
	k2.Inflight.Store(4)
	slot, err = kr.SelectKeyAt(now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if slot.Ref != "K3" {
		t.Fatalf("expected K3, got %s", slot.Ref)
	}
}

func TestKeyRing_AllExhausted(t *testing.T) {
	k1 := &domain.KeySlot{Ref: "K1"}
	k2 := &domain.KeySlot{Ref: "K2"}

	kr := NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{k1, k2})
	now := time.Now().UnixNano()

	// Both in cooldown
	kr.MarkCooldownAt("K1", 10*time.Second, now)
	kr.MarkCooldownAt("K2", 10*time.Second, now)

	slot, err := kr.SelectKeyAt(now)
	if !errors.Is(err, ErrAllKeysExhausted) {
		t.Fatalf("expected ErrAllKeysExhausted, got slot=%v, err=%v", slot, err)
	}

	// Empty ring
	emptyRing := NewKeyRing(domain.KeyStrategyRoundRobin, nil)
	slot, err = emptyRing.SelectKeyAt(now)
	if !errors.Is(err, ErrAllKeysExhausted) {
		t.Fatalf("expected ErrAllKeysExhausted for empty ring, got %v", err)
	}
}

func TestKeyRing_LazyCooldown(t *testing.T) {
	k1 := &domain.KeySlot{Ref: "K1"}
	kr := NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{k1})

	baseTime := time.Now()
	nowNano := baseTime.UnixNano()

	// Put into 10-second cooldown
	kr.MarkCooldownAt("K1", 10*time.Second, nowNano)

	// At baseTime + 5s, still in cooldown
	_, err := kr.SelectKeyAt(nowNano + (5 * time.Second).Nanoseconds())
	if !errors.Is(err, ErrAllKeysExhausted) {
		t.Fatalf("expected ErrAllKeysExhausted at +5s, got %v", err)
	}

	// At baseTime + 11s, cooldown has lazily expired
	slot, err := kr.SelectKeyAt(nowNano + (11 * time.Second).Nanoseconds())
	if err != nil {
		t.Fatalf("expected key to become available lazily at +11s, got error: %v", err)
	}
	if slot.Ref != "K1" {
		t.Fatalf("expected K1, got %s", slot.Ref)
	}
}

func TestKeyRing_Handle429And401(t *testing.T) {
	k1 := &domain.KeySlot{Ref: "K1"}
	kr := NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{k1})

	// Handle 429 with Retry-After: 15
	d := kr.Handle429("K1", "15")
	if d != 15*time.Second {
		t.Fatalf("expected 15s cooldown, got %v", d)
	}
	if !k1.IsInCooldown(time.Now().UnixNano()) {
		t.Fatalf("expected K1 to be in cooldown after 429")
	}

	// Handle 401
	kr.Handle401("K1")
	if !k1.Revoked.Load() {
		t.Fatalf("expected K1 to be revoked after 401")
	}
}

func TestParseRetryAfter(t *testing.T) {
	defaultDur := 30 * time.Second
	maxDur := 5 * time.Minute

	tests := []struct {
		header   string
		expected time.Duration
	}{
		{"", defaultDur},
		{"   ", defaultDur},
		{"0", defaultDur},
		{"-10", defaultDur},
		{"10", 10 * time.Second},
		{"120", 120 * time.Second},
		{"1.5", 1500 * time.Millisecond},
		{"999999", maxDur}, // capped by maxDuration
		{"invalid", defaultDur},
	}

	for _, tc := range tests {
		got := ParseRetryAfter(tc.header, defaultDur, maxDur)
		if got != tc.expected {
			t.Errorf("ParseRetryAfter(%q) = %v, want %v", tc.header, got, tc.expected)
		}
	}

	// Test HTTP-date in RFC1123
	futureTime := time.Now().Add(45 * time.Second).UTC().Format(http.TimeFormat)
	gotDate := ParseRetryAfter(futureTime, defaultDur, maxDur)
	if gotDate < 40*time.Second || gotDate > 50*time.Second {
		t.Errorf("ParseRetryAfter(RFC1123 %q) = %v, expected ~45s", futureTime, gotDate)
	}

	// Past HTTP-date
	pastTime := time.Now().Add(-10 * time.Minute).UTC().Format(http.TimeFormat)
	gotPast := ParseRetryAfter(pastTime, defaultDur, maxDur)
	if gotPast != defaultDur {
		t.Errorf("ParseRetryAfter(past RFC1123) = %v, want default %v", gotPast, defaultDur)
	}
}

func TestKeyRing_Concurrent_Stress(t *testing.T) {
	slots := make([]*domain.KeySlot, 5)
	for i := range slots {
		slots[i] = &domain.KeySlot{Ref: fmt.Sprintf("K%d", i)}
	}
	kr := NewKeyRing(domain.KeyStrategyRoundRobin, slots)

	var wg sync.WaitGroup
	workers := 50
	iterations := 2000

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				slot, err := kr.SelectKey()
				if err != nil && !errors.Is(err, ErrAllKeysExhausted) {
					t.Errorf("worker %d: unexpected error %v", workerID, err)
					return
				}
				if slot != nil && i%50 == 0 {
					// Concurrently apply cooldown on slot
					kr.MarkCooldown(slot.Ref, 50*time.Millisecond)
				}
			}
		}(w)
	}

	wg.Wait()
}

func TestKeyRing_Concurrent_RemoveSlot(t *testing.T) {
	const totalKeys = 10
	slots := make([]*domain.KeySlot, totalKeys)
	for i := range slots {
		slots[i] = &domain.KeySlot{Ref: fmt.Sprintf("K%d", i)}
	}
	kr := NewKeyRing(domain.KeyStrategyRoundRobin, slots)

	var wg sync.WaitGroup
	workers := 20
	iterations := 1000

	// Reader workers
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_, _ = kr.SelectKey()
				_ = kr.SlotCount()
				_ = kr.AllSlots()
				_ = kr.SlotByRef("K0")
				_ = kr.PrimarySlot()
			}
		}()
	}

	// Writer worker removing slots
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < totalKeys; i++ {
			time.Sleep(time.Millisecond)
			kr.RemoveSlot(fmt.Sprintf("K%d", i))
		}
	}()

	wg.Wait()

	if kr.SlotCount() != 0 {
		t.Fatalf("expected 0 slots after all removed, got %d", kr.SlotCount())
	}
}

func BenchmarkKeyRing_SelectKey_Parallel(b *testing.B) {
	slots := make([]*domain.KeySlot, 4)
	for i := range slots {
		slots[i] = &domain.KeySlot{Ref: fmt.Sprintf("K%d", i)}
	}
	kr := NewKeyRing(domain.KeyStrategyRoundRobin, slots)
	now := time.Now().UnixNano()

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			slot, err := kr.SelectKeyAt(now)
			if err != nil || slot == nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkKeyRing_LeastInflight_Parallel(b *testing.B) {
	slots := make([]*domain.KeySlot, 4)
	for i := range slots {
		slots[i] = &domain.KeySlot{Ref: fmt.Sprintf("K%d", i)}
	}
	kr := NewKeyRing(domain.KeyStrategyLeastInflight, slots)
	now := time.Now().UnixNano()

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			slot, err := kr.SelectKeyAt(now)
			if err != nil || slot == nil {
				b.Fatal(err)
			}
		}
	})
}

func TestKeyRing_ThroughputExceeds10MillionOps(t *testing.T) {
	slots := make([]*domain.KeySlot, 4)
	for i := range slots {
		slots[i] = &domain.KeySlot{Ref: fmt.Sprintf("K%d", i)}
	}
	kr := NewKeyRing(domain.KeyStrategyRoundRobin, slots)
	now := time.Now().UnixNano()

	res := testing.Benchmark(func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_, _ = kr.SelectKeyAt(now)
			}
		})
	})

	opsPerSec := float64(res.N) / res.T.Seconds()
	t.Logf("BenchmarkKeyRing Parallel: %.0f ops/sec (N=%d, T=%v, Allocs=%d B/op=%d, race=%v)",
		opsPerSec, res.N, res.T, res.AllocsPerOp(), res.AllocedBytesPerOp(), RaceDetectorEnabled)

	if res.AllocsPerOp() > 0 {
		t.Fatalf("expected 0 allocs/op on happy path, got %d", res.AllocsPerOp())
	}
	if RaceDetectorEnabled {
		if opsPerSec < 800_000 {
			t.Fatalf("expected throughput >= 800,000 ops/sec under race detector, got %.0f ops/sec", opsPerSec)
		}
	} else {
		if opsPerSec < 10_000_000 {
			t.Fatalf("expected throughput > 10,000,000 ops/sec without race detector, got %.0f ops/sec", opsPerSec)
		}
	}
}
