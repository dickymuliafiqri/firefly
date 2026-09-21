package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/idgen"
	"github.com/dickymuliafiqri/firefly/internal/security/auth"
	"github.com/dickymuliafiqri/firefly/internal/transport/httpx"
)

// Tenant administration endpoints (GET/POST/PUT/DELETE /api/tenants[...]).
//
// These handlers expose CRUD over the tenant catalog to dashboard admins.
// They reuse the exact persist-and-hot-swap pipeline as handleUpdateSettings
// (validate -> Turso -> config files -> atomic snapshot swap), so a tenant
// created, edited, or deleted here behaves identically to one managed
// through the bulk settings endpoint — in both file and Turso storage modes.
//
// Security notes:
//   - Every handler is gated by authorizeAdmin (dashboard session or admin token).
//   - Tenant API keys are returned in plaintext, like GET /api/settings for an
//     authenticated admin: they are working credentials the operator must be
//     able to copy. Unauthenticated callers never reach this code, and errors
//     never echo key material.
//   - Request bodies are bounded to the gateway-wide limit; oversized bodies
//     get a 413 so "shrink the payload" is distinguishable from "fix the JSON".

// tenantDTOsFromSnapshot converts the live tenant set to DTOs. Keys are
// plaintext where the snapshot holds them; hash-only tenants fall back to
// their key_hash so the entry remains addressable.
func tenantDTOsFromSnapshot(snap *domain.CatalogSnapshot) []config.TenantDTO {
	tenants := make([]config.TenantDTO, 0, len(snap.TenantKeys()))
	for _, key := range snap.TenantKeys() {
		t, ok := snap.TenantByKey(key)
		if !ok || t == nil {
			continue
		}
		tenants = append(tenants, tenantDTOFromDomain(t))
	}
	return tenants
}

// tenantDTOFromDomain renders one live tenant as its admin DTO.
func tenantDTOFromDomain(t *domain.Tenant) config.TenantDTO {
	rps := t.RateLimit.RPS
	burst := t.RateLimit.Burst
	maxC := t.RateLimit.MaxConcurrent
	var expPtr *int64
	if t.ExpiresAt > 0 {
		v := t.ExpiresAt
		expPtr = &v
	}
	var used int64
	if t.UsedTokens != nil {
		used = t.UsedTokens.Load()
	}
	apiKey := t.APIKey
	if apiKey == "" {
		apiKey = t.KeyHash
	}
	return config.TenantDTO{
		APIKey:        apiKey,
		KeyHash:       t.KeyHash,
		Name:          t.Name,
		Status:        string(t.Status),
		MaxTokens:     t.MaxTokens,
		UsedTokens:    used,
		ExpiresAt:     expPtr,
		AllowedModels: t.AllowedModels,
		CredentialRef: t.CredentialRef,
		RateLimit: &config.RateLimitDTO{
			RPS:           &rps,
			Burst:         &burst,
			MaxConcurrent: &maxC,
		},
		Metadata: t.Metadata,
	}
}

// readTenantBody decodes a tenant payload bounded by the gateway-wide body
// limit, mapping oversized bodies to 413 and malformed JSON to 400.
func readTenantBody(w http.ResponseWriter, r *http.Request) (config.TenantDTO, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	defer r.Body.Close()

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			openai.WriteError(w, http.StatusRequestEntityTooLarge, openai.TypeInvalidRequest, "request body too large")
			return config.TenantDTO{}, false
		}
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "read request body: "+err.Error())
		return config.TenantDTO{}, false
	}

	var dto config.TenantDTO
	if err := json.Unmarshal(bodyBytes, &dto); err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "parse JSON tenant: "+err.Error())
		return config.TenantDTO{}, false
	}
	return dto, true
}

// findTenantByName locates a tenant DTO by display name in the current
// authoritative tenant list.
func findTenantByName(tenants []config.TenantDTO, name string) (config.TenantDTO, int, bool) {
	for i, t := range tenants {
		if t.Name == name {
			return t, i, true
		}
	}
	return config.TenantDTO{}, -1, false
}

