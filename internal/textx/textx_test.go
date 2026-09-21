package textx_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/dickymuliafiqri/firefly/internal/textx"
)

func TestHeadTailAgreeWithRuneSlicing(t *testing.T) {
	// "héllo 🔥 wörld" mixes 1-, 2-, and 4-byte runes; padding shifts the byte
	// offsets so every alignment against a rune boundary is exercised.
	base := "héllo 🔥 wörld ünïcode 日本"
	for pad := 0; pad < 6; pad++ {
		s := strings.Repeat("a", pad) + base
		runes := []rune(s)
		for n := 0; n <= len(s)+3; n++ {
			head := textx.Head(s, n)
			tail := textx.Tail(s, n)

			require.LessOrEqual(t, len(head), n)
			require.LessOrEqual(t, len(tail), n)
			require.True(t, utf8.ValidString(head), "head n=%d pad=%d", n, pad)
			require.True(t, utf8.ValidString(tail), "tail n=%d pad=%d", n, pad)
			require.True(t, strings.HasPrefix(s, head), "head n=%d pad=%d", n, pad)
			require.True(t, strings.HasSuffix(s, tail), "tail n=%d pad=%d", n, pad)

			// The cut keeps as much as a rune-aligned slice could: dropping one
			// more rune than necessary would be a silent data loss.
			require.Equal(t, string(runes[:len([]rune(head))]), head)
			require.Equal(t, string(runes[len(runes)-len([]rune(tail)):]), tail)
		}
	}
}

func TestHeadTailClampNonPositive(t *testing.T) {
	require.Equal(t, "", textx.Head("hello", 0))
	require.Equal(t, "", textx.Head("hello", -1))
	require.Equal(t, "", textx.Tail("hello", 0))
	require.Equal(t, "", textx.Tail("hello", -1))
}

func TestHeadTailWholeString(t *testing.T) {
	require.Equal(t, "hola", textx.Head("hola", 4))
	require.Equal(t, "hola", textx.Head("hola", 99))
	require.Equal(t, "hola", textx.Tail("hola", 4))
	require.Equal(t, "hola", textx.Tail("hola", 99))
	require.Equal(t, "", textx.Tail("", 4))
}

func TestExcerptCollapsesAndBounds(t *testing.T) {
	require.Equal(t, "boom: bad request", textx.Excerpt([]byte("\n  boom:   bad\nrequest \t"), 512))

	full := textx.Excerpt([]byte("short"), 512)
	require.Equal(t, "short", full)

	long := textx.Excerpt([]byte(strings.Repeat("x", 900)), 100)
	require.Len(t, long, 103)
	require.True(t, strings.HasSuffix(long, "..."))

	// max <= 0 is an explicit "no cap" request.
	require.Len(t, textx.Excerpt([]byte(strings.Repeat("x", 900)), 0), 900)

	require.Equal(t, "", textx.Excerpt(nil, 100))
}

func TestExcerptOnBinaryBodyStaysValidUTF8(t *testing.T) {
	binary := append([]byte{0xff, 0xfe, 0x00, 0x80}, []byte(strings.Repeat("🔥", 200))...)
	out := textx.Excerpt(binary, 64)
	require.True(t, utf8.ValidString(out), "excerpt must be valid UTF-8")
	require.LessOrEqual(t, len(out), 67)
	require.True(t, strings.HasSuffix(out, "..."))

	// A cut landing mid-rune must not emit a continuation byte on its own.
	for n := 1; n < 40; n++ {
		require.True(t, utf8.ValidString(textx.Excerpt([]byte(strings.Repeat("🔥", 50)), n)), "max=%d", n)
	}
}
