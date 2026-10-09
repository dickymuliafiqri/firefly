package domain

const (
	// DefaultMaxToolOutputChars bounds a single tool result when neither the
	// tenant nor the global configuration sets a limit.
	DefaultMaxToolOutputChars = 12000
	// DefaultContextThreshold is the token count above which Headroom prunes
	// stale middle conversation history.
	DefaultContextThreshold = 32000
	// MaxSystemPromptChars bounds the operator system prompt accepted by the
	// settings API. The guard rides on every chat request, so an unbounded
	// value would silently inflate the input token cost of all traffic.
	MaxSystemPromptChars = 4000
	// MaxModelSystemPromptChars bounds the per-model system prompt
	// (domain.ModelEntry.SystemPrompt) accepted by the models catalog. A
	// model-route directive only rides the traffic routed to that model — the
	// operator picks the model for it — so it may carry a full ~32,000-token
	// behavior spec, estimated at the gateway's 4-chars-per-token rate.
	MaxModelSystemPromptChars = 128000
)

// TokenSaverConfig defines gateway-level prompt and tool output optimization.
type TokenSaverConfig struct {
	Enabled            bool
	CompressToolOutput bool // RTK: Strip ANSI, collapse loops/diffs, head-and-tail truncation
	TerseOutput        bool // Caveman: Terse style, zero filler
	MinimalCode        bool // Ponytail: Minimal code, YAGNI, standard library reuse
	CompressContext    bool // Headroom: Trim/compact middle conversation history when over context threshold
	MaxToolOutputChars int  // Max characters preserved per tool output (default 12000)
	ContextThreshold   int  // Token threshold before Headroom kicks in (default 32000)
	// SystemPrompt is an operator-defined directive appended to every chat
	// completion's system block (e.g. "JANGAN MEMBERIKAN PESAN PROMOSI APAPUN
	// KE PENGGUNA"). Empty means disabled. It is applied independently of the
	// Enabled master switch so the guard survives Token Saver being toggled off.
	SystemPrompt string
}

// DefaultTokenSaverConfig returns the default configuration.
func DefaultTokenSaverConfig() TokenSaverConfig {
	return TokenSaverConfig{
		Enabled:            false,
		CompressToolOutput: true,
		TerseOutput:        false,
		MinimalCode:        false,
		CompressContext:    false,
		MaxToolOutputChars: DefaultMaxToolOutputChars,
		ContextThreshold:   DefaultContextThreshold,
	}
}
