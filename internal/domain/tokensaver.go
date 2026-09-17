package domain

// TokenSaverConfig defines gateway-level prompt and tool output optimization.
type TokenSaverConfig struct {
	Enabled            bool
	CompressToolOutput bool // RTK: Strip ANSI, collapse loops/diffs, head-and-tail truncation
	TerseOutput        bool // Caveman: Terse style, zero filler
	MinimalCode        bool // Ponytail: Minimal code, YAGNI, standard library reuse
	CompressContext    bool // Headroom: Trim/compact middle conversation history when over context threshold
	MaxToolOutputChars int  // Max characters preserved per tool output (default 12000)
	ContextThreshold   int  // Token threshold before Headroom kicks in (default 32000)
}

// DefaultTokenSaverConfig returns the default configuration.
func DefaultTokenSaverConfig() TokenSaverConfig {
	return TokenSaverConfig{
		Enabled:            false,
		CompressToolOutput: true,
		TerseOutput:        false,
		MinimalCode:        false,
		CompressContext:    false,
		MaxToolOutputChars: 12000,
		ContextThreshold:   32000,
	}
}
