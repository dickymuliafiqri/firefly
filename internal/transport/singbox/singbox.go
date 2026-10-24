// Package singbox re-exports the mihomo-based proxy engine for backwards
// compatibility with existing handlers, configuration, and upstreams.
package singbox

import (
	"github.com/dickymuliafiqri/firefly/internal/transport/mihomo"
)

// Re-export core types from the mihomo engine.
type (
	Manager     = mihomo.Manager
	Dialer      = mihomo.Dialer
	Metrics     = mihomo.Metrics
	Status      = mihomo.Status
	NodeStatus  = mihomo.NodeStatus
	Node        = mihomo.Node
	ParseResult = mihomo.ParseResult
)

// Re-export core constructor and helper functions.
var (
	NewManager        = mihomo.NewManager
	FetchSubscription = mihomo.FetchSubscription
	ParseSubscription = mihomo.ParseSubscription
	RedactURL         = mihomo.RedactURL
)
