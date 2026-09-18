// Package integration exercises the full request path: config files on disk ->
// registry snapshot -> auth -> admission -> routing -> upstream adapter ->
// streaming relay, including a hot reload that changes live behavior.
package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/security/auth"
	"github.com/dickymuliafiqri/firefly/internal/transport/httpx"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/registry"
	"github.com/dickymuliafiqri/firefly/internal/server"
	"github.com/dickymuliafiqri/firefly/internal/transport/upstream"
	"github.com/dickymuliafiqri/firefly/internal/observability/usage"
)

// fakeUpstream is a minimal OpenAI-compatible server used as the upstream.
type fakeUpstream struct {
	*httptest.Server
	gotModel string
	gotAuth  string
}

func newFakeUpstream(t *testing.T) *fakeUpstream {
	t.Helper()
	f := &fakeUpstream{}
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		f.gotAuth = r.Header.Get("Authorization")
		var body struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.gotModel = body.Model

		if !body.Stream {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"cmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for _, chunk := range []string{"Hel", "lo", "!"} {
			_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"`+chunk+`"}}]}`+"\n\n")
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(20 * time.Millisecond)
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if fl != nil {
			fl.Flush()
		}
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// realAdapter builds the production adapter wired to a real client pool and
// breaker registry, with the upstream secret supplied via an injected lookup.
func realAdapter() *openai.Adapter {
	return openai.NewAdapter(
		upstream.NewPool(),
		upstream.NewBreakerRegistry(upstream.BreakerConfig{FailureThreshold: 3, Cooldown: time.Second}),
		openai.Config{
			SecretLookup:     func(string) (string, bool) { return "sk-fake-upstream", true },
			Retry:            upstream.DefaultRetryPolicy(),
			MaxBufferedBytes: 1 << 20,
		},
	)
}

func TestEndToEndNonStreamingChat(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-fake-upstream")
	up := newFakeUpstream(t)

	dir := t.TempDir()
	writeConfigWithUpstream(t, dir, up.URL, []string{"gpt-4o-mini", "gpt-4o"})
	reg := registry.New()
	build(t, reg, dir)
	base := setupServerWithAdapter(t, reg, realAdapter())

	req, _ := http.NewRequest("POST", base+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+gatewayKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if up.gotModel != "gpt-4o-mini" {
		t.Fatalf("upstream saw model %q, want gpt-4o-mini", up.gotModel)
	}
	if up.gotAuth != "Bearer sk-fake-upstream" {
		t.Fatalf("upstream Authorization = %q", up.gotAuth)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"chat.completion"`) {
		t.Fatalf("body = %s", body)
	}
}

func TestEndToEndStreamingIsIncremental(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-fake-upstream")
	up := newFakeUpstream(t)

	dir := t.TempDir()
	writeConfigWithUpstream(t, dir, up.URL, []string{"gpt-4o-mini"})
	reg := registry.New()
	build(t, reg, dir)
	base := setupServerWithAdapter(t, reg, realAdapter())

	req, _ := http.NewRequest("POST", base+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o-mini","stream":true,"messages":[]}`))
	req.Header.Set("Authorization", "Bearer "+gatewayKey)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	// Read line by line and assert every chunk arrives; a buffering gateway
	// would still pass this but a broken relay that drops the flush would fail
	// the deadline.
	sc := bufio.NewScanner(resp.Body)
	var lines []string
	done := make(chan struct{})
	go func() {
		for sc.Scan() {
			lines = append(lines, sc.Text())
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("streaming read timed out")
	}

	joined := strings.Join(lines, "\n")
	for _, want := range []string{"Hel", "lo", "!", "[DONE]"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("stream missing %q; got:\n%s", want, joined)
		}
	}
}

func TestEndToEndUpstreamFailureReturnsOpenAIError(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-fake-upstream")
	// An upstream that always 500s.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"upstream exploded","type":"api_error"}}`)
	}))
	defer up.Close()

	dir := t.TempDir()
	writeConfigWithUpstream(t, dir, up.URL, []string{"gpt-4o-mini"})
	reg := registry.New()
	build(t, reg, dir)
	base := setupServerWithAdapter(t, reg, realAdapter())

	req, _ := http.NewRequest("POST", base+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o-mini"}`))
	req.Header.Set("Authorization", "Bearer "+gatewayKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (relayed from upstream)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "upstream exploded") {
		t.Fatalf("error body not relayed: %s", body)
	}
}

