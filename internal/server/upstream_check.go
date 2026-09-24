package server

import (
	"bytes"
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

	"github.com/dickymuliafiqri/firefly/internal/adapter/grok"
	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/adapter/opencode"
	"github.com/dickymuliafiqri/firefly/internal/adapter/qoder"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/transport/upstream"
	"github.com/dickymuliafiqri/firefly/internal/transport/warp"
	"github.com/tidwall/gjson"
)

type UpstreamCheckRequest struct {
	Name       string `json:"name,omitempty"`
	KeyRef     string `json:"key_ref,omitempty"`
	Protocol   string `json:"protocol"` // "openai" or "anthropic"
	BaseURL    string `json:"base_url"`
	APIKey     string `json:"api_key,omitempty"`
	TimeoutMs  int    `json:"timeout_ms,omitempty"`
	Model      string `json:"model,omitempty"`
	EgressMode string `json:"egress_mode,omitempty"`
	ProxyURL   string `json:"proxy_url,omitempty"`
}

type UpstreamCheckResponse struct {
	Healthy    bool     `json:"healthy"`
	StatusCode int      `json:"status_code"`
	LatencyMs  int64    `json:"latency_ms"`
	Message    string   `json:"message"`
	ModelCount int      `json:"model_count,omitempty"`
	Models     []string `json:"models,omitempty"`
	KeyRef     string   `json:"key_ref,omitempty"`
}

type UpstreamModelsRequest struct {
	Name       string `json:"name,omitempty"`
	KeyRef     string `json:"key_ref,omitempty"`
	Protocol   string `json:"protocol"`
	BaseURL    string `json:"base_url"`
	APIKey     string `json:"api_key,omitempty"`
	TimeoutMs  int    `json:"timeout_ms,omitempty"`
	EgressMode string `json:"egress_mode,omitempty"`
	ProxyURL   string `json:"proxy_url,omitempty"`
}

type UpstreamModelsResponse struct {
	Models     []string `json:"models"`
	ModelCount int      `json:"model_count"`
	LatencyMs  int64    `json:"latency_ms"`
	Message    string   `json:"message,omitempty"`
	KeyRef     string   `json:"key_ref,omitempty"`
}

// makeCheckClient builds an http.Client bound to the configured egress mode (warp, proxy, or direct).
func (deps RouterDeps) makeCheckClient(timeout time.Duration, egressMode, proxyURL string) *http.Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	tr := &http.Transport{
		MaxIdleConns:          50,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
	}
	warp.ConfigureTransportEgress(tr, warp.EgressConfig{
		Mode:        egressMode,
		ProxyURL:    proxyURL,
		WarpDialer:  deps.WarpManager,
		DialTimeout: timeout,
	})

	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// handleOptionsUpstreamCheck serves CORS preflight requests for upstream health check.
func (deps RouterDeps) handleOptionsUpstreamCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
	w.WriteHeader(http.StatusNoContent)
}

// extractUpstreamError extracts an error description from upstream JSON response body or plain text.
func extractUpstreamError(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	if msg := gjson.GetBytes(body, "error.message").String(); msg != "" {
		return msg
	}
	if msg := gjson.GetBytes(body, "message").String(); msg != "" {
		return msg
	}
	if msg := gjson.GetBytes(body, "detail").String(); msg != "" {
		return msg
	}
	if msg := gjson.GetBytes(body, "error").String(); msg != "" {
		return msg
	}
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) > 0 && len(trimmed) <= 300 && !strings.HasPrefix(trimmed, "<") {
		return trimmed
	}
	return ""
}

