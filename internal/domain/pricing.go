package domain

import (
	"math"
	"sort"
	"strings"
)

// Pricing source identifiers. An entry seeded from the models.dev catalog
// carries PricingSourceModelsDev so operators can tell imported rows apart
// from hand-edited ones; every other row is PricingSourceManual.
const (
	PricingSourceModelsDev = "models.dev"
	PricingSourceManual    = "manual"
)

// microsPerMillion converts a per-1M-token price into per-token micros.
// 1 USD per 1M tokens == 1 micro-USD per token, so a provider's decimal
// per-1M price maps exactly onto an integer micro-USD-per-1M value with no
// float accumulation anywhere in the ledger.
const microsPerMillion = 1_000_000

// PricingEntry is one model's price sheet. All prices are integer micro-USD
// per 1M tokens; zero means "not priced" and falls back at estimate time.
type PricingEntry struct {
	// Model is the lookup key. Three shapes are accepted:
	//   "openai/gpt-4o"   exact provider/model id
	//   "gpt-4o"          exact bare model id
	//   "gpt-4o*"         trailing-wildcard prefix match
	Model string
	// InputMicrosPerM prices uncached input tokens.
	InputMicrosPerM int64
	// OutputMicrosPerM prices completion tokens.
	OutputMicrosPerM int64
	// CacheReadMicrosPerM prices cached input tokens. Zero falls back to
	// InputMicrosPerM so a cache hit is never billed above the full price.
	CacheReadMicrosPerM int64
	// CacheWriteMicrosPerM prices cache-write tokens. Zero falls back to
	// InputMicrosPerM.
	CacheWriteMicrosPerM int64
	// Source records provenance: PricingSourceModelsDev or PricingSourceManual.
	Source string
	// CanonicalModelID disambiguates providers that serve several upstreams
	// behind one gateway protocol (antigravity serves both google/gemini-*
	// and anthropic/claude-*): a request routed through such a protocol
	// resolves its price through this id when the direct keys miss.
	CanonicalModelID string
}

// IsPriced reports whether the entry carries any non-zero price. An unpriced
// entry matches lookups but contributes zero cost, which callers treat as
// "no pricing registered" for fallback and dashboard filtering.
func (e PricingEntry) IsPriced() bool {
	return e.InputMicrosPerM > 0 || e.OutputMicrosPerM > 0 ||
		e.CacheReadMicrosPerM > 0 || e.CacheWriteMicrosPerM > 0
}

// CostMicros returns the request cost in integer micro-USD. Callers pass
// already-normalized counts: input excludes cachedRead and cacheWrite (a
// provider's prompt_tokens figure normally includes cached tokens, so the
// caller subtracts them before calling). Cache prices fall back to the input
// price when unset.
func (e PricingEntry) CostMicros(input, output, cachedRead, cacheWrite int) int64 {
	if !e.IsPriced() {
		return 0
	}
	cacheReadPrice := e.CacheReadMicrosPerM
	if cacheReadPrice <= 0 {
		cacheReadPrice = e.InputMicrosPerM
	}
	cacheWritePrice := e.CacheWriteMicrosPerM
	if cacheWritePrice <= 0 {
		cacheWritePrice = e.InputMicrosPerM
	}
	total := scaleMicros(input, e.InputMicrosPerM)
	total += scaleMicros(output, e.OutputMicrosPerM)
	total += scaleMicros(cachedRead, cacheReadPrice)
	total += scaleMicros(cacheWrite, cacheWritePrice)
	return total
}

// scaleMicros computes tokens * microsPerM / 1M in integer micro-USD,
// saturating instead of overflowing on absurd inputs.
func scaleMicros(tokens int, microsPerM int64) int64 {
	if tokens <= 0 || microsPerM <= 0 {
		return 0
	}
	t := int64(tokens)
	if t > math.MaxInt64/microsPerM {
		return math.MaxInt64
	}
	return t * microsPerM / microsPerMillion
}

// FlatRateInputMicrosPerM and FlatRateOutputMicrosPerM preserve the gateway's
// original flat estimate ($0.0000025 in / $0.0000100 out per token) so
// dashboards stay comparable for models with no registered price. They are
// expressed in the table's own unit — micro-USD per 1M tokens — so
// $0.0000025/token == $2.50 per 1M == 2_500_000 micros.
const (
	FlatRateInputMicrosPerM  int64 = 2_500_000
	FlatRateOutputMicrosPerM int64 = 10_000_000
)

// FlatRateCostMicros prices a request with the legacy flat rates. It is the
// fallback used when the pricing table has no entry for the routed model.
func FlatRateCostMicros(input, output int) int64 {
	if input < 0 {
		input = 0
	}
	if output < 0 {
		output = 0
	}
	return scaleMicros(input, FlatRateInputMicrosPerM) + scaleMicros(output, FlatRateOutputMicrosPerM)
}

// EstimateCostMicros prices one request against the table in integer
// micro-USD. Resolution is exact match first, then the longest matching
// wildcard prefix; a total miss -- or an entry that carries no price at all
// -- falls back to the legacy flat rate so dashboards stay comparable for
// models nobody has priced yet.
//
// tokensIn must already exclude cachedRead and cacheWrite: OpenAI reports the
// cached reads inside prompt_tokens while Anthropic reports them outside
// input_tokens, so the caller normalizes before calling (see
// server.estimateRequestCost).
func (t *PricingTable) EstimateCostMicros(model string, tokensIn, tokensOut, cachedRead, cacheWrite int) int64 {
	cost, _ := t.PricedCostMicros([]string{model}, tokensIn, tokensOut, cachedRead, cacheWrite)
	return cost
}

