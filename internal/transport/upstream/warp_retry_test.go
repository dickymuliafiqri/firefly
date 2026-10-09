package upstream

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/transport/warp"
)

// rtFunc adapts a closure to http.RoundTripper so a test can script the exact
// sequence of upstream responses without a live server.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// stubRotator stands in for the WARP manager: it counts rotation requests and
// decides whether a healthy alternate egress exists.
type stubRotator struct {
	rotations atomic.Int64
	alternate bool
}

func (s *stubRotator) RotateEgress() bool {
	s.rotations.Add(1)
	return s.alternate
}

func textResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// A 429 on a WARP egress is IP-bound, so the transport must rotate and replay
// the request rather than surfacing the rate limit to the adapter's key logic.
func TestWarpRetryTransport_429RetriesThroughNewEgress(t *testing.T) {
	var baseCalls atomic.Int64
	rot := &stubRotator{alternate: true}

	base := rtFunc(func(*http.Request) (*http.Response, error) {
		if baseCalls.Add(1) == 1 {
			return textResponse(http.StatusTooManyRequests, `{"error":"rate limited"}`), nil
		}
		return textResponse(http.StatusOK, `{"ok":true}`), nil
	})

	client := &http.Client{Transport: &warpRetryTransport{base: base, warp: rot}}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"https://upstream.example/v1/chat", strings.NewReader(`{"model":"m"}`))

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 after rotating egress", resp.StatusCode)
	}
	if got := baseCalls.Load(); got != 2 {
		t.Fatalf("base transport called %d times, want 2 (original + retry)", got)
	}
	if got := rot.rotations.Load(); got != 1 {
		t.Fatalf("RotateEgress called %d times, want 1", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"ok":true}` {
		t.Fatalf("retry body = %q, want the successful response", body)
	}
}

// The retried request must carry the same body as the original: http.Transport
// consumes and closes the body it is handed, so a naive clone would replay an
// empty payload.
func TestWarpRetryTransport_RetryReplaysBodyIntact(t *testing.T) {
	payload := `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`
	var bodies []string
	var baseCalls atomic.Int64

	base := rtFunc(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if baseCalls.Add(1) == 1 {
			return textResponse(http.StatusTooManyRequests, `{"error":"slow down"}`), nil
		}
		return textResponse(http.StatusOK, `{}`), nil
	})

	client := &http.Client{Transport: &warpRetryTransport{base: base, warp: &stubRotator{alternate: true}}}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"https://upstream.example/v1/chat", strings.NewReader(payload))

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if len(bodies) != 2 {
		t.Fatalf("captured %d request bodies, want 2", len(bodies))
	}
	for i, b := range bodies {
		if b != payload {
			t.Fatalf("attempt %d body = %q, want %q", i+1, b, payload)
		}
	}
}

// With no healthy alternate the 429 must come back byte-for-byte, so the
// adapter's existing key cooldown/failover path behaves exactly as before.
func TestWarpRetryTransport_NoAlternatePasses429Through(t *testing.T) {
	var baseCalls atomic.Int64
	rot := &stubRotator{alternate: false}

	base := rtFunc(func(*http.Request) (*http.Response, error) {
		baseCalls.Add(1)
		return textResponse(http.StatusTooManyRequests, `{"error":"quota exhausted"}`), nil
	})

	client := &http.Client{Transport: &warpRetryTransport{base: base, warp: rot}}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"https://upstream.example/v1/chat", strings.NewReader(`{"model":"m"}`))

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want the original 429", resp.StatusCode)
	}
	if got := baseCalls.Load(); got != 1 {
		t.Fatalf("base transport called %d times, want 1 (no retry without an alternate)", got)
	}
	if got := rot.rotations.Load(); got != 1 {
		t.Fatalf("RotateEgress called %d times, want 1 (the probe still happens)", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"error":"quota exhausted"}` {
		t.Fatalf("relayed body = %q, want the upstream 429 body verbatim", body)
	}
}

