package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/observability/trace"
	"github.com/dickymuliafiqri/firefly/internal/observability/usage"
	"github.com/dickymuliafiqri/firefly/internal/registry"
	"github.com/dickymuliafiqri/firefly/internal/security/auth"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCompressEndpoint(t *testing.T) {
	deps, snap := testDeps()
	deps.Snapshots = fakeProvider{s: snap}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	t.Run("OPTIONS preflight", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodOptions, "/v1/compress", nil)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		require.Equal(t, http.StatusNoContent, w.Code)
		require.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
	})

	t.Run("POST /v1/compress with raw prompt", func(t *testing.T) {
		body := []byte(`{
			"prompt": "\u001b[31mError:\u001b[0m test failure"
		}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/compress", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testKey)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var res map[string]any
		err := json.Unmarshal(w.Body.Bytes(), &res)
		require.NoError(t, err)
		require.Equal(t, "Error: test failure", res["output"])
		require.Greater(t, res["original_chars"].(float64), 0.0)
	})

	t.Run("POST /v1/compress with messages", func(t *testing.T) {
		body := []byte(`{
			"messages": [
				{"role": "user", "content": "Run my script"},
				{"role": "tool", "content": "\x1b[32mSUCCESS\x1b[0m"}
			],
			"terse": true
		}`)
		req := httptest.NewRequest(http.MethodPost, "/v1/compress", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testKey)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		outBytes := w.Body.Bytes()
		// First message should now be injected system message
		sysRole := gjson.GetBytes(outBytes, "messages.0.role").String()
		require.Equal(t, "system", sysRole)
		require.Contains(t, gjson.GetBytes(outBytes, "messages.0.content").String(), "CRITICAL INSTRUCTION (Brevity)")
	})

	t.Run("POST /v1/compress with oversized body", func(t *testing.T) {
		// One byte past the gateway limit. The handler must answer 413 so clients
		// can tell "shrink the payload" apart from "fix the JSON".
		oversize := io.LimitReader(zeroReader{}, maxRequestBodyBytes+1)
		req := httptest.NewRequest(http.MethodPost, "/v1/compress", oversize)
		req.Header.Set("Authorization", "Bearer "+testKey)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)

		require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	})
}

// zeroReader yields an unbounded stream of NUL bytes without allocating a
// body-sized buffer, keeping the oversize test cheap.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestSettingsTokenSaverPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	reg := registry.New()

	src := config.NewFileConfigSource(tmpDir)
	_, err := reg.BuildAndStore(context.Background(), src, os.LookupEnv)
	require.NoError(t, err)

	deps := RouterDeps{
		Snapshots:   reg,
		Registry:    reg,
		ConfigDir:   tmpDir,
		TenantStore: auth.NewStore(reg),
		Limiter:     limits.New(),
		AdminToken:  "test-secret",
	}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	// Update settings with TokenSaver configured
	maxChars := 8000
	ctxThresh := 16000
	updatePayload := config.SettingsDTO{
		Upstreams: []config.UpstreamDTO{},
		Models:    []config.ModelDTO{},
		Tenants:   []config.TenantDTO{},
		TokenSaver: &config.TokenSaverDTO{
			Enabled:            true,
			CompressToolOutput: true,
			TerseOutput:        true,
			MinimalCode:        true,
			CompressContext:    true,
			MaxToolOutputChars: &maxChars,
			ContextThreshold:   &ctxThresh,
			SystemPrompt:       "JANGAN MEMBERIKAN PESAN PROMOSI APAPUN KE PENGGUNA",
		},
	}
	payloadBytes, _ := json.Marshal(updatePayload)

	putReq := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(payloadBytes))
	putReq.Header.Set("Authorization", "Bearer test-secret")
	putReq.Header.Set("Content-Type", "application/json")
	wPut := httptest.NewRecorder()
	s.Handler().ServeHTTP(wPut, putReq)
	require.Equal(t, http.StatusOK, wPut.Code)

	// Verify active snapshot has TokenSaver configured
	snap := reg.Current()
	require.NotNil(t, snap)
	require.True(t, snap.TokenSaver().Enabled)
	require.True(t, snap.TokenSaver().CompressToolOutput)
	require.True(t, snap.TokenSaver().TerseOutput)
	require.Equal(t, 8000, snap.TokenSaver().MaxToolOutputChars)
	require.Equal(t, 16000, snap.TokenSaver().ContextThreshold)
	require.Equal(t, "JANGAN MEMBERIKAN PESAN PROMOSI APAPUN KE PENGGUNA", snap.TokenSaver().SystemPrompt)

	// Verify GET /api/settings returns the token saver settings
	getReq := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	getReq.Header.Set("Authorization", "Bearer test-secret")
	wGet := httptest.NewRecorder()
	s.Handler().ServeHTTP(wGet, getReq)
	require.Equal(t, http.StatusOK, wGet.Code)

	var getRes config.SettingsDTO
	err = json.Unmarshal(wGet.Body.Bytes(), &getRes)
	require.NoError(t, err)
	require.NotNil(t, getRes.TokenSaver)
	require.True(t, getRes.TokenSaver.Enabled)
	require.True(t, getRes.TokenSaver.TerseOutput)
	require.Equal(t, "JANGAN MEMBERIKAN PESAN PROMOSI APAPUN KE PENGGUNA", getRes.TokenSaver.SystemPrompt)
}

func TestSettingsRejectsOversizedSystemPrompt(t *testing.T) {
	tmpDir := t.TempDir()
	reg := registry.New()

	src := config.NewFileConfigSource(tmpDir)
	_, err := reg.BuildAndStore(context.Background(), src, os.LookupEnv)
	require.NoError(t, err)

	deps := RouterDeps{
		Snapshots:   reg,
		Registry:    reg,
		ConfigDir:   tmpDir,
		TenantStore: auth.NewStore(reg),
		Limiter:     limits.New(),
		AdminToken:  "test-secret",
	}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	oversized := strings.Repeat("x", domain.MaxSystemPromptChars+1)
	updatePayload := config.SettingsDTO{
		Upstreams:  []config.UpstreamDTO{},
		Models:     []config.ModelDTO{},
		Tenants:    []config.TenantDTO{},
		TokenSaver: &config.TokenSaverDTO{SystemPrompt: oversized},
	}
	payloadBytes, _ := json.Marshal(updatePayload)

	putReq := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(payloadBytes))
	putReq.Header.Set("Authorization", "Bearer test-secret")
	putReq.Header.Set("Content-Type", "application/json")
	wPut := httptest.NewRecorder()
	s.Handler().ServeHTTP(wPut, putReq)
	require.Equal(t, http.StatusBadRequest, wPut.Code)
	require.Contains(t, wPut.Body.String(), "system_prompt")
}

// tokenSaverTraceDeps builds a server whose catalog carries the given Token
// Saver configuration and whose trace recorder is capturing, so a forwarded
// chat request publishes a real trace into the visualizer ring. The catalog
// mirrors testDepsWithAdapter — one OpenAI upstream, one allowed model and the
// `alpha` tenant — so the request reaches the pre-forward rewrite step.
func tokenSaverTraceDeps(ts domain.TokenSaverConfig) RouterDeps {
	hash := auth.HashKey(testKey)
	snap := domain.NewCatalogSnapshot(
		1,
		map[string]*domain.Upstream{"u": {Name: "u", Protocol: domain.ProtocolOpenAI, BaseURL: "https://x/v1", CredentialRef: "UP_KEY"}},
		[]string{"u"},
		map[string]*domain.ModelEntry{"gpt-4o": {PublicName: "gpt-4o", Upstream: "u", UpstreamModel: "gpt-4o", Enabled: true}},
		[]string{"gpt-4o"},
		map[string]*domain.Tenant{hash: {
			KeyHash: hash, Name: "alpha", Status: domain.TenantStatusActive,
			AllowedModels: []string{"gpt-4o"},
			RateLimit:     domain.RateLimit{RPS: 1000, Burst: 1000, MaxConcurrent: 100},
		}},
		[]string{hash},
		domain.WithTokenSaver(ts),
	)
	return RouterDeps{
		Snapshots:   fakeProvider{snap},
		TenantStore: auth.NewStore(fakeProvider{snap}),
		Limiter:     limits.New(),
		Adapter:     &fakeAdapter{body: `{"ok":true}`},
		Usage:       usage.NewCounters(),
		Logger:      discardLogger(),
		Traces:      trace.New(trace.Config{}),
	}
}

// stageAt returns the index of the named stage in a trace, or -1.
func stageAt(stages []trace.Stage, name string) int {
	for i, st := range stages {
		if st.Name == name {
			return i
		}
	}
	return -1
}

// TestForwardRecordsTokenSaverStage pins the server side of the visualizer's
// Token Saver node: the stage is emitted only when a pre-forward pass really
// rewrote the chat body, its detail names the passes that ran, and it lands
// between the credential hop and the upstream attempt — exactly where the Line
// canvas draws the node.
func TestForwardRecordsTokenSaverStage(t *testing.T) {
	chatBody := func(toolOutput string) string {
		return `{"model":"gpt-4o","stream":false,"messages":[` +
			`{"role":"user","content":"run my script"},` +
			`{"role":"tool","content":"` + toolOutput + `"}]}`
	}

	compressed := domain.DefaultTokenSaverConfig()
	compressed.Enabled = true

	cases := []struct {
		name       string
		ts         domain.TokenSaverConfig
		body       string
		wantPrefix string
	}{
		{
			name:       "operator system prompt guard alone",
			ts:         domain.TokenSaverConfig{SystemPrompt: "Answer without promotional messages."},
			body:       chatBody("ok"),
			wantPrefix: "guard",
		},
		{
			name:       "tool output compression",
			ts:         compressed,
			body:       chatBody(`\u001b[31mError:\u001b[0m boom`),
			wantPrefix: "compress",
		},
		{
			name: "pass through when every pass is off",
			ts:   domain.TokenSaverConfig{},
			body: chatBody("ok"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := tokenSaverTraceDeps(tc.ts)
			_, unsub := deps.Traces.Subscribe(8)
			defer unsub()
			s := newTestServer(t, deps)

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+testKey)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			traces := deps.Traces.Snapshot()
			require.Len(t, traces, 1)
			stages := traces[0].Stages
			at := stageAt(stages, trace.StageTokenSaver)

			if tc.wantPrefix == "" {
				require.Equal(t, -1, at, "a pass-through must not report a rewrite: %+v", stages)
				return
			}
			require.NotEqual(t, -1, at, "missing %s stage in %+v", trace.StageTokenSaver, stages)
			require.True(t, strings.HasPrefix(stages[at].Detail, tc.wantPrefix),
				"detail = %q, want prefix %q", stages[at].Detail, tc.wantPrefix)

			key, attempt := stageAt(stages, trace.StageKey), stageAt(stages, trace.StageAttempt)
			require.NotEqual(t, -1, key)
			require.NotEqual(t, -1, attempt)
			require.True(t, key < at && at < attempt,
				"the rewrite must sit between the credential and the attempt: key=%d ts=%d attempt=%d", key, at, attempt)
		})
	}
}
