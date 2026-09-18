package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/security/auth"
)

type mockSnapProvider struct {
	snap *domain.CatalogSnapshot
}

func (m mockSnapProvider) Current() *domain.CatalogSnapshot {
	return m.snap
}

func TestHandleClientUsage(t *testing.T) {
	apiKey := "sk-gw-client-test-key"
	hash := auth.HashKey(apiKey)
	usedCtr := new(atomic.Int64)
	usedCtr.Store(250000)
	exp := time.Now().Add(48 * time.Hour).UnixMilli()

	tenant := &domain.Tenant{
		APIKey:     apiKey,
		KeyHash:    hash,
		Name:       "acme-corp",
		Status:     domain.TenantStatusActive,
		MaxTokens:  1000000,
		UsedTokens: usedCtr,
		ExpiresAt:  exp,
		RateLimit:  domain.RateLimit{RPS: 50, Burst: 100, MaxConcurrent: 20},
	}

	snap := domain.NewCatalogSnapshot(1, nil, nil, nil, nil,
		map[string]*domain.Tenant{hash: tenant},
		[]string{hash})

	provider := mockSnapProvider{snap: snap}
	deps := RouterDeps{
		Snapshots:   provider,
		TenantStore: auth.NewStore(provider),
	}

	handler := deps.protected(http.HandlerFunc(deps.handleClientUsage))

	// 1. Unauthenticated request -> 401
	reqUnauth := httptest.NewRequest("GET", "/v1/usage", nil)
	recUnauth := httptest.NewRecorder()
	handler.ServeHTTP(recUnauth, reqUnauth)
	if recUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated request, got %d", recUnauth.Code)
	}

	// 2. Authenticated request -> 200 with usage details
	reqAuth := httptest.NewRequest("GET", "/v1/usage", nil)
	reqAuth.Header.Set("Authorization", "Bearer "+apiKey)
	recAuth := httptest.NewRecorder()
	handler.ServeHTTP(recAuth, reqAuth)
	if recAuth.Code != http.StatusOK {
		t.Fatalf("expected 200 for authenticated request, got %d: %s", recAuth.Code, recAuth.Body.String())
	}

	var resp TenantUsageResponse
	if err := json.Unmarshal(recAuth.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Name != "acme-corp" {
		t.Errorf("got name %q, want acme-corp", resp.Name)
	}
	if resp.MaxTokens != 1000000 {
		t.Errorf("got max_tokens %d, want 1000000", resp.MaxTokens)
	}
	if resp.UsedTokens != 250000 {
		t.Errorf("got used_tokens %d, want 250000", resp.UsedTokens)
	}
	if resp.RemainingTokens != 750000 {
		t.Errorf("got remaining_tokens %d, want 750000", resp.RemainingTokens)
	}
	if resp.PercentUsed != 25.0 {
		t.Errorf("got percent_used %f, want 25.0", resp.PercentUsed)
	}
	if resp.IsExpired {
		t.Errorf("expected is_expired false")
	}
}