// Only a 429 is an egress-IP signal. A 5xx belongs to the breaker and a 4xx to
// the key policy; neither must trigger a WARP rotation.
func TestWarpRetryTransport_Non429IsUntouched(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusUnauthorized, http.StatusForbidden, http.StatusOK} {
		var baseCalls atomic.Int64
		rot := &stubRotator{alternate: true}
		base := rtFunc(func(*http.Request) (*http.Response, error) {
			baseCalls.Add(1)
			return textResponse(status, `{"error":"x"}`), nil
		})

		client := &http.Client{Transport: &warpRetryTransport{base: base, warp: rot}}
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
			"https://upstream.example/v1/chat", strings.NewReader(`{"model":"m"}`))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("status %d: Do: %v", status, err)
		}
		resp.Body.Close()

		if resp.StatusCode != status {
			t.Fatalf("status %d became %d", status, resp.StatusCode)
		}
		if got := baseCalls.Load(); got != 1 {
			t.Fatalf("status %d triggered %d base calls, want 1", status, got)
		}
		if got := rot.rotations.Load(); got != 0 {
			t.Fatalf("status %d triggered %d rotations, want 0", status, got)
		}
	}
}

// A body that cannot be rewound cannot be retried; the 429 goes straight to the
// key-failover path instead of being silently dropped.
func TestWarpRetryTransport_NonReplayableBodyPassesThrough(t *testing.T) {
	var baseCalls atomic.Int64
	rot := &stubRotator{alternate: true}
	base := rtFunc(func(*http.Request) (*http.Response, error) {
		baseCalls.Add(1)
		return textResponse(http.StatusTooManyRequests, `{"error":"rate limited"}`), nil
	})

	client := &http.Client{Transport: &warpRetryTransport{base: base, warp: rot}}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"https://upstream.example/v1/chat", nil)
	req.Body = io.NopCloser(strings.NewReader(`{"model":"m"}`)) // no GetBody
	req.GetBody = nil

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want the original 429", resp.StatusCode)
	}
	if got := baseCalls.Load(); got != 1 {
		t.Fatalf("base transport called %d times, want 1", got)
	}
	if got := rot.rotations.Load(); got != 0 {
		t.Fatalf("RotateEgress called %d times, want 0 for a non-replayable body", got)
	}
}

// A client that disconnects mid-flight must not have its request replayed:
// that would spend upstream quota on a response nobody is waiting for.
func TestWarpRetryTransport_RespectsClientCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var baseCalls atomic.Int64
	rot := &stubRotator{alternate: true}
	base := rtFunc(func(*http.Request) (*http.Response, error) {
		baseCalls.Add(1)
		return textResponse(http.StatusTooManyRequests, `{"error":"rate limited"}`), nil
	})

	client := &http.Client{Transport: &warpRetryTransport{base: base, warp: rot}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://upstream.example/v1/chat", strings.NewReader(`{"model":"m"}`))

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want the original 429", resp.StatusCode)
	}
	if got := rot.rotations.Load(); got != 0 {
		t.Fatalf("RotateEgress called %d times, want 0 on a cancelled client", got)
	}
}

// The retry budget is a backstop: once exhausted the last 429 is returned so
// the adapter's key failover can take over.
func TestWarpRetryTransport_BudgetExhaustedReturnsLast429(t *testing.T) {
	var baseCalls atomic.Int64
	rot := &stubRotator{alternate: true}
	base := rtFunc(func(*http.Request) (*http.Response, error) {
		baseCalls.Add(1)
		return textResponse(http.StatusTooManyRequests, `{"error":"still limited"}`), nil
	})

	client := &http.Client{Transport: &warpRetryTransport{base: base, warp: rot}}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"https://upstream.example/v1/chat", strings.NewReader(`{"model":"m"}`))

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	want := int64(1 + warpEgressRetryBudget)
	if got := baseCalls.Load(); got != want {
		t.Fatalf("base transport called %d times, want %d", got, want)
	}
	if got := rot.rotations.Load(); got != int64(warpEgressRetryBudget) {
		t.Fatalf("RotateEgress called %d times, want %d", got, warpEgressRetryBudget)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want the last 429", resp.StatusCode)
	}
}

