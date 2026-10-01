// Package resmon provides lightweight host and process resource sampling for
// the dashboard resource monitor (htop-style): CPU utilization, memory usage,
// network throughput, and goroutine counts.
//
// The sampler is lazy: every Sample() call computes rates as deltas against
// the previous call, so no background goroutine is required and an idle
// dashboard costs nothing. Rates are measured over the wall-clock window
// between consecutive samples; when the dashboard polls /api/telemetry every
// 2 seconds that window tracks live utilization closely. If the previous
// sample is older than maxSampleWindow (dashboard closed, quiet instance),
// rates are reported as zero and the window re-primes instead of averaging a
// misleading long interval.
package resmon

import (
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// maxSampleWindow bounds the delta window considered valid for rate
// computation. Staler windows are re-primed rather than averaged.
const maxSampleWindow = 15 * time.Second

// Stats is one point-in-time resource snapshot. Rate fields (CPU %, network
// bytes/s) are measured over the window since the previous Sample() call; the
// very first sample of a process reports zero rates because no window exists
// yet. All fields are always present in JSON (never omitted) so consumers can
// destructure without null checks; wire arrays are never null.
type Stats struct {
	Timestamp int64 `json:"timestamp"` // unix millis

	CPUPercent float64   `json:"cpu_percent"`  // aggregate host CPU, 0..100
	CPUPerCore []float64 `json:"cpu_per_core"` // per logical core, 0..100
	CPUCores   int       `json:"cpu_cores"`    // logical core count

	MemTotal   uint64  `json:"mem_total_bytes"`
	MemUsed    uint64  `json:"mem_used_bytes"`
	MemPercent float64 `json:"mem_percent"`
	SwapTotal  uint64  `json:"swap_total_bytes"`
	SwapUsed   uint64  `json:"swap_used_bytes"`

	NetRxRate  float64 `json:"net_rx_bps"` // receive throughput, bytes/s
	NetTxRate  float64 `json:"net_tx_bps"` // transmit throughput, bytes/s
	NetRxTotal uint64  `json:"net_rx_total_bytes"`
	NetTxTotal uint64  `json:"net_tx_total_bytes"`

	Goroutines     int     `json:"goroutines"`
	GoHeapBytes    uint64  `json:"go_heap_bytes"`
	GoSysBytes     uint64  `json:"go_sys_bytes"`
	ProcRSSBytes   uint64  `json:"proc_rss_bytes"`
	ProcCPUPercent float64 `json:"proc_cpu_percent"` // 0..100 across all cores
}

// Sampler computes resource deltas between successive Sample() calls. It is
// safe for concurrent use and holds no background goroutines.
type Sampler struct {
	mu sync.Mutex

	lastCPU     cpu.TimesStat
	lastPerCore []cpu.TimesStat
	lastProc    cpu.TimesStat
	lastNet     gnet.IOCountersStat
	lastTime    time.Time
	primed      bool
}

// New returns a ready-to-use sampler. The first Sample() primes the baseline.
func New() *Sampler {
	return &Sampler{}
}

// Sample reads current host and process counters and returns a Stats snapshot
// with rates computed over the window since the previous call.
func (s *Sampler) Sample() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	st := Stats{
		Timestamp:  now.UnixMilli(),
		CPUPerCore: []float64{},
	}

	dt := time.Duration(0)
	if s.primed {
		dt = now.Sub(s.lastTime)
	}
	freshWindow := s.primed && dt > 0 && dt <= maxSampleWindow
	sec := dt.Seconds()
	nCPU := float64(runtime.NumCPU())

	// ---- Host CPU: aggregate + per logical core ----
	if times, err := cpu.Times(false); err == nil && len(times) > 0 {
		cur := times[0]
		if freshWindow {
			st.CPUPercent = busyPercent(cur, s.lastCPU)
		}
		s.lastCPU = cur
	}
	if cores, err := cpu.Times(true); err == nil && len(cores) > 0 {
		if st.CPUPerCore == nil {
			st.CPUPerCore = []float64{}
		}
		if freshWindow {
			n := len(cores)
			if len(s.lastPerCore) < n {
				n = len(s.lastPerCore)
			}
			for i := 0; i < n; i++ {
				st.CPUPerCore = append(st.CPUPerCore, busyPercent(cores[i], s.lastPerCore[i]))
			}
		}
		st.CPUCores = len(cores)
		s.lastPerCore = cores
	} else {
		st.CPUCores = runtime.NumCPU()
	}

	// ---- Host memory & swap ----
	if vm, err := mem.VirtualMemory(); err == nil {
		st.MemTotal = vm.Total
		st.MemUsed = vm.Used
		st.MemPercent = clampPercent(vm.UsedPercent)
	}
	if sw, err := mem.SwapMemory(); err == nil {
		st.SwapTotal = sw.Total
		st.SwapUsed = sw.Used
	}

	// ---- Host network throughput (all interfaces) ----
	if ios, err := gnet.IOCounters(false); err == nil && len(ios) > 0 {
		cur := ios[0]
		if freshWindow && sec > 0 {
			st.NetRxRate = deltaRate(float64(cur.BytesRecv), float64(s.lastNet.BytesRecv), sec)
			st.NetTxRate = deltaRate(float64(cur.BytesSent), float64(s.lastNet.BytesSent), sec)
		}
		st.NetRxTotal = cur.BytesRecv
		st.NetTxTotal = cur.BytesSent
		s.lastNet = cur
	}

	// ---- This process: CPU % and RSS ----
	if p, err := process.NewProcess(int32(os.Getpid())); err == nil {
		if pt, err := p.Times(); err == nil {
			if freshWindow && sec > 0 {
				delta := (pt.User + pt.System) - (s.lastProc.User + s.lastProc.System)
				if delta < 0 {
					delta = 0
				}
				st.ProcCPUPercent = clampPercent(delta / sec / nCPU * 100)
			}
			s.lastProc = *pt
		}
		if mi, err := p.MemoryInfo(); err == nil {
			st.ProcRSSBytes = mi.RSS
		}
	}

	// ---- Go runtime ----
	st.Goroutines = runtime.NumGoroutine()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	st.GoHeapBytes = ms.HeapAlloc
	st.GoSysBytes = ms.Sys

	s.lastTime = now
	s.primed = true
	return st
}

// busyPercent derives 0..100 utilization from two cumulative CPU time stats,
// counting idle and iowait as non-busy.
func busyPercent(cur, prev cpu.TimesStat) float64 {
	total := cur.Total() - prev.Total()
	if total <= 0 {
		return 0
	}
	idle := (cur.Idle + cur.Iowait) - (prev.Idle + prev.Iowait)
	busy := total - idle
	if busy <= 0 {
		return 0
	}
	return clampPercent(busy / total * 100)
}

// deltaRate converts a monotonic byte counter delta into bytes per second,
// tolerating counter resets (negative deltas clamp to 0).
func deltaRate(cur, prev, sec float64) float64 {
	if cur < prev {
		return 0
	}
	return (cur - prev) / sec
}

func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
