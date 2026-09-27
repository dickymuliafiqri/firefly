package trace

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func answerFrame(text string) []byte {
	return []byte(`data: {"choices":[{"delta":{"content":"` + text + `"}}]}`)
}

func startCaptured(t *testing.T, rec *Recorder, id string) *Capture {
	t.Helper()
	_, unsub := rec.Subscribe(8)
	t.Cleanup(unsub)
	c := rec.Start(Meta{ID: id, Method: "POST", Path: "/v1/chat/completions", Model: "gpt-4o-mini", Tenant: "team-a", Stream: true})
	if c == nil {
		t.Fatal("expected a live capture while a subscriber is attached")
	}
	return c
}

func TestRecorderCapturesRoutingAndStream(t *testing.T) {
	rec := New(Config{})
	c := startCaptured(t, rec, "req-1")

	c.Stage(StageReceived, "128 bytes")
	c.Candidate("openai-main", "sk-live-abcd1234", CandidateChosen, "primary")
	c.SetTarget("openai-main", "openai", "sk-live-abcd1234")
	c.Stage(StageKey, MaskRef("sk-live-abcd1234"))
	c.Stage(StageAttempt, "openai")
	c.Stage(StageTTFB, "")
	c.OnDelta(answerFrame("hello"))
	c.OnDelta(answerFrame(" world"))
	c.OnDelta([]byte("data: [DONE]"))
	c.SetUsage(12, 3)
	c.Finish(200, nil)

	traces := rec.Snapshot()
	if len(traces) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(traces))
	}
	got := traces[0]
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal trace: %v", err)
	}
	if strings.Contains(string(raw), "sk-live-abcd1234") {
		t.Fatalf("raw credential leaked into the trace snapshot: %s", raw)
	}
	if got.KeyRef != "sk-l***1234" {
		t.Fatalf("unexpected masked key ref %q", got.KeyRef)
	}
	if got.Deltas != 2 || got.Bytes != len("hello world") {
		t.Fatalf("unexpected stream accounting: deltas=%d bytes=%d", got.Deltas, got.Bytes)
	}
	if got.State != StateDone || got.Status != 200 {
		t.Fatalf("unexpected terminal state: state=%s status=%d", got.State, got.Status)
	}
	if got.TokensIn != 12 || got.TokensOut != 3 {
		t.Fatalf("unexpected token usage: in=%d out=%d", got.TokensIn, got.TokensOut)
	}
	var sawStream, sawDone bool
	for _, s := range got.Stages {
		sawStream = sawStream || s.Name == StageStream
		sawDone = sawDone || s.Name == StageDone
	}
	if !sawStream || !sawDone {
		t.Fatalf("expected stream and done stages, got %+v", got.Stages)
	}
	if len(got.Candidates) != 1 || got.Candidates[0].Key != "sk-l***1234" {
		t.Fatalf("expected one masked candidate, got %+v", got.Candidates)
	}
	stats := rec.Stats()
	if stats.Captured != 1 || stats.Inflight != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestRecorderSkipsCaptureWithoutSubscriber(t *testing.T) {
	rec := New(Config{})
	if c := rec.Start(Meta{ID: "req-x"}); c != nil {
		t.Fatal("capture must not run while nobody is watching")
	}
	_, unsub := rec.Subscribe(4)
	if c := rec.Start(Meta{ID: "req-y"}); c == nil {
		t.Fatal("expected capture once a subscriber attached")
	}
	unsub()
	if c := rec.Start(Meta{ID: "req-z"}); c != nil {
		t.Fatal("capture must stop when the last subscriber leaves")
	}
}

func TestRecorderCaptureToggle(t *testing.T) {
	rec := New(Config{})
	_, unsub := rec.Subscribe(4)
	defer unsub()

	rec.SetEnabled(false)
	if c := rec.Start(Meta{ID: "req-1"}); c != nil {
		t.Fatal("capture must be off after SetEnabled(false)")
	}
	rec.SetEnabled(true)
	if !rec.Stats().Live {
		t.Fatal("expected stats to report a live capture session")
	}
	if c := rec.Start(Meta{ID: "req-2"}); c == nil {
		t.Fatal("capture must resume after SetEnabled(true)")
	}
}

func TestRecorderRetentionIsBoundedNewestFirst(t *testing.T) {
	rec := New(Config{Retention: 2})
	for _, id := range []string{"one", "two", "three"} {
		c := startCaptured(t, rec, id)
		c.SetTarget("up-"+id, "openai", "sk-key-"+id)
		c.Finish(200, nil)
	}
	traces := rec.Snapshot()
	if len(traces) != 2 {
		t.Fatalf("expected the ring to keep 2 traces, got %d", len(traces))
	}
	if traces[0].ID != "three" || traces[1].ID != "two" {
		t.Fatalf("expected newest-first ordering, got %s, %s", traces[0].ID, traces[1].ID)
	}
	rec.Clear()
	if len(rec.Snapshot()) != 0 {
		t.Fatal("expected Clear to empty the ring")
	}
}