// A transport failure on the retry must surface as an error, not as a nil
// response the adapter would dereference.
func TestWarpRetryTransport_RetryTransportErrorPropagates(t *testing.T) {
	wantErr := errors.New("dial reset")
	var baseCalls atomic.Int64
	base := rtFunc(func(*http.Request) (*http.Response, error) {
		if baseCalls.Add(1) == 1 {
			return textResponse(http.StatusTooManyRequests, `{"error":"rate limited"}`), nil
		}
		return nil, wantErr
	})

	client := &http.Client{Transport: &warpRetryTransport{base: base, warp: &stubRotator{alternate: true}}}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"https://upstream.example/v1/chat", strings.NewReader(`{"model":"m"}`))

	if _, err := client.Do(req); !errors.Is(err, wantErr) {
		t.Fatalf("Do error = %v, want %v", err, wantErr)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// The wrapper must only be installed where it can actually help: a WARP-egress
// upstream backed by a live manager. Everything else keeps the plain transport.
func TestPool_WarpRetryTransportWiring(t *testing.T) {
	mgr := warp.NewManager(discardLogger(), "")
	t.Cleanup(mgr.Close)

	cases := []struct {
		name     string
		pool     *Pool
		upstream *domain.Upstream
		wrapped  bool
	}{
		{
			name:     "warp egress with manager",
			pool:     NewPool(mgr),
			upstream: &domain.Upstream{Name: "w", EgressMode: "warp"},
			wrapped:  true,
		},
		{
			name:     "warp egress without manager",
			pool:     NewPool(),
			upstream: &domain.Upstream{Name: "w", EgressMode: "warp"},
			wrapped:  false,
		},
		{
			name:     "direct egress",
			pool:     NewPool(mgr),
			upstream: &domain.Upstream{Name: "d", EgressMode: "direct"},
			wrapped:  false,
		},
		{
			name:     "socks5 proxy egress",
			pool:     NewPool(mgr),
			upstream: &domain.Upstream{Name: "p", EgressMode: "proxy", ProxyURL: "socks5://127.0.0.1:1080"},
			wrapped:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, isWrapped := tc.pool.Client(tc.upstream).Transport.(*warpRetryTransport)
			if isWrapped != tc.wrapped {
				t.Fatalf("transport wrapped = %v, want %v", isWrapped, tc.wrapped)
			}
		})
	}
}

// CloseIdleWarpConnections must reach the wrapped transport, otherwise a
// rotation would leave sockets pinned to the retired egress IP and the retry
// would dial the same address again.
func TestPool_CloseIdleWarpConnectionsReachesWrappedTransport(t *testing.T) {
	mgr := warp.NewManager(discardLogger(), "")
	t.Cleanup(mgr.Close)

	pool := NewPool(mgr)
	u := &domain.Upstream{Name: "w", EgressMode: "warp"}
	client := pool.Client(u)

	wrapped, ok := client.Transport.(*warpRetryTransport)
	if !ok {
		t.Fatalf("transport is %T, want *warpRetryTransport", client.Transport)
	}
	if _, ok := wrapped.base.(idleConnCloser); !ok {
		t.Fatalf("wrapped base %T does not expose CloseIdleConnections", wrapped.base)
	}
	// Must not panic and must not be a no-op-by-type-assertion-failure.
	pool.CloseIdleWarpConnections()
}

// End-to-end through a real http.Client and a real server: a 429 followed by a
// success must be invisible to the caller, and the request body must survive
// the replay.
func TestWarpRetryTransport_EndToEndAgainstLiveServer(t *testing.T) {
	var attempts atomic.Int64
	var lastBody atomic.Value

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		lastBody.Store(string(b))
		if attempts.Add(1) == 1 {
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"rate limit exceeded"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"hi"}}]}`)
	}))
	defer srv.Close()

	rot := &stubRotator{alternate: true}
	client := &http.Client{Transport: &warpRetryTransport{base: http.DefaultTransport, warp: rot}}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL,
		strings.NewReader(`{"model":"m","messages":[]}`))

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 after the egress retry", resp.StatusCode)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("server saw %d requests, want 2", got)
	}
	if got := rot.rotations.Load(); got != 1 {
		t.Fatalf("RotateEgress called %d times, want 1", got)
	}
	if body, _ := lastBody.Load().(string); body != `{"model":"m","messages":[]}` {
		t.Fatalf("replayed body = %q, want the original payload", body)
	}
}
