package notify

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// severityColor is the Discord embed sidebar colour per severity. Discord wants
// a decimal integer, not a hex string.
func severityColor(s Severity) int {
	switch s {
	case SeverityCritical:
		return 0xED4245 // red
	case SeverityWarning:
		return 0xFEE75C // yellow
	default:
		return 0x5865F2 // blurple
	}
}

// severityEmoji prefixes the subject so a phone lock screen reads at a glance.
func severityEmoji(s Severity) string {
	switch s {
	case SeverityCritical:
		return "\U0001F6A8"
	case SeverityWarning:
		return "⚠️"
	default:
		return "ℹ️"
	}
}

// dataLines renders the structured payload as "key: value" lines. Map iteration
// order is randomised in Go, so the lines are sorted to keep two deliveries of
// the same event byte-identical.
func dataLines(ev Event) []string {
	if len(ev.Data) == 0 {
		return nil
	}
	keys := make([]string, 0, len(ev.Data))
	for k := range ev.Data {
		keys = append(keys, k)
	}
	sortStrings(keys)

	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		v := ev.Data[k]
		switch typed := v.(type) {
		case string:
			lines = append(lines, fmt.Sprintf("**%s:** %s", k, typed))
		case nil:
			lines = append(lines, fmt.Sprintf("**%s:** —", k))
		default:
			b, err := json.Marshal(typed)
			if err != nil {
				continue
			}
			lines = append(lines, fmt.Sprintf("**%s:** %s", k, string(b)))
		}
	}
	return lines
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// Render converts an event into the body and content type for one channel
// format. Every branch is total: an unknown format is an error, never a panic.
func Render(format string, ev Event) ([]byte, string, error) {
	switch format {
	case "generic":
		return renderGeneric(ev)
	case "discord":
		return renderDiscord(ev)
	case "slack":
		return renderSlack(ev)
	case "telegram":
		return renderTelegram(ev)
	default:
		return nil, "", fmt.Errorf("notify: unknown format %q", format)
	}
}

// renderGeneric posts the event verbatim as JSON, which is what a self-hosted
// consumer or a generic webhook receiver expects.
func renderGeneric(ev Event) ([]byte, string, error) {
	body, err := json.Marshal(ev)
	if err != nil {
		return nil, "", fmt.Errorf("notify: marshal generic event: %w", err)
	}
	return body, "application/json", nil
}

// renderDiscord posts a single embed. Discord caps the description at 4096
// characters, so the body is truncated rather than rejected.
func renderDiscord(ev Event) ([]byte, string, error) {
	const maxDesc = 4000

	var sb strings.Builder
	sb.WriteString(ev.Body)
	if lines := dataLines(ev); len(lines) > 0 {
		sb.WriteString("\n\n")
		sb.WriteString(strings.Join(lines, "\n"))
	}
	desc := sb.String()
	if len(desc) > maxDesc {
		desc = desc[:maxDesc] + "…"
	}

	payload := map[string]any{
		"embeds": []map[string]any{
			{
				"title":       fmt.Sprintf("%s %s", severityEmoji(ev.Severity), ev.Subject),
				"description": desc,
				"color":       severityColor(ev.Severity),
				"footer":      map[string]any{"text": "firefly · " + ev.Type},
				"timestamp":   ev.OccurredAt.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("notify: marshal discord payload: %w", err)
	}
	return body, "application/json", nil
}

// renderSlack posts a header plus a section block, with the structured payload
// as a field grid and a context footer.
func renderSlack(ev Event) ([]byte, string, error) {
	blocks := []map[string]any{
		{
			"type": "header",
			"text": map[string]any{
				"type":  "plain_text",
				"text":  fmt.Sprintf("%s %s", severityEmoji(ev.Severity), ev.Subject),
				"emoji": true,
			},
		},
		{
			"type": "section",
			"text": map[string]any{"type": "mrkdwn", "text": ev.Body},
		},
	}

	if lines := dataLines(ev); len(lines) > 0 {
		fields := make([]map[string]any, 0, len(lines))
		for _, line := range lines {
			fields = append(fields, map[string]any{"type": "mrkdwn", "text": line})
		}
		blocks = append(blocks, map[string]any{"type": "section", "fields": fields})
	}
	blocks = append(blocks, map[string]any{
		"type": "context",
		"elements": []map[string]any{
			{"type": "mrkdwn", "text": fmt.Sprintf("firefly · `%s` · %s", ev.Type, ev.OccurredAt.UTC().Format(time.RFC3339))},
		},
	})

	payload := map[string]any{
		"text":   fmt.Sprintf("%s %s", severityEmoji(ev.Severity), ev.Subject),
		"blocks": blocks,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("notify: marshal slack payload: %w", err)
	}
	return body, "application/json", nil
}

// renderTelegram posts a sendMessage call. Telegram rejects empty text, so a
// body-less event falls back to its subject alone.
func renderTelegram(ev Event) ([]byte, string, error) {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("*%s %s*\n", severityEmoji(ev.Severity), escapeTelegramMarkdown(ev.Subject)))
	if ev.Body != "" {
		sb.WriteString(escapeTelegramMarkdown(ev.Body))
		sb.WriteString("\n")
	}
	if lines := dataLines(ev); len(lines) > 0 {
		sb.WriteString("\n")
		sb.WriteString(escapeTelegramMarkdown(strings.Join(lines, "\n")))
	}
	sb.WriteString(fmt.Sprintf("\n_%s · %s_", escapeTelegramMarkdown(ev.Type), ev.OccurredAt.UTC().Format(time.RFC3339)))

	payload := map[string]any{
		"text":                     sb.String(),
		"parse_mode":               "MarkdownV2",
		"disable_web_page_preview": true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("notify: marshal telegram payload: %w", err)
	}
	return body, "application/json", nil
}

// escapeTelegramMarkdown escapes the characters Telegram's MarkdownV2 parser
// treats as control characters.
func escapeTelegramMarkdown(s string) string {
	replacer := strings.NewReplacer(
		"_", "\\_", "*", "\\*", "[", "\\[", "]", "\\]",
		"(", "\\(", ")", "\\)", "~", "\\~", "`", "\\`",
		">", "\\>", "#", "\\#", "+", "\\+", "-", "\\-",
		"=", "\\=", "|", "\\|", "{", "\\{", "}", "\\}",
		".", "\\.", "!", "\\!",
	)
	return replacer.Replace(s)
}
