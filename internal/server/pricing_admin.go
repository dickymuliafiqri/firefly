package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/pricing"
)

// pricingImportRequest is the POST /api/pricing/import body. Either All or a
// non-empty Models list must be present. Overwrite is accepted for forward
// compatibility and deliberately ignored: seeding is once-only by design, so
// a re-import can add new models but never rewrite a price the operator has
// already set (manually or via an earlier import).
type pricingImportRequest struct {
	All       bool     `json:"all"`
	Models    []string `json:"models"`
	Overwrite bool     `json:"overwrite"`
}

// pricingImportResponse reports the outcome of a seed run.
type pricingImportResponse struct {
	Status           string    `json:"status"`
	Added            int       `json:"added"`
	Skipped          int       `json:"skipped"`
	Total            int       `json:"total"`
	CatalogFetchedAt time.Time `json:"catalog_fetched_at,omitempty"`
	Error            string    `json:"error,omitempty"`
}

// pricingCatalogResponse is the GET /api/pricing/catalog body.
type pricingCatalogResponse struct {
	Status    string                 `json:"status"`
	FetchedAt time.Time              `json:"fetched_at"`
	Providers []string               `json:"providers"`
	Entries   []pricing.CatalogEntry `json:"entries"`
	Total     int                    `json:"total"`
	Error     string                 `json:"error,omitempty"`
}

// pricingEntryDTOs projects domain entries into their DTO form for persistence.
func pricingEntryDTOs(entries []domain.PricingEntry) []config.PricingEntryDTO {
	out := make([]config.PricingEntryDTO, 0, len(entries))
	for _, e := range entries {
		out = append(out, config.PricingEntryDTO{
			Model:                e.Model,
			InputMicrosPerM:      e.InputMicrosPerM,
			OutputMicrosPerM:     e.OutputMicrosPerM,
			CacheReadMicrosPerM:  e.CacheReadMicrosPerM,
			CacheWriteMicrosPerM: e.CacheWriteMicrosPerM,
			Source:               e.Source,
			CanonicalModelID:     e.CanonicalModelID,
		})
	}
	return out
}

// handlePricingImport seeds the local price sheet from the models.dev
// catalog. Existing keys are never overwritten (seed-once), so the endpoint
// is safe to re-run: it only adds models the sheet does not know yet.
func (deps RouterDeps) handlePricingImport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized: valid dashboard session or admin token required")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()

	var req pricingImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(pricingImportResponse{
			Status: "error",
			Error:  "invalid request payload: " + err.Error(),
		})
		return
	}

	snap := deps.currentSnapshot()
	if snap == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(pricingImportResponse{
			Status: "error",
			Error:  "catalog snapshot unavailable",
		})
		return
	}

	cat := deps.PricingCatalog.Load()
	if cat == nil {
		fetched, err := pricing.FetchCatalog(r.Context())
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(pricingImportResponse{
				Status: "error",
				Error:  err.Error(),
			})
			return
		}
		cat = fetched
		deps.PricingCatalog.Store(cat)
	}

	var imported []domain.PricingEntry
	switch {
	case req.All:
		imported = pricing.SelectMapped(cat, nil)
	case len(req.Models) > 0:
		imported = pricing.SelectByKeys(cat, req.Models)
	default:
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(pricingImportResponse{
			Status: "error",
			Error:  "either all=true or a non-empty models list is required",
		})
		return
	}
	if len(imported) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(pricingImportResponse{
			Status: "error",
			Error:  "no matching models found in the models.dev catalog",
		})
		return
	}

	var existing []domain.PricingEntry
	if table := snap.Pricing(); table != nil {
		existing = table.Entries()
	}
	merged, added, skipped := pricing.MergeSeedOnce(existing, imported)

	// Seed-once: when nothing new was found the sheet is already complete for
	// this selection. Report success without touching storage.
	if added > 0 {
		settings := settingsDTOFromSnapshot(snap)
		restoreMaskedSecrets(&settings, snap)
		settings.Pricing = &config.PricingFile{Entries: pricingEntryDTOs(merged)}

		if _, _, err := deps.persistSettings(r.Context(), settings); err != nil {
			deps.writePersistError(w, err)
			return
		}
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(pricingImportResponse{
		Status:           "ok",
		Added:            added,
		Skipped:          skipped,
		Total:            len(merged),
		CatalogFetchedAt: cat.FetchedAt,
	})
}

// loadPricingCatalog returns the cached catalog, fetching it on first use.
// The freshly fetched catalog is stored so later calls are served from memory.
func (deps RouterDeps) loadPricingCatalog(r *http.Request) (*pricing.Catalog, error) {
	if cat := deps.PricingCatalog.Load(); cat != nil {
		return cat, nil
	}
	cat, err := pricing.FetchCatalog(r.Context())
	if err != nil {
		return nil, err
	}
	deps.PricingCatalog.Store(cat)
	return cat, nil
}

// writePricingCatalog renders the catalog, optionally filtered by provider
// and a case-insensitive substring query over key and display name.
func (deps RouterDeps) writePricingCatalog(w http.ResponseWriter, cat *pricing.Catalog, provider, query string) {
	entries := cat.Entries
	if provider != "" || query != "" {
		q := strings.ToLower(query)
		filtered := make([]pricing.CatalogEntry, 0, len(entries))
		for _, e := range entries {
			if provider != "" && e.Provider != provider {
				continue
			}
			if q != "" && !strings.Contains(strings.ToLower(e.Key), q) && !strings.Contains(strings.ToLower(e.Name), q) {
				continue
			}
			filtered = append(filtered, e)
		}
		entries = filtered
	}
	providers := cat.Providers
	if providers == nil {
		providers = []string{}
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(pricingCatalogResponse{
		Status:    "ok",
		FetchedAt: cat.FetchedAt,
		Providers: providers,
		Entries:   entries,
		Total:     len(entries),
	})
}

// handlePricingCatalog serves the browsable models.dev catalog for the
// dashboard picker. The cache is in-memory only: a cold cache triggers one
// fetch, and an unreachable models.dev surfaces as 502 without touching the
// live price sheet.
func (deps RouterDeps) handlePricingCatalog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized: valid dashboard session or admin token required")
		return
	}

	cat, err := deps.loadPricingCatalog(r)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(pricingCatalogResponse{
			Status: "error",
			Error:  err.Error(),
		})
		return
	}
	deps.writePricingCatalog(w, cat, r.URL.Query().Get("provider"), r.URL.Query().Get("q"))
}

// handlePricingCatalogRefresh force-fetches the catalog, replacing the
// in-memory cache. It never mutates the persisted price sheet.
func (deps RouterDeps) handlePricingCatalogRefresh(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized: valid dashboard session or admin token required")
		return
	}

	cat, err := pricing.FetchCatalog(r.Context())
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(pricingCatalogResponse{
			Status: "error",
			Error:  err.Error(),
		})
		return
	}
	deps.PricingCatalog.Store(cat)
	deps.writePricingCatalog(w, cat, "", "")
}
