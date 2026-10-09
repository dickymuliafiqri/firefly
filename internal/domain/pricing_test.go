package domain

import "testing"

func TestPricingTableLookupExactBeatsWildcard(t *testing.T) {
	table := NewPricingTable([]PricingEntry{
		{Model: "gpt-4o*", InputMicrosPerM: 100, OutputMicrosPerM: 200},
		{Model: "gpt-4o", InputMicrosPerM: 2500, OutputMicrosPerM: 10000},
	})

	e, ok := table.Lookup("gpt-4o")
	if !ok {
		t.Fatal("exact key gpt-4o must resolve")
	}
	if e.InputMicrosPerM != 2500 {
		t.Fatalf("exact match must win over wildcard, got input=%d", e.InputMicrosPerM)
	}

	e, ok = table.Lookup("gpt-4o-mini")
	if !ok {
		t.Fatal("wildcard key must resolve gpt-4o-mini")
	}
	if e.InputMicrosPerM != 100 {
		t.Fatalf("wildcard match input = %d, want 100", e.InputMicrosPerM)
	}

	if _, ok := table.Lookup("claude-3"); ok {
		t.Fatal("unrelated key must miss")
	}
}

func TestPricingTableLongestWildcardWins(t *testing.T) {
	table := NewPricingTable([]PricingEntry{
		{Model: "gpt-4*", InputMicrosPerM: 1},
		{Model: "gpt-4o*", InputMicrosPerM: 2},
		{Model: "gpt-4o-mini*", InputMicrosPerM: 3},
	})

	e, ok := table.Lookup("gpt-4o-mini-2024")
	if !ok {
		t.Fatal("expected a wildcard hit")
	}
	if e.InputMicrosPerM != 3 {
		t.Fatalf("longest prefix must win, got %d", e.InputMicrosPerM)
	}

	e, ok = table.Lookup("gpt-4o")
	if !ok || e.InputMicrosPerM != 2 {
		t.Fatalf("mid-length prefix must win, got %+v ok=%v", e, ok)
	}

	e, ok = table.Lookup("gpt-4-turbo")
	if !ok || e.InputMicrosPerM != 1 {
		t.Fatalf("shortest prefix must still match, got %+v ok=%v", e, ok)
	}
}

func TestPricingTableCanonicalFallback(t *testing.T) {
	table := NewPricingTable([]PricingEntry{
		{Model: "anthropic/claude-sonnet-4-5", InputMicrosPerM: 3000, CanonicalModelID: "anthropic/claude-sonnet-4-5"},
		{Model: "google/gemini-2.5-pro", InputMicrosPerM: 1250, CanonicalModelID: "google/gemini-2.5-pro"},
	})

	// Direct key hit does not consult canonical.
	e, ok := table.Resolve("anthropic/claude-sonnet-4-5", "")
	if !ok || e.InputMicrosPerM != 3000 {
		t.Fatalf("direct hit failed: %+v ok=%v", e, ok)
	}

	// A request routed through antigravity carries the bare upstream model;
	// the canonical id selects the right provider's price.
	e, ok = table.Resolve("claude-sonnet-4-5-20250929", "anthropic/claude-sonnet-4-5")
	if !ok || e.InputMicrosPerM != 3000 {
		t.Fatalf("canonical fallback failed: %+v ok=%v", e, ok)
	}
	e, ok = table.Resolve("gemini-2.5-pro", "google/gemini-2.5-pro")
	if !ok || e.InputMicrosPerM != 1250 {
		t.Fatalf("canonical fallback (google) failed: %+v ok=%v", e, ok)
	}

	if _, ok := table.Resolve("mystery-model", "unknown/canonical"); ok {
		t.Fatal("unknown canonical must miss")
	}
}

func TestPricingTableNilAndEmptySafe(t *testing.T) {
	var nilTable *PricingTable
	if _, ok := nilTable.Lookup("gpt-4o"); ok {
		t.Fatal("nil table must miss")
	}
	if _, ok := nilTable.Resolve("gpt-4o", "openai/gpt-4o"); ok {
		t.Fatal("nil table must miss on resolve")
	}
	if nilTable.Len() != 0 || nilTable.Entries() != nil {
		t.Fatal("nil table must report empty")
	}

	empty := NewPricingTable(nil)
	if empty.Len() != 0 {
		t.Fatalf("empty table len = %d", empty.Len())
	}
	if _, ok := empty.Lookup(""); ok {
		t.Fatal("empty key must miss")
	}
}

