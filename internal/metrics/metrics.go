// Package metrics exposes the gateway's Prometheus collectors behind a small
// typed API. Callers (middleware, handlers, the usage exporter) depend on the
// Metrics type rather than the prometheus client directly, which keeps metric
// names and label cardinality in one place and makes the hot path testable with
// a nil receiver.
//
// Cardinality discipline: HTTP metrics are labelled by ROUTE TEMPLATE (the
// mux pattern, e.g. "/v1/chat/completions"), never the raw path, so an attacker
// cannot explode the series count with random URLs. Model/tenant labels are
// bounded by the config catalog.
package metrics

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
)

// Metrics owns the collectors and the registry they are registered on. The zero
// value is not usable; construct with New. A nil *Metrics is a valid no-op
// receiver, so callers that do not want metrics (tests) can pass nil.
type Metrics struct {
	reg *prometheus.Registry

	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec
	httpInflight prometheus.Gauge

	upstreamRequests *prometheus.CounterVec
	upstreamDuration *prometheus.HistogramVec

	usageRequests *prometheus.CounterVec

	configGeneration prometheus.Gauge

	keyInflight       *prometheus.GaugeVec
	keyCooldownEvents *prometheus.CounterVec
	keyRequests       *prometheus.CounterVec
	globalInflight    prometheus.Gauge
}

// durationBuckets cover the full range of gateway latency: sub-millisecond
// cache-ish hits through multi-second upstream waits. SSE streams are recorded
// by TTFB (first byte), not total stream duration, so the buckets stay useful.
var durationBuckets = []float64{
	0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60,
}

// New builds a Metrics with a private registry (not the global default, so
// multiple instances can coexist in tests without duplicate-registration
// panics).
func New() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		reg: reg,
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "firefly_http_requests_total",
			Help: "Total HTTP requests handled, by method, route template and status code.",
		}, []string{"method", "route", "status"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "firefly_http_request_duration_seconds",
			Help:    "HTTP request latency in seconds, by route template (time to response completion).",
			Buckets: durationBuckets,
		}, []string{"method", "route"}),
		httpInflight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "firefly_http_inflight_requests",
			Help: "Number of HTTP requests currently being served.",
		}),
		upstreamRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "firefly_upstream_requests_total",
			Help: "Total upstream calls, by upstream, model and outcome (ok|upstream_error|circuit_open|client_cancel).",
		}, []string{"upstream", "model", "outcome"}),
		upstreamDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "firefly_upstream_request_duration_seconds",
			Help:    "Upstream call latency in seconds, by upstream name.",
			Buckets: durationBuckets,
		}, []string{"upstream"}),
		usageRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "firefly_usage_requests_total",
			Help: "Observed request usage, by tenant, credential ref and model.",
		}, []string{"tenant", "credential_ref", "model"}),
		configGeneration: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "firefly_config_generation",
			Help: "Monotonic config generation currently serving traffic.",
		}),
		keyInflight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "firefly_key_inflight_requests",
			Help: "Number of concurrent in-flight requests currently using an upstream key.",
		}, []string{"upstream", "key_ref"}),
		keyCooldownEvents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "firefly_key_cooldown_events_total",
			Help: "Total number of times an upstream key has entered cooldown.",
		}, []string{"upstream", "key_ref"}),
		keyRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "firefly_key_requests_total",
			Help: "Total requests processed per upstream key, by status code.",
		}, []string{"upstream", "key_ref", "status"}),
		globalInflight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "firefly_global_inflight_requests",
			Help: "Number of concurrent in-flight requests admitted globally across the server.",
		}),
	}
	reg.MustRegister(
		m.httpRequests,
		m.httpDuration,
		m.httpInflight,
		m.upstreamRequests,
		m.upstreamDuration,
		m.usageRequests,
		m.configGeneration,
		m.keyInflight,
		m.keyCooldownEvents,
		m.keyRequests,
		m.globalInflight,
		prometheus.NewGoCollector(),
		prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}),
	)
	return m
}

// Handler returns the /metrics handler serving this registry.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// ObserveHTTP records one completed HTTP request. route must be a route
// TEMPLATE (not the raw path). status is the numeric status code.
func (m *Metrics) ObserveHTTP(method, route string, status int, d time.Duration) {
	if m == nil {
		return
	}
	m.httpRequests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	m.httpDuration.WithLabelValues(method, route).Observe(d.Seconds())
}

