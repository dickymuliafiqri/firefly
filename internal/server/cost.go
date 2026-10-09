package server

import "github.com/dickymuliafiqri/firefly/internal/domain"

// microsPerUSD is the scale factor between the integer micro-USD used by the
// pricing ledger and the float dollars the DTOs and live logs carry. The
// conversion happens exactly once, at the boundary, so no float arithmetic
// ever touches the ledger itself.
const microsPerUSD = 1_000_000

// microsToUSD converts integer micro-USD into float dollars.
func microsToUSD(micros int64) float64 {
	return float64(micros) / microsPerUSD
}

// estimateRequestCost prices one request in integer micro-USD. The pricing
// table is consulted with the public model name first, then the upstream's
// private model name; a miss -- or an entry that carries no price -- falls
// back to the legacy flat rate so historical dashboards stay comparable.
//
// tokensIn must be the billable (uncached) input count: callers subtract the
// cached reads only for wire shapes that fold them into the prompt figure.
func estimateRequestCost(snap *domain.CatalogSnapshot, model, upstreamModel string, tokensIn, tokensOut, cachedRead, cacheWrite int) int64 {
	keys := []string{model}
	if upstreamModel != "" && upstreamModel != model {
		keys = append(keys, upstreamModel)
	}
	cost, _ := snap.Pricing().PricedCostMicros(keys, tokensIn, tokensOut, cachedRead, cacheWrite)
	return cost
}

// billableInputTokens normalizes a provider's prompt figure into the uncached
// count the price sheet expects. OpenAI reports the cached reads inside
// prompt_tokens, so they are subtracted here; Anthropic reports them outside
// input_tokens, so the figure is already billable and passes through. The
// result is clamped at zero because a provider that reports more cached tokens
// than prompt tokens must not produce a negative bill.
func billableInputTokens(promptTokens, cachedRead, cacheWrite int, cachedInPrompt bool) int {
	if !cachedInPrompt {
		return promptTokens
	}
	billable := promptTokens - cachedRead - cacheWrite
	if billable < 0 {
		return 0
	}
	return billable
}
