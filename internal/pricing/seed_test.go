package pricing

import (
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

func testCatalog() *Catalog {
	return &Catalog{
		Providers: []string{"anthropic", "google", "openai", "xai"},
		Entries: []CatalogEntry{
			{Provider: "openai", ModelID: "gpt-4o", Key: "openai/gpt-4o", Name: "GPT-4o",
				InputMicrosPerM: 2_500_000, OutputMicrosPerM: 10_000_000, CacheReadMicrosPerM: 1_250_000,
				CanonicalModelID: "openai/gpt-4o"},
			{Provider: "openai", ModelID: "gpt-4o-mini", Key: "openai/gpt-4o-mini", Name: "GPT-4o mini",
				InputMicrosPerM: 150_000, OutputMicrosPerM: 600_000,
				CanonicalModelID: "openai/gpt-4o-mini"},
			{Provider: "anthropic", ModelID: "claude-sonnet-4-5", Key: "anthropic/claude-sonnet-4-5", Name: "Claude Sonnet 4.5",
				InputMicrosPerM: 3_000_000, OutputMicrosPerM: 15_000_000,
				CanonicalModelID: "anthropic/claude-sonnet-4-5"},
			{Provider: "google", ModelID: "gemini-2.5-pro", Key: "google/gemini-2.5-pro", Name: "Gemini 2.5 Pro",
				InputMicrosPerM: 1_250_000, OutputMicrosPerM: 10_000_000,
				CanonicalModelID: "google/gemini-2.5-pro"},
			// xai is reachable through the grok-cli protocol mapping.
			{Provider: "xai", ModelID: "grok-4", Key: "xai/grok-4", Name: "Grok 4",
				InputMicrosPerM: 3_000_000, OutputMicrosPerM: 15_000_000,
				CanonicalModelID: "xai/grok-4"},
			// Unmapped provider: must never be selected by SelectMapped.
			{Provider: "mistral", ModelID: "mistral-large", Key: "mistral/mistral-large", Name: "Mistral Large",
				InputMicrosPerM: 2_000_000, OutputMicrosPerM: 6_000_000,
				CanonicalModelID: "mistral/mistral-large"},
		},
	}
}

func TestDefaultProtocolProviders(t *testing.T) {
	m := DefaultProtocolProviders
	if len(m) == 0 {
		t.Fatal("mapping must not be empty")
	}
	for _, proto := range []string{"openai", "anthropic", "grok-cli", "opencode", "opencode-go", "antigravity"} {
		if _, ok := m[proto]; !ok {
			t.Fatalf("protocol %q missing from mapping", proto)
		}
	}
	if got := m["antigravity"]; len(got) != 2 || got[0] != "google" || got[1] != "anthropic" {
		t.Fatalf("antigravity = %v, want [google anthropic]", got)
	}
}

func TestSelectMapped(t *testing.T) {
	cat := testCatalog()

	// nil mapping: every mapped provider, antigravity included (google +
	// anthropic are already in the sheet through their own protocols).
	all := SelectMapped(cat, nil)
	if len(all) != 5 {
		t.Fatalf("all = %d entries, want 5 (mistral excluded)", len(all))
	}
	for _, e := range all {
		if e.Source != domain.PricingSourceModelsDev {
			t.Fatalf("entry %q source = %q, want models.dev", e.Model, e.Source)
		}
		if e.Model == "mistral/mistral-large" {
			t.Fatal("unmapped provider leaked into selection")
		}
	}

	// Explicit mapping: only openai.
	onlyOpenAI := SelectMapped(cat, map[string][]string{"openai": {"openai"}})
	if len(onlyOpenAI) != 2 {
		t.Fatalf("openai-only = %d, want 2", len(onlyOpenAI))
	}

	// The antigravity protocol fronts google even though "google" itself is
	// not a gateway protocol.
	ag := SelectMapped(cat, map[string][]string{"antigravity": {"google"}})
	if len(ag) != 1 || ag[0].Model != "google/gemini-2.5-pro" {
		t.Fatalf("antigravity = %+v, want google/gemini-2.5-pro", ag)
	}

	// Unknown mapping yields nothing.
	if got := SelectMapped(cat, map[string][]string{"no-such-protocol": {"nope"}}); len(got) != 0 {
		t.Fatalf("unknown mapping = %d, want 0", len(got))
	}

	// Nil catalog is safe.
	if got := SelectMapped(nil, nil); got != nil {
		t.Fatal("nil catalog must yield nil")
	}
}

func TestSelectByKeys(t *testing.T) {
	cat := testCatalog()

	got := SelectByKeys(cat, []string{"openai/gpt-4o", "xai/grok-4", "openai/nope"})
	if len(got) != 2 {
		t.Fatalf("selected = %d, want 2 (unknown skipped)", len(got))
	}
	// Order follows the request, not the catalog.
	if got[0].Model != "openai/gpt-4o" || got[1].Model != "xai/grok-4" {
		t.Fatalf("order not preserved: %s, %s", got[0].Model, got[1].Model)
	}

	// Duplicate keys collapse.
	dup := SelectByKeys(cat, []string{"openai/gpt-4o", "openai/gpt-4o"})
	if len(dup) != 1 {
		t.Fatalf("duplicates = %d, want 1", len(dup))
	}

	if got := SelectByKeys(cat, nil); len(got) != 0 {
		t.Fatal("nil keys must yield nothing")
	}
}

func TestMergeSeedOnce(t *testing.T) {
	cat := testCatalog()
	imported := SelectMapped(cat, map[string][]string{"openai": {"openai"}, "anthropic": {"anthropic"}})

	t.Run("empty sheet seeds everything", func(t *testing.T) {
		merged, added, skipped := MergeSeedOnce(nil, imported)
		if added != 3 || skipped != 0 {
			t.Fatalf("added = %d, skipped = %d, want 3/0", added, skipped)
		}
		if len(merged) != 3 {
			t.Fatalf("merged = %d, want 3", len(merged))
		}
	})

	t.Run("existing keys are never overwritten", func(t *testing.T) {
		manual := []domain.PricingEntry{
			{Model: "openai/gpt-4o", InputMicrosPerM: 999_999, OutputMicrosPerM: 999_999, Source: domain.PricingSourceManual},
		}
		merged, added, skipped := MergeSeedOnce(manual, imported)
		if added != 2 || skipped != 1 {
			t.Fatalf("added = %d, skipped = %d, want 2/1", added, skipped)
		}
		if len(merged) != 3 {
			t.Fatalf("merged = %d, want 3", len(merged))
		}
		for _, e := range merged {
			if e.Model == "openai/gpt-4o" {
				if e.InputMicrosPerM != 999_999 {
					t.Fatalf("manual price overwritten: %d", e.InputMicrosPerM)
				}
				if e.Source != domain.PricingSourceManual {
					t.Fatalf("manual source overwritten: %q", e.Source)
				}
			}
		}
	})

	t.Run("re-import is idempotent", func(t *testing.T) {
		first, _, _ := MergeSeedOnce(nil, imported)
		second, added, skipped := MergeSeedOnce(first, imported)
		if added != 0 || skipped != 3 {
			t.Fatalf("added = %d, skipped = %d, want 0/3", added, skipped)
		}
		if len(second) != 3 {
			t.Fatalf("merged = %d, want 3", len(second))
		}
	})

	t.Run("import payload dedupes internally", func(t *testing.T) {
		dup := append(append([]domain.PricingEntry{}, imported...), imported[0])
		merged, added, skipped := MergeSeedOnce(nil, dup)
		if added != 3 || skipped != 1 {
			t.Fatalf("added = %d, skipped = %d, want 3/1", added, skipped)
		}
		if len(merged) != 3 {
			t.Fatalf("merged = %d, want 3", len(merged))
		}
	})

	t.Run("empty model keys are ignored", func(t *testing.T) {
		junk := []domain.PricingEntry{{Model: "", InputMicrosPerM: 1}}
		merged, added, skipped := MergeSeedOnce(nil, junk)
		if added != 0 || skipped != 1 || len(merged) != 0 {
			t.Fatalf("added = %d, skipped = %d, merged = %d, want 0/1/0", added, skipped, len(merged))
		}
	})

	t.Run("existing sheet is copied not aliased", func(t *testing.T) {
		sheet := []domain.PricingEntry{{Model: "a/b", InputMicrosPerM: 1}}
		merged, _, _ := MergeSeedOnce(sheet, imported)
		merged[0].InputMicrosPerM = 42
		if sheet[0].InputMicrosPerM != 1 {
			t.Fatal("caller slice was mutated through the merged result")
		}
	})
}
