package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/dickymuliafiqri/firefly/internal/adapter/antigravity"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/observability/usage"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/dickymuliafiqri/firefly/internal/security/auth"
	"github.com/dickymuliafiqri/firefly/internal/security/oauth"
)

// failingRefreshProvider stands in for a Google account whose refresh token has
// been revoked: the manager then cannot mint an access token, which is the state
// the quota endpoint has to report honestly.
type failingRefreshProvider struct{ mockOAuthProvider }

func (f *failingRefreshProvider) RefreshToken(
	_ context.Context, _ *domain.OAuthConnection,
) (*domain.OAuthToken, error) {
	return nil, context.DeadlineExceeded
}

// setupQuotaServer wires an OAuth manager holding the given connections plus an
// admin session token. The antigravity quota client is replaced by a stub, so no
// test ever reaches Google, and the process-wide quota cache starts empty.
//
// Not parallel-safe by design: it swaps the global provider registry and clears
// the global cache.
func setupQuotaServer(t *testing.T, conns ...*domain.OAuthConnection) (http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := oauth.NewStore(filepath.Join(dir, "oauth.json"))
	require.NoError(t, err)
	for _, c := range conns {
		require.NoError(t, store.Save(context.Background(), c))
	}
	mgr := oauth.NewManager(store)
	require.NoError(t, mgr.RegisterProvider(&failingRefreshProvider{mockOAuthProvider{name: "antigravity"}}))
	authMgr := auth.NewManager(dir, "admin-secret-pass", "default-pass")

	original, hadOriginal := quotaProviders["antigravity"]
	quotaProviders["antigravity"] = quotaProvider{
		fetch: func(_ context.Context, _ *http.Client, token, projectID string) *antigravity.Quota {
			quotaStubToken = token
			return &antigravity.Quota{
				Plan:       "Google AI Pro",
				PaidTierID: "gems-enterprise",
				ProjectID:  projectID,
				Models: []antigravity.ModelQuota{
					{Model: "gemini-3.8-flash", RemainingPct: 60, Total: 1000, Used: 400},
				},
				Windows: []antigravity.WindowQuota{
					{Family: "gemini", Window: "weekly", RemainingPct: 90, Total: 1000, Used: 100},
				},
			}
		},
		projectOf: antigravity.NormalizeProjectID,
	}
	t.Cleanup(func() {
		if hadOriginal {
			quotaProviders["antigravity"] = original
		} else {
			delete(quotaProviders, "antigravity")
		}
	})

	quotaCache.mu.Lock()
	quotaCache.entries = make(map[string]quotaCacheEntry)
	quotaCache.inflight = make(map[string]*sync.WaitGroup)
	quotaCache.mu.Unlock()

	srv := &Server{}
	handler := srv.buildHandler(RouterDeps{
		OAuthManager:           mgr,
		Auth:                   authMgr,
		Logger:                 slog.Default(),
		DisableGlobalAdmission: true,
	})
	session, err := authMgr.Authenticate(context.Background(), "admin-secret-pass")
	require.NoError(t, err)
	return handler, "Bearer " + session.Token
}

// quotaStubToken records the access token the stub quota client was handed.
var quotaStubToken string

func quotaTestConnection(id, provider string, expires time.Time) *domain.OAuthConnection {
	return &domain.OAuthConnection{
		ID:       id,
		Provider: provider,
		Email:    id + "@example.com",
		Token: domain.OAuthToken{
			AccessToken:  "access-" + id,
			RefreshToken: "refresh-" + id,
			ExpiresAt:    expires,
		},
		ProviderSpecificData: map[string]string{"project_id": "proj-" + id},
	}
}

func TestQuotaEndpoints_ListProviders(t *testing.T) {
	handler, admin := setupQuotaServer(t,
		quotaTestConnection("ag-1", "antigravity", time.Now().Add(time.Hour)),
		quotaTestConnection("cl-1", "cline", time.Now().Add(time.Hour)),
	)

	// Unauthorized.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/quota/providers", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	// Authorized: only the provider with a quota client is listed.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/quota/providers", nil)
	req.Header.Set("Authorization", admin)
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	body := gjson.ParseBytes(rec.Body.Bytes())
	require.True(t, body.Get("providers").IsArray())
	providers := body.Get("providers").Array()
	require.Len(t, providers, 1, "cline has no quota endpoint and must not be reported as zero")
	entry := providers[0]
	assert.Equal(t, "ag-1", entry.Get("connection_id").String())
	assert.Equal(t, "antigravity", entry.Get("provider").String())
	assert.Equal(t, "Google AI Pro", entry.Get("plan").String())
	assert.Equal(t, "proj-ag-1", entry.Get("project_id").String())
	assert.False(t, entry.Get("free_tier").Bool())
	assert.Equal(t, "access-ag-1", quotaStubToken, "the resolved access token is forwarded to the quota client")
	assert.Equal(t, int64(1), entry.Get("models.#").Int())
	assert.Equal(t, int64(1), entry.Get("windows.#").Int())
	assert.False(t, entry.Get("from_cache").Bool())
	assert.Contains(t, body.Get("supported").Raw, "antigravity")
}

