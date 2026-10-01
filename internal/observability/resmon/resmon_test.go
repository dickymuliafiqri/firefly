package resmon

import (
	"testing"
	"time"
)

// TestSampler_Invariants exercises two consecutive samples and asserts the
// structural invariants of the wire contract: counts are positive, percent
// fields stay in 0..100, arrays are non-null, and the second sample carries
// real rates because the delta window is primed by then. Values themselves
// are machine-dependent and never asserted.
func TestSampler_Invariants(t *testing.T) {
	s := New()

	first := s.Sample()
	if first.Timestamp <= 0 {
		t.Errorf("first.Timestamp = %d, want > 0", first.Timestamp)
	}
	if first.Goroutines <= 0 {
		t.Errorf("first.Goroutines = %d, want > 0", first.Goroutines)
	}
	if first.CPUPerCore == nil {
		t.Errorf("first.CPUPerCore is null, want [] (wire arrays never null)")
	}
	if first.CPUCores <= 0 {
		t.Errorf("first.CPUCores = %d, want > 0", first.CPUCores)
	}
	// The very first sample has no delta window: rates must be zero.
	if first.CPUPercent != 0 || first.NetRxRate != 0 || first.NetTxRate != 0 || first.ProcCPUPercent != 0 {
		t.Errorf("first sample rates = (cpu %.2f, rx %.2f, tx %.2f, proc %.2f), want all 0 before priming",
			first.CPUPercent, first.NetRxRate, first.NetTxRate, first.ProcCPUPercent)
	}

	// Keep the sampler alive across the window; generate a little goroutine
	// activity so runtime counters move.
	done := make(chan struct{})
	go func() {
		close(done)
		time.Sleep(30 * time.Millisecond)
	}()
	<-done
	time.Sleep(120 * time.Millisecond)

	second := s.Sample()
	if second.Timestamp <= first.Timestamp {
		t.Errorf("second.Timestamp = %d, want > first %d", second.Timestamp, first.Timestamp)
	}
	if second.CPUCores <= 0 {
		t.Errorf("second.CPUCores = %d, want > 0", second.CPUCores)
	}
	if len(second.CPUPerCore) == 0 {
		t.Errorf("second.CPUPerCore empty, want one entry per logical core")
	}
	if second.MemTotal <= 0 {
		t.Errorf("second.MemTotal = %d, want > 0", second.MemTotal)
	}
	if second.MemUsed <= 0 {
		t.Errorf("second.MemUsed = %d, want > 0", second.MemUsed)
	}
	if second.MemUsed > second.MemTotal {
		t.Errorf("second.MemUsed (%d) exceeds MemTotal (%d)", second.MemUsed, second.MemTotal)
	}
	if second.MemPercent < 0 || second.MemPercent > 100 {
		t.Errorf("second.MemPercent = %.2f, want within 0..100", second.MemPercent)
	}
	for i, v := range second.CPUPerCore {
		if v < 0 || v > 100 {
			t.Errorf("second.CPUPerCore[%d] = %.2f, want within 0..100", i, v)
		}
	}
	if second.CPUPercent < 0 || second.CPUPercent > 100 {
		t.Errorf("second.CPUPercent = %.2f, want within 0..100", second.CPUPercent)
	}
	if second.ProcCPUPercent < 0 || second.ProcCPUPercent > 100 {
		t.Errorf("second.ProcCPUPercent = %.2f, want within 0..100", second.ProcCPUPercent)
	}
	if second.NetRxRate < 0 || second.NetTxRate < 0 {
		t.Errorf("second net rates = (rx %.2f, tx %.2f), want >= 0", second.NetRxRate, second.NetTxRate)
	}
	if second.NetRxTotal <= 0 || second.NetTxTotal <= 0 {
		t.Errorf("second net totals = (rx %d, tx %d), want > 0 on any host with a network stack",
			second.NetRxTotal, second.NetTxTotal)
	}
	if second.Goroutines <= 0 {
		t.Errorf("second.Goroutines = %d, want > 0", second.Goroutines)
	}
	if second.GoHeapBytes <= 0 || second.GoSysBytes <= 0 {
		t.Errorf("second Go memory = (heap %d, sys %d), want > 0", second.GoHeapBytes, second.GoSysBytes)
	}
}

// TestSampler_StaleWindowReprimes pins the guard against misleading long
// averages: when the previous sample is older than maxSampleWindow, rates are
// zero and the window re-primes instead of dividing a huge delta.
func TestSampler_StaleWindowReprimes(t *testing.T) {
	s := New()
	_ = s.Sample() // prime

	// Simulate staleness by rewinding the previous sample time.
	s.mu.Lock()
	s.lastTime = time.Now().Add(-maxSampleWindow - time.Minute)
	s.mu.Unlock()

	st := s.Sample()
	if st.CPUPercent != 0 || st.NetRxRate != 0 || st.NetTxRate != 0 || st.ProcCPUPercent != 0 {
		t.Errorf("stale-window rates = (cpu %.2f, rx %.2f, tx %.2f, proc %.2f), want all 0 (re-primed)",
			st.CPUPercent, st.NetRxRate, st.NetTxRate, st.ProcCPUPercent)
	}
}

// TestSampler_ConcurrentSamples verifies the sampler is safe for concurrent
// dashboard pollers: racing Sample() calls must not panic or corrupt state.
func TestSampler_ConcurrentSamples(t *testing.T) {
	s := New()
	_ = s.Sample()

	done := make(chan struct{})
	for i := 0; i < 4; i++ {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("concurrent Sample panicked: %v", r)
				}
				done <- struct{}{}
			}()
			for j := 0; j < 5; j++ {
				st := s.Sample()
				if st.CPUPercent < 0 || st.CPUPercent > 100 {
					t.Errorf("CPUPercent out of range: %.2f", st.CPUPercent)
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
	}
	for i := 0; i < 4; i++ {
		<-done
	}
}
