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
	"strconv"
	"strings"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/storage/turso"
	"github.com/dickymuliafiqri/firefly/internal/transport/httpx"
)

// handleOptionsSettings serves CORS preflight requests for the settings API.
func (deps RouterDeps) handleOptionsSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
	w.WriteHeader(http.StatusNoContent)
}

// maskSecret redacts sensitive credentials for frontend safe display.
func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "[REDACTED]"
	}
	return s[:3] + "..." + s[len(s)-4:]
}

// isMasked checks if a submitted secret string is a placeholder from a previous GET.
func isMasked(s string) bool {
	return strings.Contains(s, "...") || s == "[REDACTED]"
}

// sanitizePublicSettings extracts safe public metadata for dashboard display,
// strictly redacting sensitive credentials (API keys, pool, tenants, turso, autoTLS).
func sanitizePublicSettings(src config.SettingsDTO) config.SettingsDTO {
	publicUpstreams := make([]config.UpstreamDTO, 0, len(src.Upstreams))
	for _, u := range src.Upstreams {
		publicUpstreams = append(publicUpstreams, config.UpstreamDTO{
			Name:                u.Name,
			Protocol:            u.Protocol,
			BaseURL:             u.BaseURL,
			BaseURLs:            u.BaseURLs,
			TimeoutMs:           u.TimeoutMs,
			IdleTimeoutMs:       u.IdleTimeoutMs,
			StreamIdleTimeoutMs: u.StreamIdleTimeoutMs,
			KeyStrategy:         u.KeyStrategy,
			Enabled:             u.Enabled,
			EgressMode:          u.EgressMode,
			ProxyURL:            u.ProxyURL,
			WarpAutoRotateOn429: u.WarpAutoRotateOn429,
		})
	}

	publicModels := make([]config.ModelDTO, 0, len(src.Models))
	for _, m := range src.Models {
		publicModels = append(publicModels, config.ModelDTO{
			PublicName:    m.PublicName,
			Upstream:      m.Upstream,
			UpstreamModel: m.UpstreamModel,
			Capabilities:  m.Capabilities,
			MaxContext:    m.MaxContext,
			Enabled:       m.Enabled,
		})
	}

	publicCombos := make([]config.ComboDTO, 0, len(src.Combos))
	for _, c := range src.Combos {
		publicCombos = append(publicCombos, config.ComboDTO{
			Name:     c.Name,
			Strategy: c.Strategy,
			Models:   c.Models,
			Enabled:  c.Enabled,
		})
	}

	return config.SettingsDTO{
		Upstreams:  publicUpstreams,
		Models:     publicModels,
		Combos:     publicCombos,
		Tenants:    []config.TenantDTO{},
		TokenSaver: src.TokenSaver,
		AutoTLS:    nil,
		Turso:      nil,
	}
}

