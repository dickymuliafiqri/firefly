package main

import (
	"encoding/json"
	"fmt"
)

// LoadProfile represents the intensity of the LLM chat workload.
type LoadProfile string

const (
	// ProfileLow simulates a lightweight single-turn request (minimal tokens, fast turn).
	ProfileLow LoadProfile = "low"
	// ProfileMedium simulates standard interactive conversational chat with SSE streaming (~128 tokens).
	ProfileMedium LoadProfile = "medium"
	// ProfileHeavy simulates a heavy multi-turn context payload with sustained SSE streaming (~512 tokens).
	ProfileHeavy LoadProfile = "heavy"
)

// ChatMessage mirrors the OpenAI chat message schema.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatCompletionRequest mirrors the standard OpenAI chat completion request payload.
type ChatCompletionRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens"`
	Temperature float64       `json:"temperature"`
	Stream      bool          `json:"stream"`
}

// ProfileConfig encapsulates parameters and messages for a workload profile.
type ProfileConfig struct {
	Name        LoadProfile
	Description string
	MaxTokens   int
	Stream      bool
	Messages    []ChatMessage
}

// GetProfile resolves and builds the request payload for the requested load profile.
func GetProfile(name LoadProfile, model string, overrideStream *bool) (ProfileConfig, []byte, error) {
	var cfg ProfileConfig

	switch name {
	case ProfileLow:
		cfg = ProfileConfig{
			Name:        ProfileLow,
			Description: "Low Load: minimal prompt, non-streaming, 16 max tokens. Fast round-trip test.",
			MaxTokens:   16,
			Stream:      false,
			Messages: []ChatMessage{
				{Role: "user", Content: "Ping! Respond with 'pong' and nothing else."},
			},
		}

	case ProfileMedium:
		cfg = ProfileConfig{
			Name:        ProfileMedium,
			Description: "Medium Load: technical explanation prompt (~150 words), streaming SSE, 128 max tokens. Interactive UI simulation.",
			MaxTokens:   128,
			Stream:      true,
			Messages: []ChatMessage{
				{
					Role: "user",
					Content: "Explain in two concise paragraphs how an API gateway differentiates between credential " +
						"429 quota exhaustion errors (Layer 1) and upstream 5xx infrastructure outages (Layer 2).",
				},
			},
		}

	case ProfileHeavy:
		cfg = ProfileConfig{
			Name:        ProfileHeavy,
			Description: "Heavy Load: multi-turn architecture context (~2KB prompt), streaming SSE with sustained chunk processing, 512 max tokens. High-concurrency stress test.",
			MaxTokens:   512,
			Stream:      true,
			Messages: []ChatMessage{
				{
					Role: "system",
					Content: "You are an elite distributed systems architect and high-concurrency Go engineer. " +
						"You evaluate architectural invariants, memory discipline, buffer recycling, zero-allocation streams, " +
						"and circuit breaker resilience under heavy production load.",
				},
				{
					Role: "user",
					Content: "We need to serve 1,000 simultaneous Server-Sent Events (SSE) connections for LLM inference " +
						"on an API gateway written in Go. What are the key architectural invariants and memory constraints?",
				},
				{
					Role: "assistant",
					Content: "First, never set http.Server.WriteTimeout on the data plane because SSE inference streams can last " +
						"several minutes; use idle gap watchdogs instead. Second, pool buffers using sync.Pool with a 512KB capacity " +
						"ceiling to avoid persistent heap bloat. Third, strictly isolate Layer 1 (429/401) key rotation from Layer 2 " +
						"(5xx) circuit breakers. Fourth, decouple stream contexts during graceful shutdown so active streams drain cleanly.",
				},
				{
					Role: "user",
					Content: "Provide a comprehensive deep-dive analysis on how the gateway must handle client disconnect aborts, " +
						"per-credential in-flight concurrency gates, and stream watchdog idle timeouts to prevent upstream billing waste " +
						"and socket leaks when 1,000 concurrent streams are in-flight.",
				},
			},
		}

	default:
		return cfg, nil, fmt.Errorf("unknown load profile %q (expected: low, medium, heavy)", name)
	}

	if overrideStream != nil {
		cfg.Stream = *overrideStream
	}

	req := ChatCompletionRequest{
		Model:       model,
		Messages:    cfg.Messages,
		MaxTokens:   cfg.MaxTokens,
		Temperature: 0.7,
		Stream:      cfg.Stream,
	}

	raw, err := json.Marshal(req)
	if err != nil {
		return cfg, nil, fmt.Errorf("marshal chat request: %w", err)
	}

	return cfg, raw, nil
}
