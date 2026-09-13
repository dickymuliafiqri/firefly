package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/auth"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/openai"
	"github.com/dickymuliafiqri/firefly/internal/registry"
	"github.com/dickymuliafiqri/firefly/internal/server"
	"github.com/dickymuliafiqri/firefly/internal/upstream"
	"github.com/dickymuliafiqri/firefly/internal/usage"
)

func TestLiveConnectionLog_Integration(t *testing.T) {
	// Fake upstream echoing mock response
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer upstreamSrv.Close()

	dir := t.TempDir()
	writeConfigWithUpstream(t, dir, upstreamSrv.URL, []string{"gpt-4o"})

	reg := registry.New()
	build(t, reg, dir)

	pool := upstream.NewPool()
	breakers := upstream.NewBreakerRegistry(upstream.BreakerConfig{
		FailureThreshold: 5,
		SuccessThreshold: 2,
		Cooldown:         10 * time.Second,
	})
	ad := openai.NewAdapter(pool, breakers, openai.Config{
		SecretLookup: func(string) (string, bool) { return "secret", true },
		Logger:       discardLogger(),
	})

	hub := server.NewLiveLogHub()

	deps := server.RouterDeps{
		Snapshots:   reg,
		Registry:    reg,
		TenantStore: auth.NewStore(reg),
		Limiter:     limits.New(),
		Usage:       usage.NewCounters(),
		LiveLogs:    hub,
		Adapter:     ad,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := server.New(server.Config{Addr: "127.0.0.1:0"}, deps, ctx, discardLogger())
	testServer := httptest.NewServer(s.Handler())
	defer testServer.Close()

	// 1. Connect to SSE stream
	sseReq, err := http.NewRequestWithContext(ctx, http.MethodGet, testServer.URL+"/api/telemetry/events", nil)
	if err != nil {
		t.Fatal(err)
	}

	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()

	if sseResp.StatusCode != http.StatusOK {
		t.Fatalf("SSE status = %d, want 200", sseResp.StatusCode)
	}

	eventCh := make(chan string, 10)
	go func() {
		reader := bufio.NewReader(sseResp.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "data: ") {
				eventCh <- strings.TrimPrefix(line, "data: ")
			}
		}
	}()

	// 2. Make an inbound request to /v1/chat/completions
	chatPayload := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"ping"}]}`)
	req, err := http.NewRequest(http.MethodPost, testServer.URL+"/v1/chat/completions", bytes.NewReader(chatPayload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+gatewayKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /v1/chat/completions = %d: %s", resp.StatusCode, body)
	}

	// 3. Verify in-flight start event is received via SSE immediately
	select {
	case eventData := <-eventCh:
		var startLog server.LiveLog
		if err := json.Unmarshal([]byte(eventData), &startLog); err != nil {
			t.Fatalf("unmarshal start event data: %v", err)
		}
		if startLog.Upstream != "openai" {
			t.Errorf("expected start upstream 'openai', got %q", startLog.Upstream)
		}
		if startLog.Status != 0 {
			t.Errorf("expected in-flight start status 0, got %d", startLog.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SSE in-flight start event")
	}

	// 3b. Verify completion event is received via SSE
	select {
	case eventData := <-eventCh:
		var endLog server.LiveLog
		if err := json.Unmarshal([]byte(eventData), &endLog); err != nil {
			t.Fatalf("unmarshal end event data: %v", err)
		}
		if endLog.Upstream != "openai" {
			t.Errorf("expected end upstream 'openai', got %q", endLog.Upstream)
		}
		if endLog.Status != http.StatusOK {
			t.Errorf("expected end status 200, got %d", endLog.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SSE completion event")
	}

	// 4. Verify GET /api/telemetry snapshot
	telemResp, err := http.Get(testServer.URL + "/api/telemetry")
	if err != nil {
		t.Fatal(err)
	}
	defer telemResp.Body.Close()

	var telem server.TelemetryDTO
	if err := json.NewDecoder(telemResp.Body).Decode(&telem); err != nil {
		t.Fatalf("decode telemetry: %v", err)
	}

	if len(telem.RecentLogs) == 0 {
		t.Fatal("expected telem.RecentLogs to contain at least 1 entry")
	}
	if telem.RecentLogs[0].Upstream != "openai" {
		t.Errorf("expected recent log upstream 'openai', got %q", telem.RecentLogs[0].Upstream)
	}

	// 5. Test another request with unknown model
	badReq, _ := http.NewRequest(http.MethodPost, testServer.URL+"/v1/chat/completions", bytes.NewReader([]byte(`{"model":"non-existent"}`)))
	badReq.Header.Set("Authorization", "Bearer "+gatewayKey)
	badReq.Header.Set("Content-Type", "application/json")
	badResp, err := http.DefaultClient.Do(badReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = badResp.Body.Close()
	if badResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", badResp.StatusCode)
	}

	// Verify the 404 event was received via SSE
	select {
	case eventData := <-eventCh:
		var badLog server.LiveLog
		if err := json.Unmarshal([]byte(eventData), &badLog); err != nil {
			t.Fatalf("unmarshal error event: %v", err)
		}
		if badLog.Status != http.StatusNotFound {
			t.Errorf("expected status 404, got %d", badLog.Status)
		}
		if badLog.Model != "non-existent" {
			t.Errorf("expected model 'non-existent', got %q", badLog.Model)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for 404 SSE log event")
	}
}
