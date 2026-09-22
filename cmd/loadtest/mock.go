package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/adapter/anthropic"
	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/observability/usage"
	"github.com/dickymuliafiqri/firefly/internal/registry"
	"github.com/dickymuliafiqri/firefly/internal/security/auth"
	"github.com/dickymuliafiqri/firefly/internal/server"
	"github.com/dickymuliafiqri/firefly/internal/transport/upstream"
	"github.com/tidwall/gjson"
)

const (
	MockAPIKey = "sk-gw-demo-000000000000000000000000"
	MockModel  = "mock-chat"
)

// MockHarness houses the in-process mock upstream and Firefly gateway.
type MockHarness struct {
	BaseURL string
	Model   string
	APIKey  string
	Close   func()
}

type memSource struct {
	files map[string][]byte
}

func (m memSource) Load(ctx context.Context) (map[string][]byte, error) {
	return m.files, nil
}

// StartMockHarness starts an in-process mock upstream and a high-concurrency Firefly gateway.
func StartMockHarness() (*MockHarness, error) {
	// 1. Mock upstream HTTP server simulating OpenAI chat completions
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		isStream := gjson.GetBytes(bodyBytes, "stream").Bool()

		if isStream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")

			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
				return
			}

			// Adjust chunk count and delay based on request payload
			chunkCount := 3
			delay := 0 * time.Millisecond

			if bytes.Contains(bodyBytes, []byte("concurrency")) || bytes.Contains(bodyBytes, []byte("SSE")) {
				chunkCount = 12
				delay = 4 * time.Millisecond
			} else if bytes.Contains(bodyBytes, []byte("differentiates")) {
				chunkCount = 6
				delay = 2 * time.Millisecond
			}

			for i := 0; i < chunkCount; i++ {
				chunkJSON := fmt.Sprintf(
					`{"id":"chatcmpl-mock","object":"chat.completion.chunk","created":%d,"model":"mock-chat","choices":[{"index":0,"delta":{"content":" chunk_%d"},"finish_reason":null}]}`+"\n\n",
					time.Now().Unix(), i+1,
				)
				_, _ = fmt.Fprintf(w, "data: %s", chunkJSON)
				flusher.Flush()

				if delay > 0 {
					time.Sleep(delay)
				}
			}

			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}

		// Non-streaming response
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"id":      "chatcmpl-mock",
			"object":  "chat.completion",
			"created": time.Now().Unix(),
			"model":   "mock-chat",
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]string{
						"role":    "assistant",
						"content": "Pong! Firefly mock response completed successfully.",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]int{
				"prompt_tokens":     12,
				"completion_tokens": 8,
				"total_tokens":      20,
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))

	// 2. Build Firefly in-memory configuration set
	upstreams := []byte(fmt.Sprintf(`{
		"upstreams": [
			{
				"name": "mock-upstream",
				"base_url": %q,
				"protocol": "openai",
				"allow_insecure": true,
				"credential_ref": "MOCK_KEY",
				"max_idle_conns_per_host": 3000,
				"max_conns_per_host": 3000,
				"credential_max_concurrent": 3000
			}
		]
	}`, mockUpstream.URL))

	models := []byte(`{
		"models": [
			{
				"public_name": "mock-chat",
				"upstream": "mock-upstream",
				"upstream_model": "mock-chat",
				"enabled": true
			}
		]
	}`)

	keyHash := auth.HashKey(MockAPIKey)
	tenants := []byte(fmt.Sprintf(`{
		"tenants": [
			{
				"key_hash": %q,
				"name": "loadtest-tenant",
				"status": "active",
				"allowed_models": ["*"],
				"rate_limit": {
					"rps": 100000,
					"burst": 100000,
					"max_concurrent": 3000
				}
			}
		]
	}`, keyHash))

	reg := registry.New()
	src := memSource{
		files: map[string][]byte{
			"upstreams": upstreams,
			"models":    models,
			"tenants":   tenants,
		},
	}

	envLookup := func(k string) (string, bool) {
		return "mock-secret-key-12345", true
	}

	if _, err := reg.BuildAndStore(context.Background(), src, envLookup); err != nil {
		mockUpstream.Close()
		return nil, fmt.Errorf("build test config: %w", err)
	}

	// 3. Assemble Firefly router dependencies
	discardLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	clientPool := upstream.NewPool()
	breakers := upstream.NewBreakerRegistry(upstream.BreakerConfig{
		FailureThreshold: 5,
		SuccessThreshold: 2,
		Cooldown:         10 * time.Second,
	})
	retry := upstream.DefaultRetryPolicy()

	openaiAdapter := openai.NewAdapter(clientPool, breakers, openai.Config{
		SecretLookup:     envLookup,
		Retry:            retry,
		Logger:           discardLogger,
		MaxBufferedBytes: 32 << 20,
	})
	anthropicAdapter := anthropic.NewAdapter(clientPool, breakers, anthropic.Config{
		SecretLookup: envLookup,
		Retry:        retry,
		Logger:       discardLogger,
	})

	adapterRegistry := registry.NewAdapterRegistry()
	_ = adapterRegistry.Register(domain.ProtocolOpenAI, openaiAdapter)
	_ = adapterRegistry.Register(domain.ProtocolAnthropic, anthropicAdapter)

	deps := server.RouterDeps{
		Snapshots:   reg,
		TenantStore: auth.NewStore(reg),
		Limiter:     limits.New(),
		Adapters:    adapterRegistry,
		Breakers:    breakers,
		Usage:       usage.NewCounters(),
		Logger:      discardLogger,
		LiveLogs:    server.NewLiveLogHub(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := server.New(server.Config{
		Addr:          "127.0.0.1:0",
		ShutdownGrace: 5 * time.Second,
	}, deps, ctx, discardLogger)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		mockUpstream.Close()
		return nil, fmt.Errorf("listen on random port: %w", err)
	}

	var serverWg sync.WaitGroup
	serverWg.Add(1)
	go func() {
		defer serverWg.Done()
		_ = s.ServeOnListener(ln)
	}()

	baseURL := "http://" + ln.Addr().String()

	cleanup := func() {
		cancel()
		_ = ln.Close()
		mockUpstream.Close()
		serverWg.Wait()
	}

	return &MockHarness{
		BaseURL: baseURL,
		Model:   MockModel,
		APIKey:  MockAPIKey,
		Close:   cleanup,
	}, nil
}
