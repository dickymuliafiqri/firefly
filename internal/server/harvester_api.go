package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/storage/turso"
)

// Harvester credential-pool sync endpoint (POST /api/harvester/sync).
//
// The external harvester ships a snapshot of providers and their API keys; this
// handler validates the envelope, applies it through turso.Store.SyncProviderKeys
// (one transaction, stable ids, revision bump) and forces an immediate catalog
// reload so the new keys are routable before the response returns.
//
// Security notes:
//   - Gated by deps.authorizeService (dedicated service token, fails closed with
//     503 when unconfigured). It is deliberately NOT reachable with a dashboard
//     session or the admin token: the two identities have separate blast radii.
//   - Request bodies carry live credentials, so no payload value is ever logged
//     or echoed. Validation errors name fields and limits, never contents.
//   - Responses return ids and counters only. account_metadata is written to the
//     vault column and never read back.
//   - There is no CORS/OPTIONS route: this is a server-to-server surface, not a
//     browser one.

const (
	// Bounds on a single batch. The harvester chunks its snapshot to fit; the
	// 32 MB global body cap is the backstop for oversized payloads.
	maxHarvesterSyncProviders     = 500
	maxHarvesterSyncKeys          = 10000
	maxHarvesterSyncDeactivations = 20000
	// A single vault blob is capped well above the largest observed value so a
	// malformed client cannot write a multi-megabyte row. The store enforces the
	// same limit for the operator surface, so it is declared once there.
	maxHarvesterMetadataBytes = turso.MaxKeyMetadataBytes
)

// log returns a non-nil logger; RouterDeps literals in tests omit it.
func (deps RouterDeps) log() *slog.Logger {
	if deps.Logger != nil {
		return deps.Logger
	}
	return slog.Default()
}

// harvesterSyncResponse is the sync result plus the visibility signal: whether
// the catalog was rebuilt before this response was written. A false value is not
// an error (the write is committed and the periodic syncer picks it up), but it
// tells the harvester its keys are not routable yet.
type harvesterSyncResponse struct {
	turso.ProviderSyncResult
	Reloaded bool `json:"reloaded"`
}

func (deps RouterDeps) handleHarvesterSync(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeService(w, r) {
		return
	}

	store, err := deps.getTursoStore(r.Context())
	if err != nil {
		deps.log().Error("harvester sync: turso store unavailable", "err", err)
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI, "credential store is unavailable")
		return
	}
	if store == nil {
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI,
			"credential store is not configured; harvester sync requires turso storage")
		return
	}

	req, ok := readHarvesterSyncBody(w, r)
	if !ok {
		return
	}

	res, err := store.SyncProviderKeys(r.Context(), req)
	if err != nil {
		writeHarvesterSyncError(w, deps, err)
		return
	}

	reloaded := true
	if err := deps.TursoManager.TriggerSync(r.Context()); err != nil {
		// The batch is committed and the catalog revision is bumped, so the next
		// periodic sync publishes it. Report it rather than failing the request.
		reloaded = false
		deps.log().Warn("harvester sync: committed but immediate catalog reload failed",
			"err", err, "revision", res.Revision)
	}

	deps.log().Info("harvester credential pool synced",
		"created", res.Created,
		"updated", res.Updated,
		"unchanged", res.Unchanged,
		"deactivated", res.Deactivated,
		"revision", res.Revision,
		"pushed", res.Pushed,
		"reloaded", reloaded,
	)

	if err := json.NewEncoder(w).Encode(harvesterSyncResponse{ProviderSyncResult: res, Reloaded: reloaded}); err != nil {
		deps.log().Warn("harvester sync: write response failed", "err", err)
	}
}

// readHarvesterSyncBody decodes and validates the batch envelope. It writes the
// failure response and returns ok=false on any problem.
func readHarvesterSyncBody(w http.ResponseWriter, r *http.Request) (turso.ProviderSyncRequest, bool) {
	var req turso.ProviderSyncRequest

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	defer r.Body.Close()

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			openai.WriteError(w, http.StatusRequestEntityTooLarge, openai.TypeInvalidRequest, "request body too large")
			return req, false
		}
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "read request body: "+err.Error())
		return req, false
	}

	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, describeJSONError(err, "sync payload"))
		return req, false
	}

	switch {
	case len(req.Providers) > maxHarvesterSyncProviders:
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest,
			fmt.Sprintf("too many providers in one batch: limit is %d", maxHarvesterSyncProviders))
		return req, false
	case len(req.Keys) > maxHarvesterSyncKeys:
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest,
			fmt.Sprintf("too many keys in one batch: limit is %d", maxHarvesterSyncKeys))
		return req, false
	case len(req.DeactivateKeys) > maxHarvesterSyncDeactivations:
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest,
			fmt.Sprintf("too many deactivation ids in one batch: limit is %d", maxHarvesterSyncDeactivations))
		return req, false
	}

	// An empty batch is almost always a serialization bug on the client (fields
	// dropped rather than populated), and applying it would churn the catalog
	// revision for nothing. Reject it instead of reporting a hollow success.
	if len(req.Providers) == 0 && len(req.Keys) == 0 && len(req.DeactivateKeys) == 0 {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest,
			"sync payload is empty: expected providers, keys, or deactivate_keys")
		return req, false
	}

	for i := range req.Keys {
		if len(req.Keys[i].AccountMetadata) > maxHarvesterMetadataBytes {
			openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest,
				fmt.Sprintf("account_metadata exceeds the %dKiB limit for one key", maxHarvesterMetadataBytes>>10))
			return req, false
		}
	}

	return req, true
}

// writeHarvesterSyncError maps store failures onto client-visible status codes.
// Only the sentinel errors carry a message the client can act on; anything else
// is reported generically so database and driver detail stays server-side.
func writeHarvesterSyncError(w http.ResponseWriter, deps RouterDeps, err error) {
	switch {
	case errors.Is(err, turso.ErrInvalidSyncPayload), errors.Is(err, turso.ErrProviderUnknown):
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, err.Error())
	case errors.Is(err, turso.ErrKeyProviderMismatch):
		openai.WriteError(w, http.StatusConflict, openai.TypePermission, err.Error())
	default:
		deps.log().Error("harvester sync failed", "err", err)
		openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI,
			"credential pool sync failed; nothing was applied")
	}
}
