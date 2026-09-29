package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"golang.org/x/sync/errgroup"

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
	// DefaultDiscoveryParallelism bounds concurrent verification calls in a sweep.
	DefaultDiscoveryParallelism = 4
	// maxProbeBodyBytes bounds the response body read from a probe.
	maxProbeBodyBytes = 8 << 10
	// maxErrorMessageChars bounds a surfaced upstream error message.
	maxErrorMessageChars = 512
)

// curatedModels seeds the discovery sweep — it is a *candidate list*, not an
// answer. Every entry is still verified live before it is reported, and
// operators can point the sweep at any other id (the dashboard adds the models
// already mapped to the upstream in the catalog).
var curatedModels = []string{
	"gemini-3-pro-preview",
	"gemini-2.5-pro",
	"gemini-2.5-flash",
	"gemini-2.0-flash",
	"claude-sonnet-4-5",
	"claude-3-7-sonnet",
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

// DefaultProbeModel is the model used when a reachability check arrives without
// one: the cheapest widely available Gemini on Cloud Code.
const DefaultProbeModel = "gemini-2.5-flash"

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

// DiscoverModels verifies every candidate live and returns one result per
// candidate, ordered by model id. Duplicates are collapsed and blanks dropped;
// maxParallel <= 0 falls back to a small default so a sweep never serializes
// into a multi-minute request.
func DiscoverModels(
	ctx context.Context,
	client *http.Client,
	baseURL, accessToken, projectID string,
	candidates []string,
	maxParallel int,
) []ProbeResult {
	if maxParallel <= 0 {
		maxParallel = DefaultDiscoveryParallelism
	}
	unique := make([]string, 0, len(candidates))
	seen := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		unique = append(unique, c)
	}
	if len(unique) == 0 {
		return nil
	}

	results := make([]ProbeResult, len(unique))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(maxParallel)
	for i, model := range unique {
		i, model := i, model
		group.Go(func() error {
			if err := groupCtx.Err(); err != nil {
				results[i] = ProbeResult{Model: model, Message: err.Error()}
				return nil
			}
			results[i] = ProbeModel(groupCtx, client, baseURL, accessToken, projectID, model)
			return nil
		})
	}
	_ = group.Wait()
	sort.Slice(results, func(i, j int) bool { return results[i].Model < results[j].Model })
	return results
}

// DefaultDiscoveryParallelism bounds concurrent verification calls in one sweep.

// UpstreamErrorMessage extracts a human-readable reason from a Cloud Code error
// payload, falling back to the raw payload and then the HTTP status text. The
// result is single-line and bounded — it is surfaced to the dashboard and to
// clients, so it must never echo a whole error document.
func UpstreamErrorMessage(statusCode int, body []byte) string {
	if gjson.ValidBytes(body) {
		for _, path := range []string{"error.message", "error.status", "message"} {
			if msg := strings.TrimSpace(gjson.GetBytes(body, path).String()); msg != "" {
				return textx.Excerpt([]byte(msg), maxErrorMessageChars)
			}
		}
	}
	if msg := textx.Excerpt(body, maxErrorMessageChars); msg != "" {
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