func TestQuotaEndpoints_TTLCacheAndRefresh(t *testing.T) {
	handler, admin := setupQuotaServer(t,
		quotaTestConnection("ag-1", "antigravity", time.Now().Add(time.Hour)),
	)

	call := func(path string) *gjson.Result {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", admin)
		handler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		parsed := gjson.ParseBytes(rec.Body.Bytes())
		return &parsed
	}

	assert.False(t, call("/api/quota/providers").Get("providers.0.from_cache").Bool())
	assert.True(t, call("/api/quota/providers").Get("providers.0.from_cache").Bool(),
		"a second read inside the TTL is served from cache")
	assert.False(t, call("/api/quota/providers?refresh=1").Get("providers.0.from_cache").Bool(),
		"?refresh=1 bypasses the cache")
}

func TestQuotaEndpoints_GetProvider(t *testing.T) {
	handler, admin := setupQuotaServer(t,
		quotaTestConnection("ag-1", "antigravity", time.Now().Add(time.Hour)),
		quotaTestConnection("cl-1", "cline", time.Now().Add(time.Hour)),
	)

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", admin)
		handler.ServeHTTP(rec, req)
		return rec
	}

	rec := get("/api/quota/providers/ag-1")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ag-1", gjson.GetBytes(rec.Body.Bytes(), "connection_id").String())

	require.Equal(t, http.StatusNotFound, get("/api/quota/providers/missing").Code)
	require.Equal(t, http.StatusNotFound, get("/api/quota/providers/cl-1").Code)
}

func TestQuotaEndpoints_ExpiredTokenIsReported(t *testing.T) {
	handler, admin := setupQuotaServer(t,
		quotaTestConnection("ag-old", "antigravity", time.Now().Add(-time.Hour)),
	)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/quota/providers", nil)
	req.Header.Set("Authorization", admin)
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	entry := gjson.GetBytes(rec.Body.Bytes(), "providers.0")
	assert.True(t, entry.Get("token_expired").Bool())
	assert.Contains(t, entry.Get("message").String(), "reconnect")
	assert.Equal(t, int64(0), entry.Get("models.#").Int(), "no fabricated numbers")
	assert.Equal(t, int64(0), entry.Get("windows.#").Int())
}

func TestQuotaEndpoints_ConcurrentRefreshIsCoalesced(t *testing.T) {
	handler, admin := setupQuotaServer(t,
		quotaTestConnection("ag-1", "antigravity", time.Now().Add(time.Hour)),
	)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/quota/providers", nil)
			req.Header.Set("Authorization", admin)
			handler.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusOK, rec.Code)
		}()
	}
	wg.Wait()

	quotaCache.mu.Lock()
	defer quotaCache.mu.Unlock()
	assert.Empty(t, quotaCache.inflight, "the coalescing window must be released")
	assert.Len(t, quotaCache.entries, 1, "one cache entry per connection")
}

// seedQuota installs a quota snapshot for a connection without any network.
func seedQuota(id string, models ...antigravity.ModelQuota) {
	quotaCache.mu.Lock()
	defer quotaCache.mu.Unlock()
	quotaCache.entries[id] = quotaCacheEntry{
		fetchedAt: time.Now(),
		dto: QuotaProviderDTO{
			ConnectionID: id,
			Provider:     "antigravity",
			Plan:         "Google AI Pro",
			Models:       models,
			Windows:      []antigravity.WindowQuota{},
		},
	}
}

func clearQuotaCache() {
	quotaCache.mu.Lock()
	defer quotaCache.mu.Unlock()
	quotaCache.entries = make(map[string]quotaCacheEntry)
	quotaCache.inflight = make(map[string]*sync.WaitGroup)
}

func exhaustedTarget(ref, upstreamModel string) *domain.Target {
	return &domain.Target{
		Upstream:      &domain.Upstream{Name: "ag-upstream"},
		UpstreamModel: upstreamModel,
		CredentialRef: ref,
	}
}