// handleListTenants returns the full tenant catalog. Admin only.
func (deps RouterDeps) handleListTenants(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized: valid dashboard session or admin token required")
		return
	}

	settings, err := deps.loadSettingsForMutation(r.Context())
	if err != nil {
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI, "load configuration: "+err.Error())
		return
	}

	tenants := settings.Tenants
	if tenants == nil {
		tenants = []config.TenantDTO{}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"tenants": tenants})
}

// handleGetTenant returns one tenant by name. Admin only.
func (deps RouterDeps) handleGetTenant(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized: valid dashboard session or admin token required")
		return
	}

	settings, err := deps.loadSettingsForMutation(r.Context())
	if err != nil {
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI, "load configuration: "+err.Error())
		return
	}

	tenant, _, ok := findTenantByName(settings.Tenants, r.PathValue("name"))
	if !ok {
		openai.WriteError(w, http.StatusNotFound, openai.TypeNotFound, "tenant not found")
		return
	}
	_ = json.NewEncoder(w).Encode(tenant)
}

// handleCreateTenant appends a new tenant to the catalog. When api_key is
// omitted a fresh sk-gw- credential is generated; key_hash is derived from
// the plaintext key when absent. Admin only.
func (deps RouterDeps) handleCreateTenant(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized: valid dashboard session or admin token required")
		return
	}

	dto, ok := readTenantBody(w, r)
	if !ok {
		return
	}
	if dto.Name == "" {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "name is required")
		return
	}

	settings, err := deps.loadSettingsForMutation(r.Context())
	if err != nil {
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI, "load configuration: "+err.Error())
		return
	}

	if _, _, dup := findTenantByName(settings.Tenants, dto.Name); dup {
		openai.WriteError(w, http.StatusConflict, openai.TypeInvalidRequest, "tenant with name already exists: "+dto.Name)
		return
	}

	if dto.Status == "" {
		dto.Status = "active"
	}
	if dto.APIKey == "" && dto.KeyHash == "" {
		dto.APIKey = auth.KeyPrefix + idgen.Short(16)
	}
	if dto.APIKey != "" && dto.KeyHash == "" {
		dto.KeyHash = auth.HashKey(dto.APIKey)
	}

	settings.Tenants = append(settings.Tenants, dto)

	gen, warnings, err := deps.persistSettings(r.Context(), settings)
	if err != nil {
		deps.writePersistError(w, err)
		return
	}

	created, _, _ := findTenantByName(settings.Tenants, dto.Name)
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"tenant":     created,
		"generation": gen,
		"warnings":   warnings,
	})
}

// handleUpdateTenant replaces the mutable fields of an existing tenant. The
// credential is preserved when the body omits api_key or sends the masked
// placeholder from a previous GET; used_tokens stays under topup control.
// Admin only.
func (deps RouterDeps) handleUpdateTenant(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized: valid dashboard session or admin token required")
		return
	}

	name := r.PathValue("name")

	dto, ok := readTenantBody(w, r)
	if !ok {
		return
	}
	if dto.Name != "" && dto.Name != name {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "name in body does not match path")
		return
	}

	settings, err := deps.loadSettingsForMutation(r.Context())
	if err != nil {
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI, "load configuration: "+err.Error())
		return
	}

	existing, idx, found := findTenantByName(settings.Tenants, name)
	if !found {
		openai.WriteError(w, http.StatusNotFound, openai.TypeNotFound, "tenant not found")
		return
	}

	next := existing
	// Full replacement of the operator-managed fields; zero/absent keeps the
	// stored value so partial edits do not silently clear quota or access.
	if dto.Status != "" {
		next.Status = dto.Status
	}
	if dto.MaxTokens != 0 {
		next.MaxTokens = dto.MaxTokens
	}
	if dto.ExpiresAt != nil {
		next.ExpiresAt = dto.ExpiresAt
	}
	if len(dto.AllowedModels) > 0 {
		next.AllowedModels = dto.AllowedModels
	}
	if dto.CredentialRef != "" {
		next.CredentialRef = dto.CredentialRef
	}
	if dto.RateLimit != nil {
		next.RateLimit = dto.RateLimit
	}
	if len(dto.Metadata) > 0 {
		next.Metadata = dto.Metadata
	}
	// Credential rotation: only an explicit, unmasked new key replaces the
	// current one; masked placeholder or omission means "keep".
	if dto.APIKey != "" && !isMasked(dto.APIKey) && dto.APIKey != existing.APIKey {
		next.APIKey = dto.APIKey
		next.KeyHash = auth.HashKey(dto.APIKey)
	}
	// used_tokens is metered state owned by the traffic pipeline and the
	// topup endpoint, so it is intentionally never taken from the request.

	settings.Tenants[idx] = next

	gen, warnings, err := deps.persistSettings(r.Context(), settings)
	if err != nil {
		deps.writePersistError(w, err)
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"tenant":     next,
		"generation": gen,
		"warnings":   warnings,
	})
}

