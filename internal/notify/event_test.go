package notify

import (
	"testing"
	"time"
)

func sampleEvent() Event {
	return Event{
		Type:       "key.cooldown",
		Severity:   SeverityWarning,
		Subject:    "Key entered cooldown",
		Body:       "Upstream openai key sk-gw-*** is cooling down for 30s after 429.",
		Data:       map[string]any{"upstream": "openai", "key_hint": "sk-gw-***", "retry_after": 30},
		OccurredAt: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC),
	}
}

func TestChannelAllows(t *testing.T) {
	cases := []struct {
		name    string
		channel Channel
		event   Event
		want    bool
	}{
		{"disabled channel", Channel{URL: "https://x", Format: "generic", Enabled: false}, sampleEvent(), false},
		{"enabled no filter", Channel{URL: "https://x", Format: "generic", Enabled: true}, sampleEvent(), true},
		{"event allowlist hit", Channel{URL: "https://x", Format: "generic", Enabled: true, Events: []string{"key.cooldown"}}, sampleEvent(), true},
		{"event allowlist miss", Channel{URL: "https://x", Format: "generic", Enabled: true, Events: []string{"breaker.open"}}, sampleEvent(), false},
		{"severity below floor", Channel{URL: "https://x", Format: "generic", Enabled: true, MinSeverity: SeverityCritical}, sampleEvent(), false},
		{"severity at floor", Channel{URL: "https://x", Format: "generic", Enabled: true, MinSeverity: SeverityWarning}, sampleEvent(), true},
		{"severity above floor", Channel{URL: "https://x", Format: "generic", Enabled: true, MinSeverity: SeverityInfo}, sampleEvent(), true},
		{"critical passes warning floor", Channel{URL: "https://x", Format: "generic", Enabled: true, MinSeverity: SeverityWarning}, Event{Type: "breaker.open", Severity: SeverityCritical}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.channel.Allows(tc.event); got != tc.want {
				t.Fatalf("Allows() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsKnownFormat(t *testing.T) {
	for _, f := range []string{"generic", "discord", "slack", "telegram"} {
		if !IsKnownFormat(f) {
			t.Errorf("%q should be known", f)
		}
	}
	for _, f := range []string{"", "carrier-pigeon", "GENERIC"} {
		if IsKnownFormat(f) {
			t.Errorf("%q should be unknown", f)
		}
	}
}

func TestSeverityRankOrdering(t *testing.T) {
	if severityRank(SeverityInfo) >= severityRank(SeverityWarning) {
		t.Error("info must rank below warning")
	}
	if severityRank(SeverityWarning) >= severityRank(SeverityCritical) {
		t.Error("warning must rank below critical")
	}
}

func TestRedactURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://discord.com/api/webhooks/123/secret-token", "https://discord.com/api/webhooks/123/secret-token"},
		{"https://hooks.slack.com/services/T00/B00/XXXX?token=abc", "https://hooks.slack.com/services/T00/B00/XXXX"},
		{"not a url", "[unparseable-url]"},
		{"", "[unparseable-url]"},
	}
	for _, tc := range cases {
		if got := redactURL(tc.in); got != tc.want {
			t.Errorf("redactURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
