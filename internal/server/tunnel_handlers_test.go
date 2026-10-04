package server

import (
	"bytes"

	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/transport/tunnel"
)

func TestHandleGetTunnelStatus_NilManager(t *testing.T) {
	t.Parallel()

	deps := RouterDeps{
		TunnelManager: nil,
	}

	req := httptest.NewRequest(http.MethodGet, "/api/tunnel/status", nil)
	rr := httptest.NewRecorder()

	deps.handleGetTunnelStatus(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var st tunnel.Status
	if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil {
		t.Fatalf("unmarshal status error: %v", err)
	}

	if st.Enabled {
		t.Errorf("expected Enabled = false when TunnelManager is nil")
	}
	if st.Mode != "disabled" {
		t.Errorf("expected Mode = disabled, got %q", st.Mode)
	}
}

func TestHandleGetTunnelStatus_ActiveManager(t *testing.T) {
	t.Parallel()

	mgr := tunnel.NewManager(tunnel.Config{
		Mode:     tunnel.ModeQuick,
		LocalURL: "http://127.0.0.1:8080",
	})
	deps := RouterDeps{
		TunnelManager: mgr,
	}

	req := httptest.NewRequest(http.MethodGet, "/api/tunnel/status", nil)
	rr := httptest.NewRecorder()

	deps.handleGetTunnelStatus(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var st tunnel.Status
	if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil {
		t.Fatalf("unmarshal status error: %v", err)
	}

	if !st.Enabled {
		t.Errorf("expected Enabled = true")
	}
	if st.Mode != "quick" {
		t.Errorf("expected Mode = quick, got %q", st.Mode)
	}
}
func TestHandleToggleTunnel_Unauthorized(t *testing.T) {
	t.Parallel()

	deps := RouterDeps{
		AdminToken: "secret-token",
	}

	req := httptest.NewRequest(http.MethodPost, "/api/tunnel/toggle", nil)
	rr := httptest.NewRecorder()

	deps.handleToggleTunnel(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rr.Code)
	}
}

func TestHandleToggleTunnel_Stop(t *testing.T) {
	t.Parallel()

	mgr := tunnel.NewManager(tunnel.Config{
		Mode:     tunnel.ModeQuick,
		LocalURL: "http://127.0.0.1:8080",
	})
	deps := RouterDeps{
		AdminToken:    "secret-token",
		TunnelManager: mgr,
	}

	body := []byte(`{"enabled":false}`)
	req := httptest.NewRequest(http.MethodPost, "/api/tunnel/toggle", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret-token")
	rr := httptest.NewRecorder()

	deps.handleToggleTunnel(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var st tunnel.Status
	if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil {
		t.Fatalf("unmarshal status error: %v", err)
	}

	if st.Running {
		t.Errorf("expected Running = false after stop")
	}
}
func TestHandleToggleTunnel_TokenRequired(t *testing.T) {
	t.Parallel()

	mgr := tunnel.NewManager(tunnel.Config{
		Mode:     tunnel.ModeDisabled,
		LocalURL: "http://127.0.0.1:8080",
	})
	deps := RouterDeps{
		AdminToken:    "secret-token",
		TunnelManager: mgr,
	}

	body := []byte(`{"enabled":true,"mode":"named","token":""}`)
	req := httptest.NewRequest(http.MethodPost, "/api/tunnel/toggle", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret-token")
	rr := httptest.NewRecorder()

	deps.handleToggleTunnel(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400 for missing token, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleToggleTunnel_MissingBinary(t *testing.T) {
	t.Parallel()

	mgr := tunnel.NewManager(tunnel.Config{
		Mode:     tunnel.ModeDisabled,
		LocalURL: "http://127.0.0.1:8080",
	})
	deps := RouterDeps{
		AdminToken:    "secret-token",
		TunnelManager: mgr,
	}

	body := []byte(`{"enabled":true,"mode":"quick"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/tunnel/toggle", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret-token")
	rr := httptest.NewRecorder()

	deps.handleToggleTunnel(rr, req)

	// If cloudflared is not installed on the system, should return 500 Internal Server Error (server dependency failure)
	// If cloudflared is installed, it would start (200 OK).
	if rr.Code != http.StatusInternalServerError && rr.Code != http.StatusOK {
		t.Fatalf("expected status 500 or 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestPersistTunnelConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	deps := RouterDeps{ConfigDir: dir}

	// Enabling a named tunnel persists the mode and token.
	deps.persistTunnelConfig(tunnel.ModeNamed, "tok-123")
	cfg, err := config.LoadTunnel(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Mode != "named" || cfg.Token != "tok-123" {
		t.Fatalf("after named enable: %+v", cfg)
	}

	// Disabling clears the mode but retains the token so a later enable does
	// not force the operator to re-enter it.
	deps.persistTunnelConfig(tunnel.ModeDisabled, "")
	cfg, _ = config.LoadTunnel(dir)
	if cfg.Mode != "" {
		t.Fatalf("after disable: mode = %q, want empty", cfg.Mode)
	}
	if cfg.Token != "tok-123" {
		t.Fatalf("after disable: token = %q, want retained", cfg.Token)
	}

	// Switching to quick keeps the saved token but records quick mode.
	deps.persistTunnelConfig(tunnel.ModeQuick, "")
	cfg, _ = config.LoadTunnel(dir)
	if cfg.Mode != "quick" {
		t.Fatalf("after quick: mode = %q, want quick", cfg.Mode)
	}
	if cfg.Token != "tok-123" {
		t.Fatalf("after quick: token = %q, want retained", cfg.Token)
	}
}

func TestHandleToggleTunnel_PersistsDisabledConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Seed a persisted named config so we can assert the token survives.
	if err := config.SaveTunnel(dir, config.TunnelDTO{Mode: "named", Token: "tok-abc"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	mgr := tunnel.NewManager(tunnel.Config{
		Mode:     tunnel.ModeQuick,
		LocalURL: "http://127.0.0.1:8080",
	})
	deps := RouterDeps{
		AdminToken:    "secret-token",
		TunnelManager: mgr,
		ConfigDir:     dir,
	}

	body := []byte(`{"enabled":false}`)
	req := httptest.NewRequest(http.MethodPost, "/api/tunnel/toggle", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret-token")
	rr := httptest.NewRecorder()

	deps.handleToggleTunnel(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	cfg, _ := config.LoadTunnel(dir)
	if cfg.Mode != "" {
		t.Fatalf("persisted mode = %q, want empty after disable", cfg.Mode)
	}
	if cfg.Token != "tok-abc" {
		t.Fatalf("persisted token = %q, want retained", cfg.Token)
	}
}
