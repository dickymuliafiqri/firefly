package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLiveLogHub_PublishAndSnapshot(t *testing.T) {
	hub := NewLiveLogHub()
	if hub == nil {
		t.Fatal("expected non-nil hub")
	}

	snap := hub.Snapshot()
	if len(snap) != 0 {
		t.Fatalf("expected empty snapshot, got %d", len(snap))
	}

	// Publish 3 logs
	hub.Publish(LiveLog{ID: "1", Upstream: "openai", Status: 200})
	hub.Publish(LiveLog{ID: "2", Upstream: "anthropic", Status: 200})
	hub.Publish(LiveLog{ID: "3", Upstream: "groq", Status: 429})

	snap = hub.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("expected 3 logs, got %d", len(snap))
	}

	// Newest should be first
	if snap[0].ID != "3" || snap[0].Upstream != "groq" {
		t.Fatalf("expected newest log first, got %+v", snap[0])
	}
	if snap[2].ID != "1" || snap[2].Upstream != "openai" {
		t.Fatalf("expected oldest log last, got %+v", snap[2])
	}
}

func TestLiveLogHub_CappedHistory(t *testing.T) {
	hub := NewLiveLogHub()
	for i := 0; i < maxLiveLogHistory+25; i++ {
		hub.Publish(LiveLog{ID: "req-" + strconv.Itoa(i)})
	}

	snap := hub.Snapshot()
	if len(snap) != maxLiveLogHistory {
		t.Fatalf("expected capped history of %d, got %d", maxLiveLogHistory, len(snap))
	}
}

func TestLiveLogHub_UpdateInPlace(t *testing.T) {
	hub := NewLiveLogHub()
	hub.Publish(LiveLog{ID: "req-1", Status: 0, DurationMs: 0})
	hub.Publish(LiveLog{ID: "req-2", Status: 0, DurationMs: 0})

	// Complete req-1
	hub.Publish(LiveLog{ID: "req-1", Status: 200, DurationMs: 150})

	snap := hub.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 items, got %d", len(snap))
	}
	// req-1 should have been updated in place with status 200 and 150ms
	var req1 *LiveLog
	for i := range snap {
		if snap[i].ID == "req-1" {
			req1 = &snap[i]
			break
		}
	}
	if req1 == nil || req1.Status != 200 || req1.DurationMs != 150 {
		t.Fatalf("expected req-1 updated in place: %+v", req1)
	}
}

func TestLiveLogHub_SubscribeAndBroadcast(t *testing.T) {
	hub := NewLiveLogHub()
	ch, unsub := hub.Subscribe()
	defer unsub()

	expected := LiveLog{
		ID:       "req-123",
		Upstream: "test-upstream",
		Model:    "gpt-4o",
		Status:   200,
	}

	hub.Publish(expected)

	select {
	case received := <-ch:
		if received.ID != expected.ID || received.Upstream != expected.Upstream {
			t.Fatalf("received unexpected log: %+v", received)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for broadcast event")
	}

	// Unsubscribe and verify no more events or deadlocks
	unsub()
	hub.Publish(LiveLog{ID: "req-456"})
}

func TestHandleTelemetryEvents(t *testing.T) {
	hub := NewLiveLogHub()
	deps := RouterDeps{
		LiveLogs: hub,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/telemetry/events", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		deps.handleTelemetryEvents(w, req)
	}()

	// Publish a log after a brief pause
	time.Sleep(50 * time.Millisecond)
	hub.Publish(LiveLog{
		ID:       "evt-999",
		Upstream: "openai-main",
		Model:    "gpt-4o-mini",
		Status:   200,
	})

	time.Sleep(50 * time.Millisecond)
	cancel() // close client context
	<-done

	body := w.Body.String()
	if !strings.Contains(body, ": connected") {
		t.Fatalf("expected initial connected greeting in SSE, got: %s", body)
	}
	if !strings.Contains(body, "evt-999") || !strings.Contains(body, "openai-main") {
		t.Fatalf("expected published log event in SSE body, got: %s", body)
	}
}