// handleDeleteTenant removes a tenant from the catalog. Admin only.
func (deps RouterDeps) handleDeleteTenant(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized: valid dashboard session or admin token required")
		return
	}

	name := r.PathValue("name")

	settings, err := deps.loadSettingsForMutation(r.Context())
	if err != nil {
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI, "load configuration: "+err.Error())
		return
	}

	_, idx, found := findTenantByName(settings.Tenants, name)
	if !found {
		openai.WriteError(w, http.StatusNotFound, openai.TypeNotFound, "tenant not found")
		return
	}

	settings.Tenants = append(settings.Tenants[:idx], settings.Tenants[idx+1:]...)

	gen, _, err := deps.persistSettings(r.Context(), settings)
	if err != nil {
		deps.writePersistError(w, err)
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":     "ok",
		"name":       name,
		"generation": gen,
	})
}

// writePersistError maps a persistSettings failure onto the proper HTTP
// status: validation problems are 400 (the caller can fix the payload) while
// storage failures are 500.
func (deps RouterDeps) writePersistError(w http.ResponseWriter, err error) {
	var valErr *config.ValidationError
	if errors.As(err, &valErr) {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, err.Error())
		return
	}
	openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, err.Error())
}

// loadSettingsForMutation returns the current authoritative settings DTO,
// preferring the Turso database, then the config files, then the live
// snapshot. Unlike handleGetSettings (which serves display data), this path
// restores masked upstream secrets in the snapshot fallback so the result
// can safely be re-persisted.
func (deps RouterDeps) loadSettingsForMutation(ctx context.Context) (config.SettingsDTO, error) {
	// 1. Turso database is the authoritative store when configured.
	if tStore, _ := deps.getTursoStore(ctx); tStore != nil {
		if settings, err := tStore.LoadSettings(ctx); err == nil && settings != nil {
			if len(settings.Upstreams) > 0 || len(settings.Models) > 0 || len(settings.Tenants) > 0 || len(settings.Combos) > 0 {
				deps.overlayLiveUsedTokens(settings)
				return *settings, nil
			}
			// If Turso is empty and no local config directory is configured,
			// Turso is the empty authoritative store.
			if deps.ConfigDir == "" {
				deps.overlayLiveUsedTokens(settings)
				return *settings, nil
			}
		}
	}

	// 2. Config directory files.
	if deps.ConfigDir != "" {
		src := config.NewFileConfigSource(deps.ConfigDir)
		if raw, err := src.Load(ctx); err == nil {
			// Mirror handleGetSettings: only trust files that validate against
			// the current environment, so a broken edit cannot be mutated on top.
			if _, valErr := config.Build(config.FileSetFromMap(raw), os.LookupEnv); valErr == nil {
				var upFile config.UpstreamsFile
				var modFile config.ModelsFile
				var tenFile config.TenantsFile
				var combFile config.CombosFile

				_ = json.Unmarshal(raw["upstreams"], &upFile)
				_ = json.Unmarshal(raw["models"], &modFile)
				_ = json.Unmarshal(raw["tenants"], &tenFile)
				_ = json.Unmarshal(raw["combos"], &combFile)

				settings := config.SettingsDTO{
					Upstreams: upFile.Upstreams,
					Models:    modFile.Models,
					Tenants:   tenFile.Tenants,
					Combos:    combFile.Combos,
				}
				if len(raw["tokensaver"]) > 0 {
					var tsDTO config.TokenSaverDTO
					if err := json.Unmarshal(raw["tokensaver"], &tsDTO); err == nil {
						settings.TokenSaver = &tsDTO
					}
				}
				deps.overlayLiveUsedTokens(&settings)
				return settings, nil
			}
		}
	}

	// 3. Live snapshot (no persistent store configured).
	snap := deps.currentSnapshot()
	if snap == nil {
		return config.SettingsDTO{}, errors.New("no configuration source available")
	}
	settings := settingsDTOFromSnapshot(snap)
	restoreMaskedSecrets(&settings, snap)

	ts := snap.TokenSaver()
	maxTool := ts.MaxToolOutputChars
	ctxThresh := ts.ContextThreshold
	settings.TokenSaver = &config.TokenSaverDTO{
		Enabled:            ts.Enabled,
		CompressToolOutput: ts.CompressToolOutput,
		TerseOutput:        ts.TerseOutput,
		MinimalCode:        ts.MinimalCode,
		CompressContext:    ts.CompressContext,
		MaxToolOutputChars: &maxTool,
		ContextThreshold:   &ctxThresh,
	}

	return settings, nil
}

