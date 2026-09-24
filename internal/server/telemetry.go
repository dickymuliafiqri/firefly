package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/observability/metrics"
	"github.com/dickymuliafiqri/firefly/internal/transport/httpx"
)

// TelemetryDTO is the structured real-time observability snapshot.
type TelemetryDTO struct {
	Timestamp       int64                  `json:"timestamp"`
	GlobalAdmission GlobalAdmissionDTO     `json:"global_admission"`
	Summary         TelemetrySummaryDTO    `json:"summary"`
	Models          []ModelTelemetryDTO    `json:"models"`
	Upstreams       []UpstreamTelemetryDTO `json:"upstreams"`
	TenantsUsage    []TenantUsageDTO       `json:"tenants_usage"`
	Generation      uint64                 `json:"generation"`
	RecentLogs      []LiveLog              `json:"recent_logs"`
}

type GlobalAdmissionDTO struct {
	Inflight   int `json:"inflight"`
	Capacity   int `json:"capacity"`
	QueueDepth int `json:"queue_depth"`
}

type TelemetrySummaryDTO struct {
	ActiveStreams    int64   `json:"active_streams"`
	TotalRequests    int64   `json:"total_requests"`
	TotalErrors      int64   `json:"total_errors"`
	CircuitTrips     int64   `json:"circuit_trips"`
	ErrorRatePct     float64 `json:"error_rate_pct"`
	P50LatencyMs     float64 `json:"p50_latency_ms"`
	P90LatencyMs     float64 `json:"p90_latency_ms"`
	P95LatencyMs     float64 `json:"p95_latency_ms"`
	P99LatencyMs     float64 `json:"p99_latency_ms"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	EstimatedCostUsd float64 `json:"estimated_cost_usd"`
}

type ModelTelemetryDTO struct {
	Model    string  `json:"model"`
	Upstream string  `json:"upstream"`
	Enabled  bool    `json:"enabled"`
	Requests int64   `json:"requests"`
	Errors   int64   `json:"errors"`
	P50Ms    float64 `json:"p50_ms"`
	P90Ms    float64 `json:"p90_ms"`
	P99Ms    float64 `json:"p99_ms"`
}

type UpstreamTelemetryDTO struct {
	Name          string                `json:"name"`
	Protocol      string                `json:"protocol"`
	BaseURL       string                `json:"base_url"`
	BreakerState  string                `json:"breaker_state"`
	TotalRequests int64                 `json:"total_requests"`
	Slots         []KeySlotTelemetryDTO `json:"slots"`
}

type KeySlotTelemetryDTO struct {
	Ref                  string `json:"ref"`
	Inflight             int64  `json:"inflight"`
	IsCooldown           bool   `json:"is_cooldown"`
	CooldownRemainingSec int64  `json:"cooldown_remaining_sec"`
	IsRevoked            bool   `json:"is_revoked"`
	TotalCooldownEvents  int64  `json:"total_cooldown_events"`
	RequestsTotal        int64  `json:"requests_total"`
}

type TenantUsageDTO struct {
	Tenant        string `json:"tenant"`
	Model         string `json:"model"`
	CredentialRef string `json:"credential_ref"`
	TotalRequests int64  `json:"total_requests"`
}

// handleOptionsTelemetry handles CORS preflight for /api/telemetry.
func (deps RouterDeps) handleOptionsTelemetry(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
	w.WriteHeader(http.StatusNoContent)
}

// handleGetTelemetry serves real gateway telemetry metrics.
func (deps RouterDeps) handleGetTelemetry(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	isAdmin := deps.authorizeAdmin(r)

	snapCatalog := deps.currentSnapshot()
	var metricsSnap metrics.MetricsSnapshot
	if deps.Metrics != nil {
		metricsSnap = deps.Metrics.Snapshot()
	}

	// Global admission stats. A wired limiter is the source of truth; without
	// one, fall back to the admission observer gauge, which the limiter's own
	// middleware feeds — never to the total HTTP in-flight count, which also
	// includes admin/telemetry traffic that bypasses admission entirely.
	var adm GlobalAdmissionDTO
	if deps.GlobalLimiter != nil {
		adm = GlobalAdmissionDTO{
			Inflight:   deps.GlobalLimiter.Inflight(),
			Capacity:   deps.GlobalLimiter.Capacity(),
			QueueDepth: 0,
		}
	} else {
		adm = GlobalAdmissionDTO{
			Inflight:   int(metricsSnap.GlobalInflight),
			Capacity:   httpx.DefaultGlobalMaxInflight,
			QueueDepth: 0,
		}
	}

	// Active AI streams are the sum of per-credential in-flight requests: only
	// the data-plane path increments those counters. There is deliberately no
	// fallback to admission occupancy — that counts every admitted request, not
	// streams, and previously even subtracted the telemetry connection itself.
	var activeStreams int64
	for _, inf := range metricsSnap.KeyInflight {
		if inf > 0 {
			activeStreams += inf
		}
	}

	var inTokens, outTokens, hubReqs, hubErrors int64
	var estCost float64
	if deps.Analytics != nil {
		if sum, err := deps.Analytics.Summary(r.Context()); err == nil {
			inTokens = sum.InputTokens
			outTokens = sum.OutputTokens
			hubReqs = sum.TotalRequests
			hubErrors = sum.TotalErrors
			estCost = sum.EstimatedCostUSD
		}
	}
	if inTokens == 0 && outTokens == 0 && deps.LiveLogs != nil {
		inTokens, outTokens, hubReqs = deps.LiveLogs.CumulativeTotals()
		estCost = (float64(inTokens) * 0.0000025) + (float64(outTokens) * 0.0000100)
	}
	totTokens := inTokens + outTokens

	totalRequests := metricsSnap.TotalRequests
	if hubReqs > totalRequests {
		totalRequests = hubReqs
	}
	totalErrors := metricsSnap.TotalErrors
	if hubErrors > totalErrors {
		totalErrors = hubErrors
	}

	var errRate float64
	if totalRequests > 0 {
		errRate = (float64(totalErrors) / float64(totalRequests)) * 100
	}

	summary := TelemetrySummaryDTO{
		ActiveStreams:    activeStreams,
		TotalRequests:    totalRequests,
		TotalErrors:      totalErrors,
		CircuitTrips:     metricsSnap.CircuitOpenTrips,
		ErrorRatePct:     errRate,
		P50LatencyMs:     metricsSnap.P50LatencyMs,
		P90LatencyMs:     metricsSnap.P90LatencyMs,
		P95LatencyMs:     metricsSnap.P95LatencyMs,
		P99LatencyMs:     metricsSnap.P99LatencyMs,
		InputTokens:      inTokens,
		OutputTokens:     outTokens,
		TotalTokens:      totTokens,
		EstimatedCostUsd: estCost,
	}

	// Models telemetry
	var modelsDTO []ModelTelemetryDTO
	if snapCatalog != nil {
		for _, name := range snapCatalog.EnabledModels() {
			if m, ok := snapCatalog.Model(name); ok {
				reqs := metricsSnap.ModelRequests[m.PublicName]
				errs := metricsSnap.ModelErrors[m.PublicName]
				var p50, p90, p99 float64
				if lat, ok := metricsSnap.ModelLatency[m.Upstream]; ok {
					p50 = lat.P50Ms
					p90 = lat.P90Ms
					p99 = lat.P99Ms
				}
				modelsDTO = append(modelsDTO, ModelTelemetryDTO{
					Model:    m.PublicName,
					Upstream: m.Upstream,
					Enabled:  m.Enabled,
					Requests: reqs,
					Errors:   errs,
					P50Ms:    p50,
					P90Ms:    p90,
					P99Ms:    p99,
				})
			}
		}
	}

	// Upstreams telemetry
	var upstreamsDTO []UpstreamTelemetryDTO
	if snapCatalog != nil {
		nowNano := time.Now().UnixNano()
		for _, uName := range snapCatalog.UpstreamNames() {
			if u, ok := snapCatalog.Upstream(uName); ok {
				breakerState := "CLOSED"
				if inspector, ok := deps.Breakers.(interface{ BreakerStateString(string) string }); ok {
					breakerState = strings.ToUpper(inspector.BreakerStateString(u.Name))
				} else if deps.Breakers != nil {
					if err := deps.Breakers.Allow(u.Name); err != nil {
						breakerState = "OPEN"
					}
				}
				if deps.Analytics != nil {
					if overrides, err := deps.Analytics.GetBreakerOverrides(r.Context()); err == nil {
						if override, exists := overrides[u.Name]; exists {
							breakerState = strings.ToUpper(override)
						}
					}
				}

				var slotsDTO []KeySlotTelemetryDTO
				var upstreamTotalReqs int64
				if u.KeyRing != nil {
					for _, slot := range u.KeyRing.AllSlots() {
						cdUntil := slot.CooldownUntil.Load()
						isCooldown := cdUntil > nowNano
						var cdRem int64
						if isCooldown {
							cdRem = (cdUntil - nowNano) / int64(time.Second)
						}

						keyID := u.Name + "/" + slot.Ref
						events := metricsSnap.KeyCooldownEvents[keyID]
						reqs := metricsSnap.KeyRequests[keyID]
						upstreamTotalReqs += reqs

						slotsDTO = append(slotsDTO, KeySlotTelemetryDTO{
							Ref:                  slot.Ref,
							Inflight:             slot.Inflight.Load(),
							IsCooldown:           isCooldown,
							CooldownRemainingSec: cdRem,
							IsRevoked:            slot.Revoked.Load(),
							TotalCooldownEvents:  events,
							RequestsTotal:        reqs,
						})
					}
				}

				upstreamsDTO = append(upstreamsDTO, UpstreamTelemetryDTO{
					Name:          u.Name,
					Protocol:      string(u.Protocol),
					BaseURL:       u.BaseURL,
					BreakerState:  breakerState,
					TotalRequests: upstreamTotalReqs,
					Slots:         slotsDTO,
				})
			}
		}
	}

	// Tenants usage
	var usageDTO []TenantUsageDTO
	if deps.Usage != nil {
		counters := deps.Usage.Snapshot()
		for _, c := range counters {
			usageDTO = append(usageDTO, TenantUsageDTO{
				Tenant:        c.TenantName,
				Model:         c.Model,
				CredentialRef: c.CredentialRef,
				TotalRequests: c.Requests,
			})
		}
	}

	var gen uint64
	if snapCatalog != nil {
		gen = snapCatalog.Generation()
	}

	var recentLogs []LiveLog
	if deps.Analytics != nil {
		if hist, err := deps.Analytics.History(r.Context(), 100); err == nil && len(hist) > 0 {
			recentLogs = hist
		}
	}
	if len(recentLogs) == 0 && deps.LiveLogs != nil {
		recentLogs = deps.LiveLogs.Snapshot()
	}
	if recentLogs == nil {
		recentLogs = []LiveLog{}
	}

	if !isAdmin {
		usageDTO = []TenantUsageDTO{}
		sanitizedLogs := make([]LiveLog, len(recentLogs))
		for i, l := range recentLogs {
			l.Tenant = "public"
			l.KeyRef = ""
			sanitizedLogs[i] = l
		}
		recentLogs = sanitizedLogs
		for i := range upstreamsDTO {
			for j := range upstreamsDTO[i].Slots {
				upstreamsDTO[i].Slots[j].Ref = ""
			}
		}
	}

	out := TelemetryDTO{
		Timestamp:       time.Now().UnixMilli(),
		GlobalAdmission: adm,
		Summary:         summary,
		Models:          modelsDTO,
		Upstreams:       upstreamsDTO,
		TenantsUsage:    usageDTO,
		Generation:      gen,
		RecentLogs:      recentLogs,
	}

	_ = json.NewEncoder(w).Encode(out)
}

// handleTelemetryEvents streams live connection events via Server-Sent Events (SSE).
func (deps RouterDeps) handleTelemetryEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	isAdmin := deps.authorizeAdmin(r)
	if !isAdmin {
		if queryToken := r.URL.Query().Get("token"); queryToken != "" {
			reqWithBearer := r.Clone(r.Context())
			reqWithBearer.Header.Set("Authorization", "Bearer "+queryToken)
			isAdmin = deps.authorizeAdmin(reqWithBearer)
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	if deps.LiveLogs == nil {
		_, _ = w.Write([]byte(": livelogs unavailable\n\n"))
		flusher.Flush()
		return
	}

	ch, unsub := deps.LiveLogs.Subscribe()
	defer unsub()

	// Initial greeting packet to unblock client connection
	_, _ = w.Write([]byte(": connected\n\n"))
	flusher.Flush()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case log, ok := <-ch:
			if !ok {
				return
			}
			if !isAdmin {
				log.Tenant = "public"
				log.KeyRef = ""
			}
			data, err := json.Marshal(log)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: log\ndata: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
