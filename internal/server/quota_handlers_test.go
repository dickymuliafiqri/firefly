package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/dickymuliafiqri/firefly/internal/adapter/antigravity"
	"github.com/dickymuliafiqri/firefly/internal/domain"
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
