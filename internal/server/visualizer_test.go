package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/observability/trace"
)

const testAdminToken = "admin-token"

func newVisualizerDeps() RouterDeps {
	return RouterDeps{AdminToken: testAdminToken, Traces: trace.New(trace.Config{})}
}

func adminRequest(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	return req
}

func TestVisualizerEndpointsRejectAnonymousCallers(t *testing.T) {
	deps := newVisualizerDeps()
	cases := []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
		req     *http.Request
	}{
		{"traces", deps.handleVisualizerTraces, httptest.NewRequest(http.MethodGet, "/api/visualizer/traces", nil)},
		{"capture", deps.handleVisualizerCapture, httptest.NewRequest(http.MethodPost, "/api/visualizer/capture", strings.NewReader(`{"enabled":false}`))},
		{"clear", deps.handleVisualizerClear, httptest.NewRequest(http.MethodPost, "/api/visualizer/clear", nil)},
		{"events", deps.handleVisualizerEvents, httptest.NewRequest(http.MethodGet, "/api/visualizer/events", nil)},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		tc.handler(rec, tc.req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: expected 401, got %d", tc.name, rec.Code)
		}
	}
	if !deps.Traces.Enabled() {
		t.Fatal("an anonymous capture request must not disable capture")
	}
	if deps.Traces.Stats().Subscribers != 0 {
		t.Fatal("an anonymous events request must not attach a subscriber")
	}
}

func TestVisualizerTracesSnapshot(t *testing.T) {
	deps := newVisualizerDeps()
	_, unsub := deps.Traces.Subscribe(4)
	defer unsub()

	c := deps.Traces.Start(trace.Meta{ID: "req-1", Model: "gpt-4o-mini", Stream: true})
	if c == nil {
		t.Fatal("expected a live capture")
	}
	c.SetTarget("openai-main", "openai", "sk-live-abcd1234")
	c.Stage(trace.StageReceived, "10 bytes")
	c.Finish(200, nil)

	rec := httptest.NewRecorder()
	deps.handleVisualizerTraces(rec, adminRequest(http.MethodGet, "/api/visualizer/traces", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Traces []trace.Trace `json:"traces"`
		Stats  trace.Stats   `json:"stats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Traces) != 1 || payload.Traces[0].ID != "req-1" {
		t.Fatalf("unexpected traces: %+v", payload.Traces)
	}
	if strings.Contains(rec.Body.String(), "sk-live-abcd1234") {
		t.Fatal("raw credential must never reach the dashboard")
	}
	if payload.Traces[0].KeyRef != "sk-l***1234" {
		t.Fatalf("expected masked key ref, got %q", payload.Traces[0].KeyRef)
	}
	if !payload.Stats.Enabled || payload.Stats.Captured != 1 {
		t.Fatalf("unexpected stats: %+v", payload.Stats)
	}
}

func TestVisualizerCaptureToggleAndClear(t *testing.T) {
	deps := newVisualizerDeps()
	_, unsub := deps.Traces.Subscribe(4)
	defer unsub()

	rec := httptest.NewRecorder()
	deps.handleVisualizerCapture(rec, adminRequest(http.MethodPost, "/api/visualizer/capture", `{"enabled":false}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if deps.Traces.Enabled() {
		t.Fatal("capture must be off after the toggle request")
	}
	if c := deps.Traces.Start(trace.Meta{ID: "req-off"}); c != nil {
		t.Fatal("capture must not start while disabled")
	}

	deps.handleVisualizerCapture(httptest.NewRecorder(), adminRequest(http.MethodPost, "/api/visualizer/capture", `{"enabled":true}`))
	c := deps.Traces.Start(trace.Meta{ID: "req-on"})
	if c == nil {
		t.Fatal("capture must resume")
	}
	c.Finish(200, nil)
	if len(deps.Traces.Snapshot()) != 1 {
		t.Fatal("expected one stored trace")
	}

	deps.handleVisualizerClear(httptest.NewRecorder(), adminRequest(http.MethodPost, "/api/visualizer/clear", ""))
	if len(deps.Traces.Snapshot()) != 0 {
		t.Fatal("expected Clear to empty the ring")
	}
}

func TestVisualizerCaptureRejectsBadBody(t *testing.T) {
	deps := newVisualizerDeps()
	rec := httptest.NewRecorder()
	deps.handleVisualizerCapture(rec, adminRequest(http.MethodPost, "/api/visualizer/capture", `{"enabled":`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestVisualizerEventsStreamsLiveFrames(t *testing.T) {
	deps := newVisualizerDeps()
	srv := httptest.NewServer(http.HandlerFunc(deps.handleVisualizerEvents))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testAdminToken)

	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("unexpected content type %q", ct)
	}

	deadline := time.Now().Add(2 * time.Second)
	for deps.Traces.Stats().Subscribers == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	c := deps.Traces.Start(trace.Meta{ID: "req-sse", Model: "gpt-4o-mini", Stream: true})
	if c == nil {
		t.Fatal("expected capture while the SSE subscriber is attached")
	}
	c.Stage(trace.StageReceived, "1 bytes")

	buf := make([]byte, 512)
	var frame string
	deadline = time.Now().Add(2 * time.Second)
	for !strings.Contains(frame, "data: ") && time.Now().Before(deadline) {
		read, err := res.Body.Read(buf)
		if err != nil {
			t.Fatalf("read stream: %v", err)
		}
		frame += string(buf[:read])
	}
	if !strings.Contains(frame, "data: ") {
		t.Fatalf("expected an SSE frame, got %q", frame)
	}
	c.Finish(200, nil)

	cancel()
	time.Sleep(20 * time.Millisecond)
	if deps.Traces.Stats().Subscribers != 0 {
		t.Fatal("subscriber must be released when the client disconnects")
	}
}