// PricedCostMicros is EstimateCostMicros with several candidate keys: the
// public model name, the upstream's private model name, and a canonical id
// for protocols that serve several upstreams behind one gateway protocol
// (antigravity serves both google/* and anthropic/*). The first priced hit
// wins; ok reports whether the table answered at all, which lets callers
// distinguish "priced by the sheet" from "legacy flat rate".
func (t *PricingTable) PricedCostMicros(keys []string, tokensIn, tokensOut, cachedRead, cacheWrite int) (int64, bool) {
	if t != nil {
		for _, k := range keys {
			if k == "" {
				continue
			}
			if e, ok := t.Lookup(k); ok && e.IsPriced() {
				return e.CostMicros(tokensIn, tokensOut, cachedRead, cacheWrite), true
			}
		}
	}
	// The flat rate has no cache discount, so the whole prompt figure bills at
	// the input rate -- the legacy estimate charged prompt_tokens as reported,
	// which for OpenAI-shaped usage already contains the cached reads.
	return FlatRateCostMicros(tokensIn+cachedRead+cacheWrite, tokensOut), false
}

// wildcardPrefix returns the prefix of a trailing-wildcard key ("gpt-4o*" ->
// "gpt-4o"). The second result is false for exact keys.
func wildcardPrefix(model string) (string, bool) {
	if strings.HasSuffix(model, "*") {
		return strings.TrimSuffix(model, "*"), true
	}
	return model, false
}

// PricingTable is an immutable, lookup-optimized price sheet. Resolution
// order is exact match, then the longest matching wildcard prefix, then the
// canonical-model index; a total miss returns ok=false so callers can apply
// the flat-rate fallback. Once built it MUST NOT be mutated; reloads build a
// new table and swap it atomically inside the catalog snapshot.
type PricingTable struct {
	entries    map[string]PricingEntry
	wildcards  []PricingEntry // sorted by prefix length, longest first
	canonical  map[string]PricingEntry
	modelOrder []string // sorted exact keys, for stable API output
}

// NewPricingTable indexes entries for lookup. Duplicate exact keys keep the
// last occurrence; duplicate wildcard prefixes keep the longest, then the
// last. Entries are not validated here — config.Build rejects malformed
// rows before a table is ever constructed.
func NewPricingTable(entries []PricingEntry) *PricingTable {
	t := &PricingTable{
		entries:   make(map[string]PricingEntry, len(entries)),
		canonical: make(map[string]PricingEntry),
	}
	for _, e := range entries {
		key := strings.TrimSpace(e.Model)
		if key == "" {
			continue
		}
		e.Model = key
		e.CanonicalModelID = strings.TrimSpace(e.CanonicalModelID)
		if prefix, isWild := wildcardPrefix(key); isWild {
			if prefix == "" {
				continue
			}
			t.wildcards = append(t.wildcards, e)
			continue
		}
		t.entries[key] = e
		if e.CanonicalModelID != "" {
			t.canonical[e.CanonicalModelID] = e
		}
	}
	// Longest prefix first so the first wildcard hit is the most specific
	// one; lexicographic order breaks ties deterministically.
	sort.SliceStable(t.wildcards, func(i, j int) bool {
		pi, _ := wildcardPrefix(t.wildcards[i].Model)
		pj, _ := wildcardPrefix(t.wildcards[j].Model)
		if len(pi) != len(pj) {
			return len(pi) > len(pj)
		}
		return t.wildcards[i].Model < t.wildcards[j].Model
	})
	t.modelOrder = make([]string, 0, len(t.entries))
	for k := range t.entries {
		t.modelOrder = append(t.modelOrder, k)
	}
	sort.Strings(t.modelOrder)
	return t
}

// Lookup resolves a model key without canonical fallback: exact match first,
// then the longest matching wildcard prefix.
func (t *PricingTable) Lookup(model string) (PricingEntry, bool) {
	if t == nil || model == "" {
		return PricingEntry{}, false
	}
	if e, ok := t.entries[model]; ok {
		return e, true
	}
	for _, e := range t.wildcards {
		if prefix, _ := wildcardPrefix(e.Model); strings.HasPrefix(model, prefix) {
			return e, true
		}
	}
	return PricingEntry{}, false
}

// Resolve resolves a model key with canonical fallback for protocols that
// serve several upstreams: exact/wildcard on the model key first, then the
// canonical index (exact, then wildcard) when canonicalID is non-empty.
func (t *PricingTable) Resolve(model, canonicalID string) (PricingEntry, bool) {
	if e, ok := t.Lookup(model); ok {
		return e, true
	}
	if t == nil || canonicalID == "" {
		return PricingEntry{}, false
	}
	if e, ok := t.canonical[canonicalID]; ok {
		return e, true
	}
	for _, e := range t.wildcards {
		if e.CanonicalModelID == "" {
			continue
		}
		if prefix, _ := wildcardPrefix(e.CanonicalModelID); strings.HasPrefix(canonicalID, prefix) {
			return e, true
		}
	}
	return PricingEntry{}, false
}

// Entries returns every entry (exact and wildcard) in stable order so the
// dashboard can render the full sheet. Callers must not mutate the result.
func (t *PricingTable) Entries() []PricingEntry {
	if t == nil {
		return nil
	}
	out := make([]PricingEntry, 0, len(t.entries)+len(t.wildcards))
	for _, k := range t.modelOrder {
		out = append(out, t.entries[k])
	}
	out = append(out, t.wildcards...)
	return out
}

// Len returns the number of indexed entries (exact plus wildcard).
func (t *PricingTable) Len() int {
	if t == nil {
		return 0
	}
	return len(t.entries) + len(t.wildcards)
}
