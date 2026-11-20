package opencodefingerprint

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// sessionIDRe is the canonical OpenCode session id shape, matching
// packages/schema/src/session-id.ts and Firefly's IsValidSessionID.
var sessionIDRe = regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)

func TestUserAgentMatchesV2Format(t *testing.T) {
	got := UserAgent(DefaultAppInfo())
	want := "opencode/latest/2.0.29/opencode"
	if got != want {
		t.Fatalf("UserAgent() = %q, want %q", got, want)
	}
	for _, v1Segment := range []string{"ai-sdk", "runtime/bun", "provider-utils", "1.18.31"} {
		if strings.Contains(got, v1Segment) {
			t.Fatalf("UserAgent() = %q still carries the v1 segment %q", got, v1Segment)
		}
	}
}

func TestUserAgentPreviewChannel(t *testing.T) {
	got := UserAgent(AppInfo{Name: "opencode", Version: "2.1.0", Channel: "beta"})
	want := "opencode/beta/2.1.0/opencode"
	if got != want {
		t.Fatalf("UserAgent() = %q, want %q", got, want)
	}
}

func TestRootSessionSendsExactlySevenHeaders(t *testing.T) {
	h := V2Headers(DefaultAppInfo(), "ses_0123456789abcdefghijklmn", "global", "")
	if len(h) != RootHeaderCount {
		t.Fatalf("got %d headers, want %d: %v", len(h), RootHeaderCount, h)
	}

	want := []Header{
		{"x-opencode-session-id", "ses_0123456789abcdefghijklmn"},
		{"x-session-affinity", "ses_0123456789abcdefghijklmn"},
		{"X-Session-Id", "ses_0123456789abcdefghijklmn"},
		{"User-Agent", "opencode/latest/2.0.29/opencode"},
		{"x-opencode-project", "global"},
		{"x-opencode-session", "ses_0123456789abcdefghijklmn"},
		{"x-opencode-client", "opencode"},
	}
	for i, w := range want {
		if h[i] != w {
			t.Errorf("header[%d] = %+v, want %+v", i, h[i], w)
		}
	}
}

func TestParentSessionAddsTwoHeaders(t *testing.T) {
	const parent = "ses_parent000000000000000000"
	h := V2Headers(DefaultAppInfo(), "ses_child0000000000000000000", "global", parent)
	if len(h) != RootHeaderCount+2 {
		t.Fatalf("got %d headers, want %d", len(h), RootHeaderCount+2)
	}

	// affinity resolves to the parent, so all three session-ish headers
	// carry the parent id.
	for _, name := range []string{"x-opencode-session-id", "x-session-affinity", "X-Session-Id", "x-opencode-session"} {
		var got string
		for _, hdr := range h {
			if hdr.Name == name {
				got = hdr.Value
			}
		}
		if name == "x-opencode-session-id" {
			if got != "ses_child0000000000000000000" {
				t.Errorf("%s = %q, want the child session id", name, got)
			}
			continue
		}
		if got != parent {
			t.Errorf("%s = %q, want the parent id %q", name, got, parent)
		}
	}

	var sawParentHeader, sawXParent bool
	for _, hdr := range h {
		switch hdr.Name {
		case "x-opencode-parent-session-id":
			sawParentHeader = hdr.Value == parent
		case "x-parent-session-id":
			sawXParent = hdr.Value == parent
		}
	}
	if !sawParentHeader || !sawXParent {
		t.Fatalf("parent headers missing: x-opencode-parent-session-id=%v x-parent-session-id=%v", sawParentHeader, sawXParent)
	}
}

func TestNoV1ArtifactsInHeaderSet(t *testing.T) {
	h := V2Headers(DefaultAppInfo(), "ses_0123456789abcdefghijklmn", "global", "")
	for _, hdr := range h {
		switch strings.ToLower(hdr.Name) {
		case "x-opencode-request":
			t.Errorf("v2 does not send %s", hdr.Name)
		case "x-opencode-client":
			if hdr.Value == "desktop" {
				t.Errorf("x-opencode-client = %q, want %q (app.name, not the surface)", hdr.Value, V2ClientName)
			}
		}
	}
}

func TestSystemPromptHasNoModelIdentityTail(t *testing.T) {
	p := OfficialSystemPrompt()
	for _, v1 := range []string{"big-pickle", "You are opencode, an interactive CLI tool", "powered by the model named"} {
		if strings.Contains(p, v1) {
			t.Fatalf("system prompt still contains the v1 fragment %q", v1)
		}
	}
	if !strings.HasPrefix(p, "You are an AI agent running in OpenCode, a coding agent harness.") {
		t.Fatalf("system prompt does not start with the v2 opening line")
	}
	if !strings.Contains(p, toolGuidancePlaceholder) {
		t.Fatalf("system prompt lost the %s placeholder", toolGuidancePlaceholder)
	}
}

