package tokensaver_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/tokensaver"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// jsonQuote renders s as a JSON string literal using JSON escapes, which is what a
// real client sends. strconv.Quote would emit Go escapes (backslash-x1b) that are
// not valid JSON.
func jsonQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestStripANSI(t *testing.T) {
	input := "\x1b[31mError:\x1b[0m Failed to compile \x1b[1;32mmain.go\x1b[0m"
	expected := "Error: Failed to compile main.go"
	require.Equal(t, expected, tokensaver.StripANSI(input))

	noAnsi := "Plain text without escapes"
	require.Equal(t, noAnsi, tokensaver.StripANSI(noAnsi))
}

func TestDeduplicateConsecutiveLines(t *testing.T) {
	var lines []string
	for i := 0; i < 15; i++ {
		lines = append(lines, "fetching chunks from cluster [pending]")
	}
	input := strings.Join(lines, "\n")
	output := tokensaver.DeduplicateConsecutiveLines(input)

	require.Contains(t, output, "[RTK: repeated")
	require.Less(t, len(output), len(input))
}

func TestCompactGitDiff(t *testing.T) {
	const ctxLine = " \t\tresolvedDependencyGraph := registry.Load(ctx, manifestPath, opts) // cached\n"

	var sb strings.Builder
	sb.WriteString("diff --git a/server.go b/server.go\n")
	sb.WriteString("index 1234..5678 100644\n")
	sb.WriteString("--- a/server.go\n")
	sb.WriteString("+++ b/server.go\n")
	sb.WriteString("@@ -1,40 +1,41 @@\n")
	sb.WriteString(strings.Repeat(ctxLine, 20))
	sb.WriteString("-old code\n")
	sb.WriteString("+new code\n")
	sb.WriteString(strings.Repeat(ctxLine, 20))
	diff := strings.TrimSuffix(sb.String(), "\n")

	compacted := tokensaver.CompactGitDiff(diff)
	require.Contains(t, compacted, "[RTK: context lines omitted]")
	require.Contains(t, compacted, "-old code")
	require.Contains(t, compacted, "+new code")

	// Compression must actually compress, and must not invent a trailing newline.
	require.Less(t, len(compacted), len(diff))
	require.Equal(t, strings.HasSuffix(diff, "\n"), strings.HasSuffix(compacted, "\n"))

	// The hunk header is copied verbatim, and the two context lines on each side
	// of the change survive, so the edit is still readable in place.
	require.Contains(t, compacted, "@@ -1,40 +1,41 @@")
	lines := strings.Split(compacted, "\n")
	changed := 0
	for i, l := range lines {
		if l == "-old code" {
			require.Equal(t, "+new code", lines[i+1])
			require.True(t, strings.HasPrefix(lines[i-1], " \t\t"))
			require.True(t, strings.HasPrefix(lines[i+2], " \t\t"))
			changed++
		}
	}
	require.Equal(t, 1, changed)

	// A diff too small to profit from is returned byte-identical.
	small := strings.Join([]string{
		"diff --git a/x.go b/x.go", "--- a/x.go", "+++ b/x.go", "@@ -1,8 +1,8 @@",
		" one", " two", " three", " four", " five", "-old", "+new", " six",
	}, "\n")
	require.Equal(t, small, tokensaver.CompactGitDiff(small))
}

func TestSmartTruncate(t *testing.T) {
	longText := strings.Repeat("A", 500) + strings.Repeat("B", 500)
	truncated := tokensaver.SmartTruncate(longText, 200)

	// The budget includes the marker, so the cap is a real guarantee.
	require.LessOrEqual(t, len(truncated), 200)
	require.Less(t, len(truncated), len(longText))
	require.True(t, strings.HasPrefix(truncated, "AAAA"))
	require.True(t, strings.HasSuffix(truncated, "BBBB"))

	// The marker must report exactly how many bytes were dropped.
	markerStart := strings.Index(truncated, "[RTK: truncated ")
	require.Greater(t, markerStart, -1)
	digits := truncated[markerStart+len("[RTK: truncated "):]
	num := digits[:strings.Index(digits, " characters]")]
	omitted, err := strconv.Atoi(num)
	require.NoError(t, err)

	head := strings.TrimSpace(strings.SplitN(truncated, "\n", 2)[0])
	tail := truncated[strings.LastIndex(truncated, "\n\n")+2:]
	require.Equal(t, len(longText), len(head)+len(tail)+omitted)
}

