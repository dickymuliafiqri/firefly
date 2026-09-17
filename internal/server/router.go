package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/analytics"
	"github.com/dickymuliafiqri/firefly/internal/auth"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/httpx"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/metrics"
	"github.com/dickymuliafiqri/firefly/internal/oauth"
	"github.com/dickymuliafiqri/firefly/internal/openai"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/dickymuliafiqri/firefly/internal/registry"
	"github.com/dickymuliafiqri/firefly/internal/turso"
	"github.com/dickymuliafiqri/firefly/internal/warp"
)

// RouterDeps carries everything the HTTP routes need.
type RouterDeps struct {
	Snapshots   SnapshotProvider
	Registry    *registry.Registry
	ConfigDir   string
	AdminToken  string
	Auth        *auth.Manager
	TenantStore ports.TenantStore
	Limiter     *limits.Limiter
	Adapters    ports.AdapterRegistry
	Adapter     ports.UpstreamAdapter
	Breakers    openai.BreakerLookup
	Analytics   *analytics.Store

	Usage    ports.UsageRecorder
	Logger   *slog.Logger
	Metrics  *metrics.Metrics
	LiveLogs *LiveLogHub
	AutoTLS      *AutoTLS
	OAuthManager *oauth.Manager
	TursoStore   *turso.Store
	TursoManager *TursoManager
	WarpManager  *warp.Manager

	// GlobalLimiter manages server-wide in-flight concurrency with a bounded wait queue.
	// If nil and DisableGlobalAdmission is false, a default 1500-slot limiter is used.
	GlobalLimiter          *httpx.GlobalLimiter
	DisableGlobalAdmission bool
}

// getTursoStore returns the active turso.Store, prioritizing TursoManager if present.
func (deps RouterDeps) getTursoStore(ctx context.Context) (*turso.Store, error) {
	if deps.TursoManager != nil {
		return deps.TursoManager.GetOrInitStore(ctx)
	}
	return deps.TursoStore, nil
}

// SnapshotProvider yields the current catalog snapshot.
type SnapshotProvider interface {
	Current() *domain.CatalogSnapshot
}

// adapterFor retrieves the appropriate UpstreamAdapter for the given protocol.
func (deps RouterDeps) adapterFor(proto domain.Protocol) (ports.UpstreamAdapter, error) {
	if !httpx.IsNil(deps.Adapters) {
		if a, ok := deps.Adapters.Lookup(proto); ok {
			return a, nil
		}
	}
	if !httpx.IsNil(deps.Adapter) {
		if deps.Adapter.Protocol() == proto || proto == "" {
			return deps.Adapter, nil
		}
	}
	return nil, fmt.Errorf("no adapter registered for protocol %q", proto)
}

// currentSnapshot returns the active snapshot, or nil if the provider is
// missing/nil (so callers can emit a 503 instead of panicking). It uses
// httpx.IsNil to catch the typed-nil-interface case.
func (deps RouterDeps) currentSnapshot() *domain.CatalogSnapshot {
	if httpx.IsNil(deps.Snapshots) {
		return nil
	}
	return deps.Snapshots.Current()
}

// ModelsResponse is the OpenAI /v1/models list shape.
type ModelsResponse struct {
	Object string     `json:"object"`
	Data   []ModelObj `json:"data"`
}

// ModelObj is one model in the list.
type ModelObj struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// protected wraps a handler with auth then admission control. Order matters:
// auth must run first so the tenant is available for limit resolution.
func (deps RouterDeps) protected(h http.Handler) http.Handler {
	h = httpx.AdmissionMiddleware(deps.Limiter)(h)
	h = httpx.AuthMiddleware(deps.TenantStore)(h)
	return h
}

// authorizeAdmin checks if the incoming HTTP request carries a valid admin token
// or active dashboard session token.
func (deps RouterDeps) authorizeAdmin(r *http.Request) bool {
	ctx := r.Context()
	token, ok := auth.ExtractBearer(r.Header.Get("Authorization"))
	if deps.Auth != nil {
		if !ok {
			return false
		}
		return deps.Auth.ValidateToken(ctx, token)
	}
	if deps.AdminToken != "" {
		if !ok {
			return false
		}
		return subtle.ConstantTimeCompare([]byte(token), []byte(deps.AdminToken)) == 1
	}
	// If neither Auth manager nor AdminToken is configured (e.g. bare unit tests), allow
	return true
}

