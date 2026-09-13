package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
)

func runtimeNumGoroutine() int { return runtime.NumGoroutine() }

// streamUpstream starts an httptest server running u and returns the adapter
// wired to it plus the upstream base URL.
func streamUpstream(t *testing.T, u *leakUpstream) (*Adapter, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(u.handler))
	t.Cleanup(srv.Close)
	a := NewAdapter(
		fixedPool{c: srv.Client()},
		&allowAllBreaker{},
		Config{SecretLookup: func(string) (string, bool) { return "sk-up", true }},
	)
	return a, srv.URL
}

// streamTarget builds a Target whose upstream uses a short idle timeout so the
// abort happens quickly in tests.
func streamTarget(baseURL string, idleMs int) *domain.Target {
	return &domain.Target{
		Upstream: &domain.Upstream{
			Name: "u", BaseURL: baseURL, CredentialRef: "UP_KEY",
			StreamIdleTimeoutMs: idleMs,
		},
		UpstreamModel: "m",
		CredentialRef: "UP_KEY",
	}
}

func streamReq() ports.ForwardRequest {
	return ports.ForwardRequest{
		Method: http.MethodPost, Path: "/chat/completions", Stream: true,
		BodyBytes: []byte(`{"model":"m","stream":true}`), Headers: http.Header{},
	}
}

// TestRelayStreamIdleAbortDoesNotLeak drives a real streaming request through
// the adapter against an upstream that goes silent mid-stream. The idle watchdog
// must abort the relay AND every goroutine must be gone after the request
// returns. goleak in TestMain is the hard gate; this test asserts the observable
// behavior (bounded latency, error surfaced) so a hang fails loudly instead of
// as a goleak timeout.
func TestRelayStreamIdleAbortDoesNotLeak(t *testing.T) {
	u := &leakUpstream{events: 2, stallAfter: true, stallRelease: make(chan struct{})}
	t.Cleanup(func() { close(u.stallRelease) }) // release our own fake's handler
	a, base := streamUpstream(t, u)

	rec := &flushRecorder{httptest.NewRecorder()}
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- a.Forward(context.Background(), streamTarget(base, 100), streamReq(), rec) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error when the upstream stalls mid-stream")
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("relay took too long to abort stalled stream: %s", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Forward wedged on a stalled upstream (goroutine leak regression)")
	}
}

// TestRelayStreamClientDisconnectDoesNotLeak exercises the client-abort path:
// the caller cancels the request context mid-stream. The relay must return
// promptly and leave no goroutines behind.
func TestRelayStreamClientDisconnectDoesNotLeak(t *testing.T) {
	u := &leakUpstream{events: 1_000_000} // effectively endless
	a, base := streamUpstream(t, u)

	ctx, cancel := context.WithCancel(context.Background())
	rec := &flushRecorder{httptest.NewRecorder()}
	done := make(chan error, 1)
	go func() { done <- a.Forward(ctx, streamTarget(base, 5_000), streamReq(), rec) }()

	time.Sleep(50 * time.Millisecond) // let a few events flow
	cancel()

	select {
	case err := <-done:
		// A cancellation error or a clean early return are both acceptable; the
		// invariant under test is that Forward RETURNS and leaks nothing.
		if err != nil && !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "canceled") {
			t.Logf("stream ended with %v (informational)", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Forward did not return after client disconnect")
	}
}

// TestRelayStreamBlockedFirstByteNoLeak covers an upstream that accepts the
// connection but sends nothing: the watchdog must still fire and nothing leaks.
func TestRelayStreamBlockedFirstByteNoLeak(t *testing.T) {
	u := &leakUpstream{blockFirst: make(chan struct{})}
	a, base := streamUpstream(t, u)

	rec := &flushRecorder{httptest.NewRecorder()}
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- a.Forward(context.Background(), streamTarget(base, 100), streamReq(), rec) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error when the upstream never sends a byte")
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("relay took too long to abort a zero-byte stream: %s", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Forward wedged on a zero-byte upstream (goroutine leak regression)")
	}
}

// TestRelayStreamManySequentialStreamsNoGrowth runs many complete streams back
// to back and asserts the goroutine count does not grow — the direct regression
// test for the "stop() disarms the ticker" contract.
func TestRelayStreamManySequentialStreamsNoGrowth(t *testing.T) {
	u := &leakUpstream{events: 3}
	a, base := streamUpstream(t, u)

	relay := func() {
		rec := &flushRecorder{httptest.NewRecorder()}
		_ = a.Forward(context.Background(), streamTarget(base, 0), streamReq(), rec)
	}
	for i := 0; i < 5; i++ { // warm up pools/buffers
		relay()
	}
	baseline := runtimeNumGoroutine()
	for i := 0; i < 50; i++ {
		relay()
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtimeNumGoroutine() <= baseline+2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("goroutines grew across sequential streams: baseline=%d now=%d",
		baseline, runtimeNumGoroutine())
}
