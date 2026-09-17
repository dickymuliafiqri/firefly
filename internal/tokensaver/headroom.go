package tokensaver

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// PruneMiddleHistory truncates excessive middle messages when total body length
// exceeds the context threshold, preserving the system prompt and the latest 4 messages.
func PruneMiddleHistory(body []byte, tokenThreshold int) ([]byte, bool) {
	if tokenThreshold <= 0 {
		tokenThreshold = 32000
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
	// Need at least 6 messages to have a middle region (msg 0 is system, msgs N-4..N-1 are recent)
	if totalMsgs < 6 {
		return body, false
	}

	modified := false
	out := body
	startIdx := 1
	endIdx := totalMsgs - 4

	for i := startIdx; i < endIdx; i++ {
		msg := msgArr[i]
		role := msg.Get("role").String()
		content := msg.Get("content")

		// If a middle message has string content exceeding 1500 chars (old tool or old verbose response)
		if content.Type == gjson.String && len(content.String()) > 1500 {
			orig := content.String()
			// Prune to short summary
			pruned := fmt.Sprintf("%s\n\n... [Headroom: %d characters pruned from middle context history] ...", orig[:250], len(orig)-250)
			path := fmt.Sprintf("messages.%d.content", i)
			if updated, err := sjson.SetBytes(out, path, pruned); err == nil {
				out = updated
				modified = true
			}
		} else if role == "tool" && content.Type == gjson.String && len(content.String()) > 500 {
			orig := content.String()
			pruned := fmt.Sprintf("%s\n\n... [Headroom: tool output pruned from middle context] ...", orig[:200])
			path := fmt.Sprintf("messages.%d.content", i)
			if updated, err := sjson.SetBytes(out, path, pruned); err == nil {
				out = updated
				modified = true
			}
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