// handleGetSettings retrieves current configuration, safely redacting sensitive secrets.
// Unauthenticated requests receive a sanitized public view containing only display metadata.
func (deps RouterDeps) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	isAdmin := deps.authorizeAdmin(r)

	snap := deps.currentSnapshot()

	// Try reading directly from Turso database first if configured
	var settings config.SettingsDTO
	loadedFromDisk := false

	if tStore, _ := deps.getTursoStore(r.Context()); tStore != nil {
		if tSettings, err := tStore.LoadSettings(r.Context()); err == nil && tSettings != nil {
			if len(tSettings.Upstreams) > 0 || len(tSettings.Models) > 0 || len(tSettings.Tenants) > 0 || len(tSettings.Combos) > 0 {
				settings = *tSettings
				loadedFromDisk = true
			}
		}
	}

	if !loadedFromDisk && deps.ConfigDir != "" {
		src := config.NewFileConfigSource(deps.ConfigDir)
		if raw, err := src.Load(r.Context()); err == nil {
			// Only load from disk if the files on disk actually validate with current environment.
			// This prevents loading sample configs with missing ENV variables that would cause validation errors when saving.
			if _, valErr := config.Build(config.FileSetFromMap(raw), os.LookupEnv); valErr == nil {
				var upFile config.UpstreamsFile
				var modFile config.ModelsFile
				var tenFile config.TenantsFile
				var combFile config.CombosFile

				_ = json.Unmarshal(raw["upstreams"], &upFile)
				_ = json.Unmarshal(raw["models"], &modFile)
				_ = json.Unmarshal(raw["tenants"], &tenFile)
				_ = json.Unmarshal(raw["combos"], &combFile)
				if len(raw["tokensaver"]) > 0 {
					var tsDTO config.TokenSaverDTO
					if err := json.Unmarshal(raw["tokensaver"], &tsDTO); err == nil {
						settings.TokenSaver = &tsDTO
					}
				}

				if len(upFile.Upstreams) > 0 || len(modFile.Models) > 0 || len(tenFile.Tenants) > 0 || len(combFile.Combos) > 0 {
					settings.Upstreams = upFile.Upstreams
					settings.Models = modFile.Models
					settings.Tenants = tenFile.Tenants
					settings.Combos = combFile.Combos
					loadedFromDisk = true
				}
			}
		}
	}

	if !loadedFromDisk && snap != nil {
		settings = settingsDTOFromSnapshot(snap)
	}

	if !isAdmin {
		publicSettings := sanitizePublicSettings(settings)
		if publicSettings.Upstreams == nil {
			publicSettings.Upstreams = []config.UpstreamDTO{}
		}
		if publicSettings.Models == nil {
			publicSettings.Models = []config.ModelDTO{}
		}
		if publicSettings.Combos == nil {
			publicSettings.Combos = []config.ComboDTO{}
		}
		if publicSettings.Tenants == nil {
			publicSettings.Tenants = []config.TenantDTO{}
		}
		_ = json.NewEncoder(w).Encode(publicSettings)
		return
	}

	// Always mask any secrets in loaded settings
	for i := range settings.Upstreams {
		u := &settings.Upstreams[i]
		if u.APIKey != "" {
			u.APIKey = maskSecret(u.APIKey)
		}
		for j := range u.APIKeys {
			u.APIKeys[j] = maskSecret(u.APIKeys[j])
		}
		for j := range u.CredentialPool {
			k := &u.CredentialPool[j]
			if k.APIKey != "" {
				k.APIKey = maskSecret(k.APIKey)
			}
			if k.Secret != "" {
				k.Secret = maskSecret(k.Secret)
			}
		}
	}

	if settings.Upstreams == nil {
		settings.Upstreams = []config.UpstreamDTO{}
	}
	if settings.Models == nil {
		settings.Models = []config.ModelDTO{}
	}
	if settings.Tenants == nil {
		settings.Tenants = []config.TenantDTO{}
	}
	if settings.Combos == nil {
		settings.Combos = []config.ComboDTO{}
	}
	if settings.TokenSaver == nil {
		if snap != nil {
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
		} else {
			defTS := domain.DefaultTokenSaverConfig()
			maxTool := defTS.MaxToolOutputChars
			ctxThresh := defTS.ContextThreshold
			settings.TokenSaver = &config.TokenSaverDTO{
				Enabled:            defTS.Enabled,
				CompressToolOutput: defTS.CompressToolOutput,
				TerseOutput:        defTS.TerseOutput,
				MinimalCode:        defTS.MinimalCode,
				CompressContext:    defTS.CompressContext,
				MaxToolOutputChars: &maxTool,
				ContextThreshold:   &ctxThresh,
			}
		}
	}
	if autoTLS, err := config.LoadAutoTLS(deps.ConfigDir); err == nil {
		settings.AutoTLS = &autoTLS
	} else {
		// A malformed tls.json must not hide the routing catalog. The update path
		// will reject a mutation until the configuration can be read safely.
		settings.AutoTLS = &config.AutoTLSDTO{}
	}

	if tStore, _ := deps.getTursoStore(r.Context()); tStore != nil {
		settings.StorageEngine = "turso"
	} else {
		settings.StorageEngine = "local"
	}

	tursoCfg, _ := config.LoadTursoConfig(deps.ConfigDir)
	if tursoCfg.DatabaseURL == "" && os.Getenv("TURSO_DATABASE_URL") != "" {
		tursoCfg.DatabaseURL = os.Getenv("TURSO_DATABASE_URL")
	}
	if tursoCfg.AuthToken == "" && os.Getenv("TURSO_AUTH_TOKEN") != "" {
		tursoCfg.AuthToken = os.Getenv("TURSO_AUTH_TOKEN")
	}
	if tursoCfg.LocalPath == "" {
		tursoCfg.LocalPath = "data/firefly.db"
	}
	if tursoCfg.AuthToken != "" {
		tursoCfg.AuthToken = maskSecret(tursoCfg.AuthToken)
	}
	settings.Turso = &tursoCfg

	_ = json.NewEncoder(w).Encode(settings)
}

