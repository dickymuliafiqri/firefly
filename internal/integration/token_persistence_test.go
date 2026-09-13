package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/analytics"
	"github.com/dickymuliafiqri/firefly/internal/auth"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/openai"
	"github.com/dickymuliafiqri/firefly/internal/registry"
	"github.com/dickymuliafiqri/firefly/internal/server"
	"github.com/dickymuliafiqri/firefly/internal/upstream"
	"github.com/dickymuliafiqri/firefly/internal/usage"
)

func TestTokenTelemetryAndPersistence_Integration(t *testing.T) {
	// Fake upstream echoing mock response WITH OpenAI provider usage object
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-exact-usage",
			"object": "chat.completion",
			"created": 1677652288,
			"model": "gpt-4o",
			"choices": [{"message":{"role":"assistant","content":"Hello world!"}}],
			"usage": {
				"prompt_tokens": 42,
				"completion_tokens": 58,
				"total_tokens": 100
			}
		}`))
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

	analyticsStore, err := analytics.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	hub := server.NewLiveLogHub()
	hub.AttachStore(analyticsStore)

	deps := server.RouterDeps{
		Snapshots:   reg,
		Registry:    reg,
		TenantStore: auth.NewStore(reg),
		Limiter:     limits.New(),
		Usage:       usage.NewCounters(),
		LiveLogs:    hub,
		Analytics:   analyticsStore,
		Adapter:     ad,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := server.New(server.Config{Addr: "127.0.0.1:0"}, deps, ctx, discardLogger())
	testServer := httptest.NewServer(s.Handler())
	defer testServer.Close()

	// 1. Send chat completion request
	chatPayload := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"How are you?"}]}`)
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
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /v1/chat/completions = %d: %s", resp.StatusCode, b)
	}

	// 2. Query /api/telemetry to verify token statistics are updated
	telemResp, err := http.Get(testServer.URL + "/api/telemetry")
	if err != nil {
		t.Fatal(err)
	}
	defer telemResp.Body.Close()

	var telem server.TelemetryDTO
	if err := json.NewDecoder(telemResp.Body).Decode(&telem); err != nil {
		t.Fatalf("decode telemetry: %v", err)
	}

	if telem.Summary.TotalRequests < 1 {
		t.Errorf("expected TotalRequests >= 1, got %d", telem.Summary.TotalRequests)
	}
	// Verify exact token usage extracted from upstream provider response
	if telem.Summary.InputTokens != 42 {
		t.Errorf("expected InputTokens 42, got %d", telem.Summary.InputTokens)
	}
	if telem.Summary.OutputTokens != 58 {
		t.Errorf("expected OutputTokens 58, got %d", telem.Summary.OutputTokens)
	}
	if telem.Summary.TotalTokens != 100 {
		t.Errorf("expected TotalTokens 100, got %d", telem.Summary.TotalTokens)
	}
	if telem.Summary.EstimatedCostUsd <= 0 {
		t.Errorf("expected positive EstimatedCostUsd, got %f", telem.Summary.EstimatedCostUsd)
	}
	if len(telem.RecentLogs) == 0 {
		t.Fatal("expected RecentLogs to have at least 1 entry")
	}
	if telem.RecentLogs[0].TokensIn != 42 || telem.RecentLogs[0].TokensOut != 58 {
		t.Errorf("expected log tokens 42/58, got %d/%d", telem.RecentLogs[0].TokensIn, telem.RecentLogs[0].TokensOut)
	}

	// 3. Flush store to disk
	if err := analyticsStore.Flush(context.Background()); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// 4. Simulate server restart by creating a new store from the same directory
	reloadedStore, err := analytics.NewStore(dir)
	if err != nil {
		t.Fatalf("reload NewStore failed: %v", err)
	}

	reloadedSummary, err := reloadedStore.Summary(context.Background())
	if err != nil {
		t.Fatalf("reloaded Summary failed: %v", err)
	}
	if reloadedSummary.TotalRequests != 1 {
		t.Errorf("reloaded TotalRequests = %d, want 1", reloadedSummary.TotalRequests)
	}
	if reloadedSummary.InputTokens != 42 {
		t.Errorf("reloaded InputTokens = %d, want 42", reloadedSummary.InputTokens)
	}
	if reloadedSummary.OutputTokens != 58 {
		t.Errorf("reloaded OutputTokens = %d, want 58", reloadedSummary.OutputTokens)
	}
	if reloadedSummary.TotalTokens != 100 {
		t.Errorf("reloaded TotalTokens = %d, want 100", reloadedSummary.TotalTokens)
	}

	reloadedHistory, err := reloadedStore.History(context.Background(), 10)
	if err != nil {
		t.Fatalf("reloaded History failed: %v", err)
	}
	if len(reloadedHistory) != 1 {
		t.Fatalf("reloaded History length = %d, want 1", len(reloadedHistory))
	}
	if reloadedHistory[0].TokensIn != 42 || reloadedHistory[0].TokensOut != 58 {
		t.Errorf("reloaded History tokens = %d/%d, want 42/58", reloadedHistory[0].TokensIn, reloadedHistory[0].TokensOut)
	}
}

func TestTokenTelemetry_Streaming_Integration(t *testing.T) {
	// Fake upstream echoing SSE chunks
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected http.Flusher")
		}
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\" world!\"}}]}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
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

	analyticsStore, err := analytics.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	hub := server.NewLiveLogHub()
	hub.AttachStore(analyticsStore)

	deps := server.RouterDeps{
		Snapshots:   reg,
		Registry:    reg,
		TenantStore: auth.NewStore(reg),
		Limiter:     limits.New(),
		Usage:       usage.NewCounters(),
		LiveLogs:    hub,
		Analytics:   analyticsStore,
		Adapter:     ad,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := server.New(server.Config{Addr: "127.0.0.1:0"}, deps, ctx, discardLogger())
	testServer := httptest.NewServer(s.Handler())
	defer testServer.Close()

	// Send streaming request
	chatPayload := []byte(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"Stream me a message"}]}`)
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
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /v1/chat/completions = %d: %s", resp.StatusCode, b)
	}
	_, _ = io.ReadAll(resp.Body)

	// Check telemetry
	telemResp, err := http.Get(testServer.URL + "/api/telemetry")
	if err != nil {
		t.Fatal(err)
	}
	defer telemResp.Body.Close()

	var telem server.TelemetryDTO
	if err := json.NewDecoder(telemResp.Body).Decode(&telem); err != nil {
		t.Fatalf("decode telemetry: %v", err)
	}

	if telem.Summary.InputTokens <= 0 {
		t.Errorf("expected positive streaming InputTokens, got %d", telem.Summary.InputTokens)
	}
	if telem.Summary.OutputTokens <= 0 {
		t.Errorf("expected positive streaming OutputTokens, got %d", telem.Summary.OutputTokens)
	}
	if telem.Summary.TotalTokens <= 0 {
		t.Errorf("expected positive streaming TotalTokens, got %d", telem.Summary.TotalTokens)
	}
	if telem.Summary.EstimatedCostUsd <= 0 {
		t.Errorf("expected positive streaming EstimatedCostUsd, got %f", telem.Summary.EstimatedCostUsd)
	}
}
