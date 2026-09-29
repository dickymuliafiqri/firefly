package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/dickymuliafiqri/firefly/internal/adapter/antigravity"
	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/domain"
)

// Provider-side quota for OAuth-connected providers (Services → Quota).
//
// This is the *upstream* allowance, not the tenant ledger the Quota page has
// always shown: per model, per rolling window, straight from the provider's own
// quota API. The read-only property matters — none of these calls triggers a
// generation, so refreshing the page never spends the quota it reports.
//
// A small TTL cache with in-flight coalescing sits in front of the providers, so
// a dashboard poll (or several operators watching at once) costs one upstream
// call per TTL window instead of one per browser.

const (
	quotaCacheTTL     = 60 * time.Second
	quotaFetchTimeout = 15 * time.Second
	quotaMaxParallel  = 4
)

// DefaultQuotaPollInterval is how often the background poller refreshes the
// provider-quota picture. It is deliberately slower than the 60s cache TTL:
// the poller exists so routing has fresh data with no dashboard open, not to
// poll Google continuously. A tick that lands inside the TTL costs nothing.
const DefaultQuotaPollInterval = 2 * time.Minute

// quotaProvider is the per-provider seam: adding cline / codebuddy / grok-cli
// later means adding one entry here and one fetch function next to it.
type quotaProvider struct {
	// fetch fills the provider-side picture for one connection.
	fetch func(ctx context.Context, client *http.Client, token, projectID string) *antigravity.Quota
	// projectOf reads the companion project id from connection metadata.
	projectOf func(pds map[string]string) string
}

// quotaProviders maps a connection provider onto its quota client. A provider
// absent here is simply not listed: reporting "no quota endpoint" as a quota of
// zero would be a lie the operator cannot act on.
var quotaProviders = map[string]quotaProvider{
	"antigravity": {
		fetch: func(ctx context.Context, client *http.Client, token, projectID string) *antigravity.Quota {
			return antigravity.FetchQuota(ctx, client, antigravity.DefaultBaseURL(), token, projectID, antigravity.QuotaOptions{})
		},
		projectOf: antigravity.NormalizeProjectID,
	},
}

// supportsProviderQuota reports whether Firefly can read this provider's quota.
func supportsProviderQuota(provider string) bool {
	_, ok := quotaProviders[strings.ToLower(strings.TrimSpace(provider))]
	return ok
}

type QuotaProvidersResponse struct {
	Providers []QuotaProviderDTO `json:"providers"`
	FetchedAt string             `json:"fetched_at"`
	// Supported lists the providers with a quota client, so the dashboard can
	// explain an empty list ("nothing connected") versus a missing capability.
	Supported []string `json:"supported"`
}

// QuotaProviderDTO is one connection's provider-side quota.
type QuotaProviderDTO struct {
	ConnectionID string                    `json:"connection_id"`
	Provider     string                    `json:"provider"`
	Email        string                    `json:"email,omitempty"`
	Plan         string                    `json:"plan"`
	PaidTierID   string                    `json:"paid_tier_id,omitempty"`
	FreeTier     bool                      `json:"free_tier"`
	ProjectID    string                    `json:"project_id,omitempty"`
	TokenExpired bool                      `json:"token_expired"`
	FetchedAt    string                    `json:"fetched_at"`
	AgeSeconds   int64                     `json:"age_seconds"`
	FromCache    bool                      `json:"from_cache"`
	Message      string                    `json:"message,omitempty"`
	Models       []antigravity.ModelQuota  `json:"models"`
	Windows      []antigravity.WindowQuota `json:"windows"`
}

// quotaCacheEntry is a fetched picture plus its age.
type quotaCacheEntry struct {
	dto       QuotaProviderDTO
	fetchedAt time.Time
}

var quotaCache = struct {
	mu       sync.Mutex
	entries  map[string]quotaCacheEntry
	inflight map[string]*sync.WaitGroup
}{entries: make(map[string]quotaCacheEntry), inflight: make(map[string]*sync.WaitGroup)}

// cachedQuota returns a fresh-enough entry for a connection.
func cachedQuota(id string, now time.Time) (QuotaProviderDTO, bool) {
	quotaCache.mu.Lock()
	defer quotaCache.mu.Unlock()
	entry, ok := quotaCache.entries[id]
	if !ok || now.Sub(entry.fetchedAt) > quotaCacheTTL {
		return QuotaProviderDTO{}, false
	}
	dto := entry.dto
	dto.FromCache = true
	dto.AgeSeconds = int64(now.Sub(entry.fetchedAt).Seconds())
	return dto, true
}

func storeQuota(id string, dto QuotaProviderDTO, now time.Time) {
	quotaCache.mu.Lock()
	defer quotaCache.mu.Unlock()
	quotaCache.entries[id] = quotaCacheEntry{dto: dto, fetchedAt: now}
}

// acquireQuotaSlot coalesces concurrent refreshes of the same connection: the
// second caller waits for the first instead of firing a duplicate upstream call.
func acquireQuotaSlot(id string) (leader bool, wait func()) {
	quotaCache.mu.Lock()
	wg, running := quotaCache.inflight[id]
	if !running {
		wg = &sync.WaitGroup{}
		wg.Add(1)
		quotaCache.inflight[id] = wg
		quotaCache.mu.Unlock()
		return true, func() { releaseQuotaSlot(id) }
	}
	quotaCache.mu.Unlock()
	return false, wg.Wait
}

