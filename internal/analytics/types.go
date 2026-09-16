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
	Error         string  `json:"error,omitempty"`
}

// TokenLedger maintains cumulative usage and financial metrics across the gateway.
type TokenLedger struct {
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	TotalRequests    int64   `json:"total_requests"`
	TotalErrors      int64   `json:"total_errors"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
}

// PersistedState defines the structure serialized to configs/analytics.json.
type PersistedState struct {
	Summary   TokenLedger       `json:"summary"`
	Breakers  map[string]string `json:"breakers"`
	History   []RequestLog      `json:"history"`
	UpdatedAt int64             `json:"updated_at"`
}
