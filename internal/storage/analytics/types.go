package analytics

// RequestLog captures an individual inbound API request and upstream forwarding lifecycle.
type RequestLog struct {
	ID            string  `json:"id"`
	Timestamp     int64   `json:"timestamp"` // Unix millisecond timestamp
	Method        string  `json:"method"`
	Path          string  `json:"path"`
	Status        int     `json:"status"`
	DurationMs    int64   `json:"durationMs"`
	Model         string  `json:"model"`
	Upstream      string  `json:"upstream"`
	KeyRef        string  `json:"keyRef,omitempty"`
	Tenant        string  `json:"tenant"`
	Stream        bool    `json:"stream"`
	TokensIn      int     `json:"tokensIn"`
	TokensOut     int     `json:"tokensOut"`
	Tokens        int     `json:"tokens"`
	EstimatedCost float64 `json:"estimatedCost"`
	// CostMicros is the same figure as EstimatedCost in integer micro-USD.
	// Aggregation sums this field rather than the float, because adding a
	// thousand USD floats drifts in the fifth decimal and the cost report is
	// expected to reconcile exactly with the ledger.
	CostMicros int64 `json:"costMicros,omitempty"`
	// CachedReadTokens / CacheWriteTokens are the prompt tokens served from
	// (or written into) the provider prompt cache. OpenAI reports the read
	// count inside prompt_tokens, Anthropic excludes both from input_tokens,
	// so they are metered separately instead of being folded into TokensIn.
	CachedReadTokens int    `json:"cachedReadTokens,omitempty"`
	CacheWriteTokens int    `json:"cacheWriteTokens,omitempty"`
	Error            string `json:"error,omitempty"`
}

// TokenLedger maintains cumulative usage and financial metrics across the gateway.
type TokenLedger struct {
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	TotalRequests    int64   `json:"total_requests"`
	TotalErrors      int64   `json:"total_errors"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
	// EstimatedCostMicros is the same total in integer micro-USD. The cost
	// report aggregates from it so the two figures reconcile exactly instead
	// of drifting apart through a thousand float additions.
	EstimatedCostMicros int64 `json:"estimated_cost_micros"`
	CachedReadTokens    int64 `json:"cached_read_tokens"`
	CacheWriteTokens    int64 `json:"cache_write_tokens"`
}

// PersistedState defines the structure serialized to configs/analytics.json.
type PersistedState struct {
	Summary   TokenLedger       `json:"summary"`
	Breakers  map[string]string `json:"breakers"`
	History   []RequestLog      `json:"history"`
	UpdatedAt int64             `json:"updated_at"`
}
