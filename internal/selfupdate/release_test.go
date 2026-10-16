package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// releaseServer serves a GitHub-shaped releases/latest payload and counts hits.
type releaseServer struct {
	*httptest.Server
	hits    atomic.Int64
	release Release
	// notModified makes the server answer conditional requests with 304.
	notModified atomic.Bool
	// status, when non-zero, makes the server fail with that status.
	status atomic.Int64
}

func newReleaseServer(t *testing.T, rel Release) *releaseServer {
	t.Helper()
	rs := &releaseServer{release: rel}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.hits.Add(1)
		if code := rs.status.Load(); code != 0 {
			w.WriteHeader(int(code))
			return
		}
		if rs.notModified.Load() && r.Header.Get("If-None-Match") != "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"abc123"`)
		_ = json.NewEncoder(w).Encode(rs.release)
	}))
	t.Cleanup(rs.Close)
	return rs
}

func (rs *releaseServer) client(t *testing.T, opts ...Option) *Client {
	t.Helper()
	base := []Option{
		WithAPIBase(rs.URL),
		WithHTTPClient(&http.Client{Timeout: 5 * time.Second}),
	}
	return NewClient(append(base, opts...)...)
}

func sampleRelease() Release {
	return Release{
		Tag: "v1.44.13",
		Assets: []Asset{
			{Name: "firefly_v1.44.13_linux_amd64.tar.gz", URL: "https://github.com/o/r/releases/download/v1.44.13/a.tgz"},
			{Name: ChecksumAsset, URL: "https://github.com/o/r/releases/download/v1.44.13/checksums.txt"},
		},
	}
}

func TestClientLatest(t *testing.T) {
	rs := newReleaseServer(t, sampleRelease())
	client := rs.client(t)

	rel, err := client.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest failed: %v", err)
	}
	if rel.Tag != "v1.44.13" {
		t.Fatalf("tag = %q, want %q", rel.Tag, "v1.44.13")
	}
	if _, ok := rel.Asset(ChecksumAsset); !ok {
		t.Fatal("checksums.txt asset missing from the decoded release")
	}
}

func TestClientLatestCachesWithinTTL(t *testing.T) {
	rs := newReleaseServer(t, sampleRelease())
	client := rs.client(t, WithTTL(time.Hour))

	for i := 0; i < 5; i++ {
		if _, err := client.Latest(context.Background()); err != nil {
			t.Fatalf("Latest #%d failed: %v", i, err)
		}
	}
	if got := rs.hits.Load(); got != 1 {
		t.Fatalf("server hits = %d, want 1 (the TTL must collapse repeated checks)", got)
	}
}

func TestClientLatestRevalidatesWithETag(t *testing.T) {
	rs := newReleaseServer(t, sampleRelease())
	rs.notModified.Store(true)

	// A clock the test drives, so expiry does not need a real sleep.
	now := time.Now()
	client := rs.client(t, WithTTL(time.Minute), WithClock(func() time.Time { return now }))

	first, err := client.Latest(context.Background())
	if err != nil {
		t.Fatalf("first Latest failed: %v", err)
	}

	now = now.Add(2 * time.Minute) // past the TTL
	second, err := client.Latest(context.Background())
	if err != nil {
		t.Fatalf("second Latest failed: %v", err)
	}
	if second.Tag != first.Tag {
		t.Fatalf("revalidated release tag = %q, want %q", second.Tag, first.Tag)
	}
	if got := rs.hits.Load(); got != 2 {
		t.Fatalf("server hits = %d, want 2", got)
	}
}

func TestClientLatestFallsBackToCacheOnFailure(t *testing.T) {
	rs := newReleaseServer(t, sampleRelease())
	now := time.Now()
	client := rs.client(t, WithTTL(time.Minute), WithClock(func() time.Time { return now }))

	if _, err := client.Latest(context.Background()); err != nil {
		t.Fatalf("first Latest failed: %v", err)
	}

	// The API starts failing and the cache has expired.
	rs.status.Store(http.StatusInternalServerError)
	now = now.Add(2 * time.Minute)

	rel, err := client.Latest(context.Background())
	if !errors.Is(err, ErrStaleRelease) {
		t.Fatalf("Latest should report a stale fallback, got error: %v", err)
	}
	if rel == nil || rel.Tag != "v1.44.13" {
		t.Fatalf("stale release = %+v, want the cached %q", rel, "v1.44.13")
	}
}

func TestClientLatestReportsFailureWithoutCache(t *testing.T) {
	rs := newReleaseServer(t, sampleRelease())
	rs.status.Store(http.StatusInternalServerError)

	if _, err := rs.client(t).Latest(context.Background()); err == nil {
		t.Fatal("expected an error when the first check fails")
	}
}

func TestClientLatestReportsRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1700000000")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	client := NewClient(WithAPIBase(srv.URL), WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))
	_, err := client.Latest(context.Background())
	if err == nil {
		t.Fatal("expected an error for an exhausted rate limit")
	}
	if !strings.Contains(err.Error(), "rate limit exceeded") {
		t.Fatalf("error = %v, want a rate-limit explanation", err)
	}
}

func TestValidateAssetURL(t *testing.T) {
	real := NewClient()
	relaxed := NewClient(WithAPIBase("http://127.0.0.1:8080"))

	if err := real.validateAssetURL("https://github.com/o/r/releases/download/v1/a.tgz"); err != nil {
		t.Fatalf("a github.com asset URL was rejected: %v", err)
	}
	if err := real.validateAssetURL("https://objects.githubusercontent.com/x/y"); err != nil {
		t.Fatalf("a githubusercontent asset URL was rejected: %v", err)
	}
	if err := real.validateAssetURL("https://evil.example.com/firefly"); err == nil {
		t.Fatal("a foreign asset host was accepted")
	}
	if err := real.validateAssetURL("http://github.com/o/r/a.tgz"); err == nil {
		t.Fatal("a plain-http asset URL was accepted")
	}
	if err := relaxed.validateAssetURL("http://127.0.0.1:8080/asset"); err != nil {
		t.Fatalf("an overridden API base should relax the host check: %v", err)
	}
}
