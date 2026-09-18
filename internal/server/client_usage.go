package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/transport/httpx"
)

// TenantUsageResponse is the user-facing response shape for token quota inquiries.
type TenantUsageResponse struct {
	Object          string  `json:"object"`
	Name            string  `json:"name"`
	MaxTokens       int64   `json:"max_tokens"`
	UsedTokens      int64   `json:"used_tokens"`
	RemainingTokens int64   `json:"remaining_tokens"`
	PercentUsed     float64 `json:"percent_used"`
	ExpiresAt       *int64  `json:"expires_at,omitempty"`
	IsExpired       bool    `json:"is_expired"`
	Status          string  `json:"status"`
}

// handleClientUsage serves GET /v1/usage for tenant self-service balance inquiries.
func (deps RouterDeps) handleClientUsage(w http.ResponseWriter, r *http.Request) {
	tenant, ok := httpx.RequireTenant(w, r)
	if !ok {
		return
	}

	var used int64
	if tenant.UsedTokens != nil {
		used = tenant.UsedTokens.Load()
	}

	now := time.Now().UnixMilli()
	isExp := tenant.IsExpired(now)
	rem := tenant.RemainingTokens()

	var pct float64
	if tenant.MaxTokens > 0 {
		pct = (float64(used) / float64(tenant.MaxTokens)) * 100.0
		if pct > 100.0 {
			pct = 100.0
		}
	}

	var expPtr *int64
	if tenant.ExpiresAt > 0 {
		v := tenant.ExpiresAt
		expPtr = &v
	}

	resp := TenantUsageResponse{
		Object:          "tenant_usage",
		Name:            tenant.Name,
		MaxTokens:       tenant.MaxTokens,
		UsedTokens:      used,
		RemainingTokens: rem,
		PercentUsed:     pct,
		ExpiresAt:       expPtr,
		IsExpired:       isExp,
		Status:          string(tenant.Status),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