func TestPricingEntryCostMicros(t *testing.T) {
	e := PricingEntry{InputMicrosPerM: 2_500_000, OutputMicrosPerM: 10_000_000}

	// 1M input tokens at $2.50/1M == 2_500_000 micros; 1M output at $10/1M.
	if got := e.CostMicros(1_000_000, 1_000_000, 0, 0); got != 12_500_000 {
		t.Fatalf("cost = %d, want 12500000", got)
	}
	// Sub-million requests keep exact integer math (no float drift).
	if got := e.CostMicros(1000, 500, 0, 0); got != 2500+5000 {
		t.Fatalf("cost = %d, want 7500", got)
	}
	// Zero tokens cost nothing.
	if got := e.CostMicros(0, 0, 0, 0); got != 0 {
		t.Fatalf("zero-token cost = %d, want 0", got)
	}
}

func TestPricingEntryCacheFallbackAndExplicit(t *testing.T) {
	base := PricingEntry{InputMicrosPerM: 2_500_000, OutputMicrosPerM: 10_000_000}

	// Cache prices unset: cached tokens bill at the input price.
	if got := base.CostMicros(0, 0, 1_000_000, 0); got != 2_500_000 {
		t.Fatalf("cache-read fallback = %d, want 2500000", got)
	}
	if got := base.CostMicros(0, 0, 0, 1_000_000); got != 2_500_000 {
		t.Fatalf("cache-write fallback = %d, want 2500000", got)
	}

	// Explicit cache prices override the fallback.
	cached := PricingEntry{
		InputMicrosPerM:      2_500_000,
		OutputMicrosPerM:     10_000_000,
		CacheReadMicrosPerM:  250_000, // $0.25/1M
		CacheWriteMicrosPerM: 3_125_000,
	}
	if got := cached.CostMicros(0, 0, 1_000_000, 1_000_000); got != 250_000+3_125_000 {
		t.Fatalf("explicit cache cost = %d, want 3375000", got)
	}

	// An unpriced entry contributes zero.
	if got := (PricingEntry{}).CostMicros(1000, 1000, 1000, 1000); got != 0 {
		t.Fatalf("unpriced entry cost = %d, want 0", got)
	}
	if (PricingEntry{}).IsPriced() {
		t.Fatal("zero entry must not report priced")
	}
}

func TestFlatRateCostMicros(t *testing.T) {
	// Legacy flat estimate: $0.0000025 in / $0.0000100 out per token.
	// 1000 in == $0.0025 == 2_500 micros; 500 out == $0.005 == 5_000 micros.
	if got := FlatRateCostMicros(1000, 500); got != 7_500 {
		t.Fatalf("flat cost = %d, want 7500", got)
	}
	if got := FlatRateCostMicros(-5, -5); got != 0 {
		t.Fatalf("negative tokens must clamp to zero, got %d", got)
	}
}

func TestPricingTableEntriesStableOrder(t *testing.T) {
	table := NewPricingTable([]PricingEntry{
		{Model: "zeta", InputMicrosPerM: 1},
		{Model: "alpha", InputMicrosPerM: 2},
		{Model: "mid*", InputMicrosPerM: 3},
	})
	entries := table.Entries()
	if len(entries) != 3 {
		t.Fatalf("entries len = %d, want 3", len(entries))
	}
	if entries[0].Model != "alpha" || entries[1].Model != "zeta" {
		t.Fatalf("exact entries must sort by model, got %s,%s", entries[0].Model, entries[1].Model)
	}
	if entries[2].Model != "mid*" {
		t.Fatalf("wildcard entries must come last, got %s", entries[2].Model)
	}
	if table.Len() != 3 {
		t.Fatalf("len = %d, want 3", table.Len())
	}
}

func TestEstimateCostMicros_PricedEntry(t *testing.T) {
	table := NewPricingTable([]PricingEntry{
		{Model: "openai/gpt-4o", InputMicrosPerM: 2_500_000, OutputMicrosPerM: 10_000_000},
	})
	// $2.50 per 1M input and $10 per 1M output: 1M in + 1M out = $12.50.
	got := table.EstimateCostMicros("openai/gpt-4o", 1_000_000, 1_000_000, 0, 0)
	if want := int64(12_500_000); got != want {
		t.Fatalf("EstimateCostMicros = %d, want %d", got, want)
	}
}

