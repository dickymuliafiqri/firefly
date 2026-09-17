package tokensaver

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	// ansiRegex matches ANSI escape sequences (colors, cursor movements, etc.),
	// supporting both raw byte 27 (\x1b) and literal escaped representations (\u001b, \x1b).
	ansiRegex = regexp.MustCompile(`(?:\x1b|\\u001b|\\x1b)\[[0-9;]*[a-zA-Z]`)

	// progressLineRegex matches common progress bar outputs.
	progressLineRegex = regexp.MustCompile(`(?i)(?:progress|downloading|uploading|fetching|fetching chunk|\d+%).*`)
)

// StripANSI removes all ANSI color and control escape sequences.
func StripANSI(s string) string {
	if !strings.Contains(s, "\x1b") && !strings.Contains(s, `\u001b`) && !strings.Contains(s, `\x1b`) {
		return s
	}
	return ansiRegex.ReplaceAllString(s, "")
}

// DeduplicateConsecutiveLines collapses 3 or more consecutive identical or near-identical lines.
func DeduplicateConsecutiveLines(s string) string {
	if !strings.Contains(s, "\n") {
		return s
	}

	lines := strings.Split(s, "\n")
	if len(lines) < 4 {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))

	prevLine := ""
	repeatCount := 0
	isProgress := false

	flushRepeat := func() {
		if repeatCount > 2 {
			msg := fmt.Sprintf("... [RTK: repeated %d times] ...\n", repeatCount)
			// Only collapse if it actually saves characters
			if repeatCount*(len(prevLine)+1) > len(msg) {
				b.WriteString(msg)
			} else {
				for r := 0; r < repeatCount; r++ {
					b.WriteString(prevLine)
					b.WriteByte('\n')
				}
			}
		} else if repeatCount > 0 {
			for r := 0; r < repeatCount; r++ {
				b.WriteString(prevLine)
				b.WriteByte('\n')
			}
		}
		repeatCount = 0
	}

	for i, line := range lines {
		trimmed := strings.TrimRight(line, "\r ")
		lineIsProgress := progressLineRegex.MatchString(trimmed)

		if i > 0 && (trimmed == prevLine || (lineIsProgress && isProgress)) {
			repeatCount++
			continue
		}

		flushRepeat()

		b.WriteString(line)
		if i < len(lines)-1 {
			b.WriteByte('\n')
		}
		prevLine = trimmed
		isProgress = lineIsProgress
	}

	flushRepeat()
	return b.String()
}

// CompactGitDiff reduces unmodified context lines in unified diffs while preserving all hunks.
func CompactGitDiff(s string) string {
	if !strings.Contains(s, "@@") && !strings.Contains(s, "diff --git") {
		return s
	}

	lines := strings.Split(s, "\n")
	if len(lines) < 10 {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))

	contextCount := 0
	for i, line := range lines {
		if strings.HasPrefix(line, " ") {
			contextCount++
			// Preserve up to 2 context lines; collapse excess
			if contextCount > 2 {
				continue
			}
		} else {
			if contextCount > 2 {
				b.WriteString(" ... [RTK: context lines omitted] ...\n")
			}
			contextCount = 0
		}

		b.WriteString(line)
		if i < len(lines)-1 {
			b.WriteByte('\n')
		}
	}

	if contextCount > 2 {
		b.WriteString(" ... [RTK: context lines omitted] ...\n")
	}

	return b.String()
}

// SmartTruncate truncates text exceeding maxChars, preserving the head and tail.
func SmartTruncate(s string, maxChars int) string {
	if maxChars <= 0 {
		maxChars = 12000
	}
	if len(s) <= maxChars {
		return s
	}

	// Preserve 1/3 at head (command, initial state) and 2/3 at tail (recent outputs, errors, summaries)
	headLen := maxChars / 3
	tailLen := maxChars - headLen

	if headLen <= 0 || tailLen <= 0 || headLen+tailLen >= len(s) {
		return s[:maxChars]
	}

	head := s[:headLen]
	tail := s[len(s)-tailLen:]
	omitted := len(s) - headLen - tailLen

	return head + "\n\n... [RTK: truncated " + strconv.Itoa(omitted) + " characters] ...\n\n" + tail
}

// CompressToolContent runs the full RTK pipeline on a tool result string.
func CompressToolContent(raw string, maxChars int) string {
	if len(raw) == 0 {
		return raw
	}
	res := StripANSI(raw)
	res = DeduplicateConsecutiveLines(res)
	res = CompactGitDiff(res)
	res = SmartTruncate(res, maxChars)
	return res
}

// CompressMessagesToolOutputs iterates through chat messages in the JSON payload
// and compresses any message with role == "tool" or Anthropic-style tool_result.
func CompressMessagesToolOutputs(body []byte, maxChars int) ([]byte, bool) {
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body, false
	}

	modified := false
	var out = body

	msgArr := messages.Array()
	for i, msg := range msgArr {
		role := msg.Get("role").String()

		// Case 1: Standard OpenAI tool response (role == "tool")
		if role == "tool" {
			contentNode := msg.Get("content")
			if contentNode.Type == gjson.String {
				orig := contentNode.String()
				compressed := CompressToolContent(orig, maxChars)
				if compressed != orig {
					path := fmt.Sprintf("messages.%d.content", i)
					if updated, err := sjson.SetBytes(out, path, compressed); err == nil {
						out = updated
						modified = true
					}
				}
			}
			continue
		}

		// Case 2: Anthropic-style tool_result inside user messages
		// [{"type": "tool_result", "content": "..."}]
		if role == "user" {
			contentNode := msg.Get("content")
			if contentNode.IsArray() {
				for j, part := range contentNode.Array() {
					if part.Get("type").String() == "tool_result" {
						partContent := part.Get("content")
						if partContent.Type == gjson.String {
							orig := partContent.String()
							compressed := CompressToolContent(orig, maxChars)
							if compressed != orig {
								path := fmt.Sprintf("messages.%d.content.%d.content", i, j)
								if updated, err := sjson.SetBytes(out, path, compressed); err == nil {
									out = updated
									modified = true
								}
							}
						}
					}
				}
			}
		}
	}

	return out, modified
}