// handleUpdateSettings validates, persists, and hot-swaps configuration from the frontend.
func (deps RouterDeps) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized: valid dashboard session or admin token required")
		return
	}

	// Bound incoming payload to the same gateway-wide body limit used by the
	// inference endpoints, so one handler never accepts what another rejects.
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	defer r.Body.Close()

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		// An oversized body is a 413, not a generic 400, so clients can tell
		// "shrink the payload" apart from "fix the JSON".
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			openai.WriteError(w, http.StatusRequestEntityTooLarge, openai.TypeInvalidRequest, "request body too large")
			return
		}
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "read request body: "+err.Error())
		return
	}

	var payload config.SettingsDTO
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "parse JSON settings: "+err.Error())
		return
	}

	// Catalog mutations issued by other dashboard pages predate Auto-TLS and do
	// not include auto_tls. Preserve the persisted setting when that field is
	// absent; only an explicit field can enable, replace, or disable HTTPS.
	previousTLS, err := config.LoadAutoTLS(deps.ConfigDir)
	if err != nil {
		openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, "read TLS config: "+err.Error())
		return
	}
	nextTLS := previousTLS
	if payload.AutoTLS != nil {
		nextTLS = *payload.AutoTLS
	}
	normalizedTLS, err := NormalizeAutoTLSConfig(AutoTLSConfig{
		Enabled: nextTLS.Enabled,
		Domain:  nextTLS.Domain,
		Email:   nextTLS.Email,
	})
	if err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, err.Error())
		return
	}
	nextTLS = config.AutoTLSDTO{
		Enabled: normalizedTLS.Enabled,
		Domain:  normalizedTLS.Domain,
		Email:   normalizedTLS.Email,
	}
	payload.AutoTLS = &nextTLS

	// If any secret was left masked (unchanged in frontend), restore existing secret from snapshot
	snap := deps.currentSnapshot()
	if snap != nil {
		restoreMaskedSecrets(&payload, snap)
	}

	if payload.Combos == nil && snap != nil {
		for _, c := range snap.AllCombos() {
			en := c.Enabled
			payload.Combos = append(payload.Combos, config.ComboDTO{
				Name:     c.Name,
				Strategy: string(c.Strategy),
				Models:   c.Models,
				Enabled:  &en,
			})
		}
	}

	// Prepare file structures for validation
	upFile := config.UpstreamsFile{Upstreams: payload.Upstreams}
	modFile := config.ModelsFile{Models: payload.Models}
	tenFile := config.TenantsFile{Tenants: payload.Tenants}
	combFile := config.CombosFile{Combos: payload.Combos}

	upRaw, err := json.Marshal(upFile)
	if err != nil {
		openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, "marshal upstreams: "+err.Error())
		return
	}
	modRaw, err := json.Marshal(modFile)
	if err != nil {
		openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, "marshal models: "+err.Error())
		return
	}
	tenRaw, err := json.Marshal(tenFile)
	if err != nil {
		openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, "marshal tenants: "+err.Error())
		return
	}
	combRaw, err := json.Marshal(combFile)
	if err != nil {
		openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, "marshal combos: "+err.Error())
		return
	}

	var tsRaw []byte
	if payload.TokenSaver != nil {
		tsRaw, err = json.Marshal(payload.TokenSaver)
		if err != nil {
			openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, "marshal tokensaver: "+err.Error())
			return
		}
	}

	fs := config.FileSet{
		Upstreams:  upRaw,
		Models:     modRaw,
		Tenants:    tenRaw,
		Combos:     combRaw,
		TokenSaver: tsRaw,
	}

	// Validate configuration
	res, err := config.Build(fs, os.LookupEnv)
	if err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, err.Error())
		return
	}

	// Bind the public ports before writing tls.json. A conflict (for example a
	// pre-existing reverse proxy on :80/:443) is returned to the administrator
	// instead of persisting a configuration that cannot operate.
	tlsApplied := false
	rollbackTLS := func() {
		if deps.AutoTLS == nil || !tlsApplied {
			return
		}
		if err := deps.AutoTLS.Apply(AutoTLSConfig{
			Enabled: previousTLS.Enabled,
			Domain:  previousTLS.Domain,
			Email:   previousTLS.Email,
		}); err != nil && deps.Logger != nil {
			deps.Logger.Error("could not roll back auto TLS configuration", "err", err)
		}
	}
	if deps.AutoTLS != nil {
		if err := deps.AutoTLS.Apply(normalizedTLS); err != nil {
			openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "apply auto TLS: "+err.Error())
			return
		}
		tlsApplied = true
	}

	// Update Turso credentials if provided in payload
	if payload.Turso != nil {
		tursoCfg := *payload.Turso
		if isMasked(tursoCfg.AuthToken) {
			savedCfg, _ := config.LoadTursoConfig(deps.ConfigDir)
			if savedCfg.AuthToken != "" {
				tursoCfg.AuthToken = savedCfg.AuthToken
			} else {
				tursoCfg.AuthToken = os.Getenv("TURSO_AUTH_TOKEN")
			}
		}
		if tursoCfg.LocalPath == "" {
			tursoCfg.LocalPath = "data/firefly.db"
		}
		if deps.ConfigDir != "" {
			_ = config.SaveTursoConfig(deps.ConfigDir, tursoCfg)
		}
		if deps.TursoManager != nil {
			if _, err := deps.TursoManager.UpdateConfig(r.Context(), tursoCfg); err != nil {
				deps.Logger.Warn("failed to update turso client configuration", "err", err)
			}
		}
	}

	// Persist to Turso database if Turso store is configured
	if tStore, _ := deps.getTursoStore(r.Context()); tStore != nil {
		if err := tStore.SaveSettings(r.Context(), payload); err != nil {
			rollbackTLS()
			openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, "save settings to turso: "+err.Error())
			return
		}
	}

	// Persist to disk if config directory is set
	if deps.ConfigDir != "" {
		if err := os.MkdirAll(deps.ConfigDir, 0o755); err != nil {
			rollbackTLS()
			openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, "create config dir: "+err.Error())
			return
		}

		upIndent, _ := json.MarshalIndent(upFile, "", "  ")
		modIndent, _ := json.MarshalIndent(modFile, "", "  ")
		tenIndent, _ := json.MarshalIndent(tenFile, "", "  ")
		combIndent, _ := json.MarshalIndent(combFile, "", "  ")

		var tsIndent []byte
		if payload.TokenSaver != nil {
			tsIndent, _ = json.MarshalIndent(payload.TokenSaver, "", "  ")
		}

		if err := writeCatalogFiles(deps.ConfigDir, catalogFileSet{
			upstreams:  upIndent,
			models:     modIndent,
			tenants:    tenIndent,
			combos:     combIndent,
			tokenSaver: tsIndent,
		}); err != nil {
			rollbackTLS()
			openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, err.Error())
			return
		}
		if err := config.SaveAutoTLS(deps.ConfigDir, nextTLS); err != nil {
			rollbackTLS()
			openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, "write TLS config: "+err.Error())
			return
		}
	}

	// Hot swap snapshot in registry
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

	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":     "ok",
		"generation": newGen,
		"warnings":   res.Warnings,
		"auto_tls":   nextTLS,
	})
}

