package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/storage/analytics"
)

// usageCostsDeps builds a router with an analytics store pre-loaded with logs.
func usageCostsDeps(t *testing.T, logs []analytics.RequestLog) RouterDeps {
	t.Helper()
	store, err := analytics.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	ctx := t.Context()
	for _, l := range logs {
		if err := store.Record(ctx, l); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	deps, _ := testDeps()
	deps.Analytics = store
	return deps
}

func dayMs(day string) int64 {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		panic(err)
	}
	return t.UnixMilli()
}

func costLog(id, tenant, model, key string, tsMs int64, tokIn, tokOut, cachedRead, cacheWrite int, costMicros int64, status int) analytics.RequestLog {
	return analytics.RequestLog{
		ID:               id,
		Timestamp:        tsMs,
		Method:           "POST",
		Path:             "/v1/chat/completions",
		Status:           status,
		Model:            model,
		KeyRef:           key,
		Tenant:           tenant,
		TokensIn:         tokIn,
		TokensOut:        tokOut,
		Tokens:           tokIn + tokOut,
		EstimatedCost:    float64(costMicros) / 1e6,
		CostMicros:       costMicros,
		CachedReadTokens: cachedRead,
		CacheWriteTokens: cacheWrite,
	}
}

func doCostsRequest(t *testing.T, deps RouterDeps, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	rec := httptest.NewRecorder()
	deps.handleUsageCosts(rec, req)
	return rec
}

func TestHandleUsageCosts_RequiresAdmin(t *testing.T) {
	// A configured admin token must be enforced: with neither Auth nor
	// AdminToken set the router deliberately allows bare unit tests through,
	// so the guard has to be exercised with a token in place.
	deps := usageCostsDeps(t, nil)
	deps.AdminToken = "secret-admin"

	req := httptest.NewRequest(http.MethodGet, "/api/usage/costs", nil)
	rec := httptest.NewRecorder()
	deps.handleUsageCosts(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: status = %d, want 401", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/usage/costs", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec = httptest.NewRecorder()
	deps.handleUsageCosts(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: status = %d, want 401", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/usage/costs", nil)
	req.Header.Set("Authorization", "Bearer secret-admin")
	rec = httptest.NewRecorder()
	deps.handleUsageCosts(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleUsageCosts_DefaultsToSevenDays(t *testing.T) {
	old := dayMs(time.Now().UTC().AddDate(0, 0, -30).Format("2006-01-02"))
	recent := dayMs(time.Now().UTC().Format("2006-01-02"))
	deps := usageCostsDeps(t, []analytics.RequestLog{
		costLog("old", "alpha", "m", "k", old, 10, 5, 0, 0, 100, 200),
		costLog("new", "alpha", "m", "k", recent, 10, 5, 0, 0, 200, 200),
	})

	rec := doCostsRequest(t, deps, "/api/usage/costs")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var rep analytics.CostReport
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatalf("bad payload: %v", err)
	}
	if rep.GroupBy != analytics.GroupTenant {
		t.Fatalf("group_by = %q, want tenant", rep.GroupBy)
	}
	if rep.Total.Requests != 1 {
		t.Fatalf("requests = %d, want 1 (default window is 7 days)", rep.Total.Requests)
	}
	if rep.Total.CostMicros != 200 {
		t.Fatalf("cost = %d, want 200", rep.Total.CostMicros)
	}
	if rep.Since == 0 || rep.Until == 0 {
		t.Fatalf("window not populated: %+v", rep)
	}
}

func TestHandleUsageCosts_GroupByModel(t *testing.T) {
	deps := usageCostsDeps(t, []analytics.RequestLog{
		costLog("a", "alpha", "gpt-4o", "k1", dayMs("2026-10-01"), 10, 5, 0, 0, 100, 200),
		costLog("b", "alpha", "claude", "k2", dayMs("2026-10-01"), 10, 5, 0, 0, 300, 200),
	})

	rec := doCostsRequest(t, deps, "/api/usage/costs?group_by=model&since=2026-09-01T00:00:00Z&until=2026-11-01T00:00:00Z")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var rep analytics.CostReport
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatalf("bad payload: %v", err)
	}
	if rep.GroupBy != analytics.GroupModel {
		t.Fatalf("group_by = %q", rep.GroupBy)
	}
	if len(rep.Buckets) != 2 {
		t.Fatalf("buckets = %d, want 2", len(rep.Buckets))
	}
	if rep.Buckets[0].Key != "claude" || rep.Buckets[0].CostMicros != 300 {
		t.Fatalf("top bucket = %+v", rep.Buckets[0])
	}
	if len(rep.Series) != 2 {
		t.Fatalf("series = %d, want 2", len(rep.Series))
	}
}

func TestHandleUsageCosts_GroupByKey(t *testing.T) {
	deps := usageCostsDeps(t, []analytics.RequestLog{
		costLog("a", "alpha", "m", "k1", dayMs("2026-10-01"), 10, 5, 0, 0, 100, 200),
		costLog("b", "beta", "m", "k2", dayMs("2026-10-01"), 10, 5, 0, 0, 300, 200),
	})

	rec := doCostsRequest(t, deps, "/api/usage/costs?group_by=key&since=2026-09-01T00:00:00Z")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var rep analytics.CostReport
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatalf("bad payload: %v", err)
	}
	if rep.GroupBy != analytics.GroupKey {
		t.Fatalf("group_by = %q", rep.GroupBy)
	}
	if rep.Buckets[0].Key != "k2" {
		t.Fatalf("top bucket = %+v", rep.Buckets[0])
	}
}