// IncInflight / DecInflight track the current in-flight request count.
func (m *Metrics) IncInflight() {
	if m == nil {
		return
	}
	m.httpInflight.Inc()
}

// DecInflight decrements the in-flight gauge.
func (m *Metrics) DecInflight() {
	if m == nil {
		return
	}
	m.httpInflight.Dec()
}

// ObserveUpstream records one upstream call outcome and its latency. outcome is
// one of the Outcome* constants.
func (m *Metrics) ObserveUpstream(upstream, model, outcome string, d time.Duration) {
	if m == nil {
		return
	}
	m.upstreamRequests.WithLabelValues(upstream, model, outcome).Inc()
	m.upstreamDuration.WithLabelValues(upstream).Observe(d.Seconds())
}

// AddUsage adds n requests to the usage counter for an identity. It is safe to
// call from the hot path; the exporter calls it with the delta since the last
// snapshot (see usage.Exporter).
func (m *Metrics) AddUsage(tenant, credentialRef, model string, n int64) {
	if m == nil || n == 0 {
		return
	}
	m.usageRequests.WithLabelValues(tenant, credentialRef, model).Add(float64(n))
}

// SetConfigGeneration records the currently-serving config generation.
func (m *Metrics) SetConfigGeneration(gen uint64) {
	if m == nil {
		return
	}
	m.configGeneration.Set(float64(gen))
}

// IncKeyInflight increments the active in-flight request gauge for an upstream key slot.
func (m *Metrics) IncKeyInflight(upstream, keyRef string) {
	if m == nil || upstream == "" || keyRef == "" {
		return
	}
	m.keyInflight.WithLabelValues(upstream, keyRef).Inc()
}

// DecKeyInflight decrements the active in-flight request gauge for an upstream key slot.
func (m *Metrics) DecKeyInflight(upstream, keyRef string) {
	if m == nil || upstream == "" || keyRef == "" {
		return
	}
	m.keyInflight.WithLabelValues(upstream, keyRef).Dec()
}

// ObserveKeyCooldown increments the cooldown events counter for an upstream key slot.
func (m *Metrics) ObserveKeyCooldown(upstream, keyRef string) {
	if m == nil || upstream == "" || keyRef == "" {
		return
	}
	m.keyCooldownEvents.WithLabelValues(upstream, keyRef).Inc()
}

// ObserveKeyRequest records a completed request on an upstream key slot with numeric status.
func (m *Metrics) ObserveKeyRequest(upstream, keyRef string, status int) {
	if m == nil || upstream == "" || keyRef == "" {
		return
	}
	m.keyRequests.WithLabelValues(upstream, keyRef, strconv.Itoa(status)).Inc()
}

// ObserveKeyRequestStatus records a completed request on an upstream key slot with string status.
func (m *Metrics) ObserveKeyRequestStatus(upstream, keyRef, status string) {
	if m == nil || upstream == "" || keyRef == "" {
		return
	}
	if status == "" {
		status = "unknown"
	}
	m.keyRequests.WithLabelValues(upstream, keyRef, status).Inc()
}

// IncGlobalInflight increments the global admitted in-flight request gauge.
func (m *Metrics) IncGlobalInflight() {
	if m == nil {
		return
	}
	m.globalInflight.Inc()
}

// DecGlobalInflight decrements the global admitted in-flight request gauge.
func (m *Metrics) DecGlobalInflight() {
	if m == nil {
		return
	}
	m.globalInflight.Dec()
}

// Upstream call outcomes.
const (
	OutcomeOK            = "ok"
	OutcomeUpstreamError = "upstream_error"
	OutcomeCircuitOpen   = "circuit_open"
	OutcomeClientCancel  = "client_cancel"
)

// ModelLatencyStat records latency percentiles for an upstream or model.
type ModelLatencyStat struct {
	SampleCount int64   `json:"sample_count"`
	AvgMs       float64 `json:"avg_ms"`
	P50Ms       float64 `json:"p50_ms"`
	P90Ms       float64 `json:"p90_ms"`
	P99Ms       float64 `json:"p99_ms"`
}