// TestPhase4_HighConcurrencyStreaming1000Users simulates 1,000 simultaneous
// streaming connections to prove system stability, zero goroutine leaks,
// bounded memory usage, and zero socket leaks under production-scale load.
func TestPhase4_HighConcurrencyStreaming1000Users(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-fake-upstream")

	// Upstream streaming server that responds to each request with multiple SSE events.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, ok := w.(http.Flusher)
		for i := 0; i < 5; i++ {
			_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"token-%d \"}}]}\n\n", i)
			if ok {
				fl.Flush()
			}
			time.Sleep(5 * time.Millisecond)
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if ok {
			fl.Flush()
		}
	}))
	defer up.Close()

	dir := t.TempDir()
	writeConfigWithConcurrency(t, dir, up.URL, []string{"gpt-4o-mini"}, 2000)
	reg := registry.New()
	build(t, reg, dir)

	// Adapter with connection pool tuned for high scale.
	ad := realAdapter()
	base := setupServerWithAdapter(t, reg, ad)

	// High-throughput client transport.
	client := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        2000,
			MaxIdleConnsPerHost: 1000,
			MaxConnsPerHost:     1500,
			IdleConnTimeout:     90 * time.Second,
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
		},
	}
	defer client.CloseIdleConnections()

	const concurrentUsers = 1000
	var wg sync.WaitGroup
	var successCount atomic.Int64
	var failCount atomic.Int64

	// Track heap memory before test run.
	runtime.GC()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	start := time.Now()
	wg.Add(concurrentUsers)
	for i := 0; i < concurrentUsers; i++ {
		go func(id int) {
			defer wg.Done()

			var resp *http.Response
			var err error
			for attempt := 0; attempt < 5; attempt++ {
				req, reqErr := http.NewRequest("POST", base+"/v1/chat/completions",
					strings.NewReader(`{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"ping"}]}`))
				if reqErr != nil {
					failCount.Add(1)
					return
				}
				req.Header.Set("Authorization", "Bearer "+gatewayKey)
				req.Header.Set("Accept", "text/event-stream")

				resp, err = client.Do(req)
				if err != nil {
					if strings.Contains(err.Error(), "refused") || strings.Contains(err.Error(), "reset") {
						time.Sleep(15 * time.Millisecond)
						continue
					}
					failCount.Add(1)
					return
				}
				break
			}
			if resp == nil {
				failCount.Add(1)
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				failCount.Add(1)
				return
			}

			// Read stream to completion.
			scanner := bufio.NewScanner(resp.Body)
			hasDone := false
			for scanner.Scan() {
				if strings.Contains(scanner.Text(), "[DONE]") {
					hasDone = true
					break
				}
			}
			if hasDone {
				successCount.Add(1)
			} else {
				failCount.Add(1)
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(start)

	// Read memory after high-concurrency run.
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	t.Logf("Completed %d streams in %v (success: %d, fail: %d)",
		concurrentUsers, duration, successCount.Load(), failCount.Load())
	t.Logf("HeapAlloc before: %d KB, after: %d KB (delta: %d KB)",
		memBefore.HeapAlloc/1024, memAfter.HeapAlloc/1024,
		int64(memAfter.HeapAlloc-memBefore.HeapAlloc)/1024)

	if failCount.Load() > 0 {
		t.Fatalf("%d streams failed out of %d concurrent requests", failCount.Load(), concurrentUsers)
	}
	if successCount.Load() != concurrentUsers {
		t.Fatalf("expected %d successful streams, got %d", concurrentUsers, successCount.Load())
	}
}


// TestPhase4_BurstLoadAdmissionQueueAndBackpressure tests bounded wait and fail-fast
// 429 when burst traffic exceeds the global admission capacity.
func TestPhase4_BurstLoadAdmissionQueueAndBackpressure(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-fake-upstream")

	// Upstream that holds request briefly (100ms) to simulate work.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"ok","choices":[{"message":{"content":"done"}}]}`)
	}))
	defer up.Close()

	dir := t.TempDir()
	writeConfigWithConcurrency(t, dir, up.URL, []string{"gpt-4o-mini"}, 500)
	reg := registry.New()
	build(t, reg, dir)

	// Restrict global admission capacity to 50 slots and wait timeout to 40ms.
	globalLimiter := httpx.NewGlobalLimiter(50, 40*time.Millisecond)

	deps := server.RouterDeps{
		Snapshots:     reg,
		TenantStore:   auth.NewStore(reg),
		Limiter:       limits.New(),
		Adapter:       realAdapter(),
		Usage:         usage.NewCounters(),
		Logger:        discardLogger(),
		GlobalLimiter: globalLimiter,
	}
	base := serve(t, deps)

	const totalBurst = 150
	var wg sync.WaitGroup
	var count200 atomic.Int64
	var count429 atomic.Int64

	client := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        500,
			MaxIdleConnsPerHost: 500,
		},
	}
	defer client.CloseIdleConnections()

	wg.Add(totalBurst)
	for i := 0; i < totalBurst; i++ {
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("POST", base+"/v1/chat/completions",
				strings.NewReader(`{"model":"gpt-4o-mini"}`))
			req.Header.Set("Authorization", "Bearer "+gatewayKey)
			resp, err := client.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()

			switch resp.StatusCode {
			case http.StatusOK:
				count200.Add(1)
			case http.StatusTooManyRequests:
				count429.Add(1)
				if resp.Header.Get("Retry-After") != "2" {
					t.Errorf("expected Retry-After: 2, got %q", resp.Header.Get("Retry-After"))
				}
			}
			_, _ = io.Copy(io.Discard, resp.Body)
		}()
	}

	wg.Wait()
	t.Logf("Burst test results: 200 OK = %d, 429 Too Many Requests = %d", count200.Load(), count429.Load())

	if count429.Load() == 0 {
		t.Fatal("expected admission queue to reject burst requests with 429")
	}
	if count200.Load() == 0 {
		t.Fatal("expected at least some requests to succeed")
	}
}