// releaseQuotaSlot ends the leader's coalescing window so the next caller
// refetches (or is served from the entry the leader stored).
func releaseQuotaSlot(id string) {
	quotaCache.mu.Lock()
	wg, running := quotaCache.inflight[id]
	delete(quotaCache.inflight, id)
	quotaCache.mu.Unlock()
	if running {
		wg.Done()
	}
}

// fetchConnectionQuota reads one connection's provider-side quota and folds the
// connection identity around it. Split from the HTTP layer so the shape can be
// tested against a stub host without reaching Google.
func (deps RouterDeps) fetchConnectionQuota(
	ctx context.Context, conn *domain.OAuthConnection, now time.Time, force bool,
) QuotaProviderDTO {
	dto := QuotaProviderDTO{
		ConnectionID: conn.ID,
		Provider:     conn.Provider,
		Email:        conn.Email,
		Plan:         "Unknown",
		Models:       []antigravity.ModelQuota{},
		Windows:      []antigravity.WindowQuota{},
	}
	if !force {
		if cached, ok := cachedQuota(conn.ID, now); ok {
			cached.Email = conn.Email
			return cached
		}
	}
	dto.FetchedAt = now.UTC().Format(time.RFC3339)

	// Coalesce concurrent misses: a dashboard poll (or two operators watching)
	// must cost one upstream call, not one per request.
	if leader, wait := acquireQuotaSlot(conn.ID); !leader {
		wait()
		if cached, ok := cachedQuota(conn.ID, time.Now()); ok {
			cached.Email = conn.Email
			return cached
		}
	} else {
		defer releaseQuotaSlot(conn.ID)
	}

	provider, ok := quotaProviders[strings.ToLower(conn.Provider)]
	if !ok {
		dto.Message = "no quota endpoint for provider " + conn.Provider
		storeQuota(conn.ID, dto, now)
		return dto
	}

	token, err := deps.OAuthManager.ResolveToken(ctx, "oauth:"+conn.ID)
	if err != nil || strings.TrimSpace(token) == "" {
		dto.TokenExpired = conn.Token.IsExpired(0)
		dto.Message = "OAuth token unavailable — reconnect this account to read its quota."
		storeQuota(conn.ID, dto, now)
		return dto
	}

	fetchCtx, cancel := context.WithTimeout(ctx, quotaFetchTimeout)
	defer cancel()
	client := deps.makeCheckClient(quotaFetchTimeout, "", "")

	quota := provider.fetch(fetchCtx, client, token, provider.projectOf(conn.ProviderSpecificData))
	dto.Plan = quota.Plan
	dto.PaidTierID = quota.PaidTierID
	dto.FreeTier = quota.FreeTier
	dto.ProjectID = quota.ProjectID
	dto.Message = quota.Message
	dto.Models = quota.Models
	dto.Windows = quota.Windows
	if dto.Models == nil {
		dto.Models = []antigravity.ModelQuota{}
	}
	if dto.Windows == nil {
		dto.Windows = []antigravity.WindowQuota{}
	}
	storeQuota(conn.ID, dto, now)
	return dto
}

// handleListProviderQuota answers GET /api/quota/providers for every connected
// provider Firefly can read. `?refresh=1` bypasses the TTL cache.
func (deps RouterDeps) handleListProviderQuota(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized: valid dashboard session or admin token required")
		return
	}
	if deps.OAuthManager == nil {
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI, "oauth subsystem is not configured")
		return
	}
	connections, err := deps.OAuthManager.ListConnections(r.Context())
	if err != nil {
		openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, "failed to list connections: "+err.Error())
		return
	}

	now := time.Now()
	force := r.URL.Query().Get("refresh") == "1"
	targets := make([]*domain.OAuthConnection, 0, len(connections))
	for _, c := range connections {
		if c != nil && supportsProviderQuota(c.Provider) {
			targets = append(targets, c)
		}
	}

	results := make([]QuotaProviderDTO, len(targets))
	group, groupCtx := errgroup.WithContext(r.Context())
	group.SetLimit(quotaMaxParallel)
	for i, conn := range targets {
		i, conn := i, conn
		group.Go(func() error {
			results[i] = deps.fetchConnectionQuota(groupCtx, conn, now, force)
			return nil
		})
	}
	_ = group.Wait()

	supported := make([]string, 0, len(quotaProviders))
	for name := range quotaProviders {
		supported = append(supported, name)
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(QuotaProvidersResponse{
		Providers: results,
		FetchedAt: now.UTC().Format(time.RFC3339),
		Supported: supported,
	})
}

// handleGetProviderQuota answers GET /api/quota/providers/{id} for one connection.
func (deps RouterDeps) handleGetProviderQuota(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized: valid dashboard session or admin token required")
		return
	}
	if deps.OAuthManager == nil {
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI, "oauth subsystem is not configured")
		return
	}
	id := r.PathValue("id")
	conn, err := deps.OAuthManager.ResolveConnection(r.Context(), "oauth:"+id)
	if err != nil || conn == nil {
		openai.WriteError(w, http.StatusNotFound, openai.TypeInvalidRequest, "oauth connection not found: "+id)
		return
	}
	if !supportsProviderQuota(conn.Provider) {
		openai.WriteError(w, http.StatusNotFound, openai.TypeInvalidRequest,
			"provider "+conn.Provider+" has no quota endpoint")
		return
	}

	force := r.URL.Query().Get("refresh") == "1"
	dto := deps.fetchConnectionQuota(r.Context(), conn, time.Now(), force)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(dto)
}
