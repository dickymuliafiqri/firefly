package tokensaver

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// DefaultMaxToolOutputChars bounds a single tool result when the caller does not
// configure a limit. It matches domain.DefaultTokenSaverConfig.
const DefaultMaxToolOutputChars = 12000

var (
	// ansiRegex matches ANSI escape sequences (colors, cursor movements, etc.),
	// supporting both raw byte 27 (\x1b) and literal escaped representations (\u001b, \x1b).
	ansiRegex = regexp.MustCompile(`(?:\x1b|\\u001b|\\x1b)\[[0-9;]*[a-zA-Z]`)

	// progressLineRegex is only a *classifier*: it decides whether a line is
	// allowed to participate in progress-bar collapsing. It is never used on its
	// own to merge two lines (see progressSkeleton).
	progressLineRegex = regexp.MustCompile(`(?i)(?:progress|downloading|uploading|fetching|\d+%)`)
)

// StripANSI removes all ANSI color and control escape sequences.
func StripANSI(s string) string {
	if !strings.Contains(s, "\x1b") && !strings.Contains(s, `\u001b`) && !strings.Contains(s, `\x1b`) {
		return s
	}
	return ansiRegex.ReplaceAllString(s, "")
}

// progressSkeleton reduces a progress/spinner line to a comparable shape: digits
// become '#', bar art (= - > * # . | + /) becomes 'B', runs of whitespace collapse
// to one space, and letters are lower-cased. Two lines share a skeleton only when
// they are the same progress indicator advancing (e.g. "45%" vs "99%", or two
// widths of the same "[===>   ]" bar). Lines such as "webpack compiled 100%" and
// "go: downloading foo 40%" have different skeletons and are never merged.
//
// ok is false when the line is not progress-like at all, in which case only exact
// equality may collapse it.
func progressSkeleton(line string) (string, bool) {
	if !progressLineRegex.MatchString(line) {
		return "", false
	}

	var b strings.Builder
	b.Grow(len(line))
	pendingSpace := false

	for _, r := range line {
		switch {
		case r >= '0' && r <= '9':
			b.WriteByte('#')
			pendingSpace = false
		case r == ' ' || r == '\t':
			pendingSpace = true
		case strings.ContainsRune("=->*.#|+/", r):
			b.WriteByte('B')
			pendingSpace = false
		default:
			if pendingSpace {
				b.WriteByte(' ')
				pendingSpace = false
			}
			b.WriteRune(unicode.ToLower(r))
		}
	}

	return b.String(), true
}

// repeatedMarker renders the in-place note that replaces a collapsed run.
func repeatedMarker(duplicates int) string {
	return fmt.Sprintf("... [RTK: repeated %d times] ...", duplicates)
}

// DeduplicateConsecutiveLines collapses runs of repeated lines into one kept line
// plus a "... [RTK: repeated N times] ..." marker.
//
// A run advances only when the next line is byte-identical to the previous one
// (ignoring trailing whitespace), or when both are progress-style lines whose
// skeletons match, i.e. they differ only in counters and bar art. The kept line is
// the last of the run so a collapsed progress bar still reports its final state.
// A run is collapsed only when the marker is actually smaller than the lines it
// replaces, and the input's original line terminator is preserved exactly.
func DeduplicateConsecutiveLines(s string) string {
	if !strings.Contains(s, "\n") {
		return s
	}

	lines := strings.Split(s, "\n")
	if len(lines) < 4 {
		return s
	}

	out := make([]string, 0, len(lines))

	// The run's opening line defines what every later line must match against:
	// either its exact text, or (for progress lines) its skeleton.
	runStart, runEnd := 0, 0
	opening := ""
	openingSkeleton := ""
	openingIsProgress := false

	keep := func(i int) { out = append(out, lines[i]) }

	// flush collapses [start, end] (inclusive) if it is a repeated run.
	flush := func(start, end int) {
		duplicates := end - start
		if duplicates <= 0 {
			keep(start)
			return
		}
		// Runs shorter than this never save bytes, but the size check below is
		// what actually guards collapsing (it also protects blank-line runs).
		original := 0
		for i := start; i <= end; i++ {
			original += len(lines[i]) + 1 // +1 for the joining newline
		}
		kept := len(lines[end]) + 1
		marker := len(repeatedMarker(duplicates)) + 1
		if kept+marker < original {
			out = append(out, lines[end], repeatedMarker(duplicates))
		} else {
			for i := start; i <= end; i++ {
				keep(i)
			}
		}
	}

	for i := range lines {
		trimmed := strings.TrimRight(lines[i], "\r ")
		skeleton, isProgress := progressSkeleton(trimmed)

		if i > 0 {
			exact := trimmed == opening
			progress := isProgress && openingIsProgress && skeleton == openingSkeleton
			if exact || progress {
				runEnd = i
				continue
			}
			flush(runStart, runEnd)
			runStart, runEnd = i, i
		}

		opening = trimmed
		openingSkeleton = skeleton
		openingIsProgress = isProgress
	}
	flush(runStart, runEnd)

	return strings.Join(out, "\n")
}