func TestRenderSystemPromptSubstitutesToolGuidance(t *testing.T) {
	got := RenderSystemPrompt([]string{"shell", "read"})
	if strings.Contains(got, toolGuidancePlaceholder) {
		t.Fatal("placeholder was not substituted")
	}
	if !strings.Contains(got, "- Prefer dedicated tools over shell commands; fall back to the shell when a tool cannot do what you need.") {
		t.Fatal("shell guidance line missing")
	}
	if !strings.Contains(got, "- Do not chain shell commands with separators like `echo \"====\";`") {
		t.Fatal("shell chaining guidance line missing")
	}
	// write/edit guidance must not appear when those tools are absent.
	if strings.Contains(got, "Use the write tool to create files") {
		t.Fatal("write guidance leaked in without the write tool")
	}

	withWrite := RenderSystemPrompt([]string{"write", "edit"})
	if !strings.Contains(withWrite, "- Use the write tool to create files or completely replace their content.") {
		t.Fatal("write guidance missing when the write tool is present")
	}
	if !strings.Contains(withWrite, "- Use the edit tool for targeted changes to existing text files.") {
		t.Fatal("edit guidance missing when the edit tool is present")
	}
	if strings.Contains(withWrite, "Prefer dedicated tools over shell commands") {
		t.Fatal("shell guidance leaked in without the shell tool")
	}
}

func TestVerificationToolsUseShellNotBash(t *testing.T) {
	tools := VerificationTools()
	if len(tools) != 2 {
		t.Fatalf("got %d tools, want 2", len(tools))
	}

	names := []string{tools[0].Function.Name, tools[1].Function.Name}
	if names[0] != "shell" || names[1] != "read" {
		t.Fatalf("tool names = %v, want [shell read]", names)
	}
	for _, tool := range tools {
		if tool.Type != "function" {
			t.Errorf("%s: type = %q, want \"function\"", tool.Function.Name, tool.Type)
		}
		params, ok := tool.Function.Parameters["type"]
		if !ok || params != "object" {
			t.Errorf("%s: parameters.type missing or not \"object\"", tool.Function.Name)
		}
		props, ok := tool.Function.Parameters["properties"].(map[string]any)
		if !ok || len(props) == 0 {
			t.Fatalf("%s: parameters.properties missing or empty", tool.Function.Name)
		}
		if _, ok := tool.Function.Parameters["required"].([]string); !ok {
			t.Errorf("%s: parameters.required missing", tool.Function.Name)
		}
		if tool.Function.Description == "" {
			t.Errorf("%s: empty description", tool.Function.Name)
		}
	}

	shell, read := tools[0], tools[1]
	shellProps := shell.Function.Parameters["properties"].(map[string]any)
	for _, key := range []string{"command", "workdir", "timeout", "background"} {
		if _, ok := shellProps[key]; !ok {
			t.Errorf("shell tool missing the %q parameter", key)
		}
	}
	readProps := read.Function.Parameters["properties"].(map[string]any)
	for _, key := range []string{"path", "offset", "limit"} {
		if _, ok := readProps[key]; !ok {
			t.Errorf("read tool missing the %q parameter", key)
		}
	}
}

