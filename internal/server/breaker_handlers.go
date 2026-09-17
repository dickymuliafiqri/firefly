package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/dickymuliafiqri/firefly/internal/openai"
	"github.com/dickymuliafiqri/firefly/internal/upstream"
)

type UpdateBreakerRequest struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

// handleOptionsBreakers serves CORS preflight for breaker endpoints.
func (deps RouterDeps) handleOptionsBreakers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
	w.WriteHeader(http.StatusNoContent)
}

// handleGetBreakers returns the real-time circuit breaker states for all upstreams.
func (deps RouterDeps) handleGetBreakers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	result := make(map[string]string)

	// 1. Preload from catalog upstreams
	snap := deps.currentSnapshot()
	if snap != nil {
		for _, name := range snap.UpstreamNames() {
			result[name] = "CLOSED"
		}
	}

	// 2. Query breaker registry if available
	if reg, ok := deps.Breakers.(*upstream.BreakerRegistry); ok && reg != nil {
		for name, st := range reg.AllStates() {
			result[name] = strings.ToUpper(st.String())
		}
	} else if inspector, ok := deps.Breakers.(interface{ BreakerStateString(string) string }); ok {
		for name := range result {
			result[name] = strings.ToUpper(inspector.BreakerStateString(name))
		}
	}

	// 3. Merge persisted overrides if present
	if deps.Analytics != nil {
		if overrides, err := deps.Analytics.GetBreakerOverrides(r.Context()); err == nil {
			for k, v := range overrides {
				result[k] = v
			}
		}
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":   "ok",
		"breakers": result,
	})
}

// handleUpdateBreaker forces an upstream circuit breaker to a specific state and persists it.
func (deps RouterDeps) handleUpdateBreaker(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized: valid session or admin token required")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	defer r.Body.Close()

	var req UpdateBreakerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "invalid JSON payload: "+err.Error())
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "upstream name is required")
		return
	}

	st, ok := upstream.ParseBreakerState(req.State)
	if !ok {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "invalid breaker state: must be CLOSED, OPEN, or HALF-OPEN")
		return
	}

	// 1. Force state on live breaker registry
	if reg, ok := deps.Breakers.(*upstream.BreakerRegistry); ok && reg != nil {
		reg.SetState(req.Name, st)
	}

	// 2. Persist override in backend analytics store
	if deps.Analytics != nil {
		if err := deps.Analytics.SetBreakerOverride(r.Context(), req.Name, st.String()); err != nil {
			openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, "failed to persist breaker override: "+err.Error())
			return
		}
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"name":   req.Name,
		"state":  strings.ToUpper(st.String()),
	})
}

// handleOptionsHistory serves CORS preflight for request history endpoints.
func (deps RouterDeps) handleOptionsHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
	w.WriteHeader(http.StatusNoContent)
}

// handleGetHistory returns the recent request history logs.
func (deps RouterDeps) handleGetHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	limit := 50
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > 200 {
		limit = 200
	}

	var history []LiveLog
	if deps.Analytics != nil {
		if hist, err := deps.Analytics.History(r.Context(), limit); err == nil {
			history = hist
		}
	} else if deps.LiveLogs != nil {
		history = deps.LiveLogs.Snapshot()
		if len(history) > limit {
			history = history[:limit]
		}
	}

	if history == nil {
		history = []LiveLog{}
	}

	if !deps.authorizeAdmin(r) {
		sanitized := make([]LiveLog, len(history))
		for i, l := range history {
			l.Tenant = "public"
			l.KeyRef = ""
			sanitized[i] = l
		}
		history = sanitized
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"history": history,
	})
}

// handleDeleteHistory clears the request history from backend memory and disk.
func (deps RouterDeps) handleDeleteHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized: valid session or admin token required")
		return
	}

	if deps.Analytics != nil {
		_ = deps.Analytics.ClearHistory(r.Context())
	}
	if deps.LiveLogs != nil {
		deps.LiveLogs.Clear()
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"message": "request history cleared successfully",
	})
}
