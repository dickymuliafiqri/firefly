package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// CodeAssistResult carries the resolved Google Cloud Project ID and subscription Tier ID.
type CodeAssistResult struct {
	ProjectID string
	TierID    string
}

type loadCodeAssistPayload struct {
	Metadata ClientMetadataDTO `json:"metadata"`
}

type onboardUserPayload struct {
	TierID   string            `json:"tierId"`
	Metadata ClientMetadataDTO `json:"metadata"`
}

// LoadCodeAssist queries Google Cloud Code PA (PROD) to discover the user's project and tier.
// Note: Google's backend will reject this call if X-Goog-Api-Client or Client-Metadata headers
// are sent; we only send standard User-Agent and x-request-source: local.
func LoadCodeAssist(ctx context.Context, client *http.Client, accessToken, prodBaseURL string) (*CodeAssistResult, error) {
	if client == nil {
		client = http.DefaultClient
	}
	url := strings.TrimRight(prodBaseURL, "/") + "/v1internal:loadCodeAssist"

	payloadBytes, err := json.Marshal(loadCodeAssistPayload{Metadata: DefaultClientMetadata()})
	if err != nil {
		return nil, fmt.Errorf("marshal loadCodeAssist payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payloadBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", AntigravityIDEUserAgent)
	req.Header.Set("x-request-source", "local")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("loadCodeAssist request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read loadCodeAssist response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("loadCodeAssist failed (HTTP %d): %s", resp.StatusCode, string(body))
	}

	projectVal := gjson.GetBytes(body, "cloudaicompanionProject")
	projectID := ""
	if projectVal.Type == gjson.String {
		projectID = projectVal.Str
	} else if projectVal.IsObject() {
		projectID = projectVal.Get("id").String()
	}

	tierID := "legacy-tier"
	tiers := gjson.GetBytes(body, "allowedTiers")
	if tiers.IsArray() {
		tiers.ForEach(func(_, val gjson.Result) bool {
			if val.Get("isDefault").Bool() {
				if id := strings.TrimSpace(val.Get("id").String()); id != "" {
					tierID = id
					return false
				}
			}
			return true
		})
	}

	return &CodeAssistResult{
		ProjectID: strings.TrimSpace(projectID),
		TierID:    tierID,
	}, nil
}

// DefaultOnboardMaxAttempts matches 9router's anti-abuse protection threshold.
const DefaultOnboardMaxAttempts = 2

// DefaultOnboardBaseDelay is the inter-attempt backoff delay.
const DefaultOnboardBaseDelay = 12 * time.Second

// OnboardUser polls Google Cloud Code PA (PROD) until project onboarding completes.
func OnboardUser(ctx context.Context, client *http.Client, accessToken, prodBaseURL, tierID string, maxAttempts int) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if maxAttempts <= 0 {
		maxAttempts = DefaultOnboardMaxAttempts
	}
	url := strings.TrimRight(prodBaseURL, "/") + "/v1internal:onboardUser"

	payloadBytes, err := json.Marshal(onboardUserPayload{
		TierID:   tierID,
		Metadata: DefaultClientMetadata(),
	})
	if err != nil {
		return "", fmt.Errorf("marshal onboardUser payload: %w", err)
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payloadBytes))
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", AntigravityIDEUserAgent)
		req.Header.Set("x-request-source", "local")

		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("onboardUser attempt %d failed: %w", attempt, err)
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", fmt.Errorf("onboardUser failed (HTTP %d): %s", resp.StatusCode, string(body))
		}

		if gjson.GetBytes(body, "done").Bool() {
			projectVal := gjson.GetBytes(body, "response.cloudaicompanionProject")
			projectID := ""
			if projectVal.Type == gjson.String {
				projectID = projectVal.Str
			} else if projectVal.IsObject() {
				projectID = projectVal.Get("id").String()
			}
			return strings.TrimSpace(projectID), nil
		}

		if attempt < maxAttempts {
			delay := DefaultOnboardBaseDelay
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", ctx.Err()
			case <-timer.C:
			}
		}
	}

	return "", errors.New("onboardUser timed out waiting for completion")
}