// handleCheckUpstream actively tests connectivity and authentication against an upstream endpoint.
func (deps RouterDeps) handleCheckUpstream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized: valid dashboard session or admin token required")
		return
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
	protocol := strings.ToLower(strings.TrimSpace(req.Protocol))
	if protocol == "codebuddy_cn" {
		protocol = "codebuddy-cn"
	} else if protocol == "codebuddy_intl" {
		protocol = "codebuddy-intl"
	} else if protocol == "grok_cli" || protocol == "grok" || protocol == "gcli" || protocol == "grok-build" {
		protocol = "grok-cli"
	} else if protocol == "qodercli" || protocol == "qoder-cli" {
		protocol = "qoder"
	}

	egressMode := strings.TrimSpace(req.EgressMode)
	proxyURL := strings.TrimSpace(req.ProxyURL)

	var existingUp *domain.Upstream
	var targetSlot *domain.KeySlot
	resolvedKeyRef := ""

	// If an existing upstream name is provided, resolve missing or masked fields from snapshot
	if req.Name != "" {
		snap := deps.currentSnapshot()
		if snap != nil {
			if up, ok := snap.Upstream(req.Name); ok && up != nil {
				existingUp = up
				if baseURL == "" {
					baseURL = existingUp.BaseURL
				}
				if protocol == "" {
					protocol = string(existingUp.Protocol)
				}
				if egressMode == "" && existingUp.EgressMode != "" {
					egressMode = existingUp.EgressMode
				}
				if proxyURL == "" && existingUp.ProxyURL != "" {
					proxyURL = existingUp.ProxyURL
				}
				if existingUp.KeyRing != nil {
					if req.KeyRef != "" {
						targetSlot = existingUp.KeyRing.SlotByRef(req.KeyRef)
					}
					if targetSlot == nil && apiKey != "" {
						targetSlot = existingUp.KeyRing.SlotByRef(apiKey)
					}
					if targetSlot == nil && isMasked(apiKey) {
						for _, slot := range existingUp.KeyRing.AllSlots() {
							if slot != nil && maskSecret(slot.Secret) == apiKey {
								targetSlot = slot
								break
							}
						}
					}
					// If no specific key slot was pinned, select an available key according to the KeyRing's load balancing strategy!
					if targetSlot == nil && (apiKey == "" || isMasked(apiKey) || strings.HasPrefix(apiKey, "env:") || strings.HasPrefix(apiKey, "oauth:")) {
						selected, err := existingUp.KeyRing.SelectKey(time.Now().UnixNano())
						if err == nil && selected != nil {
							targetSlot = selected
						} else {
							targetSlot = existingUp.KeyRing.PrimarySlot()
						}
					}
					if targetSlot != nil {
						resolvedKeyRef = targetSlot.Ref
						if targetSlot.Secret != "" {
							apiKey = targetSlot.Secret
						} else if targetSlot.Ref != "" {
							apiKey = targetSlot.Ref
						}
					}
				}
			}
		}
	}

	if resolvedKeyRef == "" && req.KeyRef != "" {
		resolvedKeyRef = req.KeyRef
	}
	if apiKey == "" && req.KeyRef != "" {
		apiKey = req.KeyRef
	}

	if protocol == "" {
		protocol = "openai"
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

	// Resolve OAuth dynamic token references
	if deps.OAuthManager != nil {
		if strings.HasPrefix(apiKey, "oauth:") {
			if tok, err := deps.OAuthManager.ResolveToken(ctx, apiKey); err == nil && tok != "" {
				apiKey = tok
			}
		} else if strings.HasPrefix(req.KeyRef, "oauth:") && (apiKey == "" || strings.HasPrefix(apiKey, "oauth:")) {
			if tok, err := deps.OAuthManager.ResolveToken(ctx, req.KeyRef); err == nil && tok != "" {
				apiKey = tok
			}
		} else if apiKey == "" && (protocol == "cline" || protocol == "antigravity" || strings.HasPrefix(protocol, "codebuddy")) {
			if conns, err := deps.OAuthManager.ListConnections(ctx); err == nil {
				for _, c := range conns {
					if c.Provider == protocol || (strings.HasPrefix(protocol, "codebuddy") && strings.HasPrefix(c.Provider, "codebuddy")) {
						if tok, err := deps.OAuthManager.ResolveToken(ctx, c.ID); err == nil && tok != "" {
							apiKey = tok
							break
						}
					}
				}
			}
		}
	}

	if protocol == "antigravity" {
		models := []string{
			"gemini-2.5-pro",
			"gemini-2.5-flash",
			"gemini-2.0-flash",
			"gemini-2.0-pro",
			"claude-3-7-sonnet",
			"claude-3-5-sonnet",
		}
		if req.Model != "" {
			reqModel := strings.TrimSpace(req.Model)
			found := false
			for _, m := range models {
				if strings.EqualFold(m, reqModel) {
					found = true
					break
				}
			}
			if found {
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
					Healthy:    true,
					StatusCode: 200,
					LatencyMs:  1,
					Message:    fmt.Sprintf("Antigravity Cloud Code supports model %q", reqModel),
					KeyRef:     resolvedKeyRef,
				})
			} else {
				// Custom / unlisted models are allowed: the curated list is not
				// exhaustive, so an unknown id is accepted and routed as-is.
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
					Healthy:    true,
					StatusCode: 200,
					LatencyMs:  1,
					Message:    fmt.Sprintf("Antigravity accepts custom model %q (not in the curated list; routed as-is)", reqModel),
					KeyRef:     resolvedKeyRef,
				})
			}
			return
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
			Healthy:    true,
			StatusCode: 200,
			LatencyMs:  1,
			Message:    fmt.Sprintf("Antigravity Cloud Code upstream (%d models available)", len(models)),
			ModelCount: len(models),
			Models:     models,
			KeyRef:     resolvedKeyRef,
		})
		return
	}

	if protocol == "grok-cli" {
		// When an apiKey is provided (e.g. testing an individual key or keyring check in Upstream Modal),
		// actively probe the live Grok CLI /models endpoint to verify credentials.
		if strings.TrimSpace(apiKey) != "" {
			client := deps.makeCheckClient(timeout, egressMode, proxyURL)
			start := time.Now()
			live, err := grok.FetchModels(ctx, client, baseURL, apiKey)
			latencyMs := time.Since(start).Milliseconds()

			if err != nil {
				statusCode := 0
				rawBody := err.Error()
				var httpErr *grok.HTTPError
				if errors.As(err, &httpErr) {
					statusCode = httpErr.StatusCode
					rawBody = httpErr.Body
				} else if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
					rawBody = fmt.Sprintf("Health check timed out after %dms", latencyMs)
				}

				errMsg := extractUpstreamError([]byte(rawBody))
				if errMsg == "" {
					errMsg = rawBody
				}

				var msg string
				switch {
				case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
					if errMsg == "" {
						errMsg = "Invalid, expired, or unauthorized API key"
					}
					msg = fmt.Sprintf("Authentication failed (HTTP %d): %s", statusCode, errMsg)
				case statusCode == http.StatusTooManyRequests:
					if errMsg == "" {
						errMsg = "Rate limit or quota exceeded"
					}
					msg = fmt.Sprintf("Rate limit exceeded (HTTP 429): %s", errMsg)
				case statusCode >= 500:
					if errMsg == "" {
						errMsg = http.StatusText(statusCode)
					}
					msg = fmt.Sprintf("Upstream server error (HTTP %d): %s", statusCode, errMsg)
				case statusCode == 0:
					msg = fmt.Sprintf("Connection error: %s", errMsg)
				default:
					msg = fmt.Sprintf("Upstream returned HTTP %d: %s", statusCode, errMsg)
				}

				if targetSlot != nil {
					if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
						targetSlot.Revoked.Store(true)
					} else if statusCode == http.StatusTooManyRequests && existingUp != nil && existingUp.KeyRing != nil {
						kr := &upstream.KeyRing{KeyRing: existingUp.KeyRing}
						kr.Handle429(targetSlot.Ref, "")
					}
				}

				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
					Healthy:    false,
					StatusCode: statusCode,
					LatencyMs:  latencyMs,
					Message:    msg,
					KeyRef:     resolvedKeyRef,
				})
				return
			}

			// Key is healthy and models were discovered live
			if reqModel := strings.TrimSpace(req.Model); reqModel != "" {
				known := false
				for _, m := range live {
					if strings.EqualFold(m, reqModel) {
						known = true
						break
					}
				}
				if !known {
					known = grok.SupportsModel(reqModel)
				}
				msg := fmt.Sprintf("Grok CLI supports model %q", reqModel)
				if !known {
					msg = fmt.Sprintf("Grok CLI accepts custom model %q (routed as-is)", reqModel)
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
					Healthy:    true,
					StatusCode: 200,
					LatencyMs:  latencyMs,
					Message:    msg,
					KeyRef:     resolvedKeyRef,
				})
				return
			}

			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
				Healthy:    true,
				StatusCode: 200,
				LatencyMs:  latencyMs,
				Message:    fmt.Sprintf("Grok CLI upstream (%d models available, live)", len(live)),
				ModelCount: len(live),
				Models:     live,
				KeyRef:     resolvedKeyRef,
			})
			return
		}

		// When no apiKey is provided, fall back to curated static list (discovery mode without credentials)
		staticModels := grok.SupportedModels()
		if reqModel := strings.TrimSpace(req.Model); reqModel != "" {
			known := false
			for _, m := range staticModels {
				if strings.EqualFold(m, reqModel) {
					known = true
					break
				}
			}
			if !known {
				known = grok.SupportsModel(reqModel)
			}
			msg := fmt.Sprintf("Grok CLI supports model %q", reqModel)
			if !known {
				msg = fmt.Sprintf("Grok CLI accepts custom model %q (not in the curated list; routed as-is)", reqModel)
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
				Healthy:    true,
				StatusCode: 200,
				LatencyMs:  1,
				Message:    msg,
				KeyRef:     resolvedKeyRef,
			})
			return
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
			Healthy:    true,
			StatusCode: 200,
			LatencyMs:  1,
			Message:    fmt.Sprintf("Grok CLI upstream (%d models available, curated)", len(staticModels)),
			ModelCount: len(staticModels),
			Models:     staticModels,
			KeyRef:     resolvedKeyRef,
		})
		return
	}

	if protocol == "qoder" {
		if strings.TrimSpace(apiKey) == "" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
				Healthy: false,
				Message: "Qoder requires a credential (pt-/jt-/dt- token) to verify; add a key first.",
				KeyRef:  resolvedKeyRef,
			})
			return
		}
		client := deps.makeCheckClient(timeout, egressMode, proxyURL)
		start := time.Now()
		live, err := qoder.FetchModels(ctx, client, baseURL, apiKey)
		latencyMs := time.Since(start).Milliseconds()
		if err != nil {
			if targetSlot != nil {
				low := strings.ToLower(err.Error())
				if strings.Contains(low, "userid") || strings.Contains(low, "exchange") || strings.Contains(low, "401") || strings.Contains(low, "403") {
					targetSlot.Revoked.Store(true)
				}
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
				Healthy:   false,
				LatencyMs: latencyMs,
				Message:   fmt.Sprintf("Qoder credential check failed: %s", err.Error()),
				KeyRef:    resolvedKeyRef,
			})
			return
		}
		if reqModel := strings.TrimSpace(req.Model); reqModel != "" {
			known := slices.ContainsFunc(live, func(m string) bool { return strings.EqualFold(m, reqModel) })
			msg := fmt.Sprintf("Qoder supports model %q", reqModel)
			if !known {
				msg = fmt.Sprintf("Qoder accepts model %q (not in the discovered list; routed as-is)", reqModel)
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
				Healthy: true, StatusCode: 200, LatencyMs: latencyMs, Message: msg, KeyRef: resolvedKeyRef,
			})
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
			Healthy:    true,
			StatusCode: 200,
			LatencyMs:  latencyMs,
			Message:    fmt.Sprintf("Qoder upstream healthy (%d models discovered, live)", len(live)),
			ModelCount: len(live),
			Models:     live,
			KeyRef:     resolvedKeyRef,
		})
		return
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

	var probeURL string
	trimmedBase := strings.TrimRight(baseURL, "/")
	reqModel := strings.TrimSpace(req.Model)

	// If no model was explicitly provided in the check request, resolve from the existing upstream's ProbeModel
	// or fallback to the first enabled catalog model targeting this upstream.
	if reqModel == "" && req.Name != "" {
		snap := deps.currentSnapshot()
		if snap != nil {
			if existingUp, ok := snap.Upstream(req.Name); ok && existingUp != nil {
				if existingUp.ProbeModel != "" {
					reqModel = existingUp.ProbeModel
				} else {
					for _, modelID := range snap.EnabledModels() {
						if m, ok := snap.Model(modelID); ok && m != nil && m.Upstream == existingUp.Name {
							reqModel = m.UpstreamModel
							if reqModel == "" {
								reqModel = m.PublicName
							}
							break
						}
					}
				}
			}
		}
	}

	if protocol == "opencode" || protocol == "opencode-go" {
		isFree := apiKey == "" || strings.EqualFold(apiKey, "public")
		if isFree && strings.Contains(trimmedBase, "/zen/go/v1") {
			trimmedBase = strings.Replace(trimmedBase, "/zen/go/v1", "/zen/v1", 1)
		} else if !isFree && strings.Contains(trimmedBase, "/zen/v1") && !strings.Contains(trimmedBase, "/zen/go/v1") {
			trimmedBase = strings.Replace(trimmedBase, "/zen/v1", "/zen/go/v1", 1)
		}
	}

	var httpReq *http.Request
	if reqModel != "" {
		// Specific model connectivity check via minimal inference request
		var probeBody []byte
		if protocol == "anthropic" {
			probePath := "/v1/messages"
			if strings.HasSuffix(trimmedBase, "/v1") {
				probePath = "/messages"
			}
			probeURL = trimmedBase + probePath
			probeBody, _ = json.Marshal(map[string]any{
				"model": reqModel,
				"messages": []map[string]string{
					{"role": "user", "content": "ping"},
				},
				"max_tokens": 10,
			})
		} else if protocol == "cline" {
			if strings.Contains(trimmedBase, "api.cline.bot") && !strings.HasSuffix(trimmedBase, "/api/v1") && !strings.HasSuffix(trimmedBase, "/v1") {
				trimmedBase += "/api/v1"
			}
			probeURL = trimmedBase + "/chat/completions"
			probeBody, _ = json.Marshal(map[string]any{
				"model": reqModel,
				"messages": []map[string]string{
					{"role": "user", "content": "ping"},
				},
				"max_tokens": 10,
			})
		} else if protocol == "opencode" || protocol == "opencode-go" {
			isFree := apiKey == "" || strings.EqualFold(apiKey, "public")
			if isFree && !opencode.IsResponsesModel(reqModel) {
				probePath := "/chat/completions"
				if !strings.HasSuffix(trimmedBase, "/v1") {
					probePath = "/v1/chat/completions"
				}
				probeURL = trimmedBase + probePath
				probeBody = opencode.OfficialTitleProbePayload(reqModel)
			} else if opencode.IsResponsesModel(reqModel) {
				probePath := "/responses"
				if !strings.HasSuffix(trimmedBase, "/v1") {
					probePath = "/v1/responses"
				}
				probeURL = trimmedBase + probePath
				if isFree {
					probeBody = opencode.OfficialResponsesProbePayload(reqModel)
				} else {
					probeBody, _ = json.Marshal(map[string]any{
						"model": reqModel,
						"input": []map[string]any{
							{
								"type": "message",
								"role": "user",
								"content": []map[string]string{
									{"type": "input_text", "text": "ping"},
								},
							},
						},
						"max_output_tokens": 16,
						"stream":            true,
						"store":             false,
					})
				}
			} else {
				probePath := "/chat/completions"
				if !strings.HasSuffix(trimmedBase, "/v1") {
					probePath = "/v1/chat/completions"
				}
				probeURL = trimmedBase + probePath
				probeBody, _ = json.Marshal(map[string]any{
					"model": reqModel,
					"messages": []map[string]string{
						{"role": "user", "content": "ping"},
					},
					"max_tokens": 16,
				})
			}
		} else {
			// OpenAI compatible (including codebuddy)
			if strings.Contains(strings.ToLower(reqModel), "embed") {
				probePath := "/embeddings"
				if strings.Contains(trimmedBase, "api.openai.com") && !strings.HasSuffix(trimmedBase, "/v1") {
					probePath = "/v1/embeddings"
				}
				probeURL = trimmedBase + probePath
				probeBody, _ = json.Marshal(map[string]any{
					"model": reqModel,
					"input": "ping",
				})
			} else {
				probePath := "/chat/completions"
				if strings.Contains(trimmedBase, "api.openai.com") && !strings.HasSuffix(trimmedBase, "/v1") {
					probePath = "/v1/chat/completions"
				}
				probeURL = trimmedBase + probePath
				probeBody, _ = json.Marshal(map[string]any{
					"model": reqModel,
					"messages": []map[string]string{
						{"role": "user", "content": "ping"},
					},
					"max_tokens": 10,
				})
			}
		}

		httpReq, err = http.NewRequestWithContext(ctx, http.MethodPost, probeURL, bytes.NewReader(probeBody))
		if err != nil {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
				Healthy: false,
				Message: "failed to construct model probe request: " + err.Error(),
			})
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
	} else {
		// General upstream Reachability & Model List Probe (GET /models)
		if protocol == "anthropic" {
			probePath := "/v1/models"
			if strings.HasSuffix(trimmedBase, "/v1") {
				probePath = "/models"
			}
			probeURL = trimmedBase + probePath
		} else if protocol == "cline" {
			if strings.Contains(trimmedBase, "api.cline.bot") && !strings.HasSuffix(trimmedBase, "/api/v1") && !strings.HasSuffix(trimmedBase, "/v1") {
				trimmedBase += "/api/v1"
			}
			probeURL = trimmedBase + "/models"
		} else if protocol == "opencode" || protocol == "opencode-go" {
			probePath := "/models"
			if !strings.HasSuffix(trimmedBase, "/v1") {
				probePath = "/v1/models"
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

		httpReq, err = http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
		if err != nil {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
				Healthy: false,
				Message: "failed to construct probe request: " + err.Error(),
			})
			return
		}
	}

	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", "firefly-healthcheck/1.0")

	if protocol == "anthropic" {
		httpReq.Header.Set("anthropic-version", "2023-06-01")
		if apiKey != "" {
			httpReq.Header.Set("x-api-key", apiKey)
		}
	} else if protocol == "opencode" || protocol == "opencode-go" {
		effectiveKey := apiKey
		if effectiveKey == "" || strings.EqualFold(effectiveKey, "public") {
			effectiveKey = "public"
		}
		httpReq.Header.Set("Authorization", "Bearer "+effectiveKey)
		httpReq.Header.Set("User-Agent", opencode.OpenCodeUserAgent)
		httpReq.Header.Set("x-opencode-client", "cli")
		httpReq.Header.Set("x-opencode-project", "global")
		httpReq.Header.Set("x-opencode-request", opencode.GenerateRequestID())
		httpReq.Header.Set("x-opencode-session", opencode.GenerateSessionID())
		if effectiveKey == "public" {
			httpReq.Header.Set("Accept", "text/event-stream")
		}
	} else {
		if apiKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+apiKey)
		}
		if protocol == "cline" {
			httpReq.Header.Set("HTTP-Referer", "https://cline.bot")
			httpReq.Header.Set("X-Title", "Cline")
			httpReq.Header.Set("X-PLATFORM", "Visual Studio Code")
			httpReq.Header.Set("X-CLIENT-TYPE", "VSCode Extension")
			httpReq.Header.Set("X-CLIENT-VERSION", "3.49.1")
			httpReq.Header.Set("X-CORE-VERSION", "3.49.1")
		}
	}

	client := deps.makeCheckClient(timeout, egressMode, proxyURL)

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
			KeyRef:     resolvedKeyRef,
		})
		return
	}

	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))

	// Update Layer 1 key status if a specific slot from an existing upstream was used
	if targetSlot != nil {
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			isFreeTierErr := strings.Contains(string(bodyBytes), "FreeTierError") || strings.Contains(string(bodyBytes), "free tier can only be used")
			isPublicKey := targetSlot.Ref == "public" || targetSlot.Secret == "public" || strings.HasPrefix(targetSlot.Ref, "public:")
			if !isFreeTierErr && !isPublicKey {
				targetSlot.Revoked.Store(true)
			}
		case http.StatusTooManyRequests:
			if existingUp != nil && existingUp.KeyRing != nil {
				retryAfter := resp.Header.Get("Retry-After")
				kr := &upstream.KeyRing{KeyRing: existingUp.KeyRing}
				kr.Handle429(targetSlot.Ref, retryAfter)
			}
		}
	}

	if reqModel != "" {
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
				Healthy:    true,
				StatusCode: resp.StatusCode,
				LatencyMs:  latencyMs,
				Message:    fmt.Sprintf("Model %q connected successfully (%dms)", reqModel, latencyMs),
				KeyRef:     resolvedKeyRef,
			})
			return
		}

		errMsg := extractUpstreamError(bodyBytes)
		var msg string
		switch {
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			if strings.Contains(string(bodyBytes), "FreeTierError") || strings.Contains(string(bodyBytes), "free tier can only be used") {
				errMsg = "OpenCode Free Tier is restricted by provider console to official client sessions. For production use or third-party proxies, configure an OpenCode Go subscription API key (https://opencode.ai/zen/go/v1)."
			} else if errMsg == "" {
				errMsg = "Invalid, expired, or unauthorized API key"
			}
			msg = fmt.Sprintf("Authentication failed (HTTP %d): %s", resp.StatusCode, errMsg)
		case resp.StatusCode == http.StatusNotFound:
			if errMsg == "" {
				errMsg = fmt.Sprintf("Model %q was not found or is unavailable on this upstream", reqModel)
			}
			msg = fmt.Sprintf("Model not found (HTTP 404): %s", errMsg)
		case resp.StatusCode == http.StatusTooManyRequests:
			if errMsg == "" {
				errMsg = "Rate limit or quota exceeded on upstream provider"
			}
			msg = fmt.Sprintf("Rate limit exceeded (HTTP 429): %s", errMsg)
		case resp.StatusCode == http.StatusBadRequest:
			if errMsg == "" {
				errMsg = "Invalid request or unsupported model parameters"
			}
			msg = fmt.Sprintf("Request rejected (HTTP 400): %s", errMsg)
		case resp.StatusCode >= 500:
			if errMsg == "" {
				errMsg = http.StatusText(resp.StatusCode)
			}
			msg = fmt.Sprintf("Upstream server error (HTTP %d): %s", resp.StatusCode, errMsg)
		default:
			if errMsg == "" {
				errMsg = http.StatusText(resp.StatusCode)
			}
			msg = fmt.Sprintf("Upstream returned HTTP %d: %s", resp.StatusCode, errMsg)
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
			Healthy:    false,
			StatusCode: resp.StatusCode,
			LatencyMs:  latencyMs,
			Message:    msg,
			KeyRef:     resolvedKeyRef,
		})
		return
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		modelIDs := parseModelIDsFromBody(bodyBytes)
		modelCount := len(modelIDs)

		msg := fmt.Sprintf("Upstream is reachable (%dms)", latencyMs)
		if modelCount > 0 {
			msg = fmt.Sprintf("Upstream is reachable (%dms, %d models discovered via /models). Set a probe model to verify live inference quota.", latencyMs, modelCount)
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
			Healthy:    true,
			StatusCode: resp.StatusCode,
			LatencyMs:  latencyMs,
			Message:    msg,
			ModelCount: modelCount,
			Models:     modelIDs,
			KeyRef:     resolvedKeyRef,
		})
		return
	}

	// 401 / 403 Authentication failure
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		errMsg := extractUpstreamError(bodyBytes)
		if strings.Contains(string(bodyBytes), "FreeTierError") || strings.Contains(string(bodyBytes), "free tier can only be used") {
			errMsg = "OpenCode Free Tier is restricted by provider console to official client sessions. For production use or third-party proxies, configure an OpenCode Go subscription API key (https://opencode.ai/zen/go/v1)."
		} else if errMsg == "" {
			errMsg = "Invalid or expired API key"
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(UpstreamCheckResponse{
			Healthy:    false,
			StatusCode: resp.StatusCode,
			LatencyMs:  latencyMs,
			Message:    fmt.Sprintf("Authentication failed (HTTP %d): %s", resp.StatusCode, errMsg),
			KeyRef:     resolvedKeyRef,
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
			KeyRef:     resolvedKeyRef,
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
			KeyRef:     resolvedKeyRef,
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
		KeyRef:     resolvedKeyRef,
	})
}

