package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ioCopyDiscard drains a request body without importing io at every call site.
func ioCopyDiscard(r *http.Request) (int64, error) { return io.Copy(io.Discard, r.Body) }

func okChannel() Channel {
	return Channel{URL: "https://example.invalid/hook", Format: "generic", Enabled: true}
}

func TestPost_Success(t *testing.T) {
	var mu sync.Mutex
	var hdr http.Header
	var body []byte
	p := &Poster{Client: headerCaptureClient(t, &mu, &hdr, &body), Now: time.Now}

	if err := p.Post(context.Background(), okChannel(), sampleEvent()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if ct := hdr.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
	if !strings.Contains(string(body), "key.cooldown") {
		t.Fatalf("body missing event type: %s", body)
	}
}

func TestPost_HMACSignatureVerifies(t *testing.T) {
	var mu sync.Mutex
	var hdr http.Header
	var body []byte
	p := &Poster{Client: headerCaptureClient(t, &mu, &hdr, &body), Now: func() time.Time {
		return time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	}}

	ch := okChannel()
	ch.Secret = "top-secret"
	if err := p.Post(context.Background(), ch, sampleEvent()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	sig := hdr.Get("X-Firefly-Signature")
	if sig == "" {
		t.Fatal("signature header missing")
	}

	// Recompute independently, the way a consumer would.
	fixed := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	mac := hmac.New(sha256.New, []byte("top-secret"))
	fmt.Fprintf(mac, "%d.", fixed.Unix())
	mac.Write(body)
	want := fmt.Sprintf("t=%d,v1=%s", fixed.Unix(), hex.EncodeToString(mac.Sum(nil)))
	if sig != want {
		t.Fatalf("signature mismatch\ngot:  %s\nwant: %s", sig, want)
	}
	if !strings.HasPrefix(sig, "t="+strconv.FormatInt(fixed.Unix(), 10)+",v1=") {
		t.Fatalf("unexpected signature shape: %q", sig)
	}
}

func TestPost_BearerHeader(t *testing.T) {
	var mu sync.Mutex
	var hdr http.Header
	var body []byte
	p := &Poster{Client: headerCaptureClient(t, &mu, &hdr, &body), Now: time.Now}

	ch := okChannel()
	ch.Bearer = "abc123"
	if err := p.Post(context.Background(), ch, sampleEvent()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := hdr.Get("Authorization"); got != "Bearer abc123" {
		t.Fatalf("authorization = %q", got)
	}
}

func TestPost_RetriesTransientThenSucceeds(t *testing.T) {
	var attempts atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		_, _ = ioCopyDiscard(r)
		n := attempts.Add(1)
		if n < 3 {
			return nil, errTransport
		}
		return newResponse(200, `{"ok":true}`), nil
	})}
	p := &Poster{Client: client, Now: time.Now}

	if err := p.Post(context.Background(), okChannel(), sampleEvent()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
}

func TestPost_GivesUpAfterMaxAttempts(t *testing.T) {
	var attempts atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		_, _ = ioCopyDiscard(r)
		attempts.Add(1)
		return nil, errTransport
	})}
	p := &Poster{Client: client, Now: time.Now}

	err := p.Post(context.Background(), okChannel(), sampleEvent())
	if err == nil {
		t.Fatal("expected an error after exhausting retries")
	}
	if got := attempts.Load(); got != posterAttempts {
		t.Fatalf("attempts = %d, want %d", got, posterAttempts)
	}
}

func TestPost_DoesNotRetryClientError(t *testing.T) {
	var attempts atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		_, _ = ioCopyDiscard(r)
		attempts.Add(1)
		return newResponse(400, `{"detail":"bad payload"}`), nil
	})}
	p := &Poster{Client: client, Now: time.Now}

	err := p.Post(context.Background(), okChannel(), sampleEvent())
	if err == nil {
		t.Fatal("expected an error for a 400")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("error should name the status: %v", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1 (4xx must not retry)", got)
	}
}

func TestPost_RetriesRateLimit(t *testing.T) {
	var attempts atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		_, _ = ioCopyDiscard(r)
		attempts.Add(1)
		return newResponse(429, `{"detail":"slow down"}`), nil
	})}
	p := &Poster{Client: client, Now: time.Now}

	_ = p.Post(context.Background(), okChannel(), sampleEvent())
	if got := attempts.Load(); got != posterAttempts {
		t.Fatalf("attempts = %d, want %d (429 is transient)", got, posterAttempts)
	}
}

func TestPost_RejectsBadChannel(t *testing.T) {
	p := &Poster{Client: statusClient(200), Now: time.Now}

	if err := p.Post(context.Background(), Channel{Format: "generic"}, sampleEvent()); err == nil {
		t.Fatal("empty url must error")
	}
	bad := okChannel()
	bad.Format = "carrier-pigeon"
	if err := p.Post(context.Background(), bad, sampleEvent()); err == nil {
		t.Fatal("unknown format must error")
	}
}

func TestPost_RespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &Poster{Client: failingClient(), Now: time.Now}
	if err := p.Post(ctx, okChannel(), sampleEvent()); err == nil {
		t.Fatal("cancelled context must error")
	}
}
