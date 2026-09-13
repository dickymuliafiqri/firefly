// Package integration — Phase 6 SDK end-to-end coverage.
//
// These tests drive the gateway with the OFFICIAL OpenAI Go client
// (github.com/openai/openai-go) rather than hand-rolled HTTP. That matters
// because the SDK encodes real client assumptions we must satisfy to be
// "OpenAI-compatible": it sends `Authorization: Bearer <key>`, expects specific
// response JSON shapes (chat.completion / chat.completion.chunk), and parses the
// SSE framing itself. A hand-written http.Client test can accidentally pass
// against a shape the real SDK rejects; this file closes that gap.
package integration

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"github.com/dickymuliafiqri/firefly/internal/registry"
)

// newSDKClient points the official client at the gateway. apiKey is the
// GATEWAY key (not the upstream key): the gateway authenticates the client and
// injects the real upstream secret itself. maxRetries=0 so failures surface
// immediately instead of being retried (important for the auth-failure tests).
func newSDKClient(baseURL, apiKey string) openai.Client {
	return openai.NewClient(
		option.WithBaseURL(baseURL+"/v1"),
		option.WithAPIKey(apiKey),
		option.WithMaxRetries(0),
	)
}

// TestSDKNonStreamingChatCompletion drives a buffered chat completion through the
// real SDK and asserts the SDK can decode our relayed response.
func TestSDKNonStreamingChatCompletion(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-fake-upstream")
	up := newFakeUpstream(t)

	dir := t.TempDir()
	writeConfigWithUpstream(t, dir, up.URL, []string{"gpt-4o-mini"})
	reg := registry.New()
	build(t, reg, dir)
	base := setupServerWithAdapter(t, reg, realAdapter())

	client := newSDKClient(base, gatewayKey)
	resp, err := client.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model:    "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("SDK completion: %v", err)
	}
	if up.gotModel != "gpt-4o-mini" {
		t.Fatalf("upstream saw model %q", up.gotModel)
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content != "hi" {
		t.Fatalf("decoded response = %+v", resp)
	}
}

// TestSDKStreamingChatCompletion drives a streaming completion through the real
// SDK, proving the SDK's SSE parser accepts our verbatim relay (including the
// live flush behavior) and that it reassembles the deltas.
func TestSDKStreamingChatCompletion(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-fake-upstream")
	up := newFakeUpstream(t)

	dir := t.TempDir()
	writeConfigWithUpstream(t, dir, up.URL, []string{"gpt-4o-mini"})
	reg := registry.New()
	build(t, reg, dir)
	base := setupServerWithAdapter(t, reg, realAdapter())

	client := newSDKClient(base, gatewayKey)
	stream := client.Chat.Completions.NewStreaming(context.Background(), openai.ChatCompletionNewParams{
		Model:    "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	})
	defer stream.Close()

	var got strings.Builder
	for stream.Next() {
		chunk := stream.Current()
		if len(chunk.Choices) > 0 {
			got.WriteString(chunk.Choices[0].Delta.Content)
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if got.String() != "Hello!" {
		t.Fatalf("streamed content = %q, want %q", got.String(), "Hello!")
	}
}

// TestSDKRejectsBadGatewayKey proves the SDK surfaces our 401 as a typed error
// with StatusCode 401 (fail-closed auth), not a transport error.
func TestSDKRejectsBadGatewayKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-fake-upstream")
	up := newFakeUpstream(t)

	dir := t.TempDir()
	writeConfigWithUpstream(t, dir, up.URL, []string{"gpt-4o-mini"})
	reg := registry.New()
	build(t, reg, dir)
	base := setupServerWithAdapter(t, reg, realAdapter())

	client := newSDKClient(base, "sk-wrong-key")
	_, err := client.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model:    "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	})
	if err == nil {
		t.Fatal("expected 401 for a bad gateway key")
	}
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is not *openai.Error: %T %v", err, err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("StatusCode = %d, want 401", apiErr.StatusCode)
	}
}

// TestSDKForbiddenModelIs403 proves a model the tenant may not use comes back as
// a 403 the SDK decodes, not a 500 or a hang.
func TestSDKForbiddenModelIs403(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-fake-upstream")
	up := newFakeUpstream(t)

	dir := t.TempDir()
	writeConfigWithUpstream(t, dir, up.URL, []string{"gpt-4o-mini"}) // gpt-4o NOT allowed
	reg := registry.New()
	build(t, reg, dir)
	base := setupServerWithAdapter(t, reg, realAdapter())

	client := newSDKClient(base, gatewayKey)
	_, err := client.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model:    "gpt-4o",
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	})
	if err == nil {
		t.Fatal("expected 403 for a forbidden model")
	}
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is not *openai.Error: %T %v", err, err)
	}
	if apiErr.StatusCode != http.StatusForbidden {
		t.Fatalf("StatusCode = %d, want 403", apiErr.StatusCode)
	}
}