// parseModelIDsFromBody parses model IDs from OpenAI, Anthropic, or compatible models JSON payload.
func parseModelIDsFromBody(bodyBytes []byte) []string {
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

	if len(modelIDs) == 0 {
		var topArray []json.RawMessage
		if err := json.Unmarshal(bodyBytes, &topArray); err == nil {
			for _, item := range topArray {
				extractID(item)
			}
		}
	}

	slices.Sort(modelIDs)
	return modelIDs
}

// handleFetchUpstreamModels queries an upstream provider exclusively for its available models list (GET /models).
// It NEVER executes inference or consumes LLM generation tokens.
func (deps RouterDeps) handleFetchUpstreamModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized: valid dashboard session or admin token required")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()

	var req UpstreamModelsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(UpstreamModelsResponse{
			Message: "invalid request payload: " + err.Error(),
		})
		return
	}

	baseURL := strings.TrimSpace(req.BaseURL)
	apiKey := strings.TrimSpace(req.APIKey)
	protocol := strings.ToLower(strings.TrimSpace(req.Protocol))
	if protocol == "codebuddy_cn" {
		protocol = "codebuddy-cn"
	} else if protocol == "codebuddy_intl" {
		protocol = "codebuddy-intl"
	} else if protocol == "grok_cli" || protocol == "grok" || protocol == "gcli" || protocol == "grok-build" {
		protocol = "grok-cli"
	} else if protocol == "qodercli" || protocol == "qoder-cli" {
		protocol = "qoder"
	}

	egressMode := strings.TrimSpace(req.EgressMode)
	proxyURL := strings.TrimSpace(req.ProxyURL)

	var existingUp *domain.Upstream
	var targetSlot *domain.KeySlot
	resolvedKeyRef := ""

	if req.Name != "" {
		snap := deps.currentSnapshot()
		if snap != nil {
			if up, ok := snap.Upstream(req.Name); ok && up != nil {
				existingUp = up
				if baseURL == "" {
					baseURL = existingUp.BaseURL
				}
				if protocol == "" {
					protocol = string(existingUp.Protocol)
				}
				if egressMode == "" && existingUp.EgressMode != "" {
					egressMode = existingUp.EgressMode
				}
				if proxyURL == "" && existingUp.ProxyURL != "" {
					proxyURL = existingUp.ProxyURL
				}
				if existingUp.KeyRing != nil {
					if req.KeyRef != "" {
						targetSlot = existingUp.KeyRing.SlotByRef(req.KeyRef)
					}
					if targetSlot == nil && apiKey != "" {
						targetSlot = existingUp.KeyRing.SlotByRef(apiKey)
					}
					if targetSlot == nil && isMasked(apiKey) {
						for _, slot := range existingUp.KeyRing.AllSlots() {
							if slot != nil && maskSecret(slot.Secret) == apiKey {
								targetSlot = slot
								break
							}
						}
					}
					// If no specific key slot was pinned, select an available key according to the KeyRing's load balancing strategy!
					if targetSlot == nil && (apiKey == "" || isMasked(apiKey) || strings.HasPrefix(apiKey, "env:") || strings.HasPrefix(apiKey, "oauth:")) {
						selected, err := existingUp.KeyRing.SelectKey(time.Now().UnixNano())
						if err == nil && selected != nil {
							targetSlot = selected
						} else {
							targetSlot = existingUp.KeyRing.PrimarySlot()
						}
					}
					if targetSlot != nil {
						resolvedKeyRef = targetSlot.Ref
						if targetSlot.Secret != "" {
							apiKey = targetSlot.Secret
						} else if targetSlot.Ref != "" {
							apiKey = targetSlot.Ref
						}
					}
				}
			}
		}
	}

	if resolvedKeyRef == "" && req.KeyRef != "" {
		resolvedKeyRef = req.KeyRef
	}
	if apiKey == "" && req.KeyRef != "" {
		apiKey = req.KeyRef
	}

	if protocol == "" {
		protocol = "openai"
	}

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

	// Resolve OAuth dynamic token references
	if deps.OAuthManager != nil {
		if strings.HasPrefix(apiKey, "oauth:") {
			if tok, err := deps.OAuthManager.ResolveToken(ctx, apiKey); err == nil && tok != "" {
				apiKey = tok
			}
		} else if strings.HasPrefix(req.KeyRef, "oauth:") && (apiKey == "" || strings.HasPrefix(apiKey, "oauth:")) {
			if tok, err := deps.OAuthManager.ResolveToken(ctx, req.KeyRef); err == nil && tok != "" {
				apiKey = tok
			}
		} else if apiKey == "" && (protocol == "cline" || protocol == "antigravity" || strings.HasPrefix(protocol, "codebuddy")) {
			if conns, err := deps.OAuthManager.ListConnections(ctx); err == nil {
				for _, c := range conns {
					if c.Provider == protocol || (strings.HasPrefix(protocol, "codebuddy") && strings.HasPrefix(c.Provider, "codebuddy")) {
						if tok, err := deps.OAuthManager.ResolveToken(ctx, c.ID); err == nil && tok != "" {
							apiKey = tok
							break
						}
					}
				}
			}
		}
	}

	if protocol == "antigravity" {
		models := []string{
			"gemini-2.5-pro",
			"gemini-2.5-flash",
			"gemini-2.0-flash",
			"gemini-1.5-pro",
			"gemini-1.5-flash",
			"claude-3-5-sonnet-20241022",
			"claude-3-7-sonnet-20250219",
			"claude-3-5-haiku-20241022",
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(UpstreamModelsResponse{
			Models:     models,
			ModelCount: len(models),
			LatencyMs:  1,
			Message:    fmt.Sprintf("Antigravity Cloud Code (%d models available)", len(models)),
			KeyRef:     resolvedKeyRef,
		})
		return
	}

	if protocol == "grok-cli" {
		if strings.TrimSpace(apiKey) != "" {
			client := deps.makeCheckClient(timeout, egressMode, proxyURL)
			start := time.Now()
			live, err := grok.FetchModels(ctx, client, baseURL, apiKey)
			latencyMs := time.Since(start).Milliseconds()
			if err == nil && len(live) > 0 {
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(UpstreamModelsResponse{
					Models:     live,
					ModelCount: len(live),
					LatencyMs:  latencyMs,
					Message:    fmt.Sprintf("Discovered %d models live from Grok CLI", len(live)),
					KeyRef:     resolvedKeyRef,
				})
				return
			}
		}
		staticModels := grok.SupportedModels()
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(UpstreamModelsResponse{
			Models:     staticModels,
			ModelCount: len(staticModels),
			LatencyMs:  1,
			Message:    fmt.Sprintf("Grok CLI (%d models available, curated)", len(staticModels)),
			KeyRef:     resolvedKeyRef,
		})
		return
	}

	if protocol == "qoder" {
		if strings.TrimSpace(apiKey) == "" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(UpstreamModelsResponse{
				Message: "Qoder requires a credential (pt-/jt-/dt- token) to discover models; add a key first.",
				KeyRef:  resolvedKeyRef,
			})
			return
		}
		client := deps.makeCheckClient(timeout, egressMode, proxyURL)
		start := time.Now()
		live, err := qoder.FetchModels(ctx, client, baseURL, apiKey)
		latencyMs := time.Since(start).Milliseconds()
		if err != nil {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(UpstreamModelsResponse{
				LatencyMs: latencyMs,
				Message:   fmt.Sprintf("Failed to discover Qoder models: %s", err.Error()),
				KeyRef:    resolvedKeyRef,
			})
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(UpstreamModelsResponse{
			Models:     live,
			ModelCount: len(live),
			LatencyMs:  latencyMs,
			Message:    fmt.Sprintf("Discovered %d models live from Qoder", len(live)),
			KeyRef:     resolvedKeyRef,
		})
		return
	}

	if baseURL == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(UpstreamModelsResponse{
			Message: "base_url is required",
		})
		return
	}

	parsedURL, err := url.Parse(baseURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(UpstreamModelsResponse{
			Message: "invalid base_url: must be a valid URL starting with http:// or https://",
		})
		return
	}

	trimmedBase := strings.TrimRight(baseURL, "/")
	if protocol == "opencode" || protocol == "opencode-go" {
		isFree := apiKey == "" || strings.EqualFold(apiKey, "public")
		if isFree && strings.Contains(trimmedBase, "/zen/go/v1") {
			trimmedBase = strings.Replace(trimmedBase, "/zen/go/v1", "/zen/v1", 1)
		} else if !isFree && strings.Contains(trimmedBase, "/zen/v1") && !strings.Contains(trimmedBase, "/zen/go/v1") {
			trimmedBase = strings.Replace(trimmedBase, "/zen/v1", "/zen/go/v1", 1)
		}
	}
	var probeURL string
	if protocol == "anthropic" {
		probePath := "/v1/models"
		if strings.HasSuffix(trimmedBase, "/v1") {
			probePath = "/models"
		}
		probeURL = trimmedBase + probePath
	} else if protocol == "cline" {
		if strings.Contains(trimmedBase, "api.cline.bot") && !strings.HasSuffix(trimmedBase, "/api/v1") && !strings.HasSuffix(trimmedBase, "/v1") {
			trimmedBase += "/api/v1"
		}
		probeURL = trimmedBase + "/models"
	} else if protocol == "opencode" || protocol == "opencode-go" {
		probePath := "/models"
		if !strings.HasSuffix(trimmedBase, "/v1") {
			probePath = "/v1/models"
		}
		probeURL = trimmedBase + probePath
	} else {
		probePath := "/models"
		if strings.Contains(trimmedBase, "api.openai.com") && !strings.HasSuffix(trimmedBase, "/v1") {
			probePath = "/v1/models"
		}
		probeURL = trimmedBase + probePath
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(UpstreamModelsResponse{
			Message: "failed to construct models request: " + err.Error(),
		})
		return
	}

	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", "firefly-models/1.0")

	if protocol == "anthropic" {
		if apiKey != "" {
			httpReq.Header.Set("x-api-key", apiKey)
			httpReq.Header.Set("anthropic-version", "2023-06-01")
		}
	} else if protocol == "opencode" || protocol == "opencode-go" {
		effectiveKey := apiKey
		if effectiveKey == "" || strings.EqualFold(effectiveKey, "public") {
			effectiveKey = "public"
		}
		httpReq.Header.Set("Authorization", "Bearer "+effectiveKey)
		httpReq.Header.Set("User-Agent", opencode.OpenCodeUserAgent)
		httpReq.Header.Set("x-opencode-client", "cli")
		httpReq.Header.Set("x-opencode-project", "global")
		httpReq.Header.Set("x-opencode-request", opencode.GenerateRequestID())
		httpReq.Header.Set("x-opencode-session", opencode.ResolveSessionID(nil, "", ""))
	} else {
		if apiKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+apiKey)
		}
	}

	client := deps.makeCheckClient(timeout, egressMode, proxyURL)

	start := time.Now()
	resp, err := client.Do(httpReq)
	latencyMs := time.Since(start).Milliseconds()

	if err != nil {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(UpstreamModelsResponse{
			LatencyMs: latencyMs,
			Message:   fmt.Sprintf("Failed to query models from %s: %s", probeURL, err.Error()),
			KeyRef:    resolvedKeyRef,
		})
		return
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		modelIDs := parseModelIDsFromBody(bodyBytes)
		msg := fmt.Sprintf("Discovered %d models from upstream host (%dms)", len(modelIDs), latencyMs)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(UpstreamModelsResponse{
			Models:     modelIDs,
			ModelCount: len(modelIDs),
			LatencyMs:  latencyMs,
			Message:    msg,
			KeyRef:     resolvedKeyRef,
		})
		return
	}

	// If Anthropic returns 404, fallback to known Claude models
	if protocol == "anthropic" && resp.StatusCode == http.StatusNotFound {
		anthropicModels := []string{
			"claude-3-7-sonnet-20250219",
			"claude-3-5-sonnet-20241022",
			"claude-3-5-haiku-20241022",
			"claude-3-opus-20240229",
			"claude-3-sonnet-20240229",
			"claude-3-haiku-20240307",
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(UpstreamModelsResponse{
			Models:     anthropicModels,
			ModelCount: len(anthropicModels),
			LatencyMs:  latencyMs,
			Message:    fmt.Sprintf("Anthropic host does not expose /models (HTTP 404); loaded %d curated Claude models", len(anthropicModels)),
			KeyRef:     resolvedKeyRef,
		})
		return
	}

	errMsg := extractUpstreamError(bodyBytes)
	if errMsg == "" {
		errMsg = http.StatusText(resp.StatusCode)
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(UpstreamModelsResponse{
		LatencyMs: latencyMs,
		Message:   fmt.Sprintf("Upstream returned HTTP %d: %s", resp.StatusCode, errMsg),
		KeyRef:    resolvedKeyRef,
	})
}
