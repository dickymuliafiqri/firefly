package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/metrics"
)

// TestAdminServesMetrics verifies /metrics is reachable without a token.
func TestAdminServesMetrics(t *testing.T) {
	mx := metrics.New()
	mx.ObserveHTTP("GET", "/x", 200, time.Millisecond)
	a := NewAdmin("127.0.0.1:0", "", mx.Handler(), discardLogger())

	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "firefly_http_requests_total") {
		t.Fatalf("metrics body missing counter: %s", rec.Body.String())
	}
}

// TestAdminDebugRequiresToken verifies /debug/* is guarded and fail-closed.
func TestAdminDebugRequiresToken(t *testing.T) {
	a := NewAdmin("127.0.0.1:0", "s3cret", nil, discardLogger())

	// No token -> 401.
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/goroutines", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-token status = %d, want 401", rec.Code)
	}

	// Wrong token -> 401.
	req := httptest.NewRequest(http.MethodGet, "/debug/goroutines", nil)
	req.Header.Set("X-Admin-Token", "nope")
	rec = httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-token status = %d, want 401", rec.Code)
	}

	// Correct token via X-Admin-Token -> 200 + goroutine dump.
	req = httptest.NewRequest(http.MethodGet, "/debug/goroutines", nil)
	req.Header.Set("X-Admin-Token", "s3cret")
	rec = httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("good-token status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "goroutine") {
		t.Fatalf("goroutine dump missing: %s", rec.Body.String())
	}

	// Correct token via Authorization: Bearer -> 200.
	req = httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	rec = httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bearer status = %d, want 200", rec.Code)
	}
}

// TestAdminDebugDisabledWithoutToken verifies sensitive routes are absent when
// no token is configured (fail-closed): requests get 404, not a leak.
func TestAdminDebugDisabledWithoutToken(t *testing.T) {
	a := NewAdmin("127.0.0.1:0", "", nil, discardLogger())
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/goroutines", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unconfigured debug route status = %d, want 404", rec.Code)
	}
}

// TestAdminServeShutsDownOnCtxCancel verifies the admin server drains cleanly
// and returns (no goroutine leak) when the context is cancelled.
func TestAdminServeShutsDownOnCtxCancel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	a := NewAdmin(ln.Addr().String(), "", nil, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.ServeOnListener(ctx, ln) }()

	// Give it a moment to start serving, then hit it.
	waitServing(t, "http://"+ln.Addr().String()+"/healthz")
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("admin serve returned %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("admin server did not shut down within 3s")
	}
}

// waitServing polls url until it returns 200 or the deadline passes.
func waitServing(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server at %s never became ready", url)
}
