package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/storage/turso"
)

// Provider & credential-pool administration endpoints (dashboard).
//
//	GET    /api/providers              every provider, active or not
//	POST   /api/providers              create one
//	GET    /api/providers/{id}         read one
//	PUT    /api/providers/{id}         update (name not editable)
//	DELETE /api/providers/{id}         hard delete provider + its keys
//	GET    /api/providers/{id}/keys    list keys, any state, masked hints
//	POST   /api/providers/{id}/keys    batch key upsert
//	PATCH  /api/keys/{id}              rotate secret / status / is_active / expires_at
//	DELETE /api/keys/{id}              hard delete one key
//
// These are the operator side of the harvester ownership shift: before them,
// providers/api_keys had no writer except the harvester and the data-plane key
// policy. Both tables live only in Turso storage, so every handler needs a
// configured store and fails closed (503) without one.
//
// Security notes:
//   - Every handler is gated by authorizeAdmin (dashboard session or admin token).
//   - Responses carry ids, counters, and a masked key hint only. The raw secret
//     never leaves the store, and account_metadata — a vault holding private
//     keys, mnemonics, and OAuth tokens — is write-only here: it is accepted on
//     an upsert and never read back.
//   - Errors name fields, ids, and limits; never credential contents. Unknown
//     store failures are reported generically with the detail left server-side.
//   - Request bodies are bounded by the gateway-wide limit; oversized bodies get
//     a 413, so "shrink the payload" stays distinguishable from "fix the JSON".

// adminKeyView is the response shape for key reads: the stored row plus a masked
// hint. ProviderKeyRecord.Secret is json:"-", so the raw credential cannot be
// serialized even by accident.
type adminKeyView struct {
	turso.ProviderKeyRecord
	APIKeyHint string `json:"api_key_hint"`
}

func adminKeyViewFrom(rec turso.ProviderKeyRecord) adminKeyView {
	return adminKeyView{ProviderKeyRecord: rec, APIKeyHint: maskSecret(rec.Secret)}
}

func adminKeyViewsFrom(recs []turso.ProviderKeyRecord) []adminKeyView {
	views := make([]adminKeyView, 0, len(recs))
	for _, rec := range recs {
		views = append(views, adminKeyViewFrom(rec))
	}
	return views
}

// providersAdminStore authorizes the request and resolves the Turso store the
// provider tables live in, writing the failure response itself: 401 without an
// admin identity, 503 when the store is unconfigured (fail closed).
func (deps RouterDeps) providersAdminStore(w http.ResponseWriter, r *http.Request) (*turso.Store, bool) {
	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized: valid dashboard session or admin token required")
		return nil, false
	}

	store, err := deps.getTursoStore(r.Context())
	if err != nil {
		deps.log().Error("providers admin: turso store unavailable", "err", err)
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI, "credential store is unavailable")
		return nil, false
	}
	if store == nil {
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI,
			"credential store is not configured; provider administration requires turso storage")
		return nil, false
	}
	return store, true
}

// reloadAfterMutation publishes a committed write to the routing snapshot and
// reports whether it landed. A false value is not an error: the write bumped the
// catalog revision, so the periodic sync publishes it on its next tick.
func (deps RouterDeps) reloadAfterMutation(ctx context.Context, op string) bool {
	if err := deps.TursoManager.TriggerSync(ctx); err != nil {
		deps.log().Warn("providers admin: committed but immediate catalog reload failed",
			"op", op, "err", err)
		return false
	}
	return true
}

// readAdminJSON decodes a request body bounded by the gateway-wide limit into T,
// writing 413 for oversized bodies and 400 for malformed JSON or unreadable
// bodies. what names the payload in failure messages ("provider", "key patch").
func readAdminJSON[T any](w http.ResponseWriter, r *http.Request, what string) (T, bool) {
	var v T

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	defer r.Body.Close()

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			openai.WriteError(w, http.StatusRequestEntityTooLarge, openai.TypeInvalidRequest, "request body too large")
			return v, false
		}
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "read request body: "+err.Error())
		return v, false
	}

	if err := json.Unmarshal(bodyBytes, &v); err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, describeJSONError(err, what))
		return v, false
	}
	return v, true
}

