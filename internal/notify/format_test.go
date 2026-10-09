package notify

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRender_Generic(t *testing.T) {
	body, ct, err := Render("generic", sampleEvent())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ct != "application/json" {
		t.Fatalf("content type = %q", ct)
	}
	var ev Event
	if err := json.Unmarshal(body, &ev); err != nil {
		t.Fatalf("body is not an event: %v", err)
	}
	if ev.Type != "key.cooldown" || ev.Severity != SeverityWarning {
		t.Fatalf("round trip lost fields: %+v", ev)
	}
	if ev.Data["upstream"] != "openai" {
		t.Fatalf("data lost: %+v", ev.Data)
	}
}

func TestRender_Discord(t *testing.T) {
	body, _, err := Render("discord", sampleEvent())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var payload struct {
		Embeds []struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			Color       int    `json:"color"`
			Footer      struct {
				Text string `json:"text"`
			} `json:"footer"`
			Timestamp string `json:"timestamp"`
		} `json:"embeds"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("bad discord payload: %v\n%s", err, body)
	}
	if len(payload.Embeds) != 1 {
		t.Fatalf("want 1 embed, got %d", len(payload.Embeds))
	}
	emb := payload.Embeds[0]
	if !strings.Contains(emb.Title, "Key entered cooldown") {
		t.Fatalf("title = %q", emb.Title)
	}
	if !strings.Contains(emb.Description, "cooling down") {
		t.Fatalf("description = %q", emb.Description)
	}
	if !strings.Contains(emb.Description, "upstream") {
		t.Fatalf("description missing data lines: %q", emb.Description)
	}
	if emb.Color != 0xFEE75C {
		t.Fatalf("warning colour = %#x", emb.Color)
	}
	if !strings.HasSuffix(emb.Footer.Text, "key.cooldown") {
		t.Fatalf("footer = %q", emb.Footer.Text)
	}
	if emb.Timestamp == "" {
		t.Fatal("timestamp missing")
	}
}

func TestRender_DiscordCriticalColour(t *testing.T) {
	ev := sampleEvent()
	ev.Severity = SeverityCritical
	body, _, err := Render("discord", ev)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(body), "15548997") { // 0xED4245
		t.Fatalf("critical colour missing: %s", body)
	}
}

func TestRender_Slack(t *testing.T) {
	body, _, err := Render("slack", sampleEvent())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var payload struct {
		Text   string `json:"text"`
		Blocks []struct {
			Type string `json:"type"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("bad slack payload: %v\n%s", err, body)
	}
	if payload.Text == "" {
		t.Fatal("fallback text missing")
	}
	wantTypes := []string{"header", "section", "section", "context"}
	if len(payload.Blocks) != len(wantTypes) {
		t.Fatalf("blocks = %d, want %d: %s", len(payload.Blocks), len(wantTypes), body)
	}
	for i, want := range wantTypes {
		if payload.Blocks[i].Type != want {
			t.Fatalf("block %d = %q, want %q", i, payload.Blocks[i].Type, want)
		}
	}
}

func TestRender_Telegram(t *testing.T) {
	body, _, err := Render("telegram", sampleEvent())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var payload struct {
		Text      string `json:"text"`
		ParseMode string `json:"parse_mode"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("bad telegram payload: %v\n%s", err, body)
	}
	if payload.ParseMode != "MarkdownV2" {
		t.Fatalf("parse_mode = %q", payload.ParseMode)
	}
	if !strings.Contains(payload.Text, "Key entered cooldown") {
		t.Fatalf("text = %q", payload.Text)
	}
	if !strings.Contains(payload.Text, "cooling down") {
		t.Fatalf("body missing from text: %q", payload.Text)
	}
	if !strings.Contains(payload.Text, `key\.cooldown`) {
		t.Fatalf("event type not escaped: %q", payload.Text)
	}
}

func TestRender_UnknownFormat(t *testing.T) {
	if _, _, err := Render("carrier-pigeon", sampleEvent()); err == nil {
		t.Fatal("unknown format must error")
	}
}

func TestRender_NoDataNoPanic(t *testing.T) {
	ev := Event{Type: "ping", Severity: SeverityInfo, Subject: "s", Body: "b"}
	for _, f := range KnownFormats {
		body, _, err := Render(f, ev)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if len(body) == 0 {
			t.Fatalf("%s produced an empty body", f)
		}
	}
}

func TestDataLinesSortedAndStable(t *testing.T) {
	ev := Event{Data: map[string]any{"zeta": 1, "alpha": 2, "mid": 3}}
	first := strings.Join(dataLines(ev), "|")
	for i := 0; i < 20; i++ {
		if got := strings.Join(dataLines(ev), "|"); got != first {
			t.Fatalf("unstable ordering:\n%s\n%s", first, got)
		}
	}
	if !strings.HasPrefix(first, "**alpha:**") {
		t.Fatalf("not sorted: %s", first)
	}
}

func TestEscapeTelegramMarkdown(t *testing.T) {
	got := escapeTelegramMarkdown("a_b*c[d]e.f!g")
	want := "a\\_b\\*c\\[d\\]e\\.f\\!g"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
