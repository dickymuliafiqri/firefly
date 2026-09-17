package tokensaver_test

import (
	"strings"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/tokensaver"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

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
	diff := `diff --git a/server.go b/server.go
index 1234..5678 100644
--- a/server.go
+++ b/server.go
@@ -1,10 +1,10 @@
 context line 1
 context line 2
 context line 3
 context line 4
 context line 5
-old code
+new code
 context line 6
 context line 7
 context line 8`

	compacted := tokensaver.CompactGitDiff(diff)
	require.Contains(t, compacted, "[RTK: context lines omitted]")
	require.Contains(t, compacted, "-old code")
	require.Contains(t, compacted, "+new code")
}

func TestSmartTruncate(t *testing.T) {
	longText := strings.Repeat("A", 500) + strings.Repeat("B", 500)
	truncated := tokensaver.SmartTruncate(longText, 200)

	require.Len(t, truncated, 200+len("\n\n... [RTK: truncated 800 characters] ...\n\n"))
	require.True(t, strings.HasPrefix(truncated, "AAAA"))
	require.True(t, strings.HasSuffix(truncated, "BBBB"))
	require.Contains(t, truncated, "truncated 800 characters")
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
