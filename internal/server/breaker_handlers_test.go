package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/storage/analytics"
	"github.com/dickymuliafiqri/firefly/internal/security/auth"
	"github.com/dickymuliafiqri/firefly/internal/transport/upstream"
)

func TestBreakerHandlers_GetAndUpdate(t *testing.T) {
	reg := upstream.NewBreakerRegistry(upstream.BreakerConfig{})
	analyticsStore, _ := analytics.NewStore("")
	authMgr := auth.NewManager("", "test-admin-secret", "12345678")

	deps := RouterDeps{
		Breakers:   reg,
		Analytics:  analyticsStore,
		Auth:       authMgr,
		AdminToken: "test-admin-secret",
	}

	// 1. Initial GET /api/breakers
	req := httptest.NewRequest(http.MethodGet, "/api/breakers", nil)
	w := httptest.NewRecorder()
	deps.handleGetBreakers(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// 2. Unauthorized update
	updateReq := httptest.NewRequest(http.MethodPost, "/api/breakers", strings.NewReader(`{"name":"openai-main","state":"OPEN"}`))
	w = httptest.NewRecorder()
	deps.handleUpdateBreaker(w, updateReq)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthorized update, got %d", w.Code)
	}

	// 3. Authorized update with admin bearer token
	updateReq = httptest.NewRequest(http.MethodPost, "/api/breakers", strings.NewReader(`{"name":"openai-main","state":"OPEN"}`))
	updateReq.Header.Set("Authorization", "Bearer test-admin-secret")
	w = httptest.NewRecorder()
	deps.handleUpdateBreaker(w, updateReq)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for authorized update, got %d: %s", w.Code, w.Body.String())
	}

	// Verify breaker registry state is OPEN
	if reg.StateFor("openai-main") != upstream.StateOpen {
		t.Fatalf("expected breaker registry state OPEN, got %v", reg.StateFor("openai-main"))
	}

	// Verify GET returns OPEN
	req = httptest.NewRequest(http.MethodGet, "/api/breakers", nil)
	w = httptest.NewRecorder()
	deps.handleGetBreakers(w, req)
	var getResp struct {
		Status   string            `json:"status"`
		Breakers map[string]string `json:"breakers"`
	}
	if err := json.NewDecoder(w.Body).Decode(&getResp); err != nil {
		t.Fatalf("decode get breakers: %v", err)
	}
	if getResp.Breakers["openai-main"] != "OPEN" {
		t.Fatalf("expected openai-main to be OPEN, got %s", getResp.Breakers["openai-main"])
	}

	// 4. Update back to CLOSED
	updateReq = httptest.NewRequest(http.MethodPost, "/api/breakers", strings.NewReader(`{"name":"openai-main","state":"CLOSED"}`))
	updateReq.Header.Set("Authorization", "Bearer test-admin-secret")
	w = httptest.NewRecorder()
	deps.handleUpdateBreaker(w, updateReq)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if reg.StateFor("openai-main") != upstream.StateClosed {
		t.Fatalf("expected breaker registry state CLOSED, got %v", reg.StateFor("openai-main"))
	}
}

func TestHistoryHandlers_Lifecycle(t *testing.T) {
	ctx := context.Background()
	analyticsStore, _ := analytics.NewStore("")
	authMgr := auth.NewManager("", "test-admin-secret", "12345678")

	deps := RouterDeps{
		Analytics:  analyticsStore,
		Auth:       authMgr,
		AdminToken: "test-admin-secret",
	}

	// Record a log
	_ = analyticsStore.Record(ctx, analytics.RequestLog{
		ID:            "req-hist-1",
		Timestamp:     time.Now().UnixMilli(),
		Model:         "gpt-4o",
		Upstream:      "openai-main",
		Tenant:        "demo",
		Status:        200,
		TokensIn:      10,
		TokensOut:     20,
		Tokens:        30,
		EstimatedCost: 0.0002,
	})

	// 1. GET /api/history
	req := httptest.NewRequest(http.MethodGet, "/api/history?limit=10", nil)
	w := httptest.NewRecorder()
	deps.handleGetHistory(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var histResp struct {
		Status  string     `json:"status"`
		History []LiveLog `json:"history"`
	}
	if err := json.NewDecoder(w.Body).Decode(&histResp); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(histResp.History) != 1 || histResp.History[0].ID != "req-hist-1" {
		t.Fatalf("expected 1 history record, got %+v", histResp.History)
	}

	// 2. DELETE /api/history (Unauthorized)
	delReq := httptest.NewRequest(http.MethodDelete, "/api/history", nil)
	w = httptest.NewRecorder()
	deps.handleDeleteHistory(w, delReq)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthorized delete, got %d", w.Code)
	}

	// 3. DELETE /api/history (Authorized)
	delReq = httptest.NewRequest(http.MethodDelete, "/api/history", nil)
	delReq.Header.Set("Authorization", "Bearer test-admin-secret")
	w = httptest.NewRecorder()
	deps.handleDeleteHistory(w, delReq)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// 4. Verify history is empty
	req = httptest.NewRequest(http.MethodGet, "/api/history", nil)
	w = httptest.NewRecorder()
	deps.handleGetHistory(w, req)
	var histEmpty struct {
		History []LiveLog `json:"history"`
	}
	_ = json.NewDecoder(w.Body).Decode(&histEmpty)
	if len(histEmpty.History) != 0 {
		t.Fatalf("expected 0 history records after delete, got %d", len(histEmpty.History))
	}
}