// overlayLiveUsedTokens copies current in-memory used_tokens counters from
// the active snapshot into settings.Tenants so metered usage is not reset or
// overwritten by stale values read from storage.
func (deps RouterDeps) overlayLiveUsedTokens(settings *config.SettingsDTO) {
	snap := deps.currentSnapshot()
	if snap == nil || settings == nil {
		return
	}
	for i := range settings.Tenants {
		t := &settings.Tenants[i]
		key := t.APIKey
		if key == "" {
			key = t.KeyHash
		}
		if dt, ok := snap.TenantByKey(key); ok && dt != nil && dt.UsedTokens != nil {
			t.UsedTokens = dt.UsedTokens.Load()
		} else {
			for _, sk := range snap.TenantKeys() {
				if dt, ok := snap.TenantByKey(sk); ok && dt != nil && dt.Name == t.Name && dt.UsedTokens != nil {
					t.UsedTokens = dt.UsedTokens.Load()
					break
				}
			}
		}
	}
}

// persistSettings validates the full settings payload and pushes it through
// the same persist-and-hot-swap steps as handleUpdateSettings: Turso save
// when configured, config file writes when a config dir is set, then an
// atomic registry swap so the change is live without a restart. It returns
// the new snapshot generation and any validation warnings.
func (deps RouterDeps) persistSettings(ctx context.Context, settings config.SettingsDTO) (uint64, []string, error) {
	settings.ManageTenants = true
	if settings.Tenants == nil {
		settings.Tenants = []config.TenantDTO{}
	}
	if settings.Upstreams == nil {
		settings.Upstreams = []config.UpstreamDTO{}
	}
	if settings.Models == nil {
		settings.Models = []config.ModelDTO{}
	}
	if settings.Combos == nil {
		settings.Combos = []config.ComboDTO{}
	}

	upFile := config.UpstreamsFile{Upstreams: settings.Upstreams}
	modFile := config.ModelsFile{Models: settings.Models}
	tenFile := config.TenantsFile{Tenants: settings.Tenants}
	combFile := config.CombosFile{Combos: settings.Combos}

	upRaw, err := json.Marshal(upFile)
	if err != nil {
		return 0, nil, fmt.Errorf("marshal upstreams: %w", err)
	}
	modRaw, err := json.Marshal(modFile)
	if err != nil {
		return 0, nil, fmt.Errorf("marshal models: %w", err)
	}
	tenRaw, err := json.Marshal(tenFile)
	if err != nil {
		return 0, nil, fmt.Errorf("marshal tenants: %w", err)
	}
	combRaw, err := json.Marshal(combFile)
	if err != nil {
		return 0, nil, fmt.Errorf("marshal combos: %w", err)
	}

	var tsRaw []byte
	if settings.TokenSaver != nil {
		tsRaw, err = json.Marshal(settings.TokenSaver)
		if err != nil {
			return 0, nil, fmt.Errorf("marshal tokensaver: %w", err)
		}
	}

	fs := config.FileSet{
		Upstreams:  upRaw,
		Models:     modRaw,
		Tenants:    tenRaw,
		Combos:     combRaw,
		TokenSaver: tsRaw,
	}

	res, err := config.Build(fs, os.LookupEnv)
	if err != nil {
		return 0, nil, err
	}

	// Persist to Turso database if the store is configured.
	if tStore, _ := deps.getTursoStore(ctx); tStore != nil {
		if err := tStore.SaveSettings(ctx, settings); err != nil {
			return 0, nil, fmt.Errorf("save settings to turso: %w", err)
		}
	}

	// Persist to disk if a config directory is set.
	if deps.ConfigDir != "" {
		if err := os.MkdirAll(deps.ConfigDir, 0o755); err != nil {
			return 0, nil, fmt.Errorf("create config dir: %w", err)
		}

		upIndent, _ := json.MarshalIndent(upFile, "", "  ")
		modIndent, _ := json.MarshalIndent(modFile, "", "  ")
		tenIndent, _ := json.MarshalIndent(tenFile, "", "  ")
		combIndent, _ := json.MarshalIndent(combFile, "", "  ")

		if err := os.WriteFile(filepath.Join(deps.ConfigDir, config.FileNameUpstreams), upIndent, 0o644); err != nil {
			return 0, nil, fmt.Errorf("write upstreams config: %w", err)
		}
		if err := os.WriteFile(filepath.Join(deps.ConfigDir, config.FileNameModels), modIndent, 0o644); err != nil {
			return 0, nil, fmt.Errorf("write models config: %w", err)
		}
		if err := os.WriteFile(filepath.Join(deps.ConfigDir, config.FileNameTenants), tenIndent, 0o644); err != nil {
			return 0, nil, fmt.Errorf("write tenants config: %w", err)
		}
		if err := os.WriteFile(filepath.Join(deps.ConfigDir, config.FileNameCombos), combIndent, 0o644); err != nil {
			return 0, nil, fmt.Errorf("write combos config: %w", err)
		}
		if settings.TokenSaver != nil {
			tsIndent, _ := json.MarshalIndent(settings.TokenSaver, "", "  ")
			if err := os.WriteFile(filepath.Join(deps.ConfigDir, config.FileNameTokenSaver), tsIndent, 0o644); err != nil {
				return 0, nil, fmt.Errorf("write tokensaver config: %w", err)
			}
		}
	}

	// Hot swap the snapshot so the change is effective immediately.
	var newGen uint64
	if !httpx.IsNil(deps.Registry) {
		newGen = deps.Registry.NextGeneration()
		snap := domain.NewCatalogSnapshot(
			newGen,
			res.Upstreams,
			res.UpstreamOrder,
			res.Models,
			res.EnabledModelIDs,
			res.TenantsByHash,
			res.TenantOrder,
			domain.WithCombos(res.Combos, res.ComboOrder),
			domain.WithTokenSaver(res.TokenSaver),
		)
		deps.Registry.Store(snap)
		if deps.Metrics != nil {
			deps.Metrics.SetConfigGeneration(newGen)
		}
	}

	return newGen, res.Warnings, nil
}
