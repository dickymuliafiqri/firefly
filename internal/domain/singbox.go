package domain

import "strings"

// The sing-box egress engine exposes a fixed set of programmatic outbound
// groups in addition to the nodes parsed out of a subscription. These names are
// shared by the config validator (which accepts an operator-typed tag), the
// subscription parser (which synthesizes node tags), and the dashboard, so they
// live here rather than in either consumer.
const (
	// SingboxDefaultOutboundTag is the default group dialed when an upstream
	// names no tag.
	SingboxDefaultOutboundTag = "auto"

	// SingboxSelectorOutboundTag is the manual-override selector group, so an
	// operator can pin one node for every upstream on the "select" tag without
	// editing each upstream.
	SingboxSelectorOutboundTag = "select"

	// SingboxDirectOutboundTag is the built-in direct outbound. The manager
	// dials it when its Box/Tunnel is not ready, which is what makes egress fail open
	// instead of failing the request.
	SingboxDirectOutboundTag = "direct"

	// SingboxFallbackOutboundTag is the auto-failover group tag that tries nodes
	// in sequence until finding an active one.
	SingboxFallbackOutboundTag = "fallback"

	// SingboxRoundRobinOutboundTag is the load-balance group tag that rotates
	// dials across healthy nodes.
	SingboxRoundRobinOutboundTag = "round-robin"
)

// Aliases for Mihomo domain concepts
const (
	MihomoDefaultOutboundTag    = SingboxDefaultOutboundTag
	MihomoSelectorOutboundTag   = SingboxSelectorOutboundTag
	MihomoDirectOutboundTag     = SingboxDirectOutboundTag
	MihomoFallbackOutboundTag   = SingboxFallbackOutboundTag
	MihomoRoundRobinOutboundTag = SingboxRoundRobinOutboundTag
)

// MaxSingboxTagLen is the maximum length of a sing-box outbound tag.
const MaxSingboxTagLen = 64

// The node-selection strategies the Manager supports.
const (
	// SingboxStrategyURLTest dials the URLTest group, which picks the
	// fastest live node.
	SingboxStrategyURLTest = "urltest"

	// SingboxStrategyRoundRobin distributes dials across healthy nodes
	// so load spreads over all nodes instead of piling on one.
	SingboxStrategyRoundRobin = "round_robin"

	// SingboxStrategyFallback dials the first healthy node in order,
	// automatically falling over to subsequent nodes if earlier ones fail.
	SingboxStrategyFallback = "fallback"

	// SingboxStrategyConsistentHash distributes dials based on consistent hashing.
	SingboxStrategyConsistentHash = "consistent_hashing"
)

// Mihomo strategy aliases
const (
	MihomoStrategyURLTest        = SingboxStrategyURLTest
	MihomoStrategyRoundRobin     = SingboxStrategyRoundRobin
	MihomoStrategyFallback       = SingboxStrategyFallback
	MihomoStrategyConsistentHash = SingboxStrategyConsistentHash
)

// ValidSingboxStrategy reports whether s names a supported strategy (case
// insensitive, trimmed by the caller or not).
func ValidSingboxStrategy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case SingboxStrategyURLTest, SingboxStrategyRoundRobin, SingboxStrategyFallback, SingboxStrategyConsistentHash:
		return true
	default:
		return false
	}
}

// NormalizeSingboxStrategy returns a usable strategy: the operator's value when
// it is valid, otherwise the URLTest default. Callers normalize at use time so
// a legacy singbox.json without a strategy field keeps working unchanged.
func NormalizeSingboxStrategy(s string) string {
	if ValidSingboxStrategy(s) {
		return strings.ToLower(strings.TrimSpace(s))
	}
	return SingboxStrategyURLTest
}

// The sing-box subscription refresh cadence. The default is applied when the
// operator never set one; the bounds keep a hand-edited file (or a hostile API
// payload) from either hammering the subscription host or silently freezing the
// node list. They live here because the config loader, the manager's refresh
// loop, and the dashboard all need the same numbers.
const (
	DefaultSingboxRefreshIntervalS = 3600
	MinSingboxRefreshIntervalS     = 60
	MaxSingboxRefreshIntervalS     = 86400
)

// ClampSingboxRefreshIntervalS returns a usable refresh interval: the default
// for zero/negative, otherwise the value bounded into [Min, Max]. Callers apply
// it at use time so a stale persisted value can never drive the loop directly.
func ClampSingboxRefreshIntervalS(s int) int {
	if s <= 0 {
		return DefaultSingboxRefreshIntervalS
	}
	if s < MinSingboxRefreshIntervalS {
		return MinSingboxRefreshIntervalS
	}
	if s > MaxSingboxRefreshIntervalS {
		return MaxSingboxRefreshIntervalS
	}
	return s
}

// validSingboxTag bounds a tag to a charset that is safe in a config file, a URL
// query, and a log line. It must start alphanumeric so a synthesized tag can
// never be mistaken for an empty string or a bare separator.
func validSingboxTag(tag string) bool {
	if len(tag) == 0 || len(tag) > MaxSingboxTagLen {
		return false
	}
	for i := 0; i < len(tag); i++ {
		c := tag[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case i > 0 && (c == '.' || c == '_' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// ValidSingboxTag reports whether tag is a well-formed sing-box outbound tag.
func ValidSingboxTag(tag string) bool {
	return validSingboxTag(strings.TrimSpace(tag))
}

// SanitizeSingboxTag converts an arbitrary source label (typically a node name
// from a subscription) into a valid outbound tag: every disallowed byte becomes
// '-', leading separators are trimmed, and the result is truncated to
// MaxSingboxTagLen. It returns "" when nothing usable remains, which callers
// must treat as "use a positional tag instead" rather than writing an empty one.
func SanitizeSingboxTag(label string) string {
	var b strings.Builder
	b.Grow(len(label))
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "._-")
	if len(out) > MaxSingboxTagLen {
		out = out[:MaxSingboxTagLen]
	}
	out = strings.TrimLeft(out, "._-")
	if !ValidSingboxTag(out) {
		return ""
	}
	return out
}
