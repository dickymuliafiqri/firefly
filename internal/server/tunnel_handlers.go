package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/dickymuliafiqri/firefly/internal/transport/tunnel"
)

// handleGetTunnelStatus retrieves real-time status and public URL for the Cloudflare Tunnel ingress engine.
func (deps RouterDeps) handleGetTunnelStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if deps.TunnelManager == nil {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(tunnel.Status{
			Enabled: false,
			Running: false,
			Mode:    "disabled",
		})
		return
	}

	status := deps.TunnelManager.Status()
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(status)
}

type toggleTunnelRequest struct {
	Enabled *bool  `json:"enabled,omitempty"`
	Mode    string `json:"mode,omitempty"`
	Token   string `json:"token,omitempty"`
}

// handleToggleTunnel dynamically starts or stops the Cloudflare Tunnel ingress process.
func (deps RouterDeps) handleToggleTunnel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "Unauthorized: admin session or valid bearer token required",
		})
		return
	}

	if deps.TunnelManager == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "Tunnel manager is not initialized",
		})
		return
	}

	var req toggleTunnelRequest
	if r.Body != nil && r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "Invalid JSON payload: " + err.Error(),
			})
			return
		}
	}

	currentStatus := deps.TunnelManager.Status()
	targetEnabled := !currentStatus.Running
	if req.Enabled != nil {
		targetEnabled = *req.Enabled
	}

	if !targetEnabled {
		if err := deps.TunnelManager.Stop(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "Failed to stop tunnel: " + err.Error(),
			})
			return
		}
	} else {
		mode := tunnel.ModeQuick
		if req.Mode != "" {
			mode = tunnel.ParseMode(req.Mode)
			if mode == tunnel.ModeDisabled {
				mode = tunnel.ModeQuick
			}
		} else if currentStatus.Mode == "named" {
			mode = tunnel.ModeNamed
		}

		if err := deps.TunnelManager.Start(mode, req.Token); err != nil {
			statusCode := http.StatusInternalServerError
			if strings.Contains(err.Error(), "token is required") {
				statusCode = http.StatusBadRequest
			}
			w.WriteHeader(statusCode)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": err.Error(),
			})
			return
		}
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(deps.TunnelManager.Status())
}
