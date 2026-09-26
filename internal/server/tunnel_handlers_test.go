package server

import (
	"bytes"

	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
