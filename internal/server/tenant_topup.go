package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/storage/turso"
)

// TenantTopupRequest carries balance extension parameters.
type TenantTopupRequest struct {
	APIKey     string `json:"api_key,omitempty"`
	TenantName string `json:"tenant_name,omitempty"`
	AddTokens  int64  `json:"add_tokens,omitempty"`
	ExtendDays int    `json:"extend_days,omitempty"`
	ResetUsed  bool   `json:"reset_used,omitempty"`
}

// TenantTopupResponse confirms topup execution.
type TenantTopupResponse struct {
	Success         bool   `json:"success"`
	Message         string `json:"message"`
	Name            string `json:"name"`
	APIKey          string `json:"api_key"`
	MaxTokens       int64  `json:"max_tokens"`
	UsedTokens      int64  `json:"used_tokens"`
	RemainingTokens int64  `json:"remaining_tokens"`
	ExpiresAt       *int64 `json:"expires_at,omitempty"`
	Status          string `json:"status"`
}

// handleTenantTopup enables automated webhook/payment recharge.
func (deps RouterDeps) handleTenantTopup(w http.ResponseWriter, r *http.Request) {
	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized admin access")
		return
	}

	var req TenantTopupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "parse JSON request: "+err.Error())
		return
	}

	snap := deps.currentSnapshot()
	if snap == nil {
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI, "snapshot not loaded")
		return
	}

	var targetTenant *domain.Tenant
	if req.APIKey != "" {
		if t, ok := snap.TenantByKey(req.APIKey); ok {
			targetTenant = t
		}
	}
	if targetTenant == nil && req.TenantName != "" {
		for _, key := range snap.TenantKeys() {
			if t, ok := snap.TenantByKey(key); ok && t.Name == req.TenantName {
				targetTenant = t
				break
			}
		}
	}

	if targetTenant == nil {
		openai.WriteError(w, http.StatusNotFound, openai.TypeInvalidRequest, "tenant not found")
		return
	}

	if req.AddTokens > 0 {
		targetTenant.MaxTokens += req.AddTokens
	}
	if req.ResetUsed && targetTenant.UsedTokens != nil {
		targetTenant.UsedTokens.Store(0)
	}
	if req.ExtendDays > 0 {
		now := time.Now().UnixMilli()
		baseTime := targetTenant.ExpiresAt
		if baseTime <= now {
			baseTime = now
		}
		targetTenant.ExpiresAt = baseTime + int64(req.ExtendDays)*24*3600*1000
	}

	if targetTenant.Status == domain.TenantStatusExhausted || targetTenant.Status == domain.TenantStatusExpired {
		targetTenant.Status = domain.TenantStatusActive
	}

	var used int64
	if targetTenant.UsedTokens != nil {
		used = targetTenant.UsedTokens.Load()
	}

	var expPtr *int64
	if targetTenant.ExpiresAt > 0 {
		v := targetTenant.ExpiresAt
		expPtr = &v
	}

	// Persist changes to database if Turso is configured
	tursoStore, err := deps.getTursoStore(r.Context())
	if err == nil && tursoStore != nil {
		_ = tursoStore.SaveTenant(r.Context(), &turso.TenantRecord{
			Name:       targetTenant.Name,
			APIKey:     targetTenant.APIKey,
			KeyHash:    targetTenant.KeyHash,
			Status:     string(targetTenant.Status),
			MaxTokens:  targetTenant.MaxTokens,
			UsedTokens: used,
			ExpiresAt:  expPtr,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(TenantTopupResponse{
		Success:         true,
		Message:         "tenant topped up successfully",
		Name:            targetTenant.Name,
		APIKey:          targetTenant.APIKey,
		MaxTokens:       targetTenant.MaxTokens,
		UsedTokens:      used,
		RemainingTokens: targetTenant.RemainingTokens(),
		ExpiresAt:       expPtr,
		Status:          string(targetTenant.Status),
	})
}
