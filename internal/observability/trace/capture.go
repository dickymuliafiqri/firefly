package trace

import (
	"context"
	"strconv"
	"sync"
	"time"
)

// Capture accumulates one request's trace. Every method is nil-safe so hot
// paths never branch on whether tracing is on.
type Capture struct {
	mu      sync.Mutex
	rec     *Recorder
	t       Trace
	events  []Event
	started time.Time
	last    int64
	lastOut int64
}

type ctxKey struct{}

// WithCapture attaches a capture to the context so the streaming relay can
// report deltas without threading an extra parameter through every adapter.
func WithCapture(ctx context.Context, c *Capture) context.Context {
	if c == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, c)
}

// FromContext returns the capture attached to ctx, or nil.
func FromContext(ctx context.Context) *Capture {
	if ctx == nil {
		return nil
	}
	c, _ := ctx.Value(ctxKey{}).(*Capture)
	return c
}

// Stage records a lifecycle milestone and pushes a live snapshot.
func (c *Capture) Stage(name, detail string) {
	if c == nil {
		return
	}
	at := time.Now().UnixMilli()
	c.mu.Lock()
	c.t.Stages = append(c.t.Stages, Stage{Name: name, At: at, Delta: at - c.last, Detail: detail})
	c.last = at
	if name == StageTTFB && c.t.TTFBMs == 0 {
		c.t.TTFBMs = at - c.t.StartedAt
	}
	if name == StageStream {
		c.t.State = StateStream
	}
	c.t.Activity = Activity{Kind: KindStage, State: c.t.State, Stage: name, Reason: detail, Bytes: c.t.Bytes, Deltas: c.t.Deltas, At: at}
	c.eventLocked(KindStage, name, detail, at)
	c.flushLocked(false, at)
	c.mu.Unlock()
}

// Candidate records one routing branch: the chosen upstream/key, or a skipped
// alternative with the reason it lost.
func (c *Capture) Candidate(upstream, keyRef, state, note string) {
	if c == nil {
		return
	}
	at := time.Now().UnixMilli()
	c.mu.Lock()
	c.t.Candidates = append(c.t.Candidates, Candidate{Upstream: upstream, Key: MaskRef(keyRef), State: state, Note: note})
	c.eventLocked(KindStage, "candidate", upstream+" "+state+noteSuffix(note), at)
	c.flushLocked(false, at)
	c.mu.Unlock()
}

// SetTarget records the resolved routing decision.
func (c *Capture) SetTarget(upstream, protocol, keyRef string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.t.Upstream, c.t.Protocol, c.t.KeyRef = upstream, protocol, MaskRef(keyRef)
	c.mu.Unlock()
}

// SetUsage records the billed token counts once they are known.
func (c *Capture) SetUsage(in, out int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.t.TokensIn, c.t.TokensOut = in, out
	c.mu.Unlock()
}

// Fail records a client-visible failure and finishes the trace.
func (c *Capture) Fail(status int, err error) {
	if c == nil {
		return
	}
	if msg := errText(err); msg != "" {
		c.mu.Lock()
		c.t.Error = msg
		c.eventLocked(KindError, "error", msg, time.Now().UnixMilli())
		c.mu.Unlock()
	}
	c.Finish(status, nil)
}

// Finish closes the capture and publishes the terminal frame. The first call
// wins, so a deferred Finish next to an explicit Fail is harmless.
func (c *Capture) Finish(status int, err error) {
	if c == nil {
		return
	}
	at := time.Now().UnixMilli()
	c.mu.Lock()
	if c.t.EndedAt != 0 {
		c.mu.Unlock()
		return
	}
	if msg := errText(err); msg != "" {
		c.t.Error = msg
	}
	c.t.EndedAt = at
	c.t.Status = status
	c.t.DurationMs = at - c.t.StartedAt
	if c.t.TTFBMs == 0 {
		c.t.TTFBMs = c.t.DurationMs
	}
	if c.t.Error != "" || status >= 400 {
		c.t.State = StateError
	} else {
		c.t.State = StateDone
	}
	c.t.Stages = append(c.t.Stages, Stage{Name: StageDone, At: at, Delta: at - c.last, Detail: statusText(status)})
	c.t.Activity = Activity{Kind: KindStage, State: c.t.State, Stage: StageDone, Reason: statusText(status), Bytes: c.t.Bytes, Deltas: c.t.Deltas, At: at}
	snapshot := c.t
	snapshot.Events = append([]Event(nil), c.events...)
	snapshot.Phases = append([]string(nil), c.t.Phases...)
	c.mu.Unlock()
	c.rec.live.Add(-1)
	c.rec.publish(snapshot)
}

// OnDelta classifies one raw SSE line and refreshes the live activity.
func (c *Capture) OnDelta(raw []byte) {
	if c == nil {
		return
	}
	kind, size, ok := Classify(raw)
	if !ok {
		return
	}
	at := time.Now().UnixMilli()
	c.mu.Lock()
	if len(c.t.Stages) == 0 || c.t.Stages[len(c.t.Stages)-1].Name != StageStream {
		c.t.Stages = append(c.t.Stages, Stage{Name: StageStream, At: at, Delta: at - c.last})
		c.last = at
		c.t.State = StateStream
		c.eventLocked(KindStage, StageStream, "", at)
	}
	c.t.Bytes += size
	c.t.Deltas++
	phaseSeen := false
	for _, p := range c.t.Phases {
		if p == kind {
			phaseSeen = true
			break
		}
	}
	if !phaseSeen {
		c.t.Phases = append(c.t.Phases, kind)
	}
	c.t.Activity = Activity{Kind: kind, State: StateStream, Stage: StageStream, Bytes: c.t.Bytes, Deltas: c.t.Deltas, At: at}
	c.flushLocked(false, at)
	c.mu.Unlock()
}

// eventLocked appends a raw frame, keeping only the newest events.
func (c *Capture) eventLocked(kind, name, detail string, at int64) {
	c.events = append(c.events, Event{At: at, Kind: kind, Name: name, Detail: detail})
	if max := c.rec.keep; len(c.events) > max {
		c.events = append(c.events[:0], c.events[len(c.events)-max:]...)
	}
}

// flushLocked broadcasts a throttled live snapshot of the trace.
func (c *Capture) flushLocked(force bool, at int64) {
	if !force && at-c.lastOut < flushInterval.Milliseconds() {
		return
	}
	c.lastOut = at
	snap := c.t
	snap.Events = append([]Event(nil), c.events...)
	c.rec.broadcast(Frame{Type: "trace", Trace: snap})
}

func statusText(status int) string {
	if status == 0 || status == 200 {
		return ""
	}
	return "HTTP " + strconv.Itoa(status)
}

func noteSuffix(note string) string {
	if note == "" {
		return ""
	}
	return " (" + note + ")"
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
