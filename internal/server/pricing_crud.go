package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/transport/httpx"
)

// This file carries the operator-facing CRUD over the local price sheet:
// listing what is registered, editing one entry, deleting an override, and
// resolving what a model would actually be billed at. The resolve endpoint
// exists because the protocol-to-provider mapping lives on the backend, so a
// client cannot answer "is this model priced?" without asking.

// handlePricingList serves GET /api/pricing: every entry in the local sheet,
// annotated with whether the gateway's own catalog actually uses it.
func (deps RouterDeps) handlePricingList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized: valid dashboard session or admin token required")
		return
	}

	snap := deps.currentSnapshot()
	if snap == nil {
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI, "snapshot not loaded")
		return
	}

	table := snap.Pricing()
	var entries []domain.PricingEntry
	if table != nil {
		entries = table.Entries()
	}

	used := usedModelKeys(snap)
	out := make([]pricingEntryView, 0, len(entries))
	for _, e := range entries {
		out = append(out, pricingEntryView{
			PricingEntryDTO: domainPricingEntryDTO(e),
			Used:            used[e.Model],
		})
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"entries": out,
		"total":   len(out),
	})
}

// pricingEntryView is one price-sheet entry plus the dashboard's "is this
// model actually routed?" flag.
type pricingEntryView struct {
	config.PricingEntryDTO
	Used bool `json:"used"`
}

// usedModelKeys collects every public and upstream model name the catalog
// routes, so a price entry can be marked as in use.
func usedModelKeys(snap *domain.CatalogSnapshot) map[string]bool {
	used := make(map[string]bool)
	for _, name := range snap.EnabledModels() {
		used[name] = true
		if m, ok := snap.Model(name); ok && m != nil {
			if m.UpstreamModel != "" {
				used[m.UpstreamModel] = true
			}
		}
	}
	return used
}

// handlePricingUpsert serves PUT /api/pricing/{key}. The key is the model name;
// a missing entry is created, an existing one is overwritten and its source
// flips to "manual" so a later models.dev import leaves it alone.
func (deps RouterDeps) handlePricingUpsert(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized: valid dashboard session or admin token required")
		return
	}

	key := strings.TrimSpace(r.PathValue("key"))
	if key == "" {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "model key is required")
		return
	}

	var entry config.PricingEntryDTO
	entry, ok := httpx.ReadJSON[config.PricingEntryDTO](w, r, maxRequestBodyBytes, "pricing entry")
	if !ok {
		return
	}
	entry.Model = key
	if entry.InputMicrosPerM < 0 || entry.OutputMicrosPerM < 0 ||
		entry.CacheReadMicrosPerM < 0 || entry.CacheWriteMicrosPerM < 0 {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "prices must be non-negative")
		return
	}
	if entry.Source == "" {
		entry.Source = "manual"
	}

	if err := deps.upsertPricingEntry(r, entry); err != nil {
		openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, err.Error())
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"entry":  entry,
	})
}

// handlePricingDelete serves DELETE /api/pricing/{key}. Deleting a manual
// override lets a previously seeded models.dev price show through again; if
// there is no seed, the model simply becomes unpriced and falls back to the
// legacy flat rate.
func (deps RouterDeps) handlePricingDelete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized: valid dashboard session or admin token required")
		return
	}

	key := strings.TrimSpace(r.PathValue("key"))
	if key == "" {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "model key is required")
		return
	}

	if err := deps.deletePricingEntry(r, key); err != nil {
		openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, err.Error())
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"key":    key,
	})
}

// pricingResolveRequest is the POST /api/pricing/resolve body: a batch of the
// gateway's own models, so the dashboard can mark each one priced or not in a
// single round trip.
type pricingResolveRequest struct {
	Models []pricingResolveModel `json:"models"`
}

type pricingResolveModel struct {
	PublicName    string `json:"public_name"`
	Upstream      string `json:"upstream"`
	UpstreamModel string `json:"upstream_model"`
}

// pricingResolveEntry is the resolved price for one model, or nulls when the
// sheet has nothing for it.
type pricingResolveEntry struct {
	PublicName string                  `json:"public_name"`
	Entry      *config.PricingEntryDTO `json:"entry"`
	MatchedKey string                  `json:"matched_key,omitempty"`
}

