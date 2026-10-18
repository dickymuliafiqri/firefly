package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/transport/singbox"
)

// maxSingboxBodyBytes bounds request bodies of the singbox admin endpoints.
// They carry a single URL or tag, so anything larger is garbage by definition.
const maxSingboxBodyBytes = 1 << 14

// buildSingboxDTO assembles a complete singbox.json payload from the running
// manager so a partial save (one field changed by one request) can never drop
// the fields that request does not mention — subscription URL, refresh
// interval, default tag, and strategy must round-trip together. Callers override
// just the field the request is about.
func (deps RouterDeps) buildSingboxDTO() config.SingboxDTO {
	if deps.SingboxManager == nil {
		return config.SingboxDTO{}
	}
	return config.SingboxDTO{
		SubscriptionURL:    deps.SingboxManager.SubscriptionURL(),
		RefreshIntervalS:   int(deps.SingboxManager.RefreshInterval() / time.Second),
		DefaultOutboundTag: deps.SingboxManager.Status().DefaultTag,
		Strategy:           deps.SingboxManager.Strategy(),
	}
}

// handleGetSingboxStatus returns the sing-box manager's runtime state. It is
// unauthenticated (mirroring /api/warp/status) so the dashboard can read it
// before an admin session is established; it never exposes credentials.
func (deps RouterDeps) handleGetSingboxStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if deps.SingboxManager == nil {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(singbox.Status{Ready: false})
		return
	}
	st := deps.SingboxManager.Status()
	if !deps.authorizeAdmin(r) {
		// A subscription URL is a token-bearing credential, and a fetch error
		// can embed it; neither may reach an unauthenticated caller.
		st.SubscriptionURL = ""
		st.LastRefreshError = singbox.RedactURL(st.LastRefreshError, deps.SingboxManager.SubscriptionURL())
	}
	_ = json.NewEncoder(w).Encode(st)
}

// handleSingboxRefresh triggers an immediate on-demand subscription refresh.
func (deps RouterDeps) handleSingboxRefresh(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Unauthorized"})
		return
	}
	if deps.SingboxManager == nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Sing-box engine is not initialized"})
		return
	}
	if err := deps.SingboxManager.Refresh(r.Context()); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(deps.SingboxManager.Status())
}

// handleGetSingboxSubscription returns the current subscription URL. The URL is
// not a credential, but it may contain a token, so it is admin-only.
func (deps RouterDeps) handleGetSingboxSubscription(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Unauthorized"})
		return
	}
	if deps.SingboxManager == nil {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"subscription_url": ""})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"subscription_url": deps.SingboxManager.SubscriptionURL()})
}

// handleSetSingboxSubscription sets the subscription URL and refresh interval,
// persists them to singbox.json, and triggers a refresh. Persistence happens
// first so a failed save never leaves the running engine on a value that would
// silently revert at the next restart.
func (deps RouterDeps) handleSetSingboxSubscription(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Unauthorized"})
		return
	}
	var req struct {
		SubscriptionURL  string `json:"subscription_url"`
		RefreshIntervalS int    `json:"refresh_interval_s"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSingboxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid JSON body: " + err.Error()})
		return
	}
	if deps.SingboxManager == nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Sing-box engine is not initialized"})
		return
	}
	cfg := deps.buildSingboxDTO()
	cfg.SubscriptionURL = req.SubscriptionURL
	cfg.RefreshIntervalS = req.RefreshIntervalS
	if err := cfg.Validate(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	if err := config.SaveSingbox(deps.ConfigDir, cfg); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "could not persist singbox.json: " + err.Error()})
		return
	}
	deps.SingboxManager.SetSubscriptionURL(cfg.SubscriptionURL, time.Duration(cfg.RefreshIntervalS)*time.Second)
	if err := deps.SingboxManager.Refresh(r.Context()); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(deps.SingboxManager.Status())
}

// handleSetSingboxDefaultTag changes the fallback outbound tag (the tag an
// upstream without its own singbox_outbound_tag dials through). Persisted
// first, then applied.
func (deps RouterDeps) handleSetSingboxDefaultTag(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Unauthorized"})
		return
	}
	var req struct {
		DefaultTag string `json:"default_tag"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSingboxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid JSON body: " + err.Error()})
		return
	}
	if !domain.ValidSingboxTag(req.DefaultTag) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid default_tag"})
		return
	}
	if deps.SingboxManager == nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Sing-box engine is not initialized"})
		return
	}
	cfg := deps.buildSingboxDTO()
	cfg.DefaultOutboundTag = req.DefaultTag
	if err := config.SaveSingbox(deps.ConfigDir, cfg); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "could not persist singbox.json: " + err.Error()})
		return
	}
	deps.SingboxManager.SetDefaultTag(req.DefaultTag)
	_ = json.NewEncoder(w).Encode(deps.SingboxManager.Status())
}

// handleSetSingboxStrategy switches the node-selection mode (urltest |
// round_robin). Persisted first, then applied — same contract as the default-tag
// endpoint: a failed save never leaves the running engine on a value that a
// restart would silently revert.
func (deps RouterDeps) handleSetSingboxStrategy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Unauthorized"})
		return
	}
	var req struct {
		Strategy string `json:"strategy"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSingboxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid JSON body: " + err.Error()})
		return
	}
	if !domain.ValidSingboxStrategy(req.Strategy) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid strategy (must be 'urltest' or 'round_robin')"})
		return
	}
	if deps.SingboxManager == nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Sing-box engine is not initialized"})
		return
	}
	cfg := deps.buildSingboxDTO()
	cfg.Strategy = domain.NormalizeSingboxStrategy(req.Strategy)
	if err := config.SaveSingbox(deps.ConfigDir, cfg); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "could not persist singbox.json: " + err.Error()})
		return
	}
	deps.SingboxManager.SetStrategy(cfg.Strategy)
	_ = json.NewEncoder(w).Encode(deps.SingboxManager.Status())
}

// handleListSingboxNodes returns the parsed node list (tag/type/server/port)
// without exposing outbound secrets.
func (deps RouterDeps) handleListSingboxNodes(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "Unauthorized"})
		return
	}
	if deps.SingboxManager == nil {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []singbox.NodeStatus{}, "count": 0})
		return
	}
	status := deps.SingboxManager.Status()
	_ = json.NewEncoder(w).Encode(map[string]any{"nodes": status.Nodes, "count": status.NodeCount, "default_tag": status.DefaultTag})
}
