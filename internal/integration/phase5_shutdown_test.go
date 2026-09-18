package integration

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/security/auth"
	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/registry"
	"github.com/dickymuliafiqri/firefly/internal/server"
	"github.com/dickymuliafiqri/firefly/internal/transport/upstream"
	"github.com/dickymuliafiqri/firefly/internal/observability/usage"
	"github.com/dickymuliafiqri/firefly/internal/watch"
)

// TestShutdownJoinsWorkers verifies the watcher + exporter goroutines are joined
// after cancel, so the process exits with no leaked background goroutines. This
// closes the deferred LOW finding that main never joined its workers.
func TestShutdownJoinsWorkers(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "active", []string{"gpt-4o"})

	ctx, cancel := context.WithCancel(context.Background())
	reg := registry.New()
	src := config.NewFileConfigSource(dir)
	if _, err := reg.BuildAndStore(ctx, src, envOK); err != nil {
		t.Fatalf("build: %v", err)
	}

	var wg sync.WaitGroup
	w := watch.New(watch.Options{Dir: dir, Logger: discardLogger()}, reg)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = w.Run(ctx, func(context.Context) error { return nil })
	}()

	exp := &usage.Exporter{Counters: usage.NewCounters(), Interval: 10 * time.Millisecond, Logger: discardLogger()}
	wg.Add(1)
	go func() {
		defer wg.Done()
		exp.Run(ctx)
	}()

	time.Sleep(50 * time.Millisecond) // let workers start
	cancel()

	joined := make(chan struct{})
	go func() { wg.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(3 * time.Second):
		t.Fatal("background workers were not joined within 3s")
	}
}

// TestPhase5_GracefulDrainForStreamingConnections verifies that when the server enters
// shutdown, active long-lived streaming connections are allowed to complete cleanly
// up to the shutdown grace period, while new incoming requests are immediately rejected with 503.
func TestPhase5_GracefulDrainForStreamingConnections(t *testing.T) {
	streamStarted := make(chan struct{})
	allowStreamFinish := make(chan struct{})

	fakeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("expected flusher")
			return
		}

		// Chunk 1
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"first-chunk\"}}]}\n\n"))
		flusher.Flush()
		close(streamStarted)

		// Wait until shutdown signal has been triggered
		<-allowStreamFinish

		// Chunk 2 and DONE
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"second-chunk\"}}]}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer fakeUpstream.Close()

	dir := t.TempDir()
	reg := registry.New()
	writeConfigWithUpstream(t, dir, fakeUpstream.URL, []string{"gpt-4o"})
	build(t, reg, dir)

	pool := upstream.NewPool()
	breakers := upstream.NewBreakerRegistry(upstream.BreakerConfig{
		FailureThreshold: 5,
		SuccessThreshold: 2,
		Cooldown:         time.Minute,
	})
	adapter := openai.NewAdapter(pool, breakers, openai.Config{
		SecretLookup: func(ref string) (string, bool) {
			return "sk-upstream-secret", true
		},
	})
	adapters := registry.NewAdapterRegistry()
	_ = adapters.Register(domain.ProtocolOpenAI, adapter)

	deps := server.RouterDeps{
		Snapshots:   reg,
		TenantStore: auth.NewStore(reg),
		Limiter:     limits.New(),
		Adapters:    adapters,
		Adapter:     adapter,
		Usage:       usage.NewCounters(),
		Logger:      discardLogger(),
	}

	serverCtx, cancelServer := context.WithCancel(context.Background())
	s := server.New(server.Config{
		Addr:          "127.0.0.1:0",
		ShutdownGrace: 3 * time.Second,
	}, deps, serverCtx, discardLogger())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- s.ServeOnListener(ln)
	}()

	base := "http://" + ln.Addr().String()

	// 1. Client 1 starts streaming request
	client1Req, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	client1Req.Header.Set("Authorization", "Bearer "+gatewayKey)
	client1Req.Header.Set("Content-Type", "application/json")

	client1Resp, err := http.DefaultClient.Do(client1Req)
	if err != nil {
		t.Fatalf("client 1 request failed: %v", err)
	}
	defer client1Resp.Body.Close()

	if client1Resp.StatusCode != http.StatusOK {
		t.Fatalf("client 1 status = %d, want 200", client1Resp.StatusCode)
	}

	// Read first chunk from client 1
	reader := bufio.NewReader(client1Resp.Body)
	line1, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read first chunk: %v", err)
	}
	if !strings.Contains(line1, "first-chunk") {
		t.Fatalf("unexpected first chunk content: %s", line1)
	}

	// 2. Deliver shutdown signal while stream is actively in-flight
	<-streamStarted
	cancelServer()

	// Give the server a moment to initiate draining and set shuttingDown
	time.Sleep(30 * time.Millisecond)
	if !s.ShuttingDown() {
		t.Fatal("expected server to be in ShuttingDown state")
	}

	// 3. Client 2 attempts a new request -> MUST be rejected with 503
	client2Req, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"new request"}]}`))
	client2Req.Header.Set("Authorization", "Bearer "+gatewayKey)
	client2Req.Header.Set("Content-Type", "application/json")

	// Use custom client with short timeout in case connection was closed
	client2 := &http.Client{Timeout: 1 * time.Second}
	client2Resp, err := client2.Do(client2Req)
	if err == nil {
		b, _ := io.ReadAll(client2Resp.Body)
		client2Resp.Body.Close()
		if client2Resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("expected new request during drain to get 503, got %d (body: %s)", client2Resp.StatusCode, string(b))
		}
		if client2Resp.Header.Get("Retry-After") != "5" {
			t.Fatalf("expected Retry-After: 5 header on 503, got %q", client2Resp.Header.Get("Retry-After"))
		}
	}

	// 4. Client 1 (active stream) completes naturally
	close(allowStreamFinish)
	var fullStreamOutput strings.Builder
	fullStreamOutput.WriteString(line1)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			fullStreamOutput.WriteString(line)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("error reading remaining stream during graceful drain: %v", err)
		}
	}

	output := fullStreamOutput.String()
	if !strings.Contains(output, "second-chunk") || !strings.Contains(output, "[DONE]") {
		t.Fatalf("stream did not complete cleanly; output:\n%s", output)
	}

	// 5. Server should return cleanly within the grace period
	select {
	case sErr := <-serverErrCh:
		if sErr != nil {
			t.Fatalf("server returned error on graceful shutdown: %v", sErr)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("server did not complete graceful drain within grace period")
	}
}

