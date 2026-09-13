package server

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

type legacyChatRequest struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
}

func decodeLegacy(body []byte) (string, bool, error) {
	var cr legacyChatRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &cr); err != nil {
			return "", false, err
		}
	}
	if cr.Model == "" {
		return "", false, errors.New("missing model")
	}
	return cr.Model, cr.Stream, nil
}

func decodeGJSON(body []byte) (string, bool, error) {
	if len(body) > 0 && !gjson.ValidBytes(body) {
		return "", false, errors.New("invalid JSON body")
	}
	model := gjson.GetBytes(body, "model").String()
	if model == "" {
		return "", false, errors.New("missing model")
	}
	stream := gjson.GetBytes(body, "stream").Bool()
	return model, stream, nil
}

func makePayload(targetSizeBytes int) []byte {
	prefix := `{"model":"gpt-4o","stream":true,"temperature":0.7,"max_tokens":4096,"messages":[`
	suffix := `]}`

	var sb strings.Builder
	sb.Grow(targetSizeBytes)
	sb.WriteString(prefix)

	msgIndex := 0
	for sb.Len()+len(suffix) < targetSizeBytes-120 {
		if msgIndex > 0 {
			sb.WriteString(",")
		}
		role := "user"
		if msgIndex%2 == 1 {
			role = "assistant"
		}
		chunk := "Lorem ipsum dolor sit amet, consectetur adipiscing elit. Integer nec odio. Praesent libero. Sed cursus ante dapibus diam."
		sb.WriteString(`{"role":"` + role + `","content":"` + chunk + `"}`)
		msgIndex++
	}
	// Pad remainder inside the last message to hit exact target size if needed
	if sb.Len()+len(suffix) < targetSizeBytes {
		if msgIndex > 0 {
			sb.WriteString(",")
		}
		remaining := targetSizeBytes - sb.Len() - len(suffix) - 30
		if remaining > 0 {
			pad := strings.Repeat("a", remaining)
			sb.WriteString(`{"role":"user","content":"` + pad + `"}`)
		} else {
			sb.WriteString(`{"role":"user","content":"end"}`)
		}
	}
	sb.WriteString(suffix)
	return []byte(sb.String())
}

func BenchmarkRoutingInspection_GJSON(b *testing.B) {
	sizes := []struct {
		name string
		size int
	}{
		{"1KB", 1 * 1024},
		{"64KB", 64 * 1024},
		{"1MB", 1 * 1024 * 1024},
		{"10MB", 10 * 1024 * 1024},
	}
	for _, tc := range sizes {
		payload := makePayload(tc.size)
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m, s, err := decodeGJSON(payload)
				if err != nil || m != "gpt-4o" || !s {
					b.Fatalf("unexpected decode result: %v, %v, %v", m, s, err)
				}
			}
		})
	}
}

func BenchmarkRoutingInspection_Unmarshal(b *testing.B) {
	sizes := []struct {
		name string
		size int
	}{
		{"1KB", 1 * 1024},
		{"64KB", 64 * 1024},
		{"1MB", 1 * 1024 * 1024},
		{"10MB", 10 * 1024 * 1024},
	}
	for _, tc := range sizes {
		payload := makePayload(tc.size)
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m, s, err := decodeLegacy(payload)
				if err != nil || m != "gpt-4o" || !s {
					b.Fatalf("unexpected decode result: %v, %v, %v", m, s, err)
				}
			}
		})
	}
}

func decodeUnmarshalMap(body []byte) (string, bool, error) {
	var obj map[string]json.RawMessage
	if len(body) > 0 {
		if err := json.Unmarshal(body, &obj); err != nil {
			return "", false, err
		}
	}
	var model string
	if m, ok := obj["model"]; ok {
		_ = json.Unmarshal(m, &model)
	}
	if model == "" {
		return "", false, errors.New("missing model")
	}
	var stream bool
	if s, ok := obj["stream"]; ok {
		_ = json.Unmarshal(s, &stream)
	}
	return model, stream, nil
}

func BenchmarkRoutingInspection_UnmarshalMap(b *testing.B) {
	sizes := []struct {
		name string
		size int
	}{
		{"1KB", 1 * 1024},
		{"64KB", 64 * 1024},
		{"1MB", 1 * 1024 * 1024},
		{"10MB", 10 * 1024 * 1024},
	}
	for _, tc := range sizes {
		payload := makePayload(tc.size)
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m, s, err := decodeUnmarshalMap(payload)
				if err != nil || m != "gpt-4o" || !s {
					b.Fatalf("unexpected decode result: %v, %v, %v", m, s, err)
				}
			}
		})
	}
}

