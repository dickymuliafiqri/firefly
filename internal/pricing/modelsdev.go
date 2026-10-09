// Package pricing imports the public models.dev model catalog and seeds the
// gateway's local price sheet (domain.PricingTable). The fetched catalog is
// cached in memory for dashboard browsing only: it is never a runtime
// dependency, and every cost decision reads the local sheet, so an
// unreachable models.dev can never affect request handling.
package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

// CatalogURL is the public models.dev API endpoint. It is a var, not a
// const, so tests can point the fetcher at a local server.
var CatalogURL = "https://models.dev/api.json"

const (
	// FetchTimeout bounds one catalog fetch, including DNS, TLS, and body.
	FetchTimeout = 30 * time.Second
	// MaxCatalogBytes bounds the response body so a compromised or bloated
	// endpoint cannot exhaust memory. The real catalog is single-digit MB.
	MaxCatalogBytes = 64 << 20
)

// catalogClient is deliberately separate from the upstream connection pool:
// a models.dev fetch is an operator action, not inference traffic, and must
// never share — or starve — the pool that serves live streams.
var catalogClient = &http.Client{
	Timeout: FetchTimeout,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// rawCatalog mirrors the subset of models.dev api.json the importer reads.
// Unknown fields are ignored: the catalog is a third-party document.
type rawCatalog map[string]rawProvider

type rawProvider struct {
	ID     string              `json:"id"`
	Name   string              `json:"name"`
	Models map[string]rawModel `json:"models"`
}

type rawModel struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Cost             *rawCost  `json:"cost"`
	Limit            *rawLimit `json:"limit"`
	CanonicalModelID string    `json:"canonical_model_id"`
}

type rawCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

type rawLimit struct {
	Context int `json:"context"`
	Output  int `json:"output"`
}

// CatalogEntry is one browsable model row served to the dashboard.
type CatalogEntry struct {
	Provider             string `json:"provider"`
	ModelID              string `json:"model_id"`
	Key                  string `json:"key"` // "provider/model_id"
	Name                 string `json:"name"`
	InputMicrosPerM      int64  `json:"input_micros_per_m"`
	OutputMicrosPerM     int64  `json:"output_micros_per_m"`
	CacheReadMicrosPerM  int64  `json:"cache_read_micros_per_m,omitempty"`
	CacheWriteMicrosPerM int64  `json:"cache_write_micros_per_m,omitempty"`
	ContextLimit         int    `json:"context_limit,omitempty"`
	OutputLimit          int    `json:"output_limit,omitempty"`
	CanonicalModelID     string `json:"canonical_model_id,omitempty"`
}

// Catalog is the parsed, in-memory snapshot of models.dev. It exists for
// browsing and seeding only and is never persisted.
type Catalog struct {
	FetchedAt time.Time      `json:"fetched_at"`
	Providers []string       `json:"providers"`
	Entries   []CatalogEntry `json:"entries"`
}

// CatalogCache holds the most recently fetched catalog behind an
// atomic.Pointer so dashboard reads are lock-free.
type CatalogCache struct {
	ptr atomic.Pointer[Catalog]
}

// NewCatalogCache returns an empty cache.
func NewCatalogCache() *CatalogCache {
	return &CatalogCache{}
}

// Load returns the cached catalog, or nil when nothing has been fetched yet.
func (c *CatalogCache) Load() *Catalog {
	if c == nil {
		return nil
	}
	return c.ptr.Load()
}

// Store replaces the cached catalog.
func (c *CatalogCache) Store(cat *Catalog) {
	if c == nil || cat == nil {
		return
	}
	c.ptr.Store(cat)
}

// FetchCatalog downloads and parses the models.dev catalog. Errors are
// descriptive and never partial: callers keep their previous state.
func FetchCatalog(ctx context.Context) (*Catalog, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CatalogURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build models.dev request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := catalogClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch models.dev catalog: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models.dev catalog returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxCatalogBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read models.dev catalog: %w", err)
	}
	if len(body) > MaxCatalogBytes {
		return nil, fmt.Errorf("models.dev catalog exceeds %d bytes", MaxCatalogBytes)
	}

	var raw rawCatalog
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode models.dev catalog: %w", err)
	}
	return buildCatalog(raw), nil
}

// maxPriceMicrosPerM clamps one parsed price at $1B per 1M tokens, far above
// any real listing, so ledger arithmetic stays sane on absurd input.
const maxPriceMicrosPerM int64 = 1_000_000_000_000

// buildCatalog flattens the raw document into sorted entries. Models without
// a cost object are skipped: there is no price to import.
func buildCatalog(raw rawCatalog) *Catalog {
	cat := &Catalog{FetchedAt: time.Now().UTC()}
	providerIDs := make([]string, 0, len(raw))
	for id := range raw {
		providerIDs = append(providerIDs, id)
	}
	sort.Strings(providerIDs)

	for _, providerID := range providerIDs {
		provider := raw[providerID]
		if len(provider.Models) == 0 {
			continue
		}
		cat.Providers = append(cat.Providers, providerID)
		modelIDs := make([]string, 0, len(provider.Models))
		for id := range provider.Models {
			modelIDs = append(modelIDs, id)
		}
		sort.Strings(modelIDs)
		for _, modelID := range modelIDs {
			m := provider.Models[modelID]
			if m.Cost == nil {
				continue
			}
			canonical := strings.TrimSpace(m.CanonicalModelID)
			if canonical == "" {
				canonical = strings.ToLower(providerID + "/" + modelID)
			}
			name := strings.TrimSpace(m.Name)
			if name == "" {
				name = modelID
			}
			entry := CatalogEntry{
				Provider:             providerID,
				ModelID:              modelID,
				Key:                  providerID + "/" + modelID,
				Name:                 name,
				InputMicrosPerM:      usdToMicros(m.Cost.Input),
				OutputMicrosPerM:     usdToMicros(m.Cost.Output),
				CacheReadMicrosPerM:  usdToMicros(m.Cost.CacheRead),
				CacheWriteMicrosPerM: usdToMicros(m.Cost.CacheWrite),
				CanonicalModelID:     canonical,
			}
			if m.Limit != nil {
				entry.ContextLimit = m.Limit.Context
				entry.OutputLimit = m.Limit.Output
			}
			cat.Entries = append(cat.Entries, entry)
		}
	}
	return cat
}

// usdToMicros converts a USD-per-1M-token price into integer micro-USD per
// 1M tokens. 1 USD per 1M tokens == 1 micro-USD per token, so the per-1M
// micro figure is the exact integer form of the provider's decimal price.
// Non-finite and negative values become 0 (unpriced).
func usdToMicros(usd float64) int64 {
	if usd <= 0 || usd != usd || usd > 1e18 {
		return 0
	}
	micros := int64(usd*1_000_000 + 0.5)
	if micros > maxPriceMicrosPerM {
		return maxPriceMicrosPerM
	}
	return micros
}

// PricingEntry converts a catalog row into a local price-sheet entry keyed
// "provider/model_id" and tagged with its canonical id, so a protocol that
// fronts several providers (antigravity) can resolve per-model prices.
func (e CatalogEntry) PricingEntry() domain.PricingEntry {
	return domain.PricingEntry{
		Model:                e.Key,
		InputMicrosPerM:      e.InputMicrosPerM,
		OutputMicrosPerM:     e.OutputMicrosPerM,
		CacheReadMicrosPerM:  e.CacheReadMicrosPerM,
		CacheWriteMicrosPerM: e.CacheWriteMicrosPerM,
		Source:               domain.PricingSourceModelsDev,
		CanonicalModelID:     e.CanonicalModelID,
	}
}
