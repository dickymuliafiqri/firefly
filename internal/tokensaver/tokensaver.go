package tokensaver

import (
	"github.com/dickymuliafiqri/firefly/internal/domain"
)

// Process applies enabled Token Saver optimizations (RTK, Caveman, Ponytail, Headroom)
// to an inbound OpenAI chat completion payload.
//
// If Token Saver is disabled or none of the sub-features are turned on, it returns
// the original body unchanged with zero allocations.
func Process(body []byte, cfg domain.TokenSaverConfig) ([]byte, bool) {
	if !cfg.Enabled || len(body) == 0 {
		return body, false
	}

	if !cfg.CompressToolOutput && !cfg.TerseOutput && !cfg.MinimalCode && !cfg.CompressContext {
		return body, false
	}

	modified := false
	out := body

	// 1. Headroom: Prune stale middle context history if over token threshold
	if cfg.CompressContext {
		if pruned, changed := PruneMiddleHistory(out, cfg.ContextThreshold); changed {
			out = pruned
			modified = true
		}
	}

	// 2. RTK: Compress tool outputs (strip ANSI, deduplicate loops, compact diffs, head/tail truncate)
	if cfg.CompressToolOutput {
		if compressed, changed := CompressMessagesToolOutputs(out, cfg.MaxToolOutputChars); changed {
			out = compressed
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