// handleGetTursoProviders retrieves available providers from the Turso centralized database.
func (deps RouterDeps) handleGetTursoProviders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized: valid dashboard session or admin token required")
		return
	}

	cfg, _ := config.LoadTursoConfig(deps.ConfigDir)
	hasConfig := cfg.DatabaseURL != "" || os.Getenv("TURSO_DATABASE_URL") != ""

	store, err := deps.getTursoStore(r.Context())
	if err != nil {
		deps.Logger.Warn("turso store initialization failed in handleGetTursoProviders", "err", err)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"configured": hasConfig,
			"providers":  []any{},
			"error":      err.Error(),
		})
		return
	}

	if store == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"configured": hasConfig,
			"providers":  []any{},
		})
		return
	}

	providers, err := store.ListProviders(r.Context())
	if err != nil {
		deps.Logger.Warn("failed to list turso providers", "err", err)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"configured": true,
			"providers":  []any{},
			"error":      err.Error(),
		})
		return
	}

	if providers == nil {
		providers = []turso.ProviderSummary{}
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"configured": true,
		"providers":  providers,
	})
}

// legacyProviderKey is a provider key as exposed by the legacy /api/turso read
// surface. Only a masked hint leaves the server: routing already takes the real
// secret straight from the store (upstream-bound providers are pooled at load
// time), so the dashboard never needed the raw value except to copy it into a
// config, which is exactly the leak this shape closes.
type legacyProviderKey struct {
	ID         int64  `json:"id"`
	ProviderID int64  `json:"provider_id"`
	APIKeyHint string `json:"api_key_hint"`
	Status     string `json:"status"`
	IsActive   bool   `json:"is_active"`
}

