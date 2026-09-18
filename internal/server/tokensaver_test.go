package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/security/auth"
	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/registry"
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
}
