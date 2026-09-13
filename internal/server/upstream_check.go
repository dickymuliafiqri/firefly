package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/auth"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/openai"
)

type UpstreamCheckRequest struct {
	Name      string `json:"name,omitempty"`
	KeyRef    string `json:"key_ref,omitempty"`
	Protocol  string `json:"protocol"` // "openai" or "anthropic"
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"api_key,omitempty"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
}

type UpstreamCheckResponse struct {
	Healthy    bool     `json:"healthy"`
	StatusCode int      `json:"status_code"`
	LatencyMs  int64    `json:"latency_ms"`
	Message    string   `json:"message"`
	ModelCount int      `json:"model_count,omitempty"`
	Models     []string `json:"models,omitempty"`
}

// handleOptionsUpstreamCheck serves CORS preflight requests for upstream health check.
func (deps RouterDeps) handleOptionsUpstreamCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
	w.WriteHeader(http.StatusNoContent)
}

// handleCheckUpstream actively tests connectivity and authentication against an upstream endpoint.
func (deps RouterDeps) handleCheckUpstream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if deps.AdminToken != "" {
		token, ok := auth.ExtractBearer(r.Header.Get("Authorization"))
		if !ok || token != deps.AdminToken {
			openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "invalid or missing admin token")
			return
		}
	}

	// Bound incoming payload size to 1 MB
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()

	var req UpstreamCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
			Healthy: false,
			Message: "invalid request payload: " + err.Error(),
		})
		return
	}

	baseURL := strings.TrimSpace(req.BaseURL)
	apiKey := strings.TrimSpace(req.APIKey)

	// If an existing upstream name is provided, resolve missing or masked fields from snapshot
	if req.Name != "" {
		snap := deps.currentSnapshot()
		if snap != nil {
			if existingUp, ok := snap.Upstream(req.Name); ok && existingUp != nil {
				if baseURL == "" {
					baseURL = existingUp.BaseURL
				}
				if req.Protocol == "" {
					req.Protocol = string(existingUp.Protocol)
				}
				if (apiKey == "" || isMasked(apiKey) || strings.HasPrefix(apiKey, "env:")) && existingUp.KeyRing != nil {
					var targetSlot *domain.KeySlot
					if req.KeyRef != "" {
						targetSlot = existingUp.KeyRing.SlotByRef(req.KeyRef)
					}
					if targetSlot == nil && isMasked(apiKey) {
						for _, slot := range existingUp.KeyRing.Slots {
							if slot != nil && maskSecret(slot.Secret) == apiKey {
								targetSlot = slot
								break
							}
						}
					}
					if targetSlot == nil {
						targetSlot = existingUp.KeyRing.PrimarySlot()
					}
					if targetSlot != nil && targetSlot.Secret != "" {
						apiKey = targetSlot.Secret
					}
				}
			}
		}
	}

	if baseURL == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
			Healthy: false,
			Message: "base_url is required",
		})
		return
	}

	parsedURL, err := url.Parse(baseURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
			Healthy: false,
			Message: "invalid base_url: must be a valid URL starting with http:// or https://",
		})
		return
	}

	// Timeout configuration
	timeout := 10 * time.Second
	if req.TimeoutMs > 0 {
		timeout = time.Duration(req.TimeoutMs) * time.Millisecond
		if timeout > 30*time.Second {
			timeout = 30 * time.Second
		} else if timeout < 1*time.Second {
			timeout = 1 * time.Second
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	protocol := strings.ToLower(strings.TrimSpace(req.Protocol))
	if protocol == "" {
		protocol = "openai"
	}

	var probeURL string
	trimmedBase := strings.TrimRight(baseURL, "/")

	if protocol == "anthropic" {
		probePath := "/v1/models"
		if strings.HasSuffix(trimmedBase, "/v1") {
			probePath = "/models"
		}
		probeURL = trimmedBase + probePath
	} else {
		// OpenAI compatible
		probePath := "/models"
		if strings.Contains(trimmedBase, "api.openai.com") && !strings.HasSuffix(trimmedBase, "/v1") {
			probePath = "/v1/models"
		}
		probeURL = trimmedBase + probePath
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
			Healthy: false,
			Message: "failed to construct probe request: " + err.Error(),
		})
		return
	}

	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", "firefly-healthcheck/1.0")

	if protocol == "anthropic" {
		httpReq.Header.Set("anthropic-version", "2023-06-01")
		if apiKey != "" {
			httpReq.Header.Set("x-api-key", apiKey)
		}
	} else {
		if apiKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+apiKey)
		}
	}

	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	start := time.Now()
	resp, err := client.Do(httpReq)
	latencyMs := time.Since(start).Milliseconds()

	if err != nil {
		msg := err.Error()
		if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
			msg = fmt.Sprintf("Health check timed out after %dms", latencyMs)
		} else if strings.Contains(msg, "connection refused") {
			msg = "Connection refused by upstream host. Ensure the server is running."
		} else if strings.Contains(msg, "no such host") {
			msg = "DNS lookup failed: host not found."
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
			Healthy:    false,
			StatusCode: 0,
			LatencyMs:  latencyMs,
			Message:    msg,
		})
		return
	}

	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var parsed struct {
			Data   []json.RawMessage `json:"data"`
			Models []json.RawMessage `json:"models"`
		}
		_ = json.Unmarshal(bodyBytes, &parsed)

		var modelIDs []string
		seen := make(map[string]bool)

		extractID := func(raw json.RawMessage) {
			if len(raw) == 0 {
				return
			}
			var s string
			if err := json.Unmarshal(raw, &s); err == nil {
				s = strings.TrimSpace(s)
				if s != "" && !seen[s] {
					seen[s] = true
					modelIDs = append(modelIDs, s)
				}
				return
			}
			var obj struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			if err := json.Unmarshal(raw, &obj); err == nil {
				id := strings.TrimSpace(obj.ID)
				if id == "" {
					id = strings.TrimSpace(obj.Name)
				}
				if id != "" && !seen[id] {
					seen[id] = true
					modelIDs = append(modelIDs, id)
				}
			}
		}

		for _, item := range parsed.Data {
			extractID(item)
		}
		for _, item := range parsed.Models {
			extractID(item)
		}

		slices.Sort(modelIDs)
		modelCount := len(modelIDs)
		if modelCount == 0 && len(parsed.Data) > 0 {
			modelCount = len(parsed.Data)
		}

		msg := fmt.Sprintf("Upstream is healthy (%dms)", latencyMs)
		if modelCount > 0 {
			msg = fmt.Sprintf("Upstream is healthy (%dms, %d models available)", latencyMs, modelCount)
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
			Healthy:    true,
			StatusCode: resp.StatusCode,
			LatencyMs:  latencyMs,
			Message:    msg,
			ModelCount: modelCount,
			Models:     modelIDs,
		})
		return
	}

	// 401 / 403 Authentication failure
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		var errResp struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			} `json:"error"`
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(bodyBytes, &errResp)
		errMsg := errResp.Error.Message
		if errMsg == "" {
			errMsg = errResp.Detail
		}
		if errMsg == "" {
			errMsg = "Invalid or expired API key"
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
			Healthy:    false,
			StatusCode: resp.StatusCode,
			LatencyMs:  latencyMs,
			Message:    fmt.Sprintf("Authentication failed (HTTP %d): %s", resp.StatusCode, errMsg),
		})
		return
	}

	// 404 Not Found
	if resp.StatusCode == http.StatusNotFound {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
			Healthy:    false,
			StatusCode: resp.StatusCode,
			LatencyMs:  latencyMs,
			Message:    "Endpoint not found (HTTP 404). Check Base URL path.",
		})
		return
	}

	// 5xx Upstream Server Error
	if resp.StatusCode >= 500 {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
			Healthy:    false,
			StatusCode: resp.StatusCode,
			LatencyMs:  latencyMs,
			Message:    fmt.Sprintf("Upstream server error (HTTP %d)", resp.StatusCode),
		})
		return
	}

	// Other status codes
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
		Healthy:    false,
		StatusCode: resp.StatusCode,
		LatencyMs:  latencyMs,
		Message:    fmt.Sprintf("Upstream returned HTTP %d", resp.StatusCode),
	})
}