// handleGetTursoProviderKeys retrieves active keys from the Turso database for a provider.
func (deps RouterDeps) handleGetTursoProviderKeys(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized: valid dashboard session or admin token required")
		return
	}

	store, err := deps.getTursoStore(r.Context())
	if err != nil {
		deps.Logger.Warn("turso store not available in handleGetTursoProviderKeys", "err", err)
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI, "turso store not available: "+err.Error())
		return
	}
	if store == nil {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "turso database is not configured")
		return
	}

	var providerID int64
	idStr := r.PathValue("id")
	if idStr == "" {
		idStr = r.URL.Query().Get("provider_id")
	}
	if idStr != "" {
		if pid, err := strconv.ParseInt(idStr, 10, 64); err == nil {
			providerID = pid
		}
	}

	keys, err := store.ListProviderKeys(r.Context(), providerID)
	if err != nil {
		openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, "list provider keys: "+err.Error())
		return
	}

	hints := make([]legacyProviderKey, 0, len(keys))
	for _, k := range keys {
		hints = append(hints, legacyProviderKey{
			ID:         k.ID,
			ProviderID: k.ProviderID,
			APIKeyHint: maskSecret(k.APIKey),
			Status:     k.Status,
			IsActive:   k.IsActive,
		})
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":          true,
		"provider_id": providerID,
		"count":       len(hints),
		"keys":        hints,
	})
}

