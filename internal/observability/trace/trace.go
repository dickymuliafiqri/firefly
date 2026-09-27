// Package trace records a lightweight in-memory picture of what the gateway
// does with each request: which upstream and credential were chosen, which
// candidates lost, and the delta-by-delta activity inside the SSE stream.
//
// It backs the admin-only /visualizer dashboard. Nothing is persisted, the
// recorder keeps a bounded ring, and publishing never blocks the data plane
// (slow subscribers drop frames instead of stalling a generation).
package trace

import (
	"bytes"

	"github.com/tidwall/gjson"
)

// Stage names emitted along the request lifecycle, in order. The dashboard
// renders its phase timeline from this sequence.
const (
	StageReceived = "received"
	StageResolve  = "resolve"
	StageRoute    = "route"
	StageKey      = "key"
	StageAttempt  = "attempt"
	StageTTFB     = "ttfb"
	StageStream   = "stream"
	StageDone     = "done"
)

// Activity kinds classified from the SSE stream.
const (
	KindStage     = "stage"
	KindReasoning = "reasoning"
	KindAnswer    = "answer"
	KindTool      = "tool"
	KindUsage     = "usage"
	KindError     = "error"
)

// Trace and candidate states.
const (
	StateRequest = "request"
	StateStream  = "stream"
	StateDone    = "done"
	StateError   = "error"

	CandidateChosen  = "chosen"
	CandidateSkipped = "skipped"
)

// Stage is one timestamped milestone in the request lifecycle.
type Stage struct {
	Name   string `json:"name"`
	At     int64  `json:"at"`
	Delta  int64  `json:"delta,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Candidate is one routing branch considered for the request: the winning
// upstream/key plus every alternative that was skipped and why.
type Candidate struct {
	Upstream string `json:"upstream"`
	Key      string `json:"key,omitempty"`
	State    string `json:"state"`
	Note     string `json:"note,omitempty"`
}

// Activity is the most recent thing the stream did, used for the live badge.
type Activity struct {
	Kind   string `json:"kind"`
	State  string `json:"state"`
	Stage  string `json:"stage,omitempty"`
	Reason string `json:"reason,omitempty"`
	Bytes  int    `json:"bytes"`
	Deltas int    `json:"deltas"`
	At     int64  `json:"at"`
}

// Event is one raw frame kept alongside the trace for the raw table view.
type Event struct {
	At     int64  `json:"at"`
	Kind   string `json:"kind"`
	Name   string `json:"name,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Meta is everything the server knows before the request body is read.
type Meta struct {
	ID     string
	Method string
	Path   string
	Model  string
	Tenant string
	Stream bool
}

// Trace is the snapshot handed to the dashboard. KeyRef is always masked.
type Trace struct {
	ID         string      `json:"id"`
	State      string      `json:"state"`
	StartedAt  int64       `json:"started_at"`
	EndedAt    int64       `json:"ended_at,omitempty"`
	Method     string      `json:"method,omitempty"`
	Path       string      `json:"path,omitempty"`
	Model      string      `json:"model,omitempty"`
	Tenant     string      `json:"tenant,omitempty"`
	Stream     bool        `json:"stream"`
	Upstream   string      `json:"upstream,omitempty"`
	Protocol   string      `json:"protocol,omitempty"`
	KeyRef     string      `json:"key_ref,omitempty"`
	Status     int         `json:"status"`
	TokensIn   int         `json:"tokens_in,omitempty"`
	TokensOut  int         `json:"tokens_out,omitempty"`
	Bytes      int         `json:"bytes"`
	Deltas     int         `json:"deltas"`
	DurationMs int64       `json:"duration_ms,omitempty"`
	TTFBMs     int64       `json:"ttfb_ms,omitempty"`
	Error      string      `json:"error,omitempty"`
	Stages     []Stage     `json:"stages"`
	Candidates []Candidate `json:"candidates,omitempty"`
	Events     []Event     `json:"events,omitempty"`
	Activity   Activity    `json:"activity"`
}

// Frame is what subscribers receive: a live snapshot ("trace") or the final
// record ("done").
type Frame struct {
	Type  string `json:"type"`
	Trace Trace  `json:"trace"`
}

// MaskRef redacts a credential reference, keeping just enough of it to tell two
// keys apart. Secrets must never reach the browser (AGENTS.md 1.6).
func MaskRef(ref string) string {
	if ref == "" {
		return ""
	}
	if len(ref) <= 8 {
		return ref[:2] + "***"
	}
	return ref[:4] + "***" + ref[len(ref)-4:]
}

// Classify maps one raw SSE line to an activity kind. ok is false for frames
// that carry no activity (the [DONE] sentinel, empty keepalives).
func Classify(raw []byte) (kind string, size int, ok bool) {
	payload := raw
	if i := bytes.Index(raw, []byte("data:")); i >= 0 {
		payload = bytes.TrimSpace(raw[i+5:])
	}
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return "", 0, false
	}
	if gjson.GetBytes(payload, "error").Exists() {
		return KindError, 0, true
	}
	if gjson.GetBytes(payload, "choices.0.delta.tool_calls").Exists() {
		return KindTool, 0, true
	}
	if v := gjson.GetBytes(payload, "choices.0.delta.reasoning_content"); v.Exists() {
		return KindReasoning, len(v.String()), true
	}
	if v := gjson.GetBytes(payload, "choices.0.delta.reasoning"); v.Exists() {
		return KindReasoning, len(v.String()), true
	}
	if v := gjson.GetBytes(payload, "choices.0.delta.content"); v.Exists() && v.String() != "" {
		return KindAnswer, len(v.String()), true
	}
	if gjson.GetBytes(payload, "usage").Exists() {
		return KindUsage, 0, true
	}
	return "", 0, false
}
