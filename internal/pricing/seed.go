package pricing

import (
	"github.com/dickymuliafiqri/firefly/internal/domain"
)

// DefaultProtocolProviders maps a Firefly upstream protocol onto the
// models.dev provider ids whose prices apply to it. Protocols absent from
// the table (cline, codebuddy-cn/intl, qoder) are priced manually: their
// vendors publish no models.dev catalog.
//
// antigravity fronts two vendors behind one protocol — Google Gemini and
// Anthropic Claude — so it maps to both providers; the per-model choice
// happens at resolve time through canonical_model_id (a Gemini request
// resolves to the google/* price, a Claude request to anthropic/*).
var DefaultProtocolProviders = map[string][]string{
	string(domain.ProtocolOpenAI):      {"openai"},
	string(domain.ProtocolAnthropic):   {"anthropic"},
	string(domain.ProtocolGrokCLI):     {"xai"},
	string(domain.ProtocolOpenCode):    {"opencode"},
	string(domain.ProtocolOpenCodeGo):  {"opencode-go"},
	string(domain.ProtocolAntigravity): {"google", "anthropic"},
}

// mappedProviders is the flat set of provider ids covered by the mapping,
// used by the "import everything" action.
func mappedProviders(mapping map[string][]string) map[string]bool {
	if mapping == nil {
		mapping = DefaultProtocolProviders
	}
	out := make(map[string]bool)
	for _, providers := range mapping {
		for _, p := range providers {
			out[p] = true
		}
	}
	return out
}

// SelectMapped returns the catalog entries whose provider is covered by the
// protocol mapping, converted into price-sheet entries tagged models.dev.
func SelectMapped(cat *Catalog, mapping map[string][]string) []domain.PricingEntry {
	if cat == nil {
		return nil
	}
	want := mappedProviders(mapping)
	var out []domain.PricingEntry
	for _, e := range cat.Entries {
		if want[e.Provider] {
			out = append(out, e.PricingEntry())
		}
	}
	return out
}

// SelectByKeys returns the entries whose "provider/model" key appears in
// keys, preserving the requested order and ignoring unknown keys. Duplicate
// keys collapse so the import response's skipped count only ever reports
// keys the price sheet already holds, never a caller's own repetition.
func SelectByKeys(cat *Catalog, keys []string) []domain.PricingEntry {
	if cat == nil || len(keys) == 0 {
		return nil
	}
	index := make(map[string]CatalogEntry, len(cat.Entries))
	for _, e := range cat.Entries {
		index[e.Key] = e
	}
	var out []domain.PricingEntry
	seen := make(map[string]bool, len(keys))
	for _, k := range keys {
		if seen[k] {
			continue
		}
		e, ok := index[k]
		if !ok {
			continue
		}
		seen[k] = true
		out = append(out, e.PricingEntry())
	}
	return out
}

// MergeSeedOnce merges imported entries into the existing sheet without ever
// overwriting an existing key — manual or models.dev, exact or wildcard.
// Only new keys are appended; the result keeps existing entries first, in
// their original order, followed by the additions. It reports how many
// imported rows were added and how many were skipped as already present
// (including duplicates inside the imported batch itself).
func MergeSeedOnce(existing, imported []domain.PricingEntry) (merged []domain.PricingEntry, added, skipped int) {
	seen := make(map[string]bool, len(existing)+len(imported))
	for _, e := range existing {
		if e.Model != "" {
			seen[e.Model] = true
		}
	}
	merged = append(merged, existing...)
	for _, e := range imported {
		if e.Model == "" || seen[e.Model] {
			skipped++
			continue
		}
		seen[e.Model] = true
		merged = append(merged, e)
		added++
	}
	return merged, added, skipped
}
