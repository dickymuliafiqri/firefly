package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/dickymuliafiqri/firefly/internal/textx"
)

// Provider quota discovery for Google Cloud Code.
//
// Cloud Code has no OpenAI-style `GET /models`, but it *does* expose two
// quota-bearing verbs that a quota tracker can ask directly:
//
//	POST {daily}/v1internal:fetchAvailableModels     → per-model quotaInfo
//	POST {daily}/v1internal:retrieveUserQuotaSummary  → weekly + 5h window buckets
//	POST {prod} /v1internal:loadCodeAssist            → plan / paid tier / project
//
// Everything here is read-only: no generation is triggered, so a quota refresh
// never spends the quota it is reporting on. When the quota API refuses (401 on
// an expired token, 403 on a plan without the API), the caller gets a
// `message` and empty lists rather than a fabricated zero — the same honesty
// rule the live model probe follows.

const (
	// QuotaModelsPath returns every model with its remaining fraction.
	QuotaModelsPath = "/v1internal:fetchAvailableModels"
	// QuotaSummaryPath returns the rolling windows (weekly, 5h) per family.
	QuotaSummaryPath = "/v1internal:retrieveUserQuotaSummary"
	// LoadCodeAssistHost serves loadCodeAssist; the daily host does not.
	LoadCodeAssistHost = "https://cloudcode-pa.googleapis.com"
	// LoadCodeAssistPath yields the subscription plan, paid tier and project.
	LoadCodeAssistPath = "/v1internal:loadCodeAssist"
	// Client name/version sent on quota calls alongside the IDE fingerprint.
	clientName    = "antigravity"
	clientVersion = "2.11.0"
	// quotaNormalizer scales a 0..1 fraction to the 1000-unit base the UI uses,
	// so a bar can be rendered without the client knowing about fractions.
	quotaNormalizer = 1000
	// quotaBodyLimit bounds a quota response body.
	quotaBodyLimit = 1 << 20
)

// ModelQuota is one model's remaining allowance, normalized for display.
type ModelQuota struct {
	Model        string  `json:"model"`
	DisplayName  string  `json:"display_name,omitempty"`
	RemainingPct float64 `json:"remaining_percent"`
	Used         int     `json:"used"`
	Total        int     `json:"total"`
	ResetAt      string  `json:"reset_at,omitempty"`
	Exhausted    bool    `json:"exhausted"`
	Unlimited    bool    `json:"unlimited,omitempty"`
}

// WindowQuota is a rolling window (weekly, 5h) for a model family.
type WindowQuota struct {
	Family       string  `json:"family"`
	Window       string  `json:"window"`
	DisplayName  string  `json:"display_name,omitempty"`
	RemainingPct float64 `json:"remaining_percent"`
	Used         int     `json:"used"`
	Total        int     `json:"total"`
	ResetAt      string  `json:"reset_at,omitempty"`
}

// Quota is the full provider-side picture for one OAuth connection.
type Quota struct {
	Plan       string        `json:"plan"`
	PaidTierID string        `json:"paid_tier_id,omitempty"`
	FreeTier   bool          `json:"free_tier"`
	ProjectID  string        `json:"project_id,omitempty"`
	Models     []ModelQuota  `json:"models"`
	Windows    []WindowQuota `json:"windows"`
	Message    string        `json:"message,omitempty"`
}

// HTTPError carries a non-2xx status from a quota verb so the caller can tell
// "quota API refused" apart from "quota API broken".
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("antigravity quota api failed (HTTP %d): %s", e.StatusCode, e.Body)
}

// NormalizeProjectID reads the Cloud Code companion project id out of a
// connection's provider metadata. Connections written by different clients spell
// it differently (`project_id`, `projectId`, or the nested cloudaicompanion
// shape), so all known spellings are accepted.
func NormalizeProjectID(pds map[string]string) string {
	for _, key := range []string{"project_id", "projectId", "cloudaicompanionProject", "cloudaicompanionProjectId"} {
		if v := strings.TrimSpace(pds[key]); v != "" {
			// The nested shape is stored as its JSON body by some clients.
			if gjson.Valid(v) {
				if id := strings.TrimSpace(gjson.Get(v, "id").String()); id != "" {
					return id
				}
				continue
			}
			return v
		}
	}
	return ""
}