// handlePricingResolve serves POST /api/pricing/resolve. It answers "what
// would this model be billed at?" for a batch, which is the only way a client
// can tell a priced model from an unpriced one: the protocol-to-provider
// mapping and the wildcard rules both live on the backend.
func (deps RouterDeps) handlePricingResolve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized: valid dashboard session or admin token required")
		return
	}

	req, ok := httpx.ReadJSON[pricingResolveRequest](w, r, maxRequestBodyBytes, "pricing resolve")
	if !ok {
		return
	}

	snap := deps.currentSnapshot()
	if snap == nil {
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI, "snapshot not loaded")
		return
	}
	table := snap.Pricing()

	out := make([]pricingResolveEntry, 0, len(req.Models))
	for _, m := range req.Models {
		entry := pricingResolveEntry{PublicName: m.PublicName}
		if table != nil {
			// The public name wins; the upstream's private name is the fallback
			// because a provider may price the private id and not the alias.
			if e, ok := table.Lookup(m.PublicName); ok {
				dto := domainPricingEntryDTO(e)
				entry.Entry = &dto
				entry.MatchedKey = m.PublicName
			} else if m.UpstreamModel != "" && m.UpstreamModel != m.PublicName {
				if e, ok := table.Lookup(m.UpstreamModel); ok {
					dto := domainPricingEntryDTO(e)
					entry.Entry = &dto
					entry.MatchedKey = m.UpstreamModel
				}
			}
		}
		out = append(out, entry)
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"entries": out,
	})
}

// domainPricingEntryDTO projects a domain price entry into its DTO form.
func domainPricingEntryDTO(e domain.PricingEntry) config.PricingEntryDTO {
	return config.PricingEntryDTO{
		Model:                e.Model,
		InputMicrosPerM:      e.InputMicrosPerM,
		OutputMicrosPerM:     e.OutputMicrosPerM,
		CacheReadMicrosPerM:  e.CacheReadMicrosPerM,
		CacheWriteMicrosPerM: e.CacheWriteMicrosPerM,
		Source:               e.Source,
		CanonicalModelID:     e.CanonicalModelID,
	}
}

// rewritePricing persists a new price sheet. It follows the same shape as the
// import endpoint: rebuild the full settings DTO from the live snapshot so no
// other section is dropped, restore masked secrets so credentials survive the
// round trip, then hand it to the single settings writer.
func (deps RouterDeps) rewritePricing(r *http.Request, entries []config.PricingEntryDTO) error {
	snap := deps.currentSnapshot()
	if snap == nil {
		return errSnapshotUnavailable
	}
	settings := settingsDTOFromSnapshot(snap)
	restoreMaskedSecrets(&settings, snap)
	settings.Pricing = &config.PricingFile{Entries: entries}
	_, _, err := deps.persistSettings(r.Context(), settings)
	return err
}

// errSnapshotUnavailable is returned when a write needs the live catalog and
// none is loaded.
var errSnapshotUnavailable = &pricingWriteError{msg: "catalog snapshot unavailable"}

type pricingWriteError struct{ msg string }

func (e *pricingWriteError) Error() string { return e.msg }

// upsertPricingEntry inserts or replaces one price-sheet entry. An entry that
// already exists keeps its position but takes the new numbers, and its source
// becomes "manual" so a later models.dev import cannot silently revert an
// operator's edit.
func (deps RouterDeps) upsertPricingEntry(r *http.Request, entry config.PricingEntryDTO) error {
	snap := deps.currentSnapshot()
	if snap == nil {
		return errSnapshotUnavailable
	}

	var existing []config.PricingEntryDTO
	if table := snap.Pricing(); table != nil {
		for _, e := range table.Entries() {
			existing = append(existing, domainPricingEntryDTO(e))
		}
	}

	entry.Source = "manual"
	replaced := false
	for i := range existing {
		if existing[i].Model == entry.Model {
			existing[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		existing = append(existing, entry)
	}

	return deps.rewritePricing(r, existing)
}

// deletePricingEntry removes one price-sheet entry. Deleting an entry that was
// seeded from models.dev simply unprices the model; the flat-rate fallback in
// the cost path then applies, exactly as it did before any import.
func (deps RouterDeps) deletePricingEntry(r *http.Request, key string) error {
	snap := deps.currentSnapshot()
	if snap == nil {
		return errSnapshotUnavailable
	}

	var kept []config.PricingEntryDTO
	found := false
	if table := snap.Pricing(); table != nil {
		for _, e := range table.Entries() {
			if e.Model == key {
				found = true
				continue
			}
			kept = append(kept, domainPricingEntryDTO(e))
		}
	}
	if !found {
		return &pricingWriteError{msg: "no pricing entry for model " + key}
	}

	return deps.rewritePricing(r, kept)
}
