package pricing

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

// fixtureCatalog is a trimmed models.dev api.json shape: two providers, one
// model without a cost object, one model without a canonical_model_id.
const fixtureCatalog = `{
  "openai": {
    "id": "openai",
    "name": "OpenAI",
    "models": {
      "gpt-4o": {
        "id": "gpt-4o",
        "name": "GPT-4o",
        "cost": {"input": 2.5, "output": 10, "cache_read": 1.25},
        "limit": {"context": 128000, "output": 16384},
        "canonical_model_id": "openai/gpt-4o"
      },
      "gpt-4o-mini": {
        "id": "gpt-4o-mini",
        "name": "GPT-4o mini",
        "cost": {"input": 0.15, "output": 0.6}
      }
    }
  },
  "google": {
    "id": "google",
    "name": "Google",
    "models": {
      "gemini-2.5-pro": {
        "id": "gemini-2.5-pro",
        "name": "Gemini 2.5 Pro",
        "cost": {"input": 1.25, "output": 10, "cache_read": 0.31, "cache_write": 1.25},
        "limit": {"context": 1048576, "output": 65536}
      },
      "embedding-001": {
        "id": "embedding-001",
        "name": "Embedding 001"
      }
    }
  }
}`

func TestBuildCatalog(t *testing.T) {
	var raw rawCatalog
	if err := json.Unmarshal([]byte(fixtureCatalog), &raw); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	cat := buildCatalog(raw)

	if cat.FetchedAt.IsZero() {
		t.Fatal("FetchedAt must be stamped")
	}
	if len(cat.Providers) != 2 || cat.Providers[0] != "google" || cat.Providers[1] != "openai" {
		t.Fatalf("providers = %v, want sorted [google openai]", cat.Providers)
	}
	// embedding-001 has no cost object and must be skipped.
	if len(cat.Entries) != 3 {
		t.Fatalf("entries = %d, want 3 (cost-less model skipped)", len(cat.Entries))
	}

	byKey := make(map[string]CatalogEntry, len(cat.Entries))
	for _, e := range cat.Entries {
		byKey[e.Key] = e
	}

	gpt := byKey["openai/gpt-4o"]
	if gpt.InputMicrosPerM != 2_500_000 || gpt.OutputMicrosPerM != 10_000_000 {
		t.Fatalf("gpt-4o prices = %d/%d, want 2500000/10000000", gpt.InputMicrosPerM, gpt.OutputMicrosPerM)
	}
	if gpt.CacheReadMicrosPerM != 1_250_000 {
		t.Fatalf("gpt-4o cache_read = %d, want 1250000", gpt.CacheReadMicrosPerM)
	}
	if gpt.CacheWriteMicrosPerM != 0 {
		t.Fatalf("gpt-4o cache_write = %d, want 0 (absent)", gpt.CacheWriteMicrosPerM)
	}
	if gpt.ContextLimit != 128000 || gpt.OutputLimit != 16384 {
		t.Fatalf("gpt-4o limits = %d/%d, want 128000/16384", gpt.ContextLimit, gpt.OutputLimit)
	}
	if gpt.CanonicalModelID != "openai/gpt-4o" {
		t.Fatalf("gpt-4o canonical = %q", gpt.CanonicalModelID)
	}

	// Missing canonical_model_id falls back to the lowercased key.
	mini := byKey["openai/gpt-4o-mini"]
	if mini.CanonicalModelID != "openai/gpt-4o-mini" {
		t.Fatalf("gpt-4o-mini canonical = %q, want fallback key", mini.CanonicalModelID)
	}

	gem := byKey["google/gemini-2.5-pro"]
	if gem.CacheWriteMicrosPerM != 1_250_000 {
		t.Fatalf("gemini cache_write = %d, want 1250000", gem.CacheWriteMicrosPerM)
	}
}

func TestUSDToMicros(t *testing.T) {
	tests := []struct {
		name string
		in   float64
		want int64
	}{
		{"zero", 0, 0},
		{"negative", -1.5, 0},
		{"nan", math.NaN(), 0},
		{"inf", math.Inf(1), 0},
		{"exact", 1, 1_000_000},
		{"fractional", 0.15, 150_000},
		{"rounds half up", 0.0000005, 1},
		{"large", 1234.5, 1_234_500_000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := usdToMicros(tc.in); got != tc.want {
				t.Fatalf("usdToMicros(%v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestCatalogEntryPricingEntry(t *testing.T) {
	e := CatalogEntry{
		Provider:            "openai",
		ModelID:             "gpt-4o",
		Key:                 "openai/gpt-4o",
		InputMicrosPerM:     2_500_000,
		OutputMicrosPerM:    10_000_000,
		CacheReadMicrosPerM: 1_250_000,
		CanonicalModelID:    "openai/gpt-4o",
	}
	got := e.PricingEntry()
	if got.Model != "openai/gpt-4o" {
		t.Fatalf("model = %q", got.Model)
	}
	if got.Source != domain.PricingSourceModelsDev {
		t.Fatalf("source = %q, want %q", got.Source, domain.PricingSourceModelsDev)
	}
	if got.InputMicrosPerM != 2_500_000 || got.OutputMicrosPerM != 10_000_000 || got.CacheReadMicrosPerM != 1_250_000 {
		t.Fatalf("prices not carried over: %+v", got)
	}
	if got.CanonicalModelID != "openai/gpt-4o" {
		t.Fatalf("canonical = %q", got.CanonicalModelID)
	}
}

func TestFetchCatalog(t *testing.T) {
	orig := CatalogURL
	t.Cleanup(func() { CatalogURL = orig })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fixtureCatalog))
	}))
	defer srv.Close()
	CatalogURL = srv.URL

	cat, err := FetchCatalog(context.Background())
	if err != nil {
		t.Fatalf("FetchCatalog: %v", err)
	}
	if len(cat.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(cat.Entries))
	}
}

func TestFetchCatalogErrors(t *testing.T) {
	orig := CatalogURL
	t.Cleanup(func() { CatalogURL = orig })

	t.Run("non-200", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		CatalogURL = srv.URL
		if _, err := FetchCatalog(context.Background()); err == nil {
			t.Fatal("expected error on non-200")
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("not json"))
		}))
		defer srv.Close()
		CatalogURL = srv.URL
		if _, err := FetchCatalog(context.Background()); err == nil {
			t.Fatal("expected error on invalid json")
		}
	})

	t.Run("unreachable", func(t *testing.T) {
		CatalogURL = "http://127.0.0.1:1"
		if _, err := FetchCatalog(context.Background()); err == nil {
			t.Fatal("expected error on unreachable host")
		}
	})
}

func TestCatalogCache(t *testing.T) {
	c := NewCatalogCache()
	if c.Load() != nil {
		t.Fatal("fresh cache must be empty")
	}
	cat := &Catalog{Providers: []string{"openai"}}
	c.Store(cat)
	if got := c.Load(); got != cat {
		t.Fatal("stored catalog must round-trip")
	}
	// Nil receiver and nil payload must not panic.
	var nilCache *CatalogCache
	if nilCache.Load() != nil {
		t.Fatal("nil cache load must be nil")
	}
	nilCache.Store(cat)
	c.Store(nil)
	if c.Load() != cat {
		t.Fatal("storing nil must not clear the cache")
	}
}
