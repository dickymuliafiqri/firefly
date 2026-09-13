package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/dickymuliafiqri/firefly/internal/auth"
	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/httpx"
	"github.com/dickymuliafiqri/firefly/internal/openai"
)

// handleOptionsSettings serves CORS preflight requests for the settings API.
func (deps RouterDeps) handleOptionsSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
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

// handleGetSettings retrieves current configuration, safely redacting sensitive secrets.
func (deps RouterDeps) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if deps.AdminToken != "" {
		token, ok := auth.ExtractBearer(r.Header.Get("Authorization"))
		if !ok || token != deps.AdminToken {
			openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "invalid or missing admin token")
			return
		}
	}

	snap := deps.currentSnapshot()

	// Try reading directly from config files first if config dir exists
	var settings config.SettingsDTO
	loadedFromDisk := false

	if deps.ConfigDir != "" {
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
		// Populate from active snapshot
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

		for _, hash := range snap.TenantHashes() {
			t, ok := snap.TenantByHash(hash)
			if !ok || t == nil {
				continue
			}
			rps := t.RateLimit.RPS
			burst := t.RateLimit.Burst
			maxC := t.RateLimit.MaxConcurrent
			settings.Tenants = append(settings.Tenants, config.TenantDTO{
				KeyHash:       t.KeyHash,
				Name:          t.Name,
				Status:        string(t.Status),
				AllowedModels: t.AllowedModels,
				CredentialRef: t.CredentialRef,
				RateLimit: &config.RateLimitDTO{
					RPS:           &rps,
					Burst:         &burst,
					MaxConcurrent: &maxC,
				},
				Metadata: t.Metadata,
			})
		}
		for _, c := range snap.AllCombos() {
			en := c.Enabled
			settings.Combos = append(settings.Combos, config.ComboDTO{
				Name:     c.Name,
				Strategy: string(c.Strategy),
				Models:   c.Models,
				Enabled:  &en,
			})
		}
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
	for i := range settings.Tenants {
		t := &settings.Tenants[i]
		if t.APIKey != "" {
			t.APIKey = maskSecret(t.APIKey)
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

	_ = json.NewEncoder(w).Encode(settings)
}

// handleUpdateSettings validates, persists, and hot-swaps configuration from the frontend.
func (deps RouterDeps) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if deps.AdminToken != "" {
		token, ok := auth.ExtractBearer(r.Header.Get("Authorization"))
		if !ok || token != deps.AdminToken {
			openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "invalid or missing admin token")
			return
		}
	}

	// Bound incoming payload size to 10 MB
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	defer r.Body.Close()

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "read request body: "+err.Error())
		return
	}

	var payload config.SettingsDTO
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "parse JSON settings: "+err.Error())
		return
	}

	// If any secret was left masked (unchanged in frontend), restore existing secret from snapshot
	snap := deps.currentSnapshot()
	if snap != nil {
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
			if len(u.APIKeys) > 0 && len(u.CredentialPool) > 0 && len(u.APIKeys) != len(u.CredentialPool) {
				u.CredentialPool = nil
			}
		}
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

	fs := config.FileSet{
		Upstreams: upRaw,
		Models:    modRaw,
		Tenants:   tenRaw,
		Combos:    combRaw,
	}

	// Validate configuration
	res, err := config.Build(fs, os.LookupEnv)
	if err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, err.Error())
		return
	}

	// Persist to disk if config directory is set
	if deps.ConfigDir != "" {
		if err := os.MkdirAll(deps.ConfigDir, 0o755); err != nil {
			openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, "create config dir: "+err.Error())
			return
		}

		upIndent, _ := json.MarshalIndent(upFile, "", "  ")
		modIndent, _ := json.MarshalIndent(modFile, "", "  ")
		tenIndent, _ := json.MarshalIndent(tenFile, "", "  ")
		combIndent, _ := json.MarshalIndent(combFile, "", "  ")

		if err := os.WriteFile(filepath.Join(deps.ConfigDir, config.FileNameUpstreams), upIndent, 0o644); err != nil {
			openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, "write upstreams config: "+err.Error())
			return
		}
		if err := os.WriteFile(filepath.Join(deps.ConfigDir, config.FileNameModels), modIndent, 0o644); err != nil {
			openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, "write models config: "+err.Error())
			return
		}
		if err := os.WriteFile(filepath.Join(deps.ConfigDir, config.FileNameTenants), tenIndent, 0o644); err != nil {
			openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, "write tenants config: "+err.Error())
			return
		}
		if err := os.WriteFile(filepath.Join(deps.ConfigDir, config.FileNameCombos), combIndent, 0o644); err != nil {
			openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, "write combos config: "+err.Error())
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
	})
}
