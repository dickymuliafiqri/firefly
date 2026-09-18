package analytics

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestStore_InMemoryLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore("")
	if err != nil {
		t.Fatalf("unexpected NewStore error: %v", err)
	}

	// 1. Record in-flight request
	req1 := RequestLog{
		ID:        "req-1",
		Timestamp: time.Now().UnixMilli(),
		Model:     "gpt-4o",
		Upstream:  "openai-main",
		Tenant:    "acme",
		Status:    0,
		TokensIn:  50,
	}
	if err := store.Record(ctx, req1); err != nil {
		t.Fatalf("record req1: %v", err)
	}

	hist, err := store.History(ctx, 10)
	if err != nil || len(hist) != 1 {
		t.Fatalf("expected 1 history item, got %d (err: %v)", len(hist), err)
	}

	// 2. Complete request req1
	req1Completed := req1
	req1Completed.Status = 200
	req1Completed.TokensOut = 100
	req1Completed.Tokens = 150
	req1Completed.EstimatedCost = 0.001125
	if err := store.Record(ctx, req1Completed); err != nil {
		t.Fatalf("record req1 completion: %v", err)
	}

	// History should still have 1 item, updated in place
	hist, _ = store.History(ctx, 10)
	if len(hist) != 1 || hist[0].Status != 200 || hist[0].TokensOut != 100 {
		t.Fatalf("expected updated in place history, got: %+v", hist)
	}

	// Verify Summary
	summary, err := store.Summary(ctx)
	if err != nil {
		t.Fatalf("summary error: %v", err)
	}
	if summary.TotalRequests != 1 {
		t.Errorf("expected 1 request, got %d", summary.TotalRequests)
	}
	if summary.InputTokens != 50 {
		t.Errorf("expected 50 input tokens, got %d", summary.InputTokens)
	}
	if summary.OutputTokens != 100 {
		t.Errorf("expected 100 output tokens, got %d", summary.OutputTokens)
	}
	if summary.TotalTokens != 150 {
		t.Errorf("expected 150 total tokens, got %d", summary.TotalTokens)
	}
	if summary.EstimatedCostUSD != 0.001125 {
		t.Errorf("expected 0.001125 cost, got %f", summary.EstimatedCostUSD)
	}

	// 3. Set Breaker Overrides
	if err := store.SetBreakerOverride(ctx, "openai-main", "OPEN"); err != nil {
		t.Fatalf("set breaker: %v", err)
	}
	breakers, err := store.GetBreakerOverrides(ctx)
	if err != nil || breakers["openai-main"] != "OPEN" {
		t.Fatalf("expected breaker OPEN, got %v", breakers)
	}

	// Invalid breaker state returns error
	if err := store.SetBreakerOverride(ctx, "openai-main", "INVALID"); err != ErrInvalidBreakerState {
		t.Fatalf("expected ErrInvalidBreakerState, got %v", err)
	}
}

func TestStore_FilePersistenceAndReload(t *testing.T) {
	ctx := context.Background()
	tmpDir, err := os.MkdirTemp("", "firefly_analytics_test_*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// Record requests
	_ = store.Record(ctx, RequestLog{
		ID:            "req-persist-1",
		Status:        200,
		TokensIn:      120,
		TokensOut:     80,
		Tokens:        200,
		EstimatedCost: 0.0015,
	})
	_ = store.Record(ctx, RequestLog{
		ID:            "req-persist-2",
		Status:        500,
		TokensIn:      40,
		TokensOut:     0,
		Tokens:        40,
		EstimatedCost: 0.0001,
	})
	_ = store.SetBreakerOverride(ctx, "anthropic-backup", "HALF-OPEN")

	// Flush to disk
	if err := store.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(filepath.Join(tmpDir, analyticsFilename)); err != nil {
		t.Fatalf("analytics.json not found: %v", err)
	}

	// Reload in new store
	reloaded, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("reload NewStore: %v", err)
	}

	hist, err := reloaded.History(ctx, 10)
	if err != nil || len(hist) != 2 {
		t.Fatalf("expected 2 reloaded logs, got %d (err: %v)", len(hist), err)
	}

	sum, err := reloaded.Summary(ctx)
	if err != nil {
		t.Fatalf("reloaded summary: %v", err)
	}
	if sum.TotalRequests != 2 || sum.TotalErrors != 1 {
		t.Errorf("expected 2 requests / 1 error, got %d reqs / %d errors", sum.TotalRequests, sum.TotalErrors)
	}
	if sum.TotalTokens != 240 {
		t.Errorf("expected 240 total tokens, got %d", sum.TotalTokens)
	}

	brk, err := reloaded.GetBreakerOverrides(ctx)
	if err != nil || brk["anthropic-backup"] != "HALF-OPEN" {
		t.Fatalf("expected reloaded breaker HALF-OPEN, got %v", brk)
	}

	// Clear history
	if err := reloaded.ClearHistory(ctx); err != nil {
		t.Fatalf("clear history: %v", err)
	}
	hist, _ = reloaded.History(ctx, 10)
	if len(hist) != 0 {
		t.Fatalf("expected empty history after clear, got %d", len(hist))
	}
	// Summary should remain intact
	sumAfter, _ := reloaded.Summary(ctx)
	if sumAfter.TotalTokens != 240 {
		t.Fatalf("expected summary to remain intact, got %d", sumAfter.TotalTokens)
	}
}

func TestStore_ContextCancellation(t *testing.T) {
	store, _ := NewStore("")
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := store.Record(canceledCtx, RequestLog{ID: "req-cancelled"}); err == nil {
		t.Fatal("expected context canceled error from Record")
	}

	if _, err := store.History(canceledCtx, 5); err == nil {
		t.Fatal("expected context canceled error from History")
	}

	if _, err := store.Summary(canceledCtx); err == nil {
		t.Fatal("expected context canceled error from Summary")
	}

	if err := store.SetBreakerOverride(canceledCtx, "up-1", "OPEN"); err == nil {
		t.Fatal("expected context canceled error from SetBreakerOverride")
	}
}

func TestStore_ConcurrentStress(t *testing.T) {
	store, _ := NewStore("")
	ctx := context.Background()

	const goroutines = 20
	const iterations = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(gid int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = store.Record(ctx, RequestLog{
					ID:            fmt.Sprintf("req-stress-%d-%d", gid, j),
					Status:        200,
					TokensIn:      10,
					TokensOut:     20,
					Tokens:        30,
					EstimatedCost: 0.0001,
				})
				_, _ = store.Summary(ctx)
				_, _ = store.History(ctx, 10)
				_ = store.SetBreakerOverride(ctx, "stress-upstream", "CLOSED")
			}
		}(i)
	}

	wg.Wait()

	summary, err := store.Summary(ctx)
	if err != nil {
		t.Fatalf("summary error: %v", err)
	}
	expectedRequests := int64(goroutines * iterations)
	if summary.TotalRequests != expectedRequests {
		t.Fatalf("expected %d requests, got %d", expectedRequests, summary.TotalRequests)
	}
}