// quotaRequest builds an authenticated POST for one of the quota verbs.
func quotaRequest(ctx context.Context, url, accessToken, projectID string, body any) (*http.Request, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, fmt.Errorf("antigravity: missing OAuth access token for quota lookup")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("antigravity: build quota body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", CloudCodeUserAgent)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-Goog-Api-Client", googleAPIClient)
	req.Header.Set("Client-Metadata", clientMetadata)
	req.Header.Set("X-Client-Name", clientName)
	req.Header.Set("X-Client-Version", clientVersion)
	return req, nil
}

// postQuota performs the call and returns the raw body, mapping a non-2xx to
// *HTTPError with the upstream's own message (never a synthesized zero).
func postQuota(ctx context.Context, client *http.Client, url, accessToken, projectID string) ([]byte, error) {
	if client == nil {
		return nil, fmt.Errorf("antigravity: nil http client")
	}
	req, err := quotaRequest(ctx, url, accessToken, projectID, map[string]any{"project": projectID})
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, quotaBodyLimit))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &HTTPError{
			StatusCode: resp.StatusCode,
			Body:       UpstreamErrorMessage(resp.StatusCode, body),
		}
	}
	return body, nil
}

// FetchAvailableModels returns the raw `v1internal:fetchAvailableModels` body.
func FetchAvailableModels(
	ctx context.Context, client *http.Client, baseURL, accessToken, projectID string,
) ([]byte, error) {
	return postQuota(ctx, client, NormalizeBaseURL(baseURL)+QuotaModelsPath, accessToken, projectID)
}

// FetchQuotaSummary returns the raw `v1internal:retrieveUserQuotaSummary` body.
func FetchQuotaSummary(
	ctx context.Context, client *http.Client, baseURL, accessToken, projectID string,
) ([]byte, error) {
	return postQuota(ctx, client, NormalizeBaseURL(baseURL)+QuotaSummaryPath, accessToken, projectID)
}

func fetchSubscriptionURL(ctx context.Context, client *http.Client, accessToken, url string) ([]byte, error) {
	if client == nil {
		return nil, fmt.Errorf("antigravity: nil http client")
	}
	req, err := quotaRequest(
		ctx, url, accessToken, "",
		map[string]any{"metadata": clientMetadata, "mode": 1},
	)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, quotaBodyLimit))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &HTTPError{StatusCode: resp.StatusCode, Body: UpstreamErrorMessage(resp.StatusCode, body)}
	}
	return body, nil
}

// QuotaOptions tunes which parts of the picture are fetched.
type QuotaOptions struct {
	// SkipModels omits the per-model sweep (a cheap windows-only refresh).
	SkipModels bool
	// SkipWindows omits the weekly/5h summary.
	SkipWindows bool
	// SubscriptionURL overrides the loadCodeAssist host. Empty means the
	// production host; a non-empty value exists so the whole assembly can be
	// exercised against a stub without reaching Google.
	SubscriptionURL string
}

// subscriptionURL resolves the loadCodeAssist endpoint to call.
func (o QuotaOptions) subscriptionURL() string {
	if s := strings.TrimSpace(o.SubscriptionURL); s != "" {
		return s
	}
	return LoadCodeAssistHost + LoadCodeAssistPath
}

// FetchQuota assembles the provider-side quota picture for one connection.
//
// Best-effort by design: a plan lookup or a window-summary failure degrades the
// result (a `Message` explains what is missing) instead of failing the whole
// call, because the per-model list alone already answers "what can I use now".
// Nothing here triggers generation, so a refresh never spends the quota.
func FetchQuota(
	ctx context.Context,
	client *http.Client,
	baseURL, accessToken, projectID string,
	opts QuotaOptions,
) *Quota {
	out := &Quota{ProjectID: projectID, Models: []ModelQuota{}, Windows: []WindowQuota{}}

	// Plan / paid tier; also the fallback source for the project id.
	if raw, err := fetchSubscriptionURL(ctx, client, accessToken, opts.subscriptionURL()); err == nil {
		plan, tier, discovered := ParseSubscriptionInfo(raw)
		out.Plan = plan
		out.PaidTierID = tier
		if out.ProjectID == "" {
			out.ProjectID = discovered
		}
	}
	// A free tier reports no per-model allowance worth showing: upstream's
	// fractions there describe the weekly limit, and the rolling window is the
	// real constraint.
	out.FreeTier = out.PaidTierID == "" || out.PaidTierID == "free-tier"

	if !opts.SkipModels {
		switch {
		case out.FreeTier:
			// Skip the call entirely rather than fetching a number that does not
			// describe this plan's allowance.
			out.Message = "Free tier: Cloud Code does not report per-model quota — see the rolling windows."
		default:
			raw, err := FetchAvailableModels(ctx, client, baseURL, accessToken, out.ProjectID)
			if err != nil {
				out.Message = quotaMessage(err, "per-model quota")
				break
			}
			out.Models = ParseAvailableModels(raw)
			if len(out.Models) == 0 {
				out.Message = "Cloud Code returned no per-model quota for this account."
			}
		}
	}

	if !opts.SkipWindows {
		raw, err := FetchQuotaSummary(ctx, client, baseURL, accessToken, out.ProjectID)
		if err != nil {
			if out.Message == "" {
				out.Message = quotaMessage(err, "rolling windows")
			}
		} else {
			out.Windows = ParseQuotaSummary(raw)
		}
	}

	if out.Plan == "" {
		out.Plan = "Unknown"
	}
	return out
}