// Regression: inputs slightly over the cap used to come back LARGER than both the
// cap and the original text, because the marker was appended for free.
func TestSmartTruncateNeverGrows(t *testing.T) {
	for _, maxChars := range []int{1, 8, 60, 12000, 40000} {
		for delta := 1; delta <= 60; delta++ {
			in := strings.Repeat("x", maxChars+delta)
			out := tokensaver.SmartTruncate(in, maxChars)
			require.LessOrEqual(t, len(out), maxChars, "cap %d delta %d", maxChars, delta)
			require.LessOrEqual(t, len(out), len(in), "cap %d delta %d", maxChars, delta)
		}
	}

	// Multibyte content must survive a cut that lands mid-rune.
	in := strings.Repeat("a", 199) + "🔥🔥🔥" + strings.Repeat("z", 4000)
	out := tokensaver.SmartTruncate(in, 300)
	require.True(t, utf8.ValidString(out))
}

// Regression: distinct tool-output lines that merely contain a percentage, the
// word "progress", or a download/upload verb used to be merged into one line and
// silently deleted.
func TestDeduplicateKeepsDistinctLogLines(t *testing.T) {
	lines := []string{
		"PASS  src/auth/login.spec.ts (1.2s) coverage 87%",
		"INFO  webpack compiled successfully 100%",
		"go: downloading github.com/foo/bar v1.2.3",
		"note: progress meter disabled in CI",
		"test result: ok. 42 passed; 0 failed [Coverage 91%]",
		"git diff --stat 12 files changed, 34% fewer lines",
	}
	input := strings.Join(lines, "\n")
	output := tokensaver.DeduplicateConsecutiveLines(input)

	require.Equal(t, input, output, "distinct lines must never be merged")
	require.NotContains(t, output, "[RTK: repeated")
}

// A real progress bar advances: same text, different counters. Those still
// collapse, and the surviving line is the final state.
func TestDeduplicateCollapsesProgressBarStates(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("building image\n")
	for _, pct := range []string{"10", "20", "35", "50", "68", "80", "92", "100"} {
		sb.WriteString("[" + strings.Repeat("=", len(pct)*3) + ">" + strings.Repeat(" ", 30-len(pct)*3) + "] " + pct + "% eta 3s\n")
	}
	sb.WriteString("done\n")
	input := sb.String()

	output := tokensaver.DeduplicateConsecutiveLines(input)
	require.Contains(t, output, "[RTK: repeated")
	require.Less(t, len(output), len(input))
	require.Contains(t, output, "100% eta 3s", "final progress state must be kept")
	require.NotContains(t, output, "20% eta 3s")
	require.Contains(t, output, "building image")
	require.Contains(t, output, "done")
}

// Regression: a repeated run at the very end used to append a newline that the
// input never had.
func TestDeduplicatePreservesTerminator(t *testing.T) {
	repeat := "waiting for lock to be released\n"
	input := strings.Repeat(repeat, 12)

	withNL := strings.TrimSuffix(input, "\n")
	require.False(t, strings.HasSuffix(tokensaver.DeduplicateConsecutiveLines(withNL), "\n"))
	require.True(t, strings.HasSuffix(tokensaver.DeduplicateConsecutiveLines(input), "\n"))

	// Unchanged text is returned byte-identical, so `modified` stays honest.
	plain := "alpha\nbravo\ncharlie\ndelta"
	require.Equal(t, plain, tokensaver.DeduplicateConsecutiveLines(plain))
}

// Regression: array (multimodal / Anthropic-style) system content used to be
// flattened into a stringified JSON blob.
func TestApplyPersonasPreservesArraySystemContent(t *testing.T) {
	body := []byte(`{"model":"claude","messages":[{"role":"system","content":[{"type":"text","text":"IMPORTANT REAL SYSTEM PROMPT"}]},{"role":"user","content":"hi"}]}`)

	transformed, ok := tokensaver.ApplyPersonas(body, true, false)
	require.True(t, ok)

	// The directive arrives as its own system message, before the structured one.
	msgs := gjson.GetBytes(transformed, "messages").Array()
	require.Len(t, msgs, 3)

	require.Equal(t, "system", msgs[0].Get("role").String())
	require.Contains(t, msgs[0].Get("content").String(), "CRITICAL INSTRUCTION (Brevity)")

	require.Equal(t, "system", msgs[1].Get("role").String())
	content := msgs[1].Get("content")
	require.Equal(t, gjson.JSON, content.Type, "structured content must stay an array, not a stringified blob")
	require.Equal(t, "IMPORTANT REAL SYSTEM PROMPT", content.Array()[0].Get("text").String())

	require.Equal(t, "user", msgs[2].Get("role").String())
	require.Equal(t, "hi", msgs[2].Get("content").String())

	// Idempotent.
	_, again := tokensaver.ApplyPersonas(transformed, true, false)
	require.False(t, again)
}

