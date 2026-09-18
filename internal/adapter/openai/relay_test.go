package openai

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stallBody blocks on Read until Close is called, simulating an upstream that
// accepted the request but then stopped delivering bytes.
type stallBody struct {
	ch   chan struct{}
	once sync.Once
	read bool
}

func newStallBody() *stallBody { return &stallBody{ch: make(chan struct{})} }

func (b *stallBody) Read(p []byte) (int, error) {
	<-b.ch
	return 0, io.ErrClosedPipe
}
func (b *stallBody) Close() error {
	b.once.Do(func() { close(b.ch) })
	return nil
}

// recorderWithFlush is an httptest recorder that satisfies http.Flusher.
type recorderWithFlush struct{ *httptest.ResponseRecorder }

func (r recorderWithFlush) Flush() {}

// TestRelaySSEIdleTimeoutAbortsStalledStream is the direct regression test for
// the CRITICAL audit finding: a stalled upstream must NOT wedge the relay
// goroutine forever. Before the fix, RelaySSE read the body with no deadline, so
// a silent upstream pinned the goroutine, its connection, and the credential
// concurrency slot indefinitely. Now the idle watchdog must abort the stream and
// return an errStreamIdle error within a small multiple of the idle budget.
func TestRelaySSEIdleTimeoutAbortsStalledStream(t *testing.T) {
	t.Parallel()
	body := newStallBody()
	rec := recorderWithFlush{httptest.NewRecorder()}

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := RelaySSE(context.Background(), rec, body, 100*time.Millisecond)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, errStreamIdle) {
			t.Fatalf("want errStreamIdle, got %v", err)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("relay took too long to abort stalled stream: %s", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RelaySSE did not abort a stalled upstream (CRITICAL leak regression)")
	}
}

// TestRelaySSECtxCancelReturnsPromptly verifies the ctx-aware behavior: with no
// idle timeout, a client disconnect (ctx cancel) plus a closed body must still
// unblock the relay. Here we close the body from the canceller to emulate the
// transport tearing the connection down on disconnect.
func TestRelaySSECtxCancelReturnsPromptly(t *testing.T) {
	t.Parallel()
	body := newStallBody()
	rec := recorderWithFlush{httptest.NewRecorder()}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		// idleTimeout <= 0 disables the watchdog, isolating the ctx path.
		_, err := RelaySSE(ctx, rec, body, 0)
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()
	body.Close() // emulate transport closing the body on client disconnect

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RelaySSE did not return after ctx cancel + body close")
	}
}

// TestRelaySSECompletesAndDoesNotLeakWatchdog ensures a normally-terminating
// stream (with a watchdog armed) neither hangs nor leaves the watchdog goroutine
// behind: the stop() must disarm it before the ticker fires.
func TestRelaySSECompletesAndDoesNotLeakWatchdog(t *testing.T) {
	before := runtime.NumGoroutine()

	payload := "data: {\"a\":1}\n\ndata: [DONE]\n\n"
	rec := recorderWithFlush{httptest.NewRecorder()}

	// Use a real streaming server so the watchdog path runs end-to-end.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		for _, part := range strings.Split(payload, "\n\n") {
			if part == "" {
				continue
			}
			_, _ = io.WriteString(w, part+"\n\n")
			f.Flush()
		}
	}))
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	n, err := RelaySSE(context.Background(), rec, resp.Body, time.Second)
	if err != nil {
		t.Fatalf("RelaySSE: %v", err)
	}
	if n == 0 {
		t.Fatal("expected bytes relayed")
	}

	// Give any stray watchdog goroutine a chance to surface.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Logf("goroutines before=%d after=%d (informational)", before, runtime.NumGoroutine())
}


type trackCloseBody struct {
	ch         chan struct{}
	closed     atomic.Bool
	closedChan chan struct{}
	once       sync.Once
}

func newTrackCloseBody() *trackCloseBody {
	return &trackCloseBody{
		ch:         make(chan struct{}),
		closedChan: make(chan struct{}),
	}
}

func (b *trackCloseBody) Read(p []byte) (int, error) {
	<-b.ch
	return 0, io.ErrClosedPipe
}

func (b *trackCloseBody) Close() error {
	b.closed.Store(true)
	b.once.Do(func() {
		close(b.ch)
		close(b.closedChan)
	})
	return nil
}

// TestRelaySSEClientCancelClosesUpstreamBodyImmediately proves that when the client
// cancels context while a stream is blocked waiting for upstream data, the upstream
// body is closed immediately by the relay, unblocking the read and terminating the stream.
func TestRelaySSEClientCancelClosesUpstreamBodyImmediately(t *testing.T) {
	body := newTrackCloseBody()
	rec := recorderWithFlush{httptest.NewRecorder()}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		_, err := RelaySSE(ctx, rec, body, 30*time.Second)
		done <- err
	}()

	// Let the relay enter the read loop.
	time.Sleep(20 * time.Millisecond)
	if body.closed.Load() {
		t.Fatal("body should not be closed before cancel")
	}

	// Cancel client context.
	cancel()

	// Upstream body must be closed immediately without waiting for any idle timeout!
	select {
	case <-body.closedChan:
		if !body.closed.Load() {
			t.Fatal("expected body to be marked closed")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("upstream body was not closed immediately after client cancel")
	}

	// RelaySSE must return immediately.
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("RelaySSE did not return promptly after cancel")
	}
}

func BenchmarkRelaySSEMemory(b *testing.B) {
	payload := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"world\"}}]}\n\n" +
		"data: [DONE]\n\n")

	rec := recorderWithFlush{httptest.NewRecorder()}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		body := io.NopCloser(bytes.NewReader(payload))
		rec.ResponseRecorder.Body.Reset()
		_, err := RelaySSE(ctx, rec, body, 0)
		if err != nil {
			b.Fatalf("RelaySSE: %v", err)
		}
	}
}