// quotaMessage turns a fetch error into an operator-facing sentence, calling out
// the two refusals that are not Firefly's fault.
func quotaMessage(err error, what string) string {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case http.StatusUnauthorized:
			return "Quota API authentication expired (" + what + "). Reconnect the Google account; inference keeps working until the token dies."
		case http.StatusForbidden:
			return "Quota API access forbidden (" + what + "). Inference may still work — this plan does not expose quota."
		}
	}
	return what + " unavailable: " + textx.Excerpt([]byte(err.Error()), 200)
}

// ParseSubscriptionInfo reads the loadCodeAssist body: the human plan name, the
// paid tier id (the free/paid discriminator) and the companion project id.
func ParseSubscriptionInfo(data []byte) (plan, paidTierID, projectID string) {
	if !gjson.ValidBytes(data) {
		return "", "", ""
	}
	plan = strings.TrimSpace(gjson.GetBytes(data, "currentTier.name").String())
	if plan == "" {
		plan = strings.TrimSpace(gjson.GetBytes(data, "currentTier.id").String())
	}
	paidTierID = strings.TrimSpace(gjson.GetBytes(data, "paidTier.id").String())
	project := gjson.GetBytes(data, "cloudaicompanionProject")
	switch {
	case project.IsObject():
		projectID = strings.TrimSpace(project.Get("id").String())
	case project.Type == gjson.String:
		projectID = strings.TrimSpace(project.String())
	}
	return plan, paidTierID, projectID
}

