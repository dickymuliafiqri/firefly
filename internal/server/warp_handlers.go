package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/transport/warp"
)

// maxWarpPoolBodyBytes bounds the request body of the pool-size endpoint. The
// payload is a single integer, so anything larger is garbage by definition.
const (
	maxWarpPoolBodyBytes = 1 << 12
	corsWildcard         = "*"
)

// handleGetWarpStatus retrieves real-time health and connection details for Cloudflare WARP.
func (deps RouterDeps) handleGetWarpStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", corsWildcard)
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

// handleSetWarpPoolSize updates the steady-state size of the WARP egress pool.
// The new size is applied to the running engine and persisted so a restart
// resumes it, but nothing is dialled here: rotation grows the pool one slot per
// tick, so a larger pool fills gradually instead of registering a burst of
// sessions that would trip Cloudflare 429s.
func (deps RouterDeps) handleSetWarpPoolSize(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", corsWildcard)
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "Unauthorized: admin session or valid bearer token required",
		})
		return
	}

	var req struct {
		PoolSize int `json:"pool_size"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxWarpPoolBodyBytes)).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "invalid JSON body: " + err.Error(),
		})
		return
	}

	if !warp.ValidPoolSize(req.PoolSize) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": fmt.Sprintf("pool_size must be between %d and %d", warp.MinPoolSize, warp.MaxPoolSize),
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

	// Persist first: if the write fails the operator must not be told the pool
	// was saved. The in-memory value is applied only after warp.json is durable,
	// so a failed save leaves the running engine on its previous size rather than
	// on a size that would silently revert at the next restart.
	if err := config.SaveWarp(deps.ConfigDir, config.WarpDTO{PoolSize: req.PoolSize}); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "could not persist the WARP pool size: " + err.Error(),
		})
		return
	}

	deps.WarpManager.SetPoolSize(req.PoolSize)

	status := deps.WarpManager.Status()
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(status)
}

// handleRotateWarp triggers an immediate on-demand IP rotation for the WARP tunnel.
func (deps RouterDeps) handleRotateWarp(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", corsWildcard)
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

// handleCheckWarp triggers an on-demand health probe across all sessions in the WARP pool.
func (deps RouterDeps) handleCheckWarp(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", corsWildcard)
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

	deps.WarpManager.CheckHealth(r.Context())
	status := deps.WarpManager.Status()
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(status)
}
