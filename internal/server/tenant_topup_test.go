package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/security/auth"
)

func TestHandleTenantTopup(t *testing.T) {
	apiKey := "sk-gw-recharge-key"
	hash := auth.HashKey(apiKey)
	usedCtr := new(atomic.Int64)
	usedCtr.Store(100000)
	pastExp := time.Now().Add(-24 * time.Hour).UnixMilli()

	tenant := &domain.Tenant{
		APIKey:     apiKey,
		KeyHash:    hash,
		Name:       "customer-1",
		Status:     domain.TenantStatusExhausted,
		MaxTokens:  100000,
		UsedTokens: usedCtr,
		ExpiresAt:  pastExp,
		RateLimit:  domain.RateLimit{RPS: 20, Burst: 40, MaxConcurrent: 10},
	}

	snap := domain.NewCatalogSnapshot(1, nil, nil, nil, nil,
		map[string]*domain.Tenant{hash: tenant},
		[]string{hash})

	provider := mockSnapProvider{snap: snap}
	deps := RouterDeps{
		AdminToken:  "super-secret-admin",
		Snapshots:   provider,
		TenantStore: auth.NewStore(provider),
	}

	handler := http.HandlerFunc(deps.handleTenantTopup)

	// 1. Unauthorized request without admin token -> 401
	payload := `{"api_key":"sk-gw-recharge-key","add_tokens":500000,"extend_days":30,"reset_used":true}`
	reqUnauth := httptest.NewRequest("POST", "/api/tenants/topup", bytes.NewBufferString(payload))
	recUnauth := httptest.NewRecorder()
	handler.ServeHTTP(recUnauth, reqUnauth)
	if recUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthorized admin, got %d", recUnauth.Code)
	}

	// 2. Authorized request with Bearer admin token -> 200
	reqAuth := httptest.NewRequest("POST", "/api/tenants/topup", bytes.NewBufferString(payload))
	reqAuth.Header.Set("Authorization", "Bearer super-secret-admin")
	recAuth := httptest.NewRecorder()
	handler.ServeHTTP(recAuth, reqAuth)
	if recAuth.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid topup, got %d: %s", recAuth.Code, recAuth.Body.String())
	}

	var resp TenantTopupResponse
	if err := json.Unmarshal(recAuth.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if !resp.Success {
		t.Errorf("expected success true")
	}
	if resp.MaxTokens != 600000 {
		t.Errorf("got max_tokens %d, want 600000", resp.MaxTokens)
	}
	if resp.UsedTokens != 0 {
		t.Errorf("got used_tokens %d, want 0 after reset", resp.UsedTokens)
	}
	if resp.RemainingTokens != 600000 {
		t.Errorf("got remaining_tokens %d, want 600000", resp.RemainingTokens)
	}
	if resp.Status != "active" {
		t.Errorf("got status %q, want active", resp.Status)
	}
	if resp.ExpiresAt == nil || *resp.ExpiresAt <= time.Now().UnixMilli() {
		t.Errorf("expected expires_at in future after 30 days extension")
	}
}
