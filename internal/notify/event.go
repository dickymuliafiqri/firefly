// Package notify delivers gateway events to operator-configured webhook
// channels. It is deliberately self-contained: the dispatcher owns a bounded
// queue and a single worker, the poster owns the HTTP call and its signature,
// and the formatters own the per-target wire shapes. Nothing here reaches back
// into the gateway, so an unreachable channel can never slow a request down.
package notify

import "time"

// Severity ranks how loudly an event should be reported. Channels may filter
// on it; the ordering is part of the contract.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Event is one notification. Type is a stable dotted identifier
// ("key.cooldown", "breaker.open") that channels filter on; Subject and Body
// are the human-readable halves; Data carries structured detail that formatters
// may surface. OccurredAt is always set by the emitter.
type Event struct {
	Type       string         `json:"type"`
	Severity   Severity       `json:"severity"`
	Subject    string         `json:"subject"`
	Body       string         `json:"body"`
	Data       map[string]any `json:"data,omitempty"`
	OccurredAt time.Time      `json:"occurred_at"`
}

// Channel describes one delivery target as persisted in settings.
type Channel struct {
	// URL is the webhook endpoint. Required.
	URL string `json:"url"`
	// Format selects the wire shape: generic, discord, slack, telegram.
	Format string `json:"format"`
	// Bearer is an optional static token sent as Authorization: Bearer.
	Bearer string `json:"bearer,omitempty"`
	// Secret enables the HMAC signature header when set.
	Secret string `json:"secret,omitempty"`
	// Enabled gates delivery without deleting the configuration.
	Enabled bool `json:"enabled"`
	// Events restricts delivery to the listed event types. Empty means all.
	Events []string `json:"events,omitempty"`
	// MinSeverity drops events below this severity. Empty means info.
	MinSeverity Severity `json:"min_severity,omitempty"`
}

// Allows reports whether the channel wants this event. An empty Events list
// accepts everything; a non-empty list is an exact-match allowlist.
func (c Channel) Allows(ev Event) bool {
	if !c.Enabled {
		return false
	}
	if len(c.Events) > 0 {
		matched := false
		for _, want := range c.Events {
			if want == ev.Type {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if c.MinSeverity != "" && severityRank(ev.Severity) < severityRank(c.MinSeverity) {
		return false
	}
	return true
}

func severityRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 3
	case SeverityWarning:
		return 2
	default:
		return 1
	}
}

// KnownFormats lists the wire shapes Format can take.
var KnownFormats = []string{"generic", "discord", "slack", "telegram"}

// IsKnownFormat reports whether format is one the poster can render.
func IsKnownFormat(format string) bool {
	for _, f := range KnownFormats {
		if f == format {
			return true
		}
	}
	return false
}