func TestQuotaGate_BlocksOnlyOnPositiveEvidence(t *testing.T) {
	var deps RouterDeps
	now := time.Now()

	// No snapshot at all: route it.
	clearQuotaCache()
	_, blocked := deps.quotaBlocked(exhaustedTarget("oauth:ag-1", "gemini-3.8-flash"), now)
	assert.False(t, blocked, "no quota data must never block a route")

	// Snapshot without the model: route it.
	seedQuota("ag-1", antigravity.ModelQuota{Model: "claude-sonnet-4-6", RemainingPct: 10})
	_, blocked = deps.quotaBlocked(exhaustedTarget("oauth:ag-1", "gemini-3.8-flash"), now)
	assert.False(t, blocked, "a model upstream does not meter is not a block")

	// Non-OAuth credential: the gate does not apply.
	seedQuota("ag-1", antigravity.ModelQuota{Model: "gemini-3.8-flash", Exhausted: true,
		ResetAt: now.Add(time.Hour).Format(time.RFC3339)})
	_, blocked = deps.quotaBlocked(exhaustedTarget("sk-gw-plain", "gemini-3.8-flash"), now)
	assert.False(t, blocked, "a non-OAuth credential has no provider quota")

	// Exhausted with a future reset: blocked, with the reset surfaced.
	reset, blocked := deps.quotaBlocked(exhaustedTarget("oauth:ag-1", "gemini-3.8-flash"), now)
	require.True(t, blocked)
	assert.True(t, reset.After(now), "the client is told when to come back")

	// Exhausted but the reset already passed: the picture is stale, route it.
	seedQuota("ag-1", antigravity.ModelQuota{Model: "gemini-3.8-flash", Exhausted: true,
		ResetAt: now.Add(-time.Minute).Format(time.RFC3339)})
	_, blocked = deps.quotaBlocked(exhaustedTarget("oauth:ag-1", "gemini-3.8-flash"), now)
	assert.False(t, blocked, "a reset in the past is not evidence")

	// Exhausted with an unreadable reset: block conservatively, no reset claim.
	reset, blocked = func() (time.Time, bool) {
		seedQuota("ag-1", antigravity.ModelQuota{Model: "gemini-3.8-flash", Exhausted: true,
			ResetAt: "not-a-timestamp"})
		return deps.quotaBlocked(exhaustedTarget("oauth:ag-1", "gemini-3.8-flash"), now)
	}()
	require.True(t, blocked)
	assert.True(t, reset.IsZero(), "an unparseable reset time must not become a Retry-After claim")

	// A healthy model on the same account routes.
	seedQuota("ag-1",
		antigravity.ModelQuota{Model: "gemini-3.8-flash", Exhausted: true, ResetAt: now.Add(time.Hour).Format(time.RFC3339)},
		antigravity.ModelQuota{Model: "gemini-3.8-pro", RemainingPct: 40},
	)
	_, blocked = deps.quotaBlocked(exhaustedTarget("oauth:ag-1", "gemini-3.8-pro"), now)
	assert.False(t, blocked, "one exhausted model must not block its siblings")

	clearQuotaCache()
}

func TestQuotaGate_KeySlotRefWinsOverCredentialRef(t *testing.T) {
	var deps RouterDeps
	now := time.Now()
	clearQuotaCache()
	seedQuota("ag-1", antigravity.ModelQuota{Model: "gemini-3.8-flash", Exhausted: true,
		ResetAt: now.Add(time.Hour).Format(time.RFC3339)})

	target := exhaustedTarget("oauth:other", "gemini-3.8-flash")
	target.KeySlot = &domain.KeySlot{Ref: "oauth:ag-1"}
	_, blocked := deps.quotaBlocked(target, now)
	assert.True(t, blocked, "the key slot actually used decides the credential")

	target.KeySlot = &domain.KeySlot{Ref: "sk-gw-other"}
	_, blocked = deps.quotaBlocked(target, now)
	assert.False(t, blocked)
	clearQuotaCache()
}

func TestQuotaRateLimit_InvalidatesSnapshot(t *testing.T) {
	clearQuotaCache()
	seedQuota("ag-1", antigravity.ModelQuota{Model: "gemini-3.8-flash", RemainingPct: 50})
	seedQuota("ag-2", antigravity.ModelQuota{Model: "gemini-3.8-flash", RemainingPct: 50})

	// A nil manager keeps the invalidation local (no background refetch).
	RouterDeps{}.noteQuotaRateLimit("oauth:ag-1")
	_, ok := quotaEntryFor("ag-1")
	assert.False(t, ok, "a contradicted snapshot is dropped")
	_, ok = quotaEntryFor("ag-2")
	assert.True(t, ok, "other connections are untouched")

	// A non-OAuth credential has no quota to invalidate.
	RouterDeps{}.noteQuotaRateLimit("sk-gw-plain")
	clearQuotaCache()
}

