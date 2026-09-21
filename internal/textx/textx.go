// Package textx holds the small text-shaping helpers shared by every package
// that truncates or excerpts text: token saving, upstream error relaying, and
// credential harvesting.
//
// Slicing a string on an arbitrary byte offset splits a UTF-8 rune, which then
// breaks JSON encoding, log output, and SSE frames downstream. The cuts here
// always land on a rune boundary.
package textx

import (
	"strings"
	"unicode/utf8"
)

// Head returns the first n bytes of s without splitting a UTF-8 rune.
func Head(s string, n int) string {
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

// Tail returns the last n bytes of s without splitting a UTF-8 rune.
func Tail(s string, n int) string {
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

// Excerpt renders a raw response body as a single line fit for an error message
// or a log attribute: whitespace runs collapse to single spaces, invalid UTF-8
// is dropped, and the result is capped at max bytes plus an ellipsis.
//
// A max <= 0 means "no cap" and returns the whole collapsed text.
func Excerpt(b []byte, max int) string {
	raw := string(b)
	if !utf8.ValidString(raw) {
		raw = strings.ToValidUTF8(raw, "")
	}
	s := strings.Join(strings.Fields(raw), " ")
	if max <= 0 || len(s) <= max {
		return s
	}
	return Head(s, max) + "..."
}