// ParseAvailableModels turns a `fetchAvailableModels` body into per-model quota.
//
// Shape: {"models": {"<id>": {displayName, isInternal, quotaInfo:{remainingFraction,
// resetTime, ...}}}}. Internal models are dropped (they are not selectable) and
// models without quotaInfo are skipped (upstream does not meter them). An
// exhausted model is reported, not hidden — "0% left" is the answer an operator
// needs. Results are sorted by model id so the UI order is stable.
func ParseAvailableModels(data []byte) []ModelQuota {
	if !gjson.ValidBytes(data) {
		return nil
	}
	models := gjson.GetBytes(data, "models")
	if !models.IsObject() {
		return nil
	}
	out := []ModelQuota{}
	models.ForEach(func(key, value gjson.Result) bool {
		id := strings.TrimSpace(key.String())
		if id == "" || value.Get("isInternal").Bool() {
			return true
		}
		info := value.Get("quotaInfo")
		if !info.Exists() {
			return true
		}
		pct := clampPercent(info.Get("remainingFraction").Float() * 100)
		total := quotaNormalizer
		remaining := int(pct / 100 * float64(total))
		out = append(out, ModelQuota{
			Model:        id,
			DisplayName:  strings.TrimSpace(value.Get("displayName").String()),
			RemainingPct: pct,
			Used:         maxInt(0, total-remaining),
			Total:        total,
			ResetAt:      parseQuotaTime(info.Get("resetTime")),
			Exhausted:    pct <= 0,
			Unlimited:    info.Get("unlimited").Bool(),
		})
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

// quotaFamily maps a Cloud Code quota group name to the family it meters, so
// the UI can show one "Gemini" and one "Claude & GPT" row per window.
func quotaFamily(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "gemini"):
		return "gemini"
	case strings.Contains(lower, "claude"), strings.Contains(lower, "gpt"):
		return "claude_gpt"
	}
	return ""
}

// ParseQuotaSummary turns a `retrieveUserQuotaSummary` body into rolling
// windows (weekly and 5h) per family.
//
// Shape: {"groups": [{displayName, buckets: [{window, remainingFraction,
// resetTime, disabled}]}]} — the groups may also live under quotaSummary.
// A bucket upstream marks `disabled` (because the other pool is exhausted) is
// kept with 0% so the row still renders, while a disabled *weekly* bucket is
// dropped: that window simply does not exist for this account. First matching
// bucket per family+window wins.
func ParseQuotaSummary(data []byte) []WindowQuota {
	if !gjson.ValidBytes(data) {
		return nil
	}
	groups := gjson.GetBytes(data, "groups")
	if !groups.IsArray() {
		groups = gjson.GetBytes(data, "quotaSummary.groups")
	}
	if !groups.IsArray() {
		return nil
	}
	out := []WindowQuota{}
	seen := map[string]bool{}
	groups.ForEach(func(_, group gjson.Result) bool {
		family := quotaFamily(group.Get("displayName").String())
		if family == "" {
			return true
		}
		buckets := group.Get("buckets")
		if !buckets.IsArray() {
			return true
		}
		buckets.ForEach(func(_, bucket gjson.Result) bool {
			window := quotaWindow(bucket)
			if window == "" {
				return true
			}
			disabled := bucket.Get("disabled").Bool()
			if disabled && window == "weekly" {
				return true
			}
			key := family + "|" + window
			if seen[key] {
				return true
			}
			pct := 0.0
			if !disabled {
				pct = clampPercent(bucket.Get("remainingFraction").Float() * 100)
			}
			total := quotaNormalizer
			remaining := int(pct / 100 * float64(total))
			seen[key] = true
			out = append(out, WindowQuota{
				Family:       family,
				Window:       window,
				DisplayName:  strings.TrimSpace(bucket.Get("displayName").String()),
				RemainingPct: pct,
				Used:         maxInt(0, total-remaining),
				Total:        total,
				ResetAt:      parseQuotaTime(bucket.Get("resetTime")),
			})
			return true
		})
		return true
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Family != out[j].Family {
			return out[i].Family < out[j].Family
		}
		return out[i].Window < out[j].Window
	})
	return out
}

// quotaWindow classifies a bucket's window. Upstream is inconsistent about the
// field ("window", "bucketId", "displayName"), so all three are inspected.
func quotaWindow(bucket gjson.Result) string {
	haystack := strings.ToLower(
		bucket.Get("window").String() + " " + bucket.Get("bucketId").String() + " " + bucket.Get("displayName").String(),
	)
	switch {
	case strings.Contains(haystack, "weekly"), strings.Contains(haystack, "week"):
		return "weekly"
	case strings.Contains(haystack, "5h"), strings.Contains(haystack, "five hour"),
		strings.Contains(haystack, "daily"), strings.Contains(haystack, "session"):
		return "5h"
	}
	return ""
}

// parseQuotaTime normalizes an upstream reset timestamp to RFC3339. Cloud Code
// sends an RFC3339 string, but numeric forms (seconds or milliseconds) are
// accepted so a shape change degrades to a value rather than to nothing.
func parseQuotaTime(value gjson.Result) string {
	if !value.Exists() || value.Type == gjson.Null {
		return ""
	}
	if value.Type == gjson.Number {
		return timeFromNumber(value.Float()).Format(time.RFC3339)
	}
	raw := strings.TrimSpace(value.String())
	if raw == "" {
		return ""
	}
	if isDigits(raw) {
		if n, err := strconv.ParseFloat(raw, 64); err == nil {
			return timeFromNumber(n).Format(time.RFC3339)
		}
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return ""
}

func timeFromNumber(n float64) time.Time {
	if n <= 0 {
		return time.Time{}
	}
	// Values below ~1e12 are seconds (year 33658 in seconds, 1973 in millis).
	if n < 1e12 {
		return time.Unix(int64(n), 0).UTC()
	}
	return time.UnixMilli(int64(n)).UTC()
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func clampPercent(pct float64) float64 {
	if pct < 0 || pct != pct { // negative or NaN
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
