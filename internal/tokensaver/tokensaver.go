package tokensaver

import (
	"github.com/dickymuliafiqri/firefly/internal/domain"
)

// defaultContextThreshold mirrors domain.DefaultTokenSaverConfig and is used when
// a caller passes a non-positive threshold.
const defaultContextThreshold = 32000

// Process applies enabled Token Saver optimizations (RTK, Caveman, Ponytail, Headroom)
// to an inbound OpenAI chat completion payload.
//
// If Token Saver is disabled or none of the sub-features are turned on, it returns
// the original body unchanged with zero allocations.
//
// Pass order matters: RTK runs before Headroom. RTK is the informed compressor
// (ANSI strip, repeat/diff collapsing, head-and-tail truncation) and Headroom is
// the blunt one (keep a short excerpt of stale middle turns). Compressing first
// means Headroom only ever touches output RTK could not make small enough,
// instead of truncating a 40 KB tool result down to 250 bytes before RTK gets a
// chance to collapse it.
func Process(body []byte, cfg domain.TokenSaverConfig) ([]byte, bool) {
	if !cfg.Enabled || len(body) == 0 {
		return body, false
	}

	if !cfg.CompressToolOutput && !cfg.TerseOutput && !cfg.MinimalCode && !cfg.CompressContext {
		return body, false
	}

	modified := false
	out := body

	// 1. RTK: Compress tool outputs (strip ANSI, deduplicate loops, compact diffs, head/tail truncate)
	if cfg.CompressToolOutput {
		if compressed, changed := CompressMessagesToolOutputs(out, cfg.MaxToolOutputChars); changed {
			out = compressed
			modified = true
		}
	}

	// 2. Headroom: Prune stale middle context history if over token threshold
	if cfg.CompressContext {
		if pruned, changed := PruneMiddleHistory(out, cfg.ContextThreshold); changed {
			out = pruned
			modified = true
		}
	}

	// 3. Caveman & Ponytail: Inject terse output and minimal code directives
	if cfg.TerseOutput || cfg.MinimalCode {
		if transformed, changed := ApplyPersonas(out, cfg.TerseOutput, cfg.MinimalCode); changed {
			out = transformed
			modified = true
		}
	}

	return out, modified
}