// CompactGitDiff reduces unmodified context lines in unified diffs while
// preserving every hunk header and every added/removed line.
//
// For each run of context lines the first two and the last two are kept (context
// adjacent to a change is the informative part) and the middle is replaced by a
// "... [RTK: context lines omitted] (N lines) ..." marker that states how many
// lines vanished. Hunk headers are copied verbatim, so the compacted text is an
// informative summary for the model and is NOT a patch that still applies — a
// diff with elided context cannot be fed to `git apply` and line numbers inside
// it are relative to the original file.
func CompactGitDiff(s string) string {
	if !strings.Contains(s, "@@") && !strings.Contains(s, "diff --git") {
		return s
	}

	lines := strings.Split(s, "\n")
	if len(lines) < 10 {
		return s
	}

	out := make([]string, 0, len(lines))
	ctx := make([]string, 0, 16)

	flushCtx := func() {
		if len(ctx) <= 4 {
			out = append(out, ctx...)
			ctx = ctx[:0]
			return
		}
		// Keep the two lines on each side of the elision: context adjacent to a
		// change is what makes a diff readable. Collapse only when the marker is
		// genuinely cheaper than the lines it replaces.
		omitted := len(ctx) - 4
		marker := fmt.Sprintf("... [RTK: context lines omitted] (%d lines) ...", omitted)
		last := len(ctx) - 1
		original, kept := 0, len(marker)+1
		for _, line := range ctx {
			original += len(line) + 1
		}
		for _, i := range []int{0, 1, last - 1, last} {
			kept += len(ctx[i]) + 1
		}
		if kept >= original {
			out = append(out, ctx...)
		} else {
			out = append(out, ctx[0], ctx[1], marker, ctx[len(ctx)-2], ctx[len(ctx)-1])
		}
		ctx = ctx[:0]
	}

	for _, line := range lines {
		if strings.HasPrefix(line, " ") {
			ctx = append(ctx, line)
			continue
		}
		flushCtx()
		out = append(out, line)
	}
	flushCtx()

	return strings.Join(out, "\n")
}

// safeHead returns the first n bytes of s without splitting a UTF-8 rune.
func safeHead(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// safeTail returns the last n bytes of s without splitting a UTF-8 rune.
func safeTail(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if n >= len(s) {
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// truncateMarker reports how many bytes were dropped from the middle.
func truncateMarker(omitted int) string {
	return "\n\n... [RTK: truncated " + strconv.Itoa(omitted) + " characters] ...\n\n"
}

// SmartTruncate shortens text to at most maxChars bytes, preserving the head (the
// command and initial state) and the tail (recent output, errors, summaries).
//
// The budget accounts for the marker itself, so the result is never longer than
// maxChars and never longer than the input. Cuts land on UTF-8 rune boundaries.
func SmartTruncate(s string, maxChars int) string {
	if maxChars <= 0 {
		maxChars = DefaultMaxToolOutputChars
	}
	if len(s) <= maxChars {
		return s
	}

	// Worst-case marker size for this input; the reported omission count never
	// exceeds len(s), and digit width is monotonic, so this is a true upper bound.
	worst := len(truncateMarker(len(s)))
	keep := maxChars - worst
	if keep < 2 {
		return safeHead(s, maxChars)
	}

	headLen := keep / 3
	tailLen := keep - headLen
	head := safeHead(s, headLen)
	// safeTail must receive the whole string: it aligns the last tailLen bytes
	// on a rune boundary itself, which a pre-sliced argument would defeat.
	tail := safeTail(s, tailLen)
	omitted := len(s) - len(head) - len(tail)

	out := head + truncateMarker(omitted) + tail
	if len(out) > maxChars || len(out) >= len(s) {
		return safeHead(s, maxChars)
	}
	return out
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