// quotaForwardServer wires a data-plane server whose only model is served by two
// OAuth-backed upstreams through a failover combo, mirroring a real deployment
// where one Google account runs out of allowance and another does not.
func quotaForwardServer(t *testing.T, adapter ports.UpstreamAdapter) *Server {
	t.Helper()
	hash := auth.HashKey(testKey)
	snap := domain.NewCatalogSnapshot(
		1,
		map[string]*domain.Upstream{
			"ag-1": {Name: "ag-1", Protocol: domain.ProtocolOpenAI, BaseURL: "https://a/v1", CredentialRef: "oauth:ag-1"},
			"ag-2": {Name: "ag-2", Protocol: domain.ProtocolOpenAI, BaseURL: "https://b/v1", CredentialRef: "oauth:ag-2"},
		},
		[]string{"ag-1", "ag-2"},
		map[string]*domain.ModelEntry{
			"flash-a": {PublicName: "flash-a", Upstream: "ag-1", UpstreamModel: "gemini-3.8-flash", Enabled: true},
			"flash-b": {PublicName: "flash-b", Upstream: "ag-2", UpstreamModel: "gemini-3.8-flash", Enabled: true},
		},
		[]string{"flash-a", "flash-b"},
		map[string]*domain.Tenant{hash: {
			KeyHash: hash, Name: "alpha", Status: domain.TenantStatusActive,
			AllowedModels: []string{"fast"},
			RateLimit:     domain.RateLimit{RPS: 1000, Burst: 1000, MaxConcurrent: 100},
		}},
		[]string{hash},
		domain.WithCombos(
			map[string]*domain.Combo{
				"fast": {Name: "fast", Strategy: domain.RoutingStrategyFailover,
					Models: []string{"flash-a", "flash-b"}, Enabled: true},
			},
			[]string{"fast"},
		),
	)
	store := auth.NewStore(fakeProvider{snap})
	return newTestServer(t, RouterDeps{
		Snapshots:   fakeProvider{snap},
		TenantStore: store,
		Limiter:     limits.New(),
		Adapter:     adapter,
		Usage:       usage.NewCounters(),
		Logger:      discardLogger(),
	})
}

func postFast(t *testing.T, s *Server) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"fast","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+testKey)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// TestForward_QuotaExhaustedFailsOverToHealthyAccount is the user-visible payoff:
// a model the primary account is out of is served by the second account instead
// of failing the request.
func TestForward_QuotaExhaustedFailsOverToHealthyAccount(t *testing.T) {
	clearQuotaCache()
	seedQuota("ag-1", antigravity.ModelQuota{
		Model: "gemini-3.8-flash", Exhausted: true,
		ResetAt: time.Now().Add(2 * time.Hour).Format(time.RFC3339),
	})
	seedQuota("ag-2", antigravity.ModelQuota{Model: "gemini-3.8-flash", RemainingPct: 80})

	fa := &fakeAdapter{body: `{"id":"x","object":"chat.completion"}`}
	s := quotaForwardServer(t, fa)

	rec := postFast(t, s)
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	require.NotNil(t, fa.lastTarget)
	assert.Equal(t, "ag-2", fa.lastTarget.Upstream.Name, "the exhausted account must be skipped")
	clearQuotaCache()
}

// TestForward_QuotaExhaustedEverywhereIs429 pins the honest failure: when no
// account has allowance, the client gets 429 with a Retry-After derived from the
// provider's reset time — not a 503 that would suggest a broken deployment.
func TestForward_QuotaExhaustedEverywhereIs429(t *testing.T) {
	clearQuotaCache()
	reset := time.Now().Add(90 * time.Minute).Format(time.RFC3339)
	for _, id := range []string{"ag-1", "ag-2"} {
		seedQuota(id, antigravity.ModelQuota{Model: "gemini-3.8-flash", Exhausted: true, ResetAt: reset})
	}

	fa := &fakeAdapter{body: `{"id":"x"}`}
	s := quotaForwardServer(t, fa)

	rec := postFast(t, s)
	require.Equal(t, http.StatusTooManyRequests, rec.Code, "body=%s", rec.Body.String())
	assert.Nil(t, fa.lastTarget, "no doomed request may reach the upstream")
	assert.Contains(t, rec.Body.String(), "quota exhausted")

	retry, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	require.NoError(t, err, "a quota answer must tell the client when to come back")
	assert.Greater(t, retry, 60, "the provider's reset time drives Retry-After")
	assert.LessOrEqual(t, retry, 3600)
	clearQuotaCache()
}

// TestForward_NoQuotaDataRoutesNormally pins the safety property: with no quota
// snapshot at all, nothing is vetoed and the primary account is used, so the
// feature can never turn into a self-inflicted outage.
func TestForward_NoQuotaDataRoutesNormally(t *testing.T) {
	clearQuotaCache()
	fa := &fakeAdapter{body: `{"id":"x","object":"chat.completion"}`}
	s := quotaForwardServer(t, fa)

	rec := postFast(t, s)
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	require.NotNil(t, fa.lastTarget)
	assert.Equal(t, "ag-1", fa.lastTarget.Upstream.Name)
}