// MetricsSnapshot carries a structured snapshot of live telemetry metrics.
type MetricsSnapshot struct {
	TotalRequests     int64                        `json:"total_requests"`
	TotalErrors       int64                        `json:"total_errors"`
	HttpInflight      int64                        `json:"http_inflight"`
	GlobalInflight    int64                        `json:"global_inflight"`
	UpstreamOK        int64                        `json:"upstream_ok"`
	UpstreamErrors    int64                        `json:"upstream_errors"`
	CircuitOpenTrips  int64                        `json:"circuit_open_trips"`
	ClientCancels     int64                        `json:"client_cancels"`
	ConfigGeneration  uint64                       `json:"config_generation"`
	KeyCooldownEvents map[string]int64             `json:"key_cooldown_events"`
	KeyInflight       map[string]int64             `json:"key_inflight"`
	KeyRequests       map[string]int64             `json:"key_requests"`
	ModelRequests     map[string]int64             `json:"model_requests"`
	ModelErrors       map[string]int64             `json:"model_errors"`
	ModelLatency      map[string]*ModelLatencyStat `json:"model_latency"`
	P50LatencyMs      float64                      `json:"p50_latency_ms"`
	P90LatencyMs      float64                      `json:"p90_latency_ms"`
	P95LatencyMs      float64                      `json:"p95_latency_ms"`
	P99LatencyMs      float64                      `json:"p99_latency_ms"`
}

// Registry returns the underlying prometheus registry.
func (m *Metrics) Registry() *prometheus.Registry {
	if m == nil {
		return nil
	}
	return m.reg
}

// Snapshot gathers all metrics from the registry and extracts key values for real-time telemetry.
func (m *Metrics) Snapshot() MetricsSnapshot {
	var snap MetricsSnapshot
	snap.KeyCooldownEvents = make(map[string]int64)
	snap.KeyInflight = make(map[string]int64)
	snap.KeyRequests = make(map[string]int64)
	snap.ModelRequests = make(map[string]int64)
	snap.ModelErrors = make(map[string]int64)
	snap.ModelLatency = make(map[string]*ModelLatencyStat)

	var aiHttpHist aggregatedHistogram

	if m == nil || m.reg == nil {
		return snap
	}

	mfs, err := m.reg.Gather()
	if err != nil {
		return snap
	}

	for _, mf := range mfs {
		name := mf.GetName()
		for _, metric := range mf.GetMetric() {
			labels := make(map[string]string, len(metric.GetLabel()))
			for _, lp := range metric.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}

			switch name {
			case "firefly_http_requests_total":
				route := labels["route"]
				if !IsAIModelRoute(route) {
					continue
				}
				val := int64(metric.GetCounter().GetValue())
				snap.TotalRequests += val
				status := labels["status"]
				if strings.HasPrefix(status, "4") || strings.HasPrefix(status, "5") {
					snap.TotalErrors += val
				}
			case "firefly_http_inflight_requests":
				snap.HttpInflight = int64(metric.GetGauge().GetValue())
			case "firefly_global_inflight_requests":
				snap.GlobalInflight = int64(metric.GetGauge().GetValue())
			case "firefly_config_generation":
				snap.ConfigGeneration = uint64(metric.GetGauge().GetValue())
			case "firefly_upstream_requests_total":
				outcome := labels["outcome"]
				model := labels["model"]
				val := int64(metric.GetCounter().GetValue())
				if model != "" {
					snap.ModelRequests[model] += val
					if outcome != OutcomeOK {
						snap.ModelErrors[model] += val
					}
				}
				switch outcome {
				case OutcomeOK:
					snap.UpstreamOK += val
				case OutcomeUpstreamError:
					snap.UpstreamErrors += val
				case OutcomeCircuitOpen:
					snap.CircuitOpenTrips += val
				case OutcomeClientCancel:
					snap.ClientCancels += val
				}
			case "firefly_upstream_request_duration_seconds":
				hist := metric.GetHistogram()
				if hist != nil {
					upstream := labels["upstream"]
					count := int64(hist.GetSampleCount())
					sum := hist.GetSampleSum()
					p50 := calculateQuantile(hist, 0.50)
					p90 := calculateQuantile(hist, 0.90)
					p99 := calculateQuantile(hist, 0.99)
					if upstream != "" {
						avg := float64(0)
						if count > 0 {
							avg = (sum * 1000) / float64(count)
						}
						snap.ModelLatency[upstream] = &ModelLatencyStat{
							SampleCount: count,
							AvgMs:       avg,
							P50Ms:       p50 * 1000,
							P90Ms:       p90 * 1000,
							P99Ms:       p99 * 1000,
						}
					}
				}
			case "firefly_http_request_duration_seconds":
				route := labels["route"]
				if !IsAIModelRoute(route) {
					continue
				}
				hist := metric.GetHistogram()
				if hist != nil {
					aiHttpHist.Add(hist)
				}
			case "firefly_key_inflight_requests":
				k := labels["upstream"] + "/" + labels["key_ref"]
				snap.KeyInflight[k] = int64(metric.GetGauge().GetValue())
			case "firefly_key_cooldown_events_total":
				k := labels["upstream"] + "/" + labels["key_ref"]
				snap.KeyCooldownEvents[k] = int64(metric.GetCounter().GetValue())
			case "firefly_key_requests_total":
				k := labels["upstream"] + "/" + labels["key_ref"]
				snap.KeyRequests[k] += int64(metric.GetCounter().GetValue())
			}
		}
	}

	if agg := aiHttpHist.ToDTO(); agg != nil {
		snap.P50LatencyMs = calculateQuantile(agg, 0.50) * 1000
		snap.P90LatencyMs = calculateQuantile(agg, 0.90) * 1000
		snap.P95LatencyMs = calculateQuantile(agg, 0.95) * 1000
		snap.P99LatencyMs = calculateQuantile(agg, 0.99) * 1000
	}

	return snap
}

