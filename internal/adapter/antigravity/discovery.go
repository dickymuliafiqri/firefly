package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/textx"
)

// Live model discovery & health probing for Google Cloud Code.
//
// Cloud Code exposes no model-list route: the only inference verb on the host
// is `v1internal:generateContent`, and every other path answers 404. A model
// list therefore cannot be *fetched* — it has to be **verified** with a
// one-token generation per candidate, which is exactly what this file does.
// Nothing here answers from a hardcoded list: a model appears in a result only
// when Google actually served it, and a failure is reported with the upstream's
// own status and message. That is the difference between a probe and a mock.

const (
	// GenerateContentPath is the non-streaming Cloud Code generation verb.
	GenerateContentPath = "/v1internal:generateContent"
	// CloudCodeUserAgent is the client fingerprint the host expects.
	CloudCodeUserAgent = "antigravity/ide/2.11.0 darwin/arm64"
	// ProbeMaxOutputTokens keeps a verification call to a single token.
	ProbeMaxOutputTokens = 1
	// maxProbeBodyBytes bounds the response body read from a probe.
	maxProbeBodyBytes = 8 << 10
	// maxErrorMessageChars bounds a surfaced upstream error message.
	maxErrorMessageChars = 512
	// googleAPIClient / clientMetadata are the two identity headers the Cloud
	// Code unified gateway documents besides Authorization and User-Agent.
	googleAPIClient = "google-cloud-sdk vscode_cloudshelleditor/0.1"
	clientMetadata  = `{"ideType":"ANTIGRAVITY","platform":"MACOS","pluginType":"GEMINI"}`
)

// curatedModels is the built-in Antigravity catalog served by Fetch models.
// Cloud Code exposes no model-list route, so this is the documented id set
// (docs: antigravity.google/docs/models, verified against the Cloud Code
// unified-gateway spec) rather than something the host can be asked for.
//
// The ids follow the Cloud Code convention, where the family keeps its version
// in the middle and the tier is a **suffix**:
//
//	gemini-<version>-<tier>               gemini-3.8-flash, gemini-3.1-pro-low
//	claude-<family>-<version>[-thinking]   claude-sonnet-4-6-thinking
//	gpt-oss-<size>[-<effort>]              gpt-oss-120b-medium
//
// Not `gemini-flash-3.8`: Antigravity rejects that shape. The Gemini 3 Pro
// family additionally *requires* a thinking-tier suffix — a bare
// `gemini-3.1-pro` answers 404 "Requested entity was not found" — which is why
// the Pro entries only exist in their tier forms. Because the Antigravity model
// selector also offers a Fast tier for Flash, those variants are listed too; the
// per-model Check is what proves an id is actually served to this account.
var curatedModels = []string{
	// Gemini 3.8 / 3.7 / 3.6 Flash — tier optional, but every offered tier is swept.
	"gemini-3.8-flash", "gemini-3.8-flash-fast", "gemini-3.8-flash-low", "gemini-3.8-flash-medium", "gemini-3.8-flash-high",
	"gemini-3.7-flash", "gemini-3.7-flash-fast", "gemini-3.7-flash-low", "gemini-3.7-flash-medium", "gemini-3.7-flash-high",
	"gemini-3.6-flash", "gemini-3.6-flash-fast", "gemini-3.6-flash-low", "gemini-3.6-flash-medium", "gemini-3.6-flash-high",
	// Gemini 3.1 Pro — tier suffix is mandatory (bare id 404s).
	"gemini-3.1-pro-low", "gemini-3.1-pro-medium", "gemini-3.1-pro-high",
	// Claude 4.6 — the Antigravity selector exposes them as thinking models.
	"claude-sonnet-4-6", "claude-sonnet-4-6-thinking",
	"claude-opus-4-6", "claude-opus-4-6-thinking",
	// GPT-OSS 120B.
	"gpt-oss-120b", "gpt-oss-120b-low", "gpt-oss-120b-medium", "gpt-oss-120b-high",
}

// CuratedModels returns a copy of the candidate model ids used to seed a live
// discovery sweep.
func CuratedModels() []string {
	out := make([]string, len(curatedModels))
	copy(out, curatedModels)
	return out
}

// DefaultBaseURL is the provider-managed Cloud Code host.
func DefaultBaseURL() string { return defaultDailyEndpoint }

// DefaultProbeModel is the first model tried when a reachability check arrives
// without one: the current Flash default, which needs no tier suffix.
const DefaultProbeModel = "gemini-3.8-flash"