// describeJSONError renders a decode failure without repeating any of the body.
// Go's syntax error quotes the offending byte, and the bodies on these surfaces
// carry credentials, so the position is reported instead of the character.
func describeJSONError(err error, what string) string {
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return fmt.Sprintf("parse JSON %s: malformed JSON at byte offset %d", what, syntaxErr.Offset)
	}
	return "parse JSON " + what + ": " + err.Error()
}

// pathID parses a positive int64 path segment, writing 400 on anything else.
func pathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, name+" must be a positive integer")
		return 0, false
	}
	return id, true
}

// writeProvidersAdminError maps store failures onto client statuses: payload
// problems are 400, conflicts 409, missing rows 404, and anything else 500 with
// the database detail left server-side.
func (deps RouterDeps) writeProvidersAdminError(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, turso.ErrInvalidPayload), errors.Is(err, turso.ErrInvalidSyncPayload):
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, err.Error())
	case errors.Is(err, turso.ErrNotFound):
		openai.WriteError(w, http.StatusNotFound, openai.TypeNotFound, err.Error())
	case errors.Is(err, turso.ErrProviderExists), errors.Is(err, turso.ErrProviderInUse),
		errors.Is(err, turso.ErrKeyProviderMismatch), errors.Is(err, turso.ErrAPIKeyTaken):
		openai.WriteError(w, http.StatusConflict, openai.TypePermission, err.Error())
	default:
		deps.log().Error("providers admin request failed", "op", op, "err", err)
		openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, "operation failed; no changes were applied")
	}
}

// handleListProvidersAdmin returns every provider, active or not.
func (deps RouterDeps) handleListProvidersAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	store, ok := deps.providersAdminStore(w, r)
	if !ok {
		return
	}

	providers, err := store.ListProviderRecords(r.Context())
	if err != nil {
		deps.writeProvidersAdminError(w, "list providers", err)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"count":     len(providers),
		"providers": providers,
	})
}

// handleCreateProviderAdmin creates one provider.
func (deps RouterDeps) handleCreateProviderAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	store, ok := deps.providersAdminStore(w, r)
	if !ok {
		return
	}

	in, ok := readAdminJSON[turso.ProviderUpdate](w, r, "provider")
	if !ok {
		return
	}

	rec, err := store.CreateProviderRecord(r.Context(), in)
	if err != nil {
		deps.writeProvidersAdminError(w, "create provider", err)
		return
	}

	deps.log().Info("provider created", "provider_id", rec.ID, "name", rec.Name)
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"provider": rec,
		"reloaded": deps.reloadAfterMutation(r.Context(), "create provider"),
	})
}

// handleGetProviderAdmin returns one provider by id.
func (deps RouterDeps) handleGetProviderAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	store, ok := deps.providersAdminStore(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}

	rec, err := store.GetProviderRecord(r.Context(), id)
	if err != nil {
		deps.writeProvidersAdminError(w, "get provider", err)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"provider": rec})
}

// handleUpdateProviderAdmin applies operator edits to one provider. The name is
// not editable (providers are matched by name, the harvester's natural key).
func (deps RouterDeps) handleUpdateProviderAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	store, ok := deps.providersAdminStore(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}

	in, ok := readAdminJSON[turso.ProviderUpdate](w, r, "provider")
	if !ok {
		return
	}
	if in.Name == nil && in.BaseURL == nil && in.Description == nil && in.IsActive == nil {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest,
			"empty update: provide base_url, description, or is_active")
		return
	}

	rec, err := store.UpdateProviderRecord(r.Context(), id, in)
	if err != nil {
		deps.writeProvidersAdminError(w, "update provider", err)
		return
	}

	deps.log().Info("provider updated", "provider_id", rec.ID, "active", rec.IsActive)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"provider": rec,
		"reloaded": deps.reloadAfterMutation(r.Context(), "update provider"),
	})
}