func TestHandleUsageCosts_UnixAndRFC3339Windows(t *testing.T) {
	deps := usageCostsDeps(t, []analytics.RequestLog{
		costLog("a", "alpha", "m", "k", dayMs("2026-10-05"), 10, 5, 0, 0, 100, 200),
	})

	// Unix seconds.
	rec := doCostsRequest(t, deps, "/api/usage/costs?since=1780000000&until=1800000000")
	if rec.Code != http.StatusOK {
		t.Fatalf("unix window status = %d: %s", rec.Code, rec.Body.String())
	}
	var rep analytics.CostReport
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatalf("bad payload: %v", err)
	}
	if rep.Total.Requests != 1 {
		t.Fatalf("unix window requests = %d, want 1", rep.Total.Requests)
	}

	// RFC3339.
	rec = doCostsRequest(t, deps, "/api/usage/costs?since=2026-10-01T00:00:00Z&until=2026-10-10T00:00:00Z")
	if rec.Code != http.StatusOK {
		t.Fatalf("rfc3339 window status = %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatalf("bad payload: %v", err)
	}
	if rep.Total.Requests != 1 {
		t.Fatalf("rfc3339 window requests = %d, want 1", rep.Total.Requests)
	}
}

func TestHandleUsageCosts_RejectsBadParams(t *testing.T) {
	deps := usageCostsDeps(t, nil)

	for _, target := range []string{
		"/api/usage/costs?since=not-a-date",
		"/api/usage/costs?until=also-not-a-date",
		"/api/usage/costs?since=2026-11-01T00:00:00Z&until=2026-10-01T00:00:00Z",
	} {
		rec := doCostsRequest(t, deps, target)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400: %s", target, rec.Code, rec.Body.String())
		}
	}
}

func TestHandleUsageCosts_NoAnalyticsStoreIs503(t *testing.T) {
	deps, _ := testDeps()
	deps.Analytics = nil
	rec := doCostsRequest(t, deps, "/api/usage/costs")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestParseTimeParam(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int64
	}{
		{"unix seconds", "1783003200", 1783003200000},
		{"unix millis", "1783003200000", 1783003200000},
		{"rfc3339", "2026-10-08T12:00:00Z", dayMs("2026-10-08") + 12*3600*1000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseTimeParam(tc.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("parseTimeParam(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}

	if _, err := parseTimeParam("garbage"); err == nil {
		t.Fatal("garbage must error")
	}
}