// buildHandler assembles the middleware chain and routes.
func (s *Server) buildHandler(deps RouterDeps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Root dashboard & static frontend assets (embedded SPA)
	deps.registerFrontendRoutes(mux)

	// Authentication & Backend Credential Authorization Endpoints
	mux.HandleFunc("OPTIONS /api/auth/login", deps.handleOptionsAuth)
	mux.HandleFunc("POST /api/auth/login", deps.handleAuthLogin)
	mux.HandleFunc("OPTIONS /api/auth/verify", deps.handleOptionsAuth)
	mux.HandleFunc("GET /api/auth/verify", deps.handleAuthVerify)
	mux.HandleFunc("POST /api/auth/verify", deps.handleAuthVerify)
	mux.HandleFunc("OPTIONS /api/auth/logout", deps.handleOptionsAuth)
	mux.HandleFunc("POST /api/auth/logout", deps.handleAuthLogout)
	mux.HandleFunc("OPTIONS /api/auth/password", deps.handleOptionsAuth)
	mux.HandleFunc("PUT /api/auth/password", deps.handleAuthPassword)
	mux.HandleFunc("POST /api/auth/password", deps.handleAuthPassword)

	// Frontend Settings API (authorization-guarded)
	mux.HandleFunc("OPTIONS /api/settings", deps.handleOptionsSettings)
	mux.HandleFunc("GET /api/settings", deps.handleGetSettings)
	mux.HandleFunc("POST /api/settings", deps.handleUpdateSettings)
	mux.HandleFunc("PUT /api/settings", deps.handleUpdateSettings)

	// Turso Centralized Database API
	mux.HandleFunc("OPTIONS /api/turso/providers", deps.handleOptionsSettings)
	mux.HandleFunc("GET /api/turso/providers", deps.handleGetTursoProviders)
	mux.HandleFunc("OPTIONS /api/turso/providers/{id}/keys", deps.handleOptionsSettings)
	mux.HandleFunc("GET /api/turso/providers/{id}/keys", deps.handleGetTursoProviderKeys)
	mux.HandleFunc("OPTIONS /api/turso/keys", deps.handleOptionsSettings)
	mux.HandleFunc("GET /api/turso/keys", deps.handleGetTursoProviderKeys)
	mux.HandleFunc("OPTIONS /api/turso/test", deps.handleOptionsSettings)
	mux.HandleFunc("POST /api/turso/test", deps.handleTestTurso)

	// Upstream Health Check Probe & Model Discovery API
	mux.HandleFunc("OPTIONS /api/upstreams/check", deps.handleOptionsUpstreamCheck)
	mux.HandleFunc("POST /api/upstreams/check", deps.handleCheckUpstream)
	mux.HandleFunc("OPTIONS /api/upstreams/models", deps.handleOptionsUpstreamCheck)
	mux.HandleFunc("POST /api/upstreams/models", deps.handleFetchUpstreamModels)

	// Gateway Telemetry API (live Prometheus & admission stats)
	mux.HandleFunc("OPTIONS /api/telemetry", deps.handleOptionsTelemetry)
	mux.HandleFunc("GET /api/telemetry", deps.handleGetTelemetry)
	mux.HandleFunc("OPTIONS /api/telemetry/events", deps.handleOptionsTelemetry)
	mux.HandleFunc("GET /api/telemetry/events", deps.handleTelemetryEvents)

	// Upstream Circuit Breakers Management API
	mux.HandleFunc("OPTIONS /api/breakers", deps.handleOptionsBreakers)
	mux.HandleFunc("GET /api/breakers", deps.handleGetBreakers)
	mux.HandleFunc("POST /api/breakers", deps.handleUpdateBreaker)
	mux.HandleFunc("PUT /api/breakers", deps.handleUpdateBreaker)

	// Request History API (Persistent logs & token analytics)
	mux.HandleFunc("OPTIONS /api/history", deps.handleOptionsHistory)
	mux.HandleFunc("GET /api/history", deps.handleGetHistory)
	mux.HandleFunc("DELETE /api/history", deps.handleDeleteHistory)

	// OAuth Management Endpoints (admin/dashboard guarded)
	mux.HandleFunc("OPTIONS /api/oauth/providers", deps.handleOptionsOAuth)
	mux.HandleFunc("GET /api/oauth/providers", deps.handleListOAuthProviders)
	mux.HandleFunc("OPTIONS /api/oauth/authorize", deps.handleOptionsOAuth)
	mux.HandleFunc("POST /api/oauth/authorize", deps.handleOAuthAuthorize)
	mux.HandleFunc("OPTIONS /api/oauth/callback", deps.handleOptionsOAuth)
	mux.HandleFunc("GET /api/oauth/callback", deps.handleOAuthCallback)
	mux.HandleFunc("POST /api/oauth/callback", deps.handleOAuthCallback)
	mux.HandleFunc("OPTIONS /api/oauth/poll", deps.handleOptionsOAuth)
	mux.HandleFunc("POST /api/oauth/poll", deps.handleOAuthPoll)
	mux.HandleFunc("OPTIONS /api/oauth/connections", deps.handleOptionsOAuth)
	mux.HandleFunc("GET /api/oauth/connections", deps.handleListOAuthConnections)
	mux.HandleFunc("OPTIONS /api/oauth/connections/{id}", deps.handleOptionsOAuth)
	mux.HandleFunc("DELETE /api/oauth/connections/{id}", deps.handleDeleteOAuthConnection)

	// Cloudflare WARP Userspace Egress API
	mux.HandleFunc("OPTIONS /api/warp/status", deps.handleOptionsSettings)
	mux.HandleFunc("GET /api/warp/status", deps.handleGetWarpStatus)
	mux.HandleFunc("OPTIONS /api/warp/rotate", deps.handleOptionsSettings)
	mux.HandleFunc("POST /api/warp/rotate", deps.handleRotateWarp)

	// /v1/models is auth-protected but needs no upstream.
	mux.Handle("GET /v1/models", deps.protected(http.HandlerFunc(deps.handleListModels)))
	// /v1/models/{id} returns a single model (OpenAI "retrieve model").
	mux.Handle("GET /v1/models/{id}", deps.protected(http.HandlerFunc(deps.handleRetrieveModel)))

	// Upstream-backed endpoints. All three proxy to the OpenAI wire surface and
	// differ only in path; they share one handler. Auth + admission run first.
	for _, route := range []struct{ local, upstream string }{
		{"/v1/chat/completions", "/chat/completions"},
		{"/v1/completions", "/completions"},
		{"/v1/embeddings", "/embeddings"},
	} {
		h := deps.protected(http.HandlerFunc(deps.forwardEndpoint(route.upstream)))
		mux.Handle("POST "+route.local, h)
	}

	// Native Token Saver prompt & context compression endpoint
	mux.HandleFunc("OPTIONS /v1/compress", deps.handleOptionsCompress)
	mux.Handle("POST /v1/compress", deps.protected(http.HandlerFunc(deps.handleCompress)))

	// Middleware order (outermost first):
	//   request-id        -> every log line has an id
	//   metrics           -> latency/status/in-flight; outside the mux so r.Pattern is set
	//   recover           -> catches panics anywhere below (incl. streaming goroutines
	//                        that write through the wrapped writer)
	//   logging           -> records status/duration for all downstream
	//   global admission  -> backpressure at the front door before routing/auth work
	// Per-route auth + admission run inside the mux (see protected).
	chain := []func(http.Handler) http.Handler{
		httpx.RequestIDMiddleware,
		httpx.MetricsMiddleware(deps.Metrics),
		httpx.RecoverMiddleware(deps.Logger),
		httpx.LoggingMiddleware(deps.Logger),
	}
	if !deps.DisableGlobalAdmission {
		gl := deps.GlobalLimiter
		if gl == nil {
			gl = httpx.NewGlobalLimiter(httpx.DefaultGlobalMaxInflight, httpx.DefaultGlobalWaitTimeout)
		}
		if deps.Metrics != nil {
			gl.SetObserver(deps.Metrics)
		}
		chain = append(chain, gl.Middleware())
	}
	root := httpx.Chain(mux, chain...)
	return s.drainGuard(root)
}