// handleDeleteProviderAdmin hard-deletes a provider and its keys. Upstreams that
// still reference the provider make this a 409: deleting would silently detach
// their credential pool.
func (deps RouterDeps) handleDeleteProviderAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	store, ok := deps.providersAdminStore(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}

	deletedKeys, err := store.DeleteProviderRecord(r.Context(), id)
	if err != nil {
		deps.writeProvidersAdminError(w, "delete provider", err)
		return
	}

	deps.log().Info("provider deleted", "provider_id", id, "deleted_keys", deletedKeys)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":       "ok",
		"id":           id,
		"deleted_keys": deletedKeys,
		"reloaded":     deps.reloadAfterMutation(r.Context(), "delete provider"),
	})
}

// handleListProviderKeysAdmin returns every key of one provider, active or not,
// each with a masked hint instead of its secret.
func (deps RouterDeps) handleListProviderKeysAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	store, ok := deps.providersAdminStore(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}

	keys, err := store.ListProviderKeyRecords(r.Context(), id)
	if err != nil {
		deps.writeProvidersAdminError(w, "list provider keys", err)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"provider_id": id,
		"count":       len(keys),
		"keys":        adminKeyViewsFrom(keys),
	})
}

// providerKeysUpsertRequest is the batch upsert payload: the keys to apply to the
// provider named in the path, and whether an existing key owned by another
// provider may be moved here.
type providerKeysUpsertRequest struct {
	Keys     []turso.ProviderKeySyncEntry `json:"keys"`
	Reassign bool                         `json:"reassign,omitempty"`
}

// handleUpsertProviderKeysAdmin applies a key batch to one provider. Bodies are
// bounded by the harvester batch limits, and account_metadata is written through
// to the vault column without ever being echoed.
func (deps RouterDeps) handleUpsertProviderKeysAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	store, ok := deps.providersAdminStore(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}

	req, ok := readAdminJSON[providerKeysUpsertRequest](w, r, "key batch")
	if !ok {
		return
	}
	if len(req.Keys) > maxHarvesterSyncKeys {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest,
			"too many keys in one batch: limit is "+strconv.Itoa(maxHarvesterSyncKeys))
		return
	}
	for i := range req.Keys {
		// Callers of this endpoint only ever see api_key_hint, so a value in mask
		// shape means a dashboard echoed its own display string back. Accepting it
		// would store a placeholder as a routable credential.
		if isMasked(req.Keys[i].APIKey) {
			openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest,
				"keys["+strconv.Itoa(i)+"].api_key is a masked hint, not a secret")
			return
		}
		if len(req.Keys[i].AccountMetadata) > maxHarvesterMetadataBytes {
			openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest,
				"account_metadata exceeds the "+strconv.Itoa(maxHarvesterMetadataBytes>>10)+"KiB limit for one key")
			return
		}
	}

	res, err := store.UpsertProviderKeyRecords(r.Context(), id, req.Keys, req.Reassign)
	if err != nil {
		if errors.Is(err, turso.ErrKeyProviderMismatch) {
			openai.WriteError(w, http.StatusConflict, openai.TypePermission,
				err.Error()+`; resend with "reassign": true to move it to this provider`)
			return
		}
		deps.writeProvidersAdminError(w, "upsert provider keys", err)
		return
	}

	deps.log().Info("provider keys upserted",
		"provider_id", id,
		"created", res.Created,
		"updated", res.Updated,
		"unchanged", res.Unchanged,
		"reassigned", res.Reassigned,
		"revision", res.Revision,
		"pushed", res.Pushed,
	)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"created":    res.Created,
		"updated":    res.Updated,
		"unchanged":  res.Unchanged,
		"reassigned": res.Reassigned,
		"key_ids":    res.KeyIDs,
		"revision":   res.Revision,
		"pushed":     res.Pushed,
		"reloaded":   deps.reloadAfterMutation(r.Context(), "upsert provider keys"),
	})
}