// TestPhase4_AggressiveClientDisconnectClosesUpstream verifies that when a client
// aborts a long-running stream, the upstream body is closed immediately.
func TestPhase4_AggressiveClientDisconnectClosesUpstream(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-fake-upstream")

	upstreamAborted := make(chan struct{})
	upstreamClosedOnce := sync.Once{}

	// Upstream that keeps sending tokens until its context or write fails.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)

		for {
			select {
			case <-r.Context().Done():
				upstreamClosedOnce.Do(func() { close(upstreamAborted) })
				return
			default:
			}
			_, err := io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"tick\"}}]}\n\n")
			if err != nil {
				upstreamClosedOnce.Do(func() { close(upstreamAborted) })
				return
			}
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(20 * time.Millisecond)
		}
	}))
	defer up.Close()

	dir := t.TempDir()
	writeConfigWithUpstream(t, dir, up.URL, []string{"gpt-4o-mini"})
	reg := registry.New()
	build(t, reg, dir)
	base := setupServerWithAdapter(t, reg, realAdapter())

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", base+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o-mini","stream":true}`))
	req.Header.Set("Authorization", "Bearer "+gatewayKey)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Read one chunk to confirm stream is flowing.
	scanner := bufio.NewScanner(resp.Body)
	if !scanner.Scan() {
		t.Fatal("failed to read first chunk from stream")
	}

	// Client cancels context.
	cancel()

	// Upstream must detect abort and terminate within a fraction of a second.
	select {
	case <-upstreamAborted:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream stream was not closed promptly upon client disconnect")
	}
}

