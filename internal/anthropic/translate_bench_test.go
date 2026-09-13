package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func makeBenchOpenAIPayload(numMessages int, targetSizeBytes int) []byte {
	prefix := `{"model":"gpt-4o","stream":true,"temperature":0.7,"max_tokens":4096,"messages":[`
	suffix := `]}`

	var sb strings.Builder
	if targetSizeBytes > 0 {
		sb.Grow(targetSizeBytes + 1024)
	} else {
		sb.Grow(numMessages * 256)
	}
	sb.WriteString(prefix)

	if targetSizeBytes > 0 {
		// Distribute target bytes across numMessages
		overheadPerMsg := 40
		totalOverhead := len(prefix) + len(suffix) + (numMessages * overheadPerMsg)
		payloadPerMsg := (targetSizeBytes - totalOverhead) / numMessages
		if payloadPerMsg < 10 {
			payloadPerMsg = 10
		}
		chunk := strings.Repeat("Lorem ipsum dolor sit amet, consectetur adipiscing elit. ", (payloadPerMsg/56)+1)
		chunk = chunk[:payloadPerMsg]

		for i := 0; i < numMessages; i++ {
			if i > 0 {
				sb.WriteString(",")
			}
			role := "user"
			if i%2 == 1 {
				role = "assistant"
			}
			sb.WriteString(`{"role":"` + role + `","content":"` + chunk + `"}`)
		}
	} else {
		for i := 0; i < numMessages; i++ {
			if i > 0 {
				sb.WriteString(",")
			}
			role := "user"
			if i%2 == 1 {
				role = "assistant"
			}
			content := fmt.Sprintf("Turn %d: Please assist me with this query regarding Go data structures and performance.", i)
			sb.WriteString(`{"role":"` + role + `","content":"` + content + `"}`)
		}
	}
	sb.WriteString(suffix)
	return []byte(sb.String())
}

func BenchmarkTranslateOpenAIToAnthropic(b *testing.B) {
	scenarios := []struct {
		name        string
		numMessages int
		targetSize  int
	}{
		{"10Messages", 10, 0},
		{"50Messages", 50, 0},
		{"100KBContext", 20, 100 * 1024},
	}

	for _, sc := range scenarios {
		payload := makeBenchOpenAIPayload(sc.numMessages, sc.targetSize)
		b.Run(sc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				out, err := TranslateOpenAIToAnthropic(payload, "claude-3-5-sonnet-20241022")
				if err != nil || len(out) == 0 {
					b.Fatalf("TranslateOpenAIToAnthropic failed: %v", err)
				}
			}
		})
	}
}

// Legacy implementation using json.Unmarshal for benchmark comparison
type legacyOpenAIMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type legacyOpenAIChatRequest struct {
	Model       string                `json:"model"`
	Messages    []legacyOpenAIMessage `json:"messages"`
	MaxTokens   *int                  `json:"max_tokens,omitempty"`
	Temperature *float64              `json:"temperature,omitempty"`
	Stream      bool                  `json:"stream,omitempty"`
	Stop        any                   `json:"stop,omitempty"`
}

func translateLegacy(body []byte, upstreamModel string) ([]byte, error) {
	var oReq legacyOpenAIChatRequest
	if err := json.Unmarshal(body, &oReq); err != nil {
		return nil, fmt.Errorf("invalid request json: %w", err)
	}

	model := upstreamModel
	if model == "" {
		model = oReq.Model
	}

	var systems []string
	var aMsgs []anthropicMessage

	for _, msg := range oReq.Messages {
		var contentStr string
		if len(msg.Content) > 0 {
			var s string
			if err := json.Unmarshal(msg.Content, &s); err == nil {
				contentStr = s
			} else {
				contentStr = string(msg.Content)
			}
		}
		switch strings.ToLower(msg.Role) {
		case "system":
			if contentStr != "" {
				systems = append(systems, contentStr)
			}
		case "user":
			aMsgs = append(aMsgs, anthropicMessage{Role: "user", Content: contentStr})
		case "assistant":
			aMsgs = append(aMsgs, anthropicMessage{Role: "assistant", Content: contentStr})
		default:
			aMsgs = append(aMsgs, anthropicMessage{Role: "user", Content: contentStr})
		}
	}

	maxTokens := 4096
	if oReq.MaxTokens != nil && *oReq.MaxTokens > 0 {
		maxTokens = *oReq.MaxTokens
	}

	aReq := anthropicRequest{
		Model:       model,
		System:      strings.Join(systems, "\n\n"),
		Messages:    aMsgs,
		MaxTokens:   maxTokens,
		Temperature: oReq.Temperature,
		Stream:      oReq.Stream,
	}

	return json.Marshal(aReq)
}

func BenchmarkTranslateOpenAIToAnthropic_Legacy(b *testing.B) {
	scenarios := []struct {
		name        string
		numMessages int
		targetSize  int
	}{
		{"10Messages", 10, 0},
		{"50Messages", 50, 0},
		{"100KBContext", 20, 100 * 1024},
	}

	for _, sc := range scenarios {
		payload := makeBenchOpenAIPayload(sc.numMessages, sc.targetSize)
		b.Run(sc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				out, err := translateLegacy(payload, "claude-3-5-sonnet-20241022")
				if err != nil || len(out) == 0 {
					b.Fatalf("translateLegacy failed: %v", err)
				}
			}
		})
	}
}