func TestEstimateCostMicros_CacheSplit(t *testing.T) {
	table := NewPricingTable([]PricingEntry{
		{
			Model:                "anthropic/claude-sonnet-4",
			InputMicrosPerM:      3_000_000, // $3.00 per 1M
			OutputMicrosPerM:     15_000_000,
			CacheReadMicrosPerM:  300_000,   // $0.30 per 1M
			CacheWriteMicrosPerM: 3_750_000, // $3.75 per 1M
		},
	})
	// 1M uncached @ $3 + 1M cached read @ $0.30 + 500k cache write @ $3.75
	// + 200k output @ $15 = 3.00 + 0.30 + 1.875 + 3.00 = $8.175.
	got := table.EstimateCostMicros("anthropic/claude-sonnet-4", 1_000_000, 200_000, 1_000_000, 500_000)
	if want := int64(8_175_000); got != want {
		t.Fatalf("EstimateCostMicros = %d, want %d", got, want)
	}
}

func TestEstimateCostMicros_CachePriceMissingBillsAtInput(t *testing.T) {
	table := NewPricingTable([]PricingEntry{
		{Model: "openai/gpt-4o-mini", InputMicrosPerM: 150_000, OutputMicrosPerM: 600_000},
	})
	// No cache prices registered: the cached reads must bill at the full input
	// rate rather than for free, otherwise a cache-heavy request under-bills.
	got := table.EstimateCostMicros("openai/gpt-4o-mini", 100_000, 0, 900_000, 0)
	if want := int64(150_000); got != want { // 1M total @ $0.15
		t.Fatalf("EstimateCostMicros = %d, want %d", got, want)
	}
}

func TestEstimateCostMicros_FlatRateFallback(t *testing.T) {
	cases := []struct {
		name  string
		table *PricingTable
	}{
		{"nil table", nil},
		{"empty table", NewPricingTable(nil)},
		{"unpriced entry", NewPricingTable([]PricingEntry{{Model: "openai/gpt-4o"}})},
		{"model miss", NewPricingTable([]PricingEntry{{Model: "other/model", InputMicrosPerM: 1}})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Legacy flat rate: $0.0000025 in / $0.0000100 out per token.
			got := tc.table.EstimateCostMicros("openai/gpt-4o", 1_000_000, 500_000, 0, 0)
			if want := int64(2_500_000 + 5_000_000); got != want {
				t.Fatalf("EstimateCostMicros = %d, want %d", got, want)
			}
		})
	}
}

func TestEstimateCostMicros_FlatRateFallbackPricesCachedPrompt(t *testing.T) {
	// The flat rate has no cache discount, so a cached prompt still bills at
	// the input rate instead of vanishing from the ledger.
	var table *PricingTable
	got := table.EstimateCostMicros("openai/gpt-4o", 0, 0, 1_000_000, 0)
	if want := int64(2_500_000); got != want {
		t.Fatalf("EstimateCostMicros = %d, want %d", got, want)
	}
}

func TestEstimateCostMicros_LargeTokenCountsKeepPrecision(t *testing.T) {
	table := NewPricingTable([]PricingEntry{
		{Model: "openai/gpt-4o", InputMicrosPerM: 2_500_000, OutputMicrosPerM: 10_000_000},
	})
	// 100M tokens at $2.50 per 1M is $250 = 250,000,000 micros: far beyond
	// float32 precision, so the integer path must carry it exactly.
	got := table.EstimateCostMicros("openai/gpt-4o", 100_000_000, 0, 0, 0)
	if want := int64(250_000_000); got != want {
		t.Fatalf("EstimateCostMicros = %d, want %d", got, want)
	}
}

func TestPricedCostMicros_MultiKeyOrder(t *testing.T) {
	table := NewPricingTable([]PricingEntry{
		{Model: "upstream-private-name", InputMicrosPerM: 1_000_000, OutputMicrosPerM: 1_000_000},
	})
	// The public name misses, the upstream's private name hits.
	cost, ok := table.PricedCostMicros([]string{"public-name", "upstream-private-name"}, 1_000_000, 0, 0, 0)
	if !ok {
		t.Fatal("PricedCostMicros ok = false, want true")
	}
	if want := int64(1_000_000); cost != want {
		t.Fatalf("PricedCostMicros = %d, want %d", cost, want)
	}
	// An unpriced first hit must not shadow a priced later key.
	table2 := NewPricingTable([]PricingEntry{
		{Model: "public-name"},
		{Model: "upstream-private-name", InputMicrosPerM: 2_000_000},
	})
	cost, ok = table2.PricedCostMicros([]string{"public-name", "upstream-private-name"}, 1_000_000, 0, 0, 0)
	if !ok {
		t.Fatal("PricedCostMicros ok = false, want true")
	}
	if want := int64(2_000_000); cost != want {
		t.Fatalf("PricedCostMicros = %d, want %d", cost, want)
	}
}