// RTK must get the first pass: a huge but highly repetitive middle tool result
// should be collapsed by RTK, not bluntly truncated by Headroom first.
func TestProcessRunsRTKBeforeHeadroom(t *testing.T) {
	toolOut := strings.Repeat("\x1b[32mwaiting for lock to be released\x1b[0m\n", 400)
	msgs := []string{`{"role":"system","content":"sys"}`}
	msgs = append(msgs, `{"role":"tool","tool_call_id":"c1","content":`+jsonQuote(toolOut)+`}`)
	for i := 0; i < 6; i++ {
		msgs = append(msgs, `{"role":"user","content":"padding padding padding padding"}`)
	}
	body := []byte(`{"model":"m","messages":[` + strings.Join(msgs, ",") + `]}`)

	cfg := domain.TokenSaverConfig{
		Enabled: true, CompressToolOutput: true, CompressContext: true,
		MaxToolOutputChars: 12000, ContextThreshold: 1000,
	}
	out, modified := tokensaver.Process(body, cfg)
	require.True(t, modified)

	tool := gjson.GetBytes(out, "messages.1.content").String()
	require.Contains(t, tool, "[RTK: repeated", "RTK should have collapsed the repetition")
	require.NotContains(t, tool, "pruned from middle context", "Headroom must not truncate what RTK already shrank")
	require.NotContains(t, tool, "\x1b")
	require.Less(t, len(out), len(body))
}

// Headroom must not split a UTF-8 rune when keeping a short excerpt. A naive
// byte cut at 250 lands inside the flame emoji here and used to emit U+FFFD.
func TestPruneMiddleHistoryKeepsUTF8(t *testing.T) {
	long := strings.Repeat("a", 248) + "🔥🔥🔥🔥🔥" + strings.Repeat("z", 3000)
	msgs := []string{`{"role":"system","content":"sys"}`, `{"role":"user","content":` + jsonQuote(long) + `}`}
	for i := 0; i < 5; i++ {
		msgs = append(msgs, `{"role":"user","content":"short"}`)
	}
	body := []byte(`{"model":"m","messages":[` + strings.Join(msgs, ",") + `]}`)

	pruned, modified := tokensaver.PruneMiddleHistory(body, 500)
	require.True(t, modified)
	require.Less(t, len(pruned), len(body))
	require.True(t, utf8.Valid(pruned), "pruned payload must stay valid UTF-8")

	content := gjson.GetBytes(pruned, "messages.1.content").String()
	require.True(t, utf8.ValidString(content))
	// A naive byte cut at 250 would leave 249/250 a's plus junk here; the excerpt
	// must back off to the rune boundary at 248.
	require.Equal(t, strings.Repeat("a", 248), strings.SplitN(content, "\n", 2)[0])
}

func TestApplyPersonas(t *testing.T) {
	t.Run("Existing system prompt", func(t *testing.T) {
		body := []byte(`{
			"model": "gpt-4o",
			"messages": [
				{"role": "system", "content": "You are a coding assistant."},
				{"role": "user", "content": "Hello"}
			]
		}`)

		transformed, ok := tokensaver.ApplyPersonas(body, true, true)
		require.True(t, ok)

		sysContent := gjson.GetBytes(transformed, "messages.0.content").String()
		require.Contains(t, sysContent, "CRITICAL INSTRUCTION (Brevity)")
		require.Contains(t, sysContent, "ENGINEERING MANDATE (Minimal Code)")
		require.Contains(t, sysContent, "You are a coding assistant.")

		// Idempotent: second pass should not double inject
		_, okSecond := tokensaver.ApplyPersonas(transformed, true, true)
		require.False(t, okSecond)
	})

	t.Run("No existing system prompt", func(t *testing.T) {
		body := []byte(`{
			"model": "gpt-4o",
			"messages": [
				{"role": "user", "content": "Hello"}
			]
		}`)

		transformed, ok := tokensaver.ApplyPersonas(body, true, false)
		require.True(t, ok)

		msgs := gjson.GetBytes(transformed, "messages").Array()
		require.Len(t, msgs, 2)
		require.Equal(t, "system", msgs[0].Get("role").String())
		require.Contains(t, msgs[0].Get("content").String(), "CRITICAL INSTRUCTION (Brevity)")
		require.Equal(t, "user", msgs[1].Get("role").String())
	})
}

