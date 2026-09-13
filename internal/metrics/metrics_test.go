package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestObserveHTTPRecords verifies the HTTP counter/histogram are wired.
func TestObserveHTTPRecords(t *testing.T) {
	m := New()
	m.ObserveHTTP("POST", "/v1/chat/completions", 200, 12*time.Millisecond)
	m.ObserveHTTP("POST", "/v1/chat/completions", 200, 30*time.Millisecond)
	m.ObserveHTTP("POST", "/v1/chat/completions", 502, time.Second)

	if got := testutil.ToFloat64(m.httpRequests.WithLabelValues("POST", "/v1/chat/completions", "200")); got != 2 {
		t.Fatalf("200 count = %v, want 2", got)
	}
	if got := testutil.ToFloat64(m.httpRequests.WithLabelValues("POST", "/v1/chat/completions", "502")); got != 1 {
		t.Fatalf("502 count = %v, want 1", got)
	}
}

// TestInflightGauge tracks increments and decrements.
func TestInflightGauge(t *testing.T) {
	m := New()
	m.IncInflight()
	m.IncInflight()
	m.DecInflight()
	if got := testutil.ToFloat64(m.httpInflight); got != 1 {
		t.Fatalf("inflight = %v, want 1", got)
	}
}

// TestObserveUpstreamRecords verifies outcome counters + generation gauge.
func TestObserveUpstreamRecords(t *testing.T) {
	m := New()
	m.ObserveUpstream("openai-main", "gpt-4o-mini", OutcomeOK, 5*time.Millisecond)
	m.ObserveUpstream("openai-main", "gpt-4o-mini", OutcomeUpstreamError, 5*time.Millisecond)
	m.ObserveUpstream("openai-main", "gpt-4o-mini", OutcomeUpstreamError, 5*time.Millisecond)
	if got := testutil.ToFloat64(m.upstreamRequests.WithLabelValues("openai-main", "gpt-4o-mini", OutcomeOK)); got != 1 {
		t.Fatalf("ok = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.upstreamRequests.WithLabelValues("openai-main", "gpt-4o-mini", OutcomeUpstreamError)); got != 2 {
		t.Fatalf("errors = %v, want 2", got)
	}

	m.SetConfigGeneration(7)
	if got := testutil.ToFloat64(m.configGeneration); got != 7 {
		t.Fatalf("generation = %v, want 7", got)
	}
}

// TestAddUsage verifies usage counters and that a zero delta is a no-op.
func TestAddUsage(t *testing.T) {
	m := New()
	m.AddUsage("alpha", "cred-A", "gpt-4o-mini", 3)
	m.AddUsage("alpha", "cred-A", "gpt-4o-mini", 0) // no-op
	if got := testutil.ToFloat64(m.usageRequests.WithLabelValues("alpha", "cred-A", "gpt-4o-mini")); got != 3 {
		t.Fatalf("usage = %v, want 3", got)
	}
}

// TestKeyAndGlobalSaturationMetrics verifies key inflight, cooldown, request, and global inflight collectors.
func TestKeyAndGlobalSaturationMetrics(t *testing.T) {
	m := New()

	// 1. Key Inflight
	m.IncKeyInflight("openai", "OPENAI_API_KEY_1")
	m.IncKeyInflight("openai", "OPENAI_API_KEY_1")
	m.DecKeyInflight("openai", "OPENAI_API_KEY_1")
	if got := testutil.ToFloat64(m.keyInflight.WithLabelValues("openai", "OPENAI_API_KEY_1")); got != 1 {
		t.Fatalf("key inflight = %v, want 1", got)
	}

	// 2. Key Cooldown Events
	m.ObserveKeyCooldown("openai", "OPENAI_API_KEY_1")
	m.ObserveKeyCooldown("openai", "OPENAI_API_KEY_1")
	if got := testutil.ToFloat64(m.keyCooldownEvents.WithLabelValues("openai", "OPENAI_API_KEY_1")); got != 2 {
		t.Fatalf("key cooldown events = %v, want 2", got)
	}

	// 3. Key Requests Total
	m.ObserveKeyRequest("openai", "OPENAI_API_KEY_1", 200)
	m.ObserveKeyRequest("openai", "OPENAI_API_KEY_1", 429)
	m.ObserveKeyRequestStatus("openai", "OPENAI_API_KEY_1", "500")
	if got := testutil.ToFloat64(m.keyRequests.WithLabelValues("openai", "OPENAI_API_KEY_1", "200")); got != 1 {
		t.Fatalf("key requests 200 = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.keyRequests.WithLabelValues("openai", "OPENAI_API_KEY_1", "429")); got != 1 {
		t.Fatalf("key requests 429 = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.keyRequests.WithLabelValues("openai", "OPENAI_API_KEY_1", "500")); got != 1 {
		t.Fatalf("key requests 500 = %v, want 1", got)
	}

	// 4. Global Inflight
	m.IncGlobalInflight()
	m.IncGlobalInflight()
	m.DecGlobalInflight()
	if got := testutil.ToFloat64(m.globalInflight); got != 1 {
		t.Fatalf("global inflight = %v, want 1", got)
	}

	// 5. Scrape check
	scrape := mustScrape(t, m)
	for _, expected := range []string{
		"firefly_key_inflight_requests",
		"firefly_key_cooldown_events_total",
		"firefly_key_requests_total",
		"firefly_global_inflight_requests",
	} {
		if !strings.Contains(scrape, expected) {
			t.Fatalf("scrape missing expected metric %s", expected)
		}
	}
}


// TestNilReceiverIsSafe ensures callers may pass a nil *Metrics (tests, or a
// build without metrics) without panicking.
func TestNilReceiverIsSafe(t *testing.T) {
	var m *Metrics
	m.ObserveHTTP("GET", "/x", 200, time.Millisecond)
	m.IncInflight()
	m.DecInflight()
	m.ObserveUpstream("u", "m", OutcomeOK, time.Millisecond)
	m.AddUsage("t", "c", "m", 1)
	m.SetConfigGeneration(1)
	// Handler on a live instance still works.
	if !strings.Contains(mustScrape(t, New()), "firefly_config_generation") {
		t.Fatal("scrape missing metric name")
	}
}

// mustScrape renders the registry to text for a string assertion.
func mustScrape(t *testing.T, m *Metrics) string {
	t.Helper()
	// Reuse the testutil gatherer to avoid pulling in a full HTTP round-trip.
	families, err := m.reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var b strings.Builder
	for _, f := range families {
		b.WriteString(f.GetName())
		b.WriteByte('\n')
	}
	return b.String()
}

func TestSnapshot_FiltersNonAIModelRoutes(t *testing.T) {
	m := New()

	// Non-AI routes (should be excluded from Snapshot telemetry)
	m.ObserveHTTP("GET", "GET /healthz", 200, 1*time.Millisecond)
	m.ObserveHTTP("GET", "/api/telemetry", 200, 2*time.Millisecond)
	m.ObserveHTTP("GET", "/api/settings", 200, 2*time.Millisecond)
	m.ObserveHTTP("GET", "/", 200, 1*time.Millisecond)
	m.ObserveHTTP("GET", "unmatched", 404, 1*time.Millisecond)

	// AI model inference routes (should be included in Snapshot telemetry)
	m.ObserveHTTP("POST", "POST /v1/chat/completions", 200, 50*time.Millisecond)
	m.ObserveHTTP("POST", "/v1/chat/completions", 500, 100*time.Millisecond)
	m.ObserveHTTP("POST", "/v1/embeddings", 200, 20*time.Millisecond)

	snap := m.Snapshot()

	if snap.TotalRequests != 3 {
		t.Fatalf("snap.TotalRequests = %d, want 3 (AI model routes only)", snap.TotalRequests)
	}
	if snap.TotalErrors != 1 {
		t.Fatalf("snap.TotalErrors = %d, want 1 (500 on /v1/chat/completions)", snap.TotalErrors)
	}
	if snap.P50LatencyMs <= 0 {
		t.Fatalf("snap.P50LatencyMs = %v, want > 0", snap.P50LatencyMs)
	}
}
