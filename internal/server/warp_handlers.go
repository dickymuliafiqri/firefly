package server

import (
	"encoding/json"
	"net/http"

	"github.com/dickymuliafiqri/firefly/internal/warp"
)

// handleGetWarpStatus retrieves real-time health and connection details for Cloudflare WARP.
func (deps RouterDeps) handleGetWarpStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if deps.WarpManager == nil {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(warp.Status{
			Enabled: false,
			Error:   "WARP engine is not initialized",
		})
		return
	}

	status := deps.WarpManager.Status()
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(status)
}

// handleRotateWarp triggers an immediate on-demand IP rotation for the WARP tunnel.
func (deps RouterDeps) handleRotateWarp(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "Unauthorized: admin session or valid bearer token required",
		})
		return
	}

	if deps.WarpManager == nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "WARP engine is not initialized",
		})
		return
	}

	_, err := deps.WarpManager.Rotate(r.Context())
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": err.Error(),
		})
		return
	}

	status := deps.WarpManager.Status()
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(status)
}