// keyPatchRequest is the partial update payload. expires_at is a raw message so
// an explicit null (clear the expiry) is distinguishable from an omitted field.
// api_key rotates the credential in place; the key row id never changes, so the
// "<upstream>-key-<id>" refs survive.
type keyPatchRequest struct {
	APIKey    string          `json:"api_key"`
	Status    *string         `json:"status"`
	IsActive  *bool           `json:"is_active"`
	ExpiresAt json.RawMessage `json:"expires_at"`
}

// handlePatchKeyAdmin rotates one key's secret and/or updates its status,
// is_active and expires_at.
func (deps RouterDeps) handlePatchKeyAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	store, ok := deps.providersAdminStore(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}

	req, ok := readAdminJSON[keyPatchRequest](w, r, "key patch")
	if !ok {
		return
	}

	// A rotation target in mask shape means the dashboard sent back the api_key_hint
	// it was shown, which would replace a working credential with its own display
	// string. A real secret that merely contains "..." is indistinguishable here, so
	// the message names what to resend instead of guessing at the value.
	if req.APIKey != "" && isMasked(req.APIKey) {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest,
			"api_key looks like a masked hint, not a secret")
		return
	}

	patch := turso.ProviderKeyPatch{APIKey: req.APIKey, Status: req.Status, IsActive: req.IsActive}
	if len(req.ExpiresAt) > 0 {
		if bytes.Equal(bytes.TrimSpace(req.ExpiresAt), []byte("null")) {
			patch.ClearExpiry = true
		} else {
			var ts int64
			if err := json.Unmarshal(req.ExpiresAt, &ts); err != nil {
				openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest,
					"expires_at must be a unix millisecond timestamp or null")
				return
			}
			// Zero and negative values go to the store's validator instead of being
			// read as "clear": only the explicit null clears, so a client that sends a
			// number is told its number is wrong rather than silently losing the expiry
			// another writer set.
			patch.ExpiresAt = &ts
		}
	}
	if patch.APIKey == "" && patch.Status == nil && patch.IsActive == nil && !patch.ClearExpiry && patch.ExpiresAt == nil {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest,
			"empty patch: provide api_key, status, is_active, or expires_at")
		return
	}

	res, err := store.PatchProviderKeyRecord(r.Context(), id, patch)
	if err != nil {
		deps.writeProvidersAdminError(w, "patch key", err)
		return
	}

	deps.log().Info("provider key patched",
		"key_id", res.Key.ID,
		"status", res.Key.Status,
		"active", res.Key.IsActive,
		"revision", res.Revision,
		"pushed", res.Pushed,
	)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"key":      adminKeyViewFrom(res.Key),
		"revision": res.Revision,
		"pushed":   res.Pushed,
		"reloaded": deps.reloadAfterMutation(r.Context(), "patch key"),
	})
}

// handleDeleteKeyAdmin hard-deletes one key and the credentials bound to it. This
// is the operator-only right (I7): the harvester can only upsert and deactivate.
func (deps RouterDeps) handleDeleteKeyAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	store, ok := deps.providersAdminStore(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}

	revision, pushed, err := store.DeleteProviderKeyRecord(r.Context(), id)
	if err != nil {
		deps.writeProvidersAdminError(w, "delete key", err)
		return
	}

	deps.log().Info("provider key deleted", "key_id", id, "revision", revision, "pushed", pushed)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":   "ok",
		"id":       id,
		"revision": revision,
		"pushed":   pushed,
		"reloaded": deps.reloadAfterMutation(r.Context(), "delete key"),
	})
}