// handleListModels serves the catalog filtered by the tenant's allowed models.
func (deps RouterDeps) handleListModels(w http.ResponseWriter, r *http.Request) {
	tenant, ok := httpx.RequireTenant(w, r)
	if !ok {
		return
	}
	snap := deps.currentSnapshot()
	if snap == nil {
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI, "config not loaded")
		return
	}
	now := time.Now().Unix()
	resp := ModelsResponse{Object: "list"}
	for _, id := range snap.EnabledModels() {
		if !tenant.AllowsModel(id) {
			continue
		}
		resp.Data = append(resp.Data, ModelObj{
			ID:      id,
			Object:  "model",
			Created: now,
			OwnedBy: "firefly",
		})
	}
	for _, combo := range snap.AllCombos() {
		if !combo.Enabled || !tenant.AllowsModel(combo.Name) {
			continue
		}
		resp.Data = append(resp.Data, ModelObj{
			ID:      combo.Name,
			Object:  "model",
			Created: now,
			OwnedBy: "firefly-combo",
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleRetrieveModel serves GET /v1/models/{id}, honoring tenant visibility:
// a model the tenant cannot see is reported as 404 (not 403) to avoid leaking
// the catalog to unauthorized tenants.
func (deps RouterDeps) handleRetrieveModel(w http.ResponseWriter, r *http.Request) {
	tenant, ok := httpx.RequireTenant(w, r)
	if !ok {
		return
	}
	snap := deps.currentSnapshot()
	if snap == nil {
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI, "config not loaded")
		return
	}
	id := r.PathValue("id")
	if combo, ok := snap.Combo(id); ok && combo.Enabled && tenant.AllowsModel(id) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ModelObj{
			ID:      id,
			Object:  "model",
			Created: time.Now().Unix(),
			OwnedBy: "firefly-combo",
		})
		return
	}
	entry, found := snap.Model(id)
	if !found || !entry.Enabled || !tenant.AllowsModel(id) {
		openai.WriteError(w, http.StatusNotFound, openai.TypeNotFound, "model '"+id+"' not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ModelObj{
		ID:      id,
		Object:  "model",
		Created: time.Now().Unix(),
		OwnedBy: "firefly",
	})
}