// DefaultProbeModels is the ordered fallback chain for a reachability check
// with no model: the first id the account can actually serve wins, so an
// unavailable default never turns a healthy upstream red.
func DefaultProbeModels() []string {
	return []string{"gemini-3.8-flash", "gemini-3.7-flash", "gemini-3.6-flash", "gemini-3.1-pro-low"}
}

// NormalizeBaseURL trims trailing slashes and falls back to the managed host
// when the caller supplies nothing.
func NormalizeBaseURL(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		return defaultDailyEndpoint
	}
	return base
}

// NewProbeRequest builds the minimal non-streaming Cloud Code generation used
// to verify one model. It is the same envelope TranslateOpenAIToAntigravity
// produces, with a one-token budget and a "ping" content, so a probe exercises
// exactly the path real traffic takes.
func NewProbeRequest(
	ctx context.Context,
	baseURL, accessToken, projectID, model string,
	maxOutputTokens int,
) (*http.Request, error) {
	token := strings.TrimSpace(accessToken)
	if token == "" {
		return nil, fmt.Errorf("antigravity: missing OAuth access token for live probe")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, fmt.Errorf("antigravity: missing model for live probe")
	}
	if strings.TrimSpace(projectID) == "" {
		projectID = GenerateProjectID()
	}
	if maxOutputTokens <= 0 {
		maxOutputTokens = ProbeMaxOutputTokens
	}

	sessionID := fmt.Sprintf("%d", time.Now().UnixNano())
	envelope := AntigravityEnvelope{
		Project:     projectID,
		Model:       model,
		UserAgent:   "antigravity",
		RequestType: "agent",
		RequestID:   BuildIdeRequestID(sessionID, model, 1),
		Request: CloudCodeRequest{
			Contents: []GeminiContent{{
				Role:  "user",
				Parts: []GeminiPart{{Text: "ping"}},
			}},
			GenerationConfig: &GeminiGenerationConfig{MaxOutputTokens: &maxOutputTokens},
			SessionID:        sessionID,
		},
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("antigravity: build probe body: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost,
		NormalizeBaseURL(baseURL)+GenerateContentPath,
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", CloudCodeUserAgent)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Goog-Api-Client", googleAPIClient)
	req.Header.Set("Client-Metadata", clientMetadata)
	return req, nil
}

// ProbeResult is the outcome of one live verification call.
type ProbeResult struct {
	Model      string
	OK         bool
	StatusCode int
	Latency    time.Duration
	Message    string
}

// ProbeModel verifies a single model with a live one-token generation and
// reports what the upstream actually answered. A transport failure is an
// error (not a fake "healthy"), and a non-2xx carries the upstream's message.
func ProbeModel(
	ctx context.Context,
	client *http.Client,
	baseURL, accessToken, projectID, model string,
) ProbeResult {
	if client == nil {
		return ProbeResult{
			Model:   model,
			Message: "antigravity: nil http client",
		}
	}
	req, err := NewProbeRequest(ctx, baseURL, accessToken, projectID, model, ProbeMaxOutputTokens)
	if err != nil {
		return ProbeResult{Model: model, Message: err.Error()}
	}

	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		return ProbeResult{
			Model:   model,
			Latency: latency,
			Message: describeTransportError(err, latency),
		}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxProbeBodyBytes))
		_ = resp.Body.Close()
	}()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxProbeBodyBytes))
	result := ProbeResult{Model: model, StatusCode: resp.StatusCode, Latency: latency}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		result.OK = true
		result.Message = fmt.Sprintf("Google Cloud Code answered live in %dms", latency.Milliseconds())
		return result
	}
	result.Message = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, UpstreamErrorMessage(resp.StatusCode, body))
	return result
}

// UpstreamErrorMessage extracts a human-readable reason from a Cloud Code error
// payload, falling back to the raw payload and then the HTTP status text. The
// result is single-line and bounded — it is surfaced to the dashboard and to
// clients, so it must never echo a whole error document.
func UpstreamErrorMessage(statusCode int, body []byte) string {
	if msg := textx.ExtractErrorMessage(body, maxErrorMessageChars); msg != "" {
		return msg
	}
	return http.StatusText(statusCode)
}

// describeTransportError turns a dial/TLS/timeout failure into an operator-facing
// sentence, mirroring the wording the generic probe path already uses.
func describeTransportError(err error, latency time.Duration) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "context deadline exceeded"), strings.Contains(msg, "timeout"):
		return fmt.Sprintf("Health check timed out after %dms", latency.Milliseconds())
	case strings.Contains(msg, "connection refused"):
		return "Connection refused by upstream host. Ensure the server is running."
	case strings.Contains(msg, "no such host"):
		return "DNS lookup failed: host not found."
	}
	return msg
}