func TestProcessPipeline(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4o",
		"messages": [
			{"role": "user", "content": "Run tests"},
			{"role": "tool", "content": "\u001b[32mPASS\u001b[0m test 1\n\u001b[32mPASS\u001b[0m test 2"}
		]
	}`)

	cfg := domain.TokenSaverConfig{
		Enabled:            true,
		CompressToolOutput: true,
		TerseOutput:        true,
		MinimalCode:        true,
		MaxToolOutputChars: 12000,
	}

	out, modified := tokensaver.Process(body, cfg)
	require.True(t, modified)

	// System message was injected at index 0
	sysRole := gjson.GetBytes(out, "messages.0.role").String()
	require.Equal(t, "system", sysRole)

	// User message shifted to index 1
	userRole := gjson.GetBytes(out, "messages.1.role").String()
	require.Equal(t, "user", userRole)

	// Tool output shifted to index 2 and had ANSI stripped
	toolRole := gjson.GetBytes(out, "messages.2.role").String()
	require.Equal(t, "tool", toolRole)
	toolContent := gjson.GetBytes(out, "messages.2.content").String()
	require.NotContains(t, toolContent, "\x1b[32m")
	require.Contains(t, toolContent, "PASS test 1")
}

func TestPruneMiddleHistory(t *testing.T) {
	hugeContent := strings.Repeat("A", 3000)
	body := []byte(`{
		"model": "gpt-4o",
		"messages": [
			{"role": "system", "content": "You are a helper."},
			{"role": "user", "content": "Query 1"},
			{"role": "assistant", "content": "` + hugeContent + `"},
			{"role": "user", "content": "Query 2"},
			{"role": "assistant", "content": "Resp 2"},
			{"role": "user", "content": "Query 3"},
			{"role": "assistant", "content": "Resp 3"}
		]
	}`)

	// Threshold low enough to trigger pruning
	pruned, modified := tokensaver.PruneMiddleHistory(body, 500)
	require.True(t, modified)
	require.Less(t, len(pruned), len(body))

	middleContent := gjson.GetBytes(pruned, "messages.2.content").String()
	require.Contains(t, middleContent, "Headroom:")
	require.Contains(t, middleContent, "pruned from middle context history")

	// Verify system message (0) and last message (6) are intact
	require.Equal(t, "You are a helper.", gjson.GetBytes(pruned, "messages.0.content").String())
	require.Equal(t, "Resp 3", gjson.GetBytes(pruned, "messages.6.content").String())
}

func TestCompressText(t *testing.T) {
	text := "\x1b[31mError:\x1b[0m\n" + strings.Repeat("downloading chunk 12345 from remote s3 storage...\n", 10)
	res := tokensaver.CompressText(text, 12000)
	require.Greater(t, res.CharsSaved, 0)
	require.NotContains(t, res.Output, "\x1b")
	require.Contains(t, res.Output, "[RTK: repeated")
}

func TestProcessDisabled(t *testing.T) {
	body := []byte(`{"model": "gpt-4o", "messages": []}`)
	cfg := domain.TokenSaverConfig{Enabled: false}

	out, modified := tokensaver.Process(body, cfg)
	require.False(t, modified)
	require.Equal(t, body, out)
}

func BenchmarkProcessDisabled(b *testing.B) {
	body := []byte(`{"model": "gpt-4o", "messages": [{"role": "user", "content": "hello"}]}`)
	cfg := domain.TokenSaverConfig{Enabled: false}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		out, _ := tokensaver.Process(body, cfg)
		if len(out) == 0 {
			b.Fatal("unexpected empty")
		}
	}
}