func TestMapFreeModelCoversDeepSeek(t *testing.T) {
	cases := map[string]string{
		"muse-spark-1.3":      "muse-spark-1.3-contributor-free",
		"muse-spark-1.2":      "muse-spark-1.2-contributor-free",
		"union-alpha-free":    "union-alpha",
		"deepseek-v4.1-flash": "deepseek-v4.1-flash-free",
		// Pass-through for anything already upstream-shaped.
		"mimo-v2.6-flash-free": "mimo-v2.6-flash-free",
	}
	for in, want := range cases {
		if got := MapFreeModel(in); got != want {
			t.Errorf("MapFreeModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGeneratedIDsMatchCanonicalShape(t *testing.T) {
	for i := 0; i < 200; i++ {
		id := GenerateSessionID()
		if !sessionIDRe.MatchString(id) {
			t.Fatalf("GenerateSessionID() = %q, does not match %s", id, sessionIDRe)
		}
		if len(id) != 30 {
			t.Fatalf("GenerateSessionID() = %q, len = %d, want 30", id, len(id))
		}
		req := GenerateRequestID()
		if !strings.HasPrefix(req, "msg_") {
			t.Fatalf("GenerateRequestID() = %q, want the msg_ prefix", req)
		}
		if len(req) != 30 {
			t.Fatalf("GenerateRequestID() = %q, len = %d, want 30", req, len(req))
		}
	}
}

func TestIDCounterIsPerMillisecond(t *testing.T) {
	// Two ids minted in the same millisecond must differ in the low 12 bits,
	// which is exactly what the free-tier gate decodes.
	first := GenerateTimestampID(false)
	second := GenerateTimestampID(false)
	if first == second {
		t.Fatal("two ids minted back to back are identical")
	}
	if first[:12] == second[:12] {
		t.Fatalf("timestamp halves match (%q), the counter did not advance", first[:12])
	}
}

func TestBuildFreeRequestShape(t *testing.T) {
	const session = "ses_0123456789abcdefghijklmn"
	req := BuildFreeRequest(DefaultAppInfo(), session, MapFreeModel("deepseek-v4.1-flash"), "ping")

	if req.Method != "POST" {
		t.Errorf("Method = %q, want POST", req.Method)
	}
	if req.URL != "https://opencode.ai/zen/v1/chat/completions" {
		t.Errorf("URL = %q", req.URL)
	}
	if req.Body["model"] != "deepseek-v4.1-flash-free" {
		t.Errorf("model = %v, want deepseek-v4.1-flash-free", req.Body["model"])
	}
	if req.Body["stream"] != true {
		t.Error("stream is not forced on")
	}
	if req.Body["max_tokens"] != 32000 {
		t.Errorf("max_tokens = %v, want 32000", req.Body["max_tokens"])
	}
	opts, ok := req.Body["stream_options"].(map[string]any)
	if !ok || opts["include_usage"] != true {
		t.Error("stream_options.include_usage is not forced on")
	}

	msgs, ok := req.Body["messages"].([]map[string]string)
	if !ok || len(msgs) != 2 {
		t.Fatalf("messages = %v, want [system user]", req.Body["messages"])
	}
	if msgs[0]["role"] != "system" {
		t.Errorf("messages[0].role = %q, want system", msgs[0]["role"])
	}
	if !strings.HasPrefix(msgs[0]["content"], "You are an AI agent running in OpenCode") {
		t.Error("the official v2 prompt is not prepended")
	}
	if msgs[1]["role"] != "user" || msgs[1]["content"] != "ping" {
		t.Errorf("messages[1] = %v, want the user turn", msgs[1])
	}

	tools, ok := req.Body["tools"].([]Tool)
	if !ok || len(tools) != 2 || tools[0].Function.Name != "shell" {
		t.Fatalf("tools = %v, want the shell + read pair", req.Body["tools"])
	}

	// The assembled request must round-trip through encoding/json, which is
	// what the adapter will actually put on the wire.
	if _, err := json.Marshal(req.Body); err != nil {
		t.Fatalf("body does not marshal: %v", err)
	}

	var sawUA, sawAuth, sawClient, sawSessionID bool
	for _, hdr := range req.Headers {
		switch hdr.Name {
		case "User-Agent":
			sawUA = hdr.Value == "opencode/latest/2.0.29/opencode"
		case "Authorization":
			sawAuth = hdr.Value == "Bearer public"
		case "x-opencode-client":
			sawClient = hdr.Value == "opencode"
		case "x-opencode-session-id":
			sawSessionID = hdr.Value == session
		}
	}
	if !sawUA || !sawAuth || !sawClient || !sawSessionID {
		t.Fatalf("headers incomplete: ua=%v auth=%v client=%v sessionID=%v", sawUA, sawAuth, sawClient, sawSessionID)
	}
}

func TestDeltaIsComplete(t *testing.T) {
	changes := Delta()
	if len(changes) == 0 {
		t.Fatal("Delta() is empty")
	}
	seen := map[string]bool{}
	for _, c := range changes {
		if c.Attribute == "" || c.V1 == "" || c.V2 == "" || c.Note == "" {
			t.Errorf("incomplete change entry: %+v", c)
		}
		if seen[c.Attribute] {
			t.Errorf("duplicate attribute %q", c.Attribute)
		}
		seen[c.Attribute] = true
	}

	// Every attribute the v2 header set actually carries must be accounted
	// for, otherwise the review is looking at an incomplete delta.
	for _, required := range []string{
		"User-Agent", "x-opencode-client", "x-opencode-session-id",
		"x-session-affinity", "X-Session-Id", "x-opencode-request",
		"system prompt", "verification tools",
	} {
		if !seen[required] {
			t.Errorf("Delta() is missing the %q attribute", required)
		}
	}
}