func TestRecorderSlowSubscriberNeverBlocks(t *testing.T) {
	rec := New(Config{})
	// Deliberately never read from this channel.
	_, unsub := rec.Subscribe(1)
	defer unsub()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			rec.broadcast(Frame{Type: "trace", Trace: Trace{ID: "req"}})
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("broadcast blocked on a slow subscriber")
	}
	if rec.Stats().Dropped == 0 {
		t.Fatal("expected dropped frames to be counted")
	}
}

func TestRecorderStreamsLiveAndTerminalFrames(t *testing.T) {
	rec := New(Config{})
	ch, unsub := rec.Subscribe(16)
	defer unsub()

	c := rec.Start(Meta{ID: "req-live", Model: "claude-sonnet-4", Stream: true})
	if c == nil {
		t.Fatal("expected a live capture")
	}
	c.Stage(StageReceived, "1 bytes")
	c.OnDelta(answerFrame("hi"))
	c.Finish(200, nil)

	var live, terminal bool
	deadline := time.After(2 * time.Second)
	for !(live && terminal) {
		select {
		case data := <-ch:
			text := string(data)
			live = live || strings.Contains(text, `"type":"trace"`)
			terminal = terminal || strings.Contains(text, `"type":"done"`)
		case <-deadline:
			t.Fatalf("missing frames: live=%v terminal=%v", live, terminal)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		kind string
		ok   bool
	}{
		{"done sentinel", "data: [DONE]", "", false},
		{"keepalive", ": ping", "", false},
		{"answer", `data: {"choices":[{"delta":{"content":"abc"}}]}`, KindAnswer, true},
		{"reasoning", `data: {"choices":[{"delta":{"reasoning_content":"hmm"}}]}`, KindReasoning, true},
		{"tool", `data: {"choices":[{"delta":{"tool_calls":[{"index":0}]}}]}`, KindTool, true},
		{"usage", `data: {"choices":[{"delta":{}}],"usage":{"total_tokens":9}}`, KindUsage, true},
		{"error", `data: {"error":{"message":"boom"}}`, KindError, true},
	}
	for _, tc := range cases {
		kind, size, ok := Classify([]byte(tc.raw))
		if ok != tc.ok || kind != tc.kind {
			t.Fatalf("%s: got (%q,%v) want (%q,%v)", tc.name, kind, ok, tc.kind, tc.ok)
		}
		if tc.kind == KindAnswer && size != 3 {
			t.Fatalf("%s: expected content size 3, got %d", tc.name, size)
		}
	}
}

func TestNilCaptureIsInert(t *testing.T) {
	var c *Capture
	c.Stage(StageReceived, "")
	c.Candidate("up", "sk-secret", CandidateChosen, "")
	c.SetTarget("up", "openai", "sk-secret")
	c.SetUsage(1, 1)
	c.OnDelta(answerFrame("x"))
	c.Fail(500, context.Canceled)
	c.Finish(200, nil)
}

func TestWithCaptureRoundTrip(t *testing.T) {
	rec := New(Config{})
	c := startCaptured(t, rec, "req-ctx")
	ctx := WithCapture(context.Background(), c)
	if FromContext(ctx) != c {
		t.Fatal("expected the capture to round-trip through the context")
	}
	if FromContext(context.Background()) != nil {
		t.Fatal("expected nil when no capture is attached")
	}
	if WithCapture(ctx, nil) != ctx {
		t.Fatal("WithCapture(nil) must return the context untouched")
	}
}

func TestMaskRefNeverLeaksSecret(t *testing.T) {
	secret := "sk-live-abcdefghijklmnop"
	masked := MaskRef(secret)
	if strings.Contains(masked, secret) || masked == secret {
		t.Fatalf("masked ref %q still contains the secret", masked)
	}
	if MaskRef("") != "" {
		t.Fatal("empty ref must stay empty")
	}
	if strings.Contains(MaskRef("short"), "short") {
		t.Fatalf("short refs must be masked too, got %q", MaskRef("short"))
	}
}

func BenchmarkOnDelta(b *testing.B) {
	rec := New(Config{})
	ch, unsub := rec.Subscribe(1)
	defer unsub()
	c := rec.Start(Meta{ID: "bench", Stream: true})
	frame := answerFrame("token")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.OnDelta(frame)
	}
	select {
	case <-ch:
	default:
	}
}

func BenchmarkOnDeltaUnwatched(b *testing.B) {
	var c *Capture
	frame := answerFrame("token")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.OnDelta(frame)
	}
}
