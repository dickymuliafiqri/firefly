package qoder

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

// modelCatalogTTL is how long a fetched catalog stays valid. Mirrors the 1h
// Kiro/9router catalog TTL.
const modelCatalogTTL = time.Hour

// modelCatalog holds the per-credential model_config blocks keyed by model key.
type modelCatalog struct {
	expiresAt time.Time
	// rawConfigs maps model key -> its verbatim model_config JSON block.
	rawConfigs map[string]json.RawMessage
	// models is the routable list (id + display name) for /v1/models.
	models []QoderModelInfo
}

// QoderModelInfo is a routable model entry.
type QoderModelInfo struct {
	ID          string
	Name        string
	ContextLen  int
	MaxOutput   int
	IsReasoning bool
	Hidden      bool
}

// modelCache caches catalogs per credential seed with single-flight fetching.
type modelCache struct {
	mu       sync.Mutex
	catalogs map[string]*modelCatalog
	inflight map[string]*sync.WaitGroup
}

func newModelCache() *modelCache {
	return &modelCache{
		catalogs: make(map[string]*modelCatalog),
		inflight: make(map[string]*sync.WaitGroup),
	}
}

// getModelConfig returns the verbatim model_config block for modelKey, fetching
// the catalog first if needed. Returns an error when the catalog can't be fetched
// or the key is unknown (Qoder silently downgrades on a wrong block, so a miss is
// a hard error).
func (c *modelCache) getModelConfig(ctx context.Context, client *http.Client, base string, creds cosyCreds, modelKey string) (json.RawMessage, error) {
	cat, err := c.resolve(ctx, client, base, creds, false)
	if err != nil {
		return nil, err
	}
	if cfg, ok := cat.rawConfigs[modelKey]; ok {
		return cfg, nil
	}
	// One forced refresh before giving up (cache may be stale/incomplete).
	cat, err = c.resolve(ctx, client, base, creds, true)
	if err != nil {
		return nil, err
	}
	if cfg, ok := cat.rawConfigs[modelKey]; ok {
		return cfg, nil
	}
	return nil, fmt.Errorf("qoder: model_config for %q not found in catalog", modelKey)
}

// listModels returns the routable model list for a credential (best-effort).
func (c *modelCache) listModels(ctx context.Context, client *http.Client, base string, creds cosyCreds) ([]QoderModelInfo, error) {
	cat, err := c.resolve(ctx, client, base, creds, false)
	if err != nil {
		return nil, err
	}
	return cat.models, nil
}

// resolve returns a cached catalog or fetches a fresh one, coalescing concurrent
// misses on the same credential.
func (c *modelCache) resolve(ctx context.Context, client *http.Client, base string, creds cosyCreds, force bool) (*modelCatalog, error) {
	key := stableHash16("qoder-catalog", creds.UserID, creds.AuthToken)

	c.mu.Lock()
	if !force {
		if cat, ok := c.catalogs[key]; ok && time.Now().Before(cat.expiresAt) {
			c.mu.Unlock()
			return cat, nil
		}
		if wg, ok := c.inflight[key]; ok {
			// Another goroutine is fetching; wait for it then read the cache.
			c.mu.Unlock()
			wg.Wait()
			c.mu.Lock()
			if cat, ok := c.catalogs[key]; ok && time.Now().Before(cat.expiresAt) {
				c.mu.Unlock()
				return cat, nil
			}
			c.mu.Unlock()
			// Fall through to fetch ourselves.
			return c.fetchAndStore(ctx, client, base, creds, key)
		}
	}
	wg := &sync.WaitGroup{}
	wg.Add(1)
	c.inflight[key] = wg
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.inflight, key)
		c.mu.Unlock()
		wg.Done()
	}()

	return c.fetchAndStore(ctx, client, base, creds, key)
}

func (c *modelCache) fetchAndStore(ctx context.Context, client *http.Client, base string, creds cosyCreds, key string) (*modelCatalog, error) {
	cat, err := fetchQoderCatalog(ctx, client, base, creds)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.catalogs[key] = cat
	c.mu.Unlock()
	return cat, nil
}

// ErrCatalogHTTP is returned by fetchQoderCatalog when the catalog endpoint
// rejects the request with a non-200 HTTP status. Carrying the status through
// lets the adapter relay genuine credential errors (401/403/429) into Layer 1
// (HandleKeyOutcome failover) instead of masking them as terminal HTTP 400.
type ErrCatalogHTTP struct {
	StatusCode int
	Message    string
}

func (e *ErrCatalogHTTP) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("qoder: model list returned %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("qoder: model list returned %d", e.StatusCode)
}

// fetchQoderCatalog performs the COSY-signed GET /model/list and parses the catalog.
func fetchQoderCatalog(ctx context.Context, client *http.Client, base string, creds cosyCreds) (*modelCatalog, error) {
	if creds.UserID == "" || creds.AuthToken == "" {
		return nil, fmt.Errorf("qoder: cannot fetch catalog without userId and token")
	}
	if base == "" {
		base = qoderInferenceBase(creds.AuthToken)
	}
	url := base + QoderModelListPath

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	headers, err := buildCosyHeaders(nil, url, creds)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		snip, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		msg := strings.TrimSpace(string(snip))
		return nil, &ErrCatalogHTTP{StatusCode: resp.StatusCode, Message: msg}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if !gjson.ValidBytes(body) {
		return nil, fmt.Errorf("qoder: model list returned invalid json")
	}

	chat := gjson.GetBytes(body, "chat")
	if !chat.IsArray() {
		return nil, fmt.Errorf("qoder: model list missing chat array")
	}

	cat := &modelCatalog{
		expiresAt:  time.Now().Add(modelCatalogTTL),
		rawConfigs: make(map[string]json.RawMessage),
	}
	chat.ForEach(func(_, entry gjson.Result) bool {
		key := entry.Get("key").String()
		if key == "" {
			return true
		}
		cat.rawConfigs[key] = json.RawMessage(entry.Raw)
		hidden := entry.Get("enable").Exists() && !entry.Get("enable").Bool()
		display := entry.Get("display_name").String()
		if display == "" {
			display = key
		}
		ctxLen := int(entry.Get("max_input_tokens").Int())
		if ctxLen == 0 {
			ctxLen = 131072
		}
		cat.models = append(cat.models, QoderModelInfo{
			ID:          key,
			Name:        display,
			ContextLen:  ctxLen,
			MaxOutput:   int(entry.Get("max_output_tokens").Int()),
			IsReasoning: entry.Get("is_reasoning").Bool(),
			Hidden:      hidden,
		})
		return true
	})

	return cat, nil
}
