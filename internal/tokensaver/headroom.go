package tokensaver

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/textx"
)

const (
	// headroomPreserveLatest is the number of trailing messages that are never
	// pruned, so the active turn keeps its full fidelity.
	headroomPreserveLatest = 4
	// headroomMiddleChars prunes any middle message whose content is longer than
	// this many bytes.
	headroomMiddleChars = 1500
	// headroomMiddleHeadChars is the rune-safe prefix kept for pruned middle
	// messages.
	headroomMiddleHeadChars = 250
	// headroomToolChars and headroomToolHeadChars do the same for stale tool
	// results, which carry less narrative value than user/assistant turns.
	headroomToolChars     = 500
	headroomToolHeadChars = 200
)

// PruneMiddleHistory shortens excessive middle messages when the total body
// length exceeds the context threshold. Message 0 (the system prompt) and the
// latest headroomPreserveLatest messages are left alone, and only message
// content is rewritten so tool_call / tool_result pairing survives.
func PruneMiddleHistory(body []byte, tokenThreshold int) ([]byte, bool) {
	if tokenThreshold <= 0 {
		tokenThreshold = domain.DefaultContextThreshold
	}
	// Approximate 1 token ~= 4 chars
	charThreshold := tokenThreshold * 4
	if len(body) < charThreshold {
		return body, false
	}

	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body, false
	}

	msgArr := messages.Array()
	totalMsgs := len(msgArr)
	// Need a middle region at all: msg 0 is the system prompt, the last
	// headroomPreserveLatest messages are the live turn.
	if totalMsgs < 2+headroomPreserveLatest {
		return body, false
	}

	modified := false
	out := body
	startIdx := 1
	endIdx := totalMsgs - headroomPreserveLatest

	for i := startIdx; i < endIdx; i++ {
		msg := msgArr[i]
		role := msg.Get("role").String()
		content := msg.Get("content")

		if content.Type != gjson.String {
			continue
		}
		orig := content.String()

		var pruned string
		switch {
		case len(orig) > headroomMiddleChars:
			// Keep a short leading excerpt so the model can still tell what the
			// turn was about; the cut never splits a UTF-8 rune.
			head := textx.Head(orig, headroomMiddleHeadChars)
			pruned = fmt.Sprintf("%s\n\n... [Headroom: %d characters pruned from middle context history] ...",
				head, len(orig)-len(head))
		case role == "tool" && len(orig) > headroomToolChars:
			head := textx.Head(orig, headroomToolHeadChars)
			pruned = fmt.Sprintf("%s\n\n... [Headroom: tool output pruned from middle context] ...", head)
		default:
			continue
		}

		// Only rewrite when it is a genuine reduction.
		if len(pruned) >= len(orig) {
			continue
		}

		path := fmt.Sprintf("messages.%d.content", i)
		if updated, err := sjson.SetBytes(out, path, pruned); err == nil {
			out = updated
			modified = true
		}
	}

	return out, modified
}

// CompressResult encapsulates compression metrics for the /v1/compress endpoint.
type CompressResult struct {
	OriginalChars    int     `json:"original_chars"`
	CompressedChars  int     `json:"compressed_chars"`
	CharsSaved       int     `json:"chars_saved"`
	CompressionRatio float64 `json:"compression_ratio"`
	Output           string  `json:"output,omitempty"`
}

// CompressText runs RTK tool output and line deduplication compression on arbitrary text.
func CompressText(raw string, maxChars int) CompressResult {
	compressed := CompressToolContent(raw, maxChars)
	saved := len(raw) - len(compressed)
	ratio := 0.0
	if len(raw) > 0 {
		ratio = float64(len(compressed)) / float64(len(raw))
	}
	return CompressResult{
		OriginalChars:    len(raw),
		CompressedChars:  len(compressed),
		CharsSaved:       saved,
		CompressionRatio: ratio,
		Output:           compressed,
	}
}
