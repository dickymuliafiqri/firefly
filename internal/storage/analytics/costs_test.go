package analytics

import (
	"context"
	"testing"
	"time"
)

func dayMs(day string) int64 {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		panic(err)
	}
	return t.UnixMilli()
}

func costLog(id, tenant, model, key string, tsMs int64, tokIn, tokOut, cachedRead, cacheWrite int, costMicros int64, status int) RequestLog {
	return RequestLog{
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

func TestParseCostGroup(t *testing.T) {
	cases := []struct {
		in   string
		want CostGroup
	}{
		{"tenant", GroupTenant},
		{"model", GroupModel},
		{"key", GroupKey},
		{"", GroupTenant},
		{"nonsense", GroupTenant},
	}
	for _, tc := range cases {
		if got := ParseCostGroup(tc.in); got != tc.want {
			t.Errorf("ParseCostGroup(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAggregateCosts_GroupByTenant(t *testing.T) {
	logs := []RequestLog{
		costLog("a", "alpha", "gpt-4o", "k1", dayMs("2026-10-01"), 100, 50, 0, 0, 3_000, 200),
		costLog("b", "alpha", "gpt-4o", "k1", dayMs("2026-10-01"), 200, 100, 40, 0, 9_000, 200),
		costLog("c", "beta", "claude", "k2", dayMs("2026-10-02"), 10, 5, 0, 0, 500, 429),
	}

	rep := AggregateCosts(logs, GroupTenant, 0, 0)
	if rep.GroupBy != GroupTenant {
		t.Fatalf("group_by = %q", rep.GroupBy)
	}
	if len(rep.Buckets) != 2 {
		t.Fatalf("buckets = %d, want 2", len(rep.Buckets))
	}
	// Sorted by descending cost: alpha (12000) before beta (500).
	if rep.Buckets[0].Key != "alpha" || rep.Buckets[0].CostMicros != 12_000 {
		t.Fatalf("first bucket = %+v", rep.Buckets[0])
	}
	if rep.Buckets[1].Key != "beta" || rep.Buckets[1].CostMicros != 500 {
		t.Fatalf("second bucket = %+v", rep.Buckets[1])
	}
	if rep.Buckets[0].Requests != 2 || rep.Buckets[0].TokensIn != 300 || rep.Buckets[0].TokensOut != 150 {
		t.Fatalf("alpha totals wrong: %+v", rep.Buckets[0])
	}
	if rep.Buckets[0].CachedRead != 40 {
		t.Fatalf("cached read not carried: %+v", rep.Buckets[0])
	}
	if rep.Buckets[1].ErrorCount != 1 {
		t.Fatalf("error count not carried: %+v", rep.Buckets[1])
	}
	if rep.Total.CostMicros != 12_500 || rep.Total.Requests != 3 {
		t.Fatalf("total = %+v", rep.Total)
	}
	if rep.Total.CostUSD != 0.0125 {
		t.Fatalf("total usd = %v", rep.Total.CostUSD)
	}
}

func TestAggregateCosts_GroupByModelAndKey(t *testing.T) {
	logs := []RequestLog{
		costLog("a", "alpha", "gpt-4o", "k1", dayMs("2026-10-01"), 10, 5, 0, 0, 100, 200),
		costLog("b", "alpha", "claude", "k2", dayMs("2026-10-01"), 10, 5, 0, 0, 200, 200),
		costLog("c", "beta", "gpt-4o", "k1", dayMs("2026-10-01"), 10, 5, 0, 0, 300, 200),
	}

	byModel := AggregateCosts(logs, GroupModel, 0, 0)
	if len(byModel.Buckets) != 2 {
		t.Fatalf("model buckets = %d, want 2", len(byModel.Buckets))
	}
	if byModel.Buckets[0].Key != "gpt-4o" || byModel.Buckets[0].CostMicros != 400 {
		t.Fatalf("gpt-4o bucket = %+v", byModel.Buckets[0])
	}

	byKey := AggregateCosts(logs, GroupKey, 0, 0)
	if len(byKey.Buckets) != 2 {
		t.Fatalf("key buckets = %d, want 2", len(byKey.Buckets))
	}
	if byKey.Buckets[0].Key != "k1" || byKey.Buckets[0].CostMicros != 400 {
		t.Fatalf("k1 bucket = %+v", byKey.Buckets[0])
	}
}

func TestAggregateCosts_SeriesPerDay(t *testing.T) {
	logs := []RequestLog{
		costLog("a", "alpha", "m", "k", dayMs("2026-10-01"), 10, 5, 0, 0, 100, 200),
		costLog("b", "alpha", "m", "k", dayMs("2026-10-02"), 10, 5, 0, 0, 200, 200),
		costLog("c", "beta", "m", "k", dayMs("2026-10-02"), 10, 5, 0, 0, 300, 200),
	}

	rep := AggregateCosts(logs, GroupTenant, 0, 0)
	if len(rep.Series) != 3 {
		t.Fatalf("series = %d, want 3 (2 tenants x 2 days minus one gap)", len(rep.Series))
	}
	// Sorted by day then key.
	if rep.Series[0].Day != "2026-10-01" || rep.Series[0].Key != "alpha" {
		t.Fatalf("series[0] = %+v", rep.Series[0])
	}
	if rep.Series[1].Day != "2026-10-02" || rep.Series[1].Key != "alpha" {
		t.Fatalf("series[1] = %+v", rep.Series[1])
	}
	if rep.Series[2].Day != "2026-10-02" || rep.Series[2].Key != "beta" {
		t.Fatalf("series[2] = %+v", rep.Series[2])
	}
	if rep.Series[1].CostMicros != 200 {
		t.Fatalf("series[1] cost = %d", rep.Series[1].CostMicros)
	}
}

func TestAggregateCosts_TimeWindowFilters(t *testing.T) {
	logs := []RequestLog{
		costLog("old", "alpha", "m", "k", dayMs("2026-09-01"), 10, 5, 0, 0, 100, 200),
		costLog("mid", "alpha", "m", "k", dayMs("2026-10-05"), 10, 5, 0, 0, 200, 200),
		costLog("new", "alpha", "m", "k", dayMs("2026-10-09"), 10, 5, 0, 0, 300, 200),
	}

	since := dayMs("2026-10-01")
	until := dayMs("2026-10-08")
	rep := AggregateCosts(logs, GroupTenant, since, until)
	if rep.Total.Requests != 1 {
		t.Fatalf("requests = %d, want 1", rep.Total.Requests)
	}
	if rep.Total.CostMicros != 200 {
		t.Fatalf("cost = %d, want 200", rep.Total.CostMicros)
	}
	if rep.Since != since || rep.Until != until {
		t.Fatalf("window not echoed: %+v", rep)
	}
}

func TestAggregateCosts_EmptyAndUnknownKeys(t *testing.T) {
	rep := AggregateCosts(nil, GroupTenant, 0, 0)
	if len(rep.Buckets) != 0 || len(rep.Series) != 0 {
		t.Fatalf("empty input must produce empty buckets, got %+v", rep)
	}
	if rep.Total.Requests != 0 {
		t.Fatalf("total = %+v", rep.Total)
	}

	// A log with no tenant must land in a visible bucket, not disappear.
	logs := []RequestLog{costLog("x", "", "m", "", dayMs("2026-10-01"), 1, 1, 0, 0, 10, 200)}
	rep = AggregateCosts(logs, GroupTenant, 0, 0)
	if len(rep.Buckets) != 1 || rep.Buckets[0].Key != "(unknown)" {
		t.Fatalf("unknown key bucket = %+v", rep.Buckets)
	}
}

func TestAggregateCosts_Deterministic(t *testing.T) {
	logs := []RequestLog{
		costLog("a", "alpha", "m", "k", dayMs("2026-10-01"), 10, 5, 0, 0, 100, 200),
		costLog("b", "beta", "m", "k", dayMs("2026-10-01"), 10, 5, 0, 0, 100, 200),
		costLog("c", "gamma", "m", "k", dayMs("2026-10-01"), 10, 5, 0, 0, 100, 200),
	}
	first := AggregateCosts(logs, GroupTenant, 0, 0)
	for i := 0; i < 25; i++ {
		again := AggregateCosts(logs, GroupTenant, 0, 0)
		if len(again.Buckets) != len(first.Buckets) {
			t.Fatalf("bucket count drifted")
		}
		for j := range first.Buckets {
			if again.Buckets[j].Key != first.Buckets[j].Key {
				t.Fatalf("bucket order drifted at %d: %s vs %s", j, again.Buckets[j].Key, first.Buckets[j].Key)
			}
		}
	}
}

func TestStoreCostReport_MatchesLedger(t *testing.T) {
	store, err := NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	ctx := context.Background()

	logs := []RequestLog{
		costLog("a", "alpha", "gpt-4o", "k1", dayMs("2026-10-01"), 100, 50, 20, 0, 3_000, 200),
		costLog("b", "beta", "claude", "k2", dayMs("2026-10-02"), 10, 5, 0, 10, 1_500, 200),
	}
	for _, l := range logs {
		if err := store.Record(ctx, l); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	rep, err := store.CostReport(ctx, GroupTenant, 0, 0)
	if err != nil {
		t.Fatalf("cost report: %v", err)
	}

	summary, err := store.Summary(ctx)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if rep.Total.CostMicros != summary.EstimatedCostMicros {
		t.Fatalf("report micros %d != ledger micros %d", rep.Total.CostMicros, summary.EstimatedCostMicros)
	}
	if rep.Total.Requests != summary.TotalRequests {
		t.Fatalf("report requests %d != ledger %d", rep.Total.Requests, summary.TotalRequests)
	}
	if rep.Total.TokensIn != summary.InputTokens {
		t.Fatalf("report tokens in %d != ledger %d", rep.Total.TokensIn, summary.InputTokens)
	}
	if rep.Total.CachedRead != summary.CachedReadTokens {
		t.Fatalf("report cached read %d != ledger %d", rep.Total.CachedRead, summary.CachedReadTokens)
	}
	if rep.Total.CacheWrite != summary.CacheWriteTokens {
		t.Fatalf("report cache write %d != ledger %d", rep.Total.CacheWrite, summary.CacheWriteTokens)
	}
}

func TestStoreCostReport_InFlightNotDoubleCounted(t *testing.T) {
	store, err := NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	ctx := context.Background()

	// An in-flight log (status 0) is recorded first, then completed in place.
	inflight := costLog("req-1", "alpha", "m", "k", dayMs("2026-10-01"), 10, 0, 0, 0, 0, 0)
	if err := store.Record(ctx, inflight); err != nil {
		t.Fatalf("record inflight: %v", err)
	}
	done := costLog("req-1", "alpha", "m", "k", dayMs("2026-10-01"), 10, 40, 0, 0, 2_500, 200)
	if err := store.Record(ctx, done); err != nil {
		t.Fatalf("record done: %v", err)
	}

	rep, err := store.CostReport(ctx, GroupTenant, 0, 0)
	if err != nil {
		t.Fatalf("cost report: %v", err)
	}
	if rep.Total.Requests != 1 {
		t.Fatalf("requests = %d, want 1 (in-flight must not count twice)", rep.Total.Requests)
	}
	if rep.Total.CostMicros != 2_500 {
		t.Fatalf("cost = %d, want 2500", rep.Total.CostMicros)
	}
}
