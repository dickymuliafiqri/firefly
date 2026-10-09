package analytics

import (
	"context"
	"testing"
	"time"
)

// TestStore_CachedTokensAggregatedAndPersisted covers the Task 6 metering
// contract: cached read/write counters accumulate into the ledger and survive
// a reload from disk, exactly like the plain input/output token fields.
func TestStore_CachedTokensAggregatedAndPersisted(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	now := time.Now().UnixMilli()
	logs := []RequestLog{
		{
			ID: "req-a", Timestamp: now, Model: "gpt-5", Upstream: "openai", Tenant: "acme",
			Status: 200, TokensIn: 1200, TokensOut: 80, Tokens: 1280,
			CachedReadTokens: 1024,
		},
		{
			ID: "req-b", Timestamp: now, Model: "claude-sonnet", Upstream: "anthropic", Tenant: "acme",
			Status: 200, TokensIn: 15, TokensOut: 42, Tokens: 57,
			CachedReadTokens: 1800, CacheWriteTokens: 240,
		},
		{
			// In-flight entry (Status 0) must not move the ledger.
			ID: "req-c", Timestamp: now, Model: "gpt-5", Upstream: "openai", Tenant: "acme",
			CachedReadTokens: 9999, CacheWriteTokens: 9999,
		},
	}
	for _, l := range logs {
		if err := store.Record(ctx, l); err != nil {
			t.Fatalf("Record %s: %v", l.ID, err)
		}
	}

	sum, err := store.Summary(ctx)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if sum.CachedReadTokens != 1024+1800 {
		t.Errorf("CachedReadTokens = %d, want %d", sum.CachedReadTokens, 1024+1800)
	}
	if sum.CacheWriteTokens != 240 {
		t.Errorf("CacheWriteTokens = %d, want 240", sum.CacheWriteTokens)
	}
	if sum.InputTokens != 1215 || sum.OutputTokens != 122 {
		t.Errorf("plain tokens drifted: in=%d out=%d", sum.InputTokens, sum.OutputTokens)
	}

	if err := store.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	reloaded, err := NewStore(store.configDir)
	if err != nil {
		t.Fatalf("reload NewStore: %v", err)
	}
	sum2, err := reloaded.Summary(ctx)
	if err != nil {
		t.Fatalf("reloaded Summary: %v", err)
	}
	if sum2.CachedReadTokens != sum.CachedReadTokens || sum2.CacheWriteTokens != sum.CacheWriteTokens {
		t.Errorf("reloaded cached = %d/%d, want %d/%d",
			sum2.CachedReadTokens, sum2.CacheWriteTokens, sum.CachedReadTokens, sum.CacheWriteTokens)
	}

	history, err := reloaded.History(ctx, 10)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("history len = %d, want 3", len(history))
	}
	var found bool
	for _, h := range history {
		if h.ID == "req-b" {
			found = true
			if h.CachedReadTokens != 1800 || h.CacheWriteTokens != 240 {
				t.Errorf("req-b cached = %d/%d, want 1800/240", h.CachedReadTokens, h.CacheWriteTokens)
			}
		}
	}
	if !found {
		t.Error("req-b missing from reloaded history")
	}
}