// IsAIModelRoute reports whether the given route corresponds to an AI model access/inference endpoint.
func IsAIModelRoute(route string) bool {
	if idx := strings.IndexByte(route, ' '); idx != -1 {
		route = route[idx+1:]
	}
	switch route {
	case "/v1/chat/completions", "/v1/completions", "/v1/embeddings", "/v1/messages":
		return true
	default:
		return strings.HasPrefix(route, "/v1/chat/") ||
			strings.HasPrefix(route, "/v1/completions/") ||
			strings.HasPrefix(route, "/v1/embeddings/") ||
			strings.HasPrefix(route, "/v1/messages/") ||
			strings.HasPrefix(route, "/v1/audio/") ||
			strings.HasPrefix(route, "/v1/images/")
	}
}

type aggregatedHistogram struct {
	sampleCount uint64
	sampleSum   float64
	buckets     []*dto.Bucket
}

func (a *aggregatedHistogram) Add(h *dto.Histogram) {
	if h == nil || h.GetSampleCount() == 0 {
		return
	}
	a.sampleCount += h.GetSampleCount()
	a.sampleSum += h.GetSampleSum()
	if len(a.buckets) == 0 {
		a.buckets = make([]*dto.Bucket, len(h.GetBucket()))
		for i, b := range h.GetBucket() {
			ub := b.GetUpperBound()
			cnt := b.GetCumulativeCount()
			a.buckets[i] = &dto.Bucket{
				UpperBound:      &ub,
				CumulativeCount: &cnt,
			}
		}
	} else {
		for i, b := range h.GetBucket() {
			if i < len(a.buckets) {
				newCount := a.buckets[i].GetCumulativeCount() + b.GetCumulativeCount()
				a.buckets[i].CumulativeCount = &newCount
			}
		}
	}
}

func (a *aggregatedHistogram) ToDTO() *dto.Histogram {
	if a.sampleCount == 0 {
		return nil
	}
	return &dto.Histogram{
		SampleCount: &a.sampleCount,
		SampleSum:   &a.sampleSum,
		Bucket:      a.buckets,
	}
}

func calculateQuantile(hist *dto.Histogram, q float64) float64 {
	if hist == nil || hist.GetSampleCount() == 0 {
		return 0
	}
	buckets := hist.GetBucket()
	if len(buckets) == 0 {
		return 0
	}
	target := float64(hist.GetSampleCount()) * q
	var prevCount float64
	var prevBound float64

	for _, b := range buckets {
		count := float64(b.GetCumulativeCount())
		bound := b.GetUpperBound()
		if count >= target {
			countDelta := count - prevCount
			if countDelta <= 0 {
				return bound
			}
			fraction := (target - prevCount) / countDelta
			return prevBound + fraction*(bound-prevBound)
		}
		prevCount = count
		prevBound = bound
	}
	return prevBound
}

