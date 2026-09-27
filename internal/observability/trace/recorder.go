package trace

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"
)

// Default bounds for the in-memory ring and the per-trace stage log, plus the
// hard cap on concurrent SSE subscribers. Capture only runs while at least one
// subscriber is attached, so an unwatched gateway pays nothing for these.
const (
	DefaultRetention  = 500
	DefaultKeepEvents = 24
	MaxSubscribers    = 4
	flushInterval     = 100 * time.Millisecond
)

// Config bounds the recorder.
type Config struct {
	Retention  int
	KeepEvents int
}

// Stats describes recorder state for the dashboard header.
type Stats struct {
	Enabled     bool  `json:"enabled"`
	Live        bool  `json:"live"`
	Retention   int   `json:"retention"`
	Captured    int64 `json:"captured"`
	Inflight    int64 `json:"inflight"`
	Dropped     int64 `json:"dropped"`
	Subscribers int64 `json:"subscribers"`
}

// Recorder keeps the bounded ring of finished traces and fans live frames out
// to subscribers. Capture runs only while at least one subscriber is attached,
// so an unwatched gateway pays nothing on the data plane.
type Recorder struct {
	mu       sync.Mutex
	ring     []Trace
	subs     map[int]chan []byte
	nextSub  int
	enabled  atomic.Bool
	live     atomic.Int64
	captured atomic.Int64
	dropped  atomic.Int64
	subsN    atomic.Int64
	retain   int
	keep     int
}

// New builds a recorder with the given bounds; zero values fall back to the
// package defaults.
func New(cfg Config) *Recorder {
	r := &Recorder{subs: make(map[int]chan []byte), retain: cfg.Retention, keep: cfg.KeepEvents}
	if r.retain <= 0 {
		r.retain = DefaultRetention
	}
	if r.keep <= 0 {
		r.keep = DefaultKeepEvents
	}
	r.ring = make([]Trace, 0, r.retain)
	r.enabled.Store(true)
	return r
}

// Enabled reports whether capture is switched on.
func (r *Recorder) Enabled() bool { return r != nil && r.enabled.Load() }

// SetEnabled toggles capture. Disabling does not clear existing traces.
func (r *Recorder) SetEnabled(on bool) {
	if r == nil {
		return
	}
	r.enabled.Store(on)
}

// Retention reports the configured ring size.
func (r *Recorder) Retention() int {
	if r == nil {
		return 0
	}
	return r.retain
}

// Stats returns a point-in-time view of the recorder.
func (r *Recorder) Stats() Stats {
	if r == nil {
		return Stats{}
	}
	subs, enabled := r.subsN.Load(), r.enabled.Load()
	return Stats{
		Enabled:     enabled,
		Live:        enabled && subs > 0,
		Retention:   r.retain,
		Captured:    r.captured.Load(),
		Inflight:    r.live.Load(),
		Dropped:     r.dropped.Load(),
		Subscribers: subs,
	}
}

// Snapshot returns the ring newest-first.
func (r *Recorder) Snapshot() []Trace {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	ring := append([]Trace(nil), r.ring...)
	r.mu.Unlock()
	out := make([]Trace, len(ring))
	for i := range ring {
		out[len(ring)-1-i] = ring[i]
	}
	return out
}

// Clear drops every remembered trace.
func (r *Recorder) Clear() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.ring = r.ring[:0]
	r.mu.Unlock()
}

// Subscribe registers a live frame stream. The returned function unregisters.
func (r *Recorder) Subscribe(buf int) (<-chan []byte, func()) {
	if r == nil {
		return nil, func() {}
	}
	if buf <= 0 {
		buf = 256
	}
	ch := make(chan []byte, buf)
	r.mu.Lock()
	id := r.nextSub
	r.nextSub++
	r.subs[id] = ch
	r.mu.Unlock()
	r.subsN.Store(int64(len(r.subs)))
	return ch, func() {
		r.mu.Lock()
		if c, ok := r.subs[id]; ok {
			delete(r.subs, id)
			close(c)
		}
		r.mu.Unlock()
		r.subsN.Store(int64(len(r.subs)))
	}
}

// Start begins a capture. It returns nil when capture is disabled or nobody is
// watching, and every Capture method tolerates a nil receiver.
func (r *Recorder) Start(meta Meta) *Capture {
	if r == nil || !r.enabled.Load() || r.subsN.Load() == 0 {
		return nil
	}
	now := time.Now()
	c := &Capture{rec: r, started: now, last: now.UnixMilli()}
	c.t = Trace{
		ID:        meta.ID,
		State:     StateRequest,
		StartedAt: now.UnixMilli(),
		Method:    meta.Method,
		Path:      meta.Path,
		Model:     meta.Model,
		Tenant:    meta.Tenant,
		Stream:    meta.Stream,
		Activity:  Activity{Kind: KindStage, State: StateRequest, At: now.UnixMilli()},
	}
	r.live.Add(1)
	return c
}

// publish stores a finished trace and broadcasts the terminal frame.
func (r *Recorder) publish(t Trace) {
	r.mu.Lock()
	if len(r.ring) == r.retain {
		copy(r.ring, r.ring[1:])
		r.ring[len(r.ring)-1] = t
	} else {
		r.ring = append(r.ring, t)
	}
	r.mu.Unlock()
	r.captured.Add(1)
	r.broadcast(Frame{Type: "done", Trace: t})
}

// broadcast pushes a frame to every subscriber, dropping it for slow readers.
func (r *Recorder) broadcast(f Frame) {
	if r == nil {
		return
	}
	data, err := json.Marshal(f)
	if err != nil {
		return
	}
	r.mu.Lock()
	for _, ch := range r.subs {
		select {
		case ch <- data:
		default:
			r.dropped.Add(1)
		}
	}
	r.mu.Unlock()
}