// handleTestTurso validates connectivity and auth credentials against Turso database.
func (deps RouterDeps) handleTestTurso(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized: valid dashboard session or admin token required")
		return
	}

	var req struct {
		DatabaseURL string `json:"database_url"`
		AuthToken   string `json:"auth_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "invalid json payload: "+err.Error())
		return
	}

	if isMasked(req.AuthToken) {
		savedCfg, _ := config.LoadTursoConfig(deps.ConfigDir)
		if savedCfg.AuthToken != "" {
			req.AuthToken = savedCfg.AuthToken
		} else {
			req.AuthToken = os.Getenv("TURSO_AUTH_TOKEN")
		}
	}
	if req.DatabaseURL == "" {
		savedCfg, _ := config.LoadTursoConfig(deps.ConfigDir)
		if savedCfg.DatabaseURL != "" {
			req.DatabaseURL = savedCfg.DatabaseURL
		} else {
			req.DatabaseURL = os.Getenv("TURSO_DATABASE_URL")
		}
	}

	if req.DatabaseURL == "" || req.AuthToken == "" {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      false,
			"message": "Database URL and Auth Token are required.",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	start := time.Now()
	testPath := filepath.Join(os.TempDir(), fmt.Sprintf("firefly_test_turso_%d.db", time.Now().UnixNano()))
	defer func() {
		_ = os.Remove(testPath)
		_ = os.Remove(testPath + ".turso-sync-metadata")
	}()

	testClient, err := turso.NewClient(ctx, turso.Config{
		RemoteURL:    req.DatabaseURL,
		AuthToken:    req.AuthToken,
		LocalPath:    testPath,
		SyncInterval: 1 * time.Minute,
		Logger:       deps.Logger,
	})
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      false,
			"message": fmt.Sprintf("Connection failed: %v", err),
		})
		return
	}
	defer testClient.Close()

	if err := testClient.DB().PingContext(ctx); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      false,
			"message": fmt.Sprintf("Database ping failed: %v", err),
		})
		return
	}

	latency := time.Since(start).Milliseconds()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":         true,
		"message":    "Successfully connected to Turso database.",
		"latency_ms": latency,
	})
}

// settingsDTOFromSnapshot reconstructs the full settings DTO from the live
// routing snapshot. Upstream credential secrets are masked for display;
// callers that intend to re-persist the result must run it through
// restoreMaskedSecrets first, otherwise masked placeholders would be written
// back as real secrets.
func settingsDTOFromSnapshot(snap *domain.CatalogSnapshot) config.SettingsDTO {
	var settings config.SettingsDTO

	for _, name := range snap.UpstreamNames() {
		u, ok := snap.Upstream(name)
		if !ok || u == nil {
			continue
		}
		var pool []config.CredentialKeyDTO
		var apiKeys []string
		var primaryKey string
		if u.KeyRing != nil {
			if u.KeyRing.PrimarySlot() != nil {
				primaryKey = maskSecret(u.KeyRing.PrimarySlot().Secret)
			}
			for _, slot := range u.KeyRing.Slots {
				if slot != nil {
					rps := slot.RPS
					maxC := slot.MaxConcurrent
					masked := maskSecret(slot.Secret)
					pool = append(pool, config.CredentialKeyDTO{
						Ref:           slot.Ref,
						Secret:        masked,
						APIKey:        masked,
						RPS:           &rps,
						MaxConcurrent: &maxC,
					})
					apiKeys = append(apiKeys, masked)
				}
			}
		}
		tMs := u.TimeoutMs
		iMs := u.IdleTimeoutMs
		sMs := u.StreamIdleTimeoutMs
		mIdle := u.MaxIdleConnsPerHost
		mConn := u.MaxConnsPerHost
		insec := u.AllowInsecure
		cRPS := u.CredentialRPS
		cMax := u.CredentialMaxConcurrent
		en := !u.Disabled

		settings.Upstreams = append(settings.Upstreams, config.UpstreamDTO{
			Name:                    u.Name,
			Protocol:                string(u.Protocol),
			BaseURL:                 u.BaseURL,
			BaseURLs:                u.BaseURLs,
			APIKey:                  primaryKey,
			APIKeys:                 apiKeys,
			CredentialRef:           u.CredentialRef,
			KeyStrategy:             string(u.KeyStrategy),
			CredentialPool:          pool,
			TimeoutMs:               &tMs,
			IdleTimeoutMs:           &iMs,
			StreamIdleTimeoutMs:     &sMs,
			MaxIdleConnsPerHost:     &mIdle,
			MaxConnsPerHost:         &mConn,
			ExtraHeaders:            u.ExtraHeaders,
			AllowInsecure:           &insec,
			CredentialRPS:           &cRPS,
			CredentialMaxConcurrent: &cMax,
			Enabled:                 &en,
		})
	}

	for _, id := range snap.SortedPublicModelIDs() {
		m, ok := snap.Model(id)
		if !ok || m == nil {
			continue
		}
		maxCtx := m.MaxContext
		en := m.Enabled
		settings.Models = append(settings.Models, config.ModelDTO{
			PublicName:    m.PublicName,
			Upstream:      m.Upstream,
			UpstreamModel: m.UpstreamModel,
			Capabilities: &config.CapabilitiesDTO{
				Stream:     m.Capabilities.Stream,
				Tools:      m.Capabilities.Tools,
				Vision:     m.Capabilities.Vision,
				JSONMode:   m.Capabilities.JSONMode,
				Embeddings: m.Capabilities.Embeddings,
				Audio:      m.Capabilities.Audio,
			},
			MaxContext: &maxCtx,
			Enabled:    &en,
		})
	}

	settings.Tenants = tenantDTOsFromSnapshot(snap)

	for _, c := range snap.AllCombos() {
		en := c.Enabled
		settings.Combos = append(settings.Combos, config.ComboDTO{
			Name:     c.Name,
			Strategy: string(c.Strategy),
			Models:   c.Models,
			Enabled:  &en,
		})
	}

	return settings
}

// ringHasSecret reports whether secret is one of the live keyring's plaintext
// secrets — used to tell a real credential that merely looks masked apart from
// an actual placeholder.
func ringHasSecret(ring *domain.KeyRing, secret string) bool {
	if ring == nil || secret == "" {
		return false
	}
	for _, s := range ring.Slots {
		if s != nil && s.Secret == secret {
			return true
		}
	}
	return false
}

// restoreMaskedSecrets replaces masked secret placeholders in payload with
// the real secrets from the live snapshot, so a re-persisted payload never
// writes masked values back over real credentials.
func restoreMaskedSecrets(payload *config.SettingsDTO, snap *domain.CatalogSnapshot) {
	for i := range payload.Upstreams {
		u := &payload.Upstreams[i]
		existingUp, hasUp := snap.Upstream(u.Name)
		if !hasUp || existingUp == nil || existingUp.KeyRing == nil {
			continue
		}

		if isMasked(u.APIKey) && existingUp.KeyRing.PrimarySlot() != nil {
			u.APIKey = existingUp.KeyRing.PrimarySlot().Secret
		}
		for j := range u.APIKeys {
			if isMasked(u.APIKeys[j]) {
				masked := u.APIKeys[j]
				found := false
				for _, slot := range existingUp.KeyRing.Slots {
					if slot != nil && maskSecret(slot.Secret) == masked {
						u.APIKeys[j] = slot.Secret
						found = true
						break
					}
				}
				if !found && j < len(existingUp.KeyRing.Slots) && existingUp.KeyRing.Slots[j] != nil {
					u.APIKeys[j] = existingUp.KeyRing.Slots[j].Secret
				}
			}
		}
		for j := range u.CredentialPool {
			k := &u.CredentialPool[j]
			slot := existingUp.KeyRing.SlotByRef(k.Ref)
			if slot == nil && (isMasked(k.Secret) || isMasked(k.APIKey)) {
				targetMasked := k.Secret
				if targetMasked == "" {
					targetMasked = k.APIKey
				}
				for _, s := range existingUp.KeyRing.Slots {
					if s != nil && maskSecret(s.Secret) == targetMasked {
						slot = s
						break
					}
				}
			}
			if slot == nil && j < len(existingUp.KeyRing.Slots) {
				slot = existingUp.KeyRing.Slots[j]
			}
			if slot != nil {
				if isMasked(k.APIKey) {
					k.APIKey = slot.Secret
				}
				if isMasked(k.Secret) {
					k.Secret = slot.Secret
				}
			}
		}

		// A pool entry whose secret is still a placeholder after every resolution
		// attempt cannot be restored: persisting it would store the mask itself as
		// a credential and mint a key slot that always fails upstream with 401.
		// Drop it instead; u.APIKeys is positional against the pool, so filter it
		// in lockstep. When both are non-empty but misaligned, positional mapping
		// is meaningless — leave the payload alone and let the guard below null
		// the pool rather than risk pairing a kept secret with a wrong APIKeys
		// entry.
		aligned := len(u.APIKeys) == len(u.CredentialPool)
		if len(u.APIKeys) == 0 || aligned {
			keptPool := make([]config.CredentialKeyDTO, 0, len(u.CredentialPool))
			keptKeys := make([]string, 0, len(u.APIKeys))
			dropped := 0
			for j := range u.CredentialPool {
				k := u.CredentialPool[j]
				eff := k.Secret
				if eff == "" {
					eff = k.APIKey
				}
				// isMasked is a heuristic (the mask keeps a 3-byte prefix and a
				// 4-byte suffix around "..."), and here it gates a delete: a live
				// secret that literally contains "..." must not be discarded just
				// because it looks like a placeholder.
				if isMasked(eff) && !ringHasSecret(existingUp.KeyRing, eff) {
					dropped++
					continue
				}
				keptPool = append(keptPool, k)
				if aligned {
					keptKeys = append(keptKeys, u.APIKeys[j])
				}
			}
			if dropped > 0 {
				u.CredentialPool = keptPool
				if aligned {
					u.APIKeys = keptKeys
				}
			}
		}

		if len(u.APIKeys) > 0 && len(u.CredentialPool) > 0 && len(u.APIKeys) != len(u.CredentialPool) {
			u.CredentialPool = nil
		}
	}

	// If a tenant API key was sent masked (e.g. from an old or cached client), restore from snapshot
	for i := range payload.Tenants {
		t := &payload.Tenants[i]
		if isMasked(t.APIKey) {
			for _, key := range snap.TenantKeys() {
				if existingTenant, ok := snap.TenantByKey(key); ok && existingTenant != nil {
					if existingTenant.Name == t.Name || (t.KeyHash != "" && existingTenant.KeyHash == t.KeyHash) {
						if existingTenant.APIKey != "" {
							t.APIKey = existingTenant.APIKey
							break
						}
					}
				}
			}
		}
	}
}
