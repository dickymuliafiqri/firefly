package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/dickymuliafiqri/firefly/internal/transport/httpx"
)

// handleGetUpdateStatus reports the running build, the newest release, and
// whether this host can install it.
//
// It is admin-only: it discloses the deployment's executable path and whether
// the process runs in a container, which is infrastructure reconnaissance.
func (deps RouterDeps) handleGetUpdateStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "Unauthorized: admin session or valid bearer token required",
		})
		return
	}
	if httpx.IsNil(deps.SelfUpdate) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "Self-update is not initialized",
		})
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(deps.SelfUpdate.Status(r.Context()))
}

// handleApplyUpdate installs the newest release.
//
// The response is 202: the download outlives this request, and the operator
// follows it through GET /api/update/status. On success the process replaces
// itself a few seconds later, so a client that sees 202 should expect the
// connection to drop and should re-poll for the new version.
func (deps RouterDeps) handleApplyUpdate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "Unauthorized: admin session or valid bearer token required",
		})
		return
	}
	if httpx.IsNil(deps.SelfUpdate) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "Self-update is not initialized",
		})
		return
	}

	if err := deps.SelfUpdate.Apply(r.Context()); err != nil {
		switch {
		case errors.Is(err, ErrUpdateInProgress):
			w.WriteHeader(http.StatusConflict)
		default:
			// A refused install is an operator mistake (container, unwritable
			// directory, nothing newer), not a server fault.
			w.WriteHeader(http.StatusBadRequest)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"accepted": true,
		"install":  deps.SelfUpdate.installDTO(),
	})
}
