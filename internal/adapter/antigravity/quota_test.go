package antigravity

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestParseAvailableModels_Table(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		wantIDs []string
		check   func(t *testing.T, got []ModelQuota)
	}{
		{
			name: "mixed payload sorts and drops internal and unmetered",
			body: `{"models":{
				"gemini-3.8-flash":{"displayName":"Flash","isInternal":false,"quotaInfo":{"remainingFraction":0.42,"resetTime":"2026-10-01T00:00:00Z"}},
				"internal-tool":{"isInternal":true,"quotaInfo":{"remainingFraction":1}},
				"gemini-3.8-pro":{"displayName":"Pro","isInternal":false,"quotaInfo":{"remainingFraction":0.0,"resetTime":"2026-10-01T00:00:00Z"}},
				"gemini-3.8-flash-lite":{"displayName":"Lite","isInternal":false}
			}}`,
			wantIDs: []string{"gemini-3.8-flash", "gemini-3.8-pro"},
			check: func(t *testing.T, got []ModelQuota) {
				assert.InDelta(t, 42.0, got[0].RemainingPct, 0.01)
				assert.False(t, got[0].Exhausted)
				assert.Equal(t, 580, got[0].Used)
				assert.Equal(t, 1000, got[0].Total)
				assert.Equal(t, "2026-10-01T00:00:00Z", got[0].ResetAt)
				assert.True(t, got[1].Exhausted, "an exhausted model is reported, not hidden")
				assert.InDelta(t, 0.0, got[1].RemainingPct, 0.01)
			},
		},
		{
			name:    "numeric reset time is normalized",
			body:    `{"models":{"m":{"quotaInfo":{"remainingFraction":0.5,"resetTime":1780000000}}}}`,
			wantIDs: []string{"m"},
			check: func(t *testing.T, got []ModelQuota) {
				assert.Equal(t, time.Unix(1780000000, 0).UTC().Format(time.RFC3339), got[0].ResetAt)
			},
		},
		{
			name:    "out of range fraction is clamped",
			body:    `{"models":{"m":{"quotaInfo":{"remainingFraction":4.2}}}}`,
			wantIDs: []string{"m"},
			check: func(t *testing.T, got []ModelQuota) {
				assert.InDelta(t, 100.0, got[0].RemainingPct, 0.01)
			},
		},
		{
			name:    "negative fraction is clamped to zero",
			body:    `{"models":{"m":{"quotaInfo":{"remainingFraction":-1}}}}`,
			wantIDs: []string{"m"},
			check: func(t *testing.T, got []ModelQuota) {
				assert.InDelta(t, 0.0, got[0].RemainingPct, 0.01)
				assert.True(t, got[0].Exhausted)
			},
		},
		{name: "malformed json", body: `not json`, wantIDs: nil},
		{name: "missing models key", body: `{"other":true}`, wantIDs: nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseAvailableModels([]byte(tc.body))
			ids := make([]string, 0, len(got))
			for _, m := range got {
				ids = append(ids, m.Model)
			}
			if tc.wantIDs == nil {
				assert.Empty(t, ids)
				return
			}
			assert.Equal(t, tc.wantIDs, ids)
			if tc.check != nil {
				tc.check(t, got)
			}
		})
	}
}

func TestParseQuotaSummary_Table(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		body   string
		want   []WindowQuota
		absent bool
	}{
		{
			name: "gemini and claude_gpt families with both windows",
			body: `{"groups":[
				{"displayName":"Gemini","buckets":[
					{"window":"weekly","remainingFraction":0.8,"resetTime":"2026-10-05T00:00:00Z"},
					{"window":"5h","remainingFraction":0.25}
				]},
				{"displayName":"Claude & GPT","buckets":[
					{"window":"weekly","remainingFraction":0.5},
					{"window":"5h","remainingFraction":0.1}
				]}
			]}`,
			want: []WindowQuota{
				{Family: "claude_gpt", Window: "5h", RemainingPct: 10, Used: 900, Total: 1000},
				{Family: "claude_gpt", Window: "weekly", RemainingPct: 50, Used: 500, Total: 1000},
				{Family: "gemini", Window: "5h", RemainingPct: 25, Used: 750, Total: 1000},
				{Family: "gemini", Window: "weekly", RemainingPct: 80, Used: 200, Total: 1000, ResetAt: "2026-10-05T00:00:00Z"},
			},
		},
		{
			name: "nested quotaSummary.groups is accepted",
			body: `{"quotaSummary":{"groups":[{"displayName":"Gemini","buckets":[
				{"window":"weekly","remainingFraction":1}
			]}]}}`,
			want: []WindowQuota{{Family: "gemini", Window: "weekly", RemainingPct: 100, Used: 0, Total: 1000}},
		},
		{
			name: "disabled weekly bucket is dropped, disabled 5h stays at zero",
			body: `{"groups":[{"displayName":"Gemini","buckets":[
				{"window":"weekly","disabled":true},
				{"window":"5h","disabled":true}
			]}]}`,
			want: []WindowQuota{{Family: "gemini", Window: "5h", RemainingPct: 0, Used: 1000, Total: 1000}},
		},
		{
			name: "unknown family and unknown window are ignored",
			body: `{"groups":[
				{"displayName":"Mystery Family","buckets":[{"window":"weekly","remainingFraction":1}]},
				{"displayName":"Gemini","buckets":[{"window":"yearly","remainingFraction":1}]}
			]}`,
			absent: true,
		},
		{
			name: "first bucket per family and window wins",
			body: `{"groups":[
				{"displayName":"Gemini","buckets":[{"window":"weekly","remainingFraction":0.9}]},
				{"displayName":"Gemini 3","buckets":[{"window":"weekly","remainingFraction":0.1}]}
			]}`,
			want: []WindowQuota{{Family: "gemini", Window: "weekly", RemainingPct: 90, Used: 100, Total: 1000}},
		},
		{name: "malformed json", body: `{`, absent: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseQuotaSummary([]byte(tc.body))
			if tc.absent {
				assert.Empty(t, got)
				return
			}
			require.Equal(t, tc.want, got)
		})
	}
}

func TestParseSubscriptionInfo_Table(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		body                string
		plan, tier, project string
	}{
		{
			name:    "paid tier with object project",
			body:    `{"currentTier":{"name":"Google AI Pro","id":"pro"},"paidTier":{"id":"gems-enterprise"},"cloudaicompanionProject":{"id":"proj-123"}}`,
			plan:    "Google AI Pro",
			tier:    "gems-enterprise",
			project: "proj-123",
		},
		{
			name:    "string project and free tier",
			body:    `{"currentTier":{"name":"Google AI Free"},"cloudaicompanionProject":"proj-free"}`,
			plan:    "Google AI Free",
			project: "proj-free",
		},
		{
			name: "tier id used as plan name when name absent",
			body: `{"currentTier":{"id":"standard-tier"}}`,
			plan: "standard-tier",
		},
		{name: "malformed json", body: `nope`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plan, tier, project := ParseSubscriptionInfo([]byte(tc.body))
			assert.Equal(t, tc.plan, plan)
			assert.Equal(t, tc.tier, tier)
			assert.Equal(t, tc.project, project)
		})
	}
}

func TestNormalizeProjectID(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "p1", NormalizeProjectID(map[string]string{"project_id": "p1"}))
	assert.Equal(t, "p2", NormalizeProjectID(map[string]string{"projectId": "p2"}))
	assert.Equal(t, "p3", NormalizeProjectID(map[string]string{"cloudaicompanionProject": `{"id":"p3"}`}))
	assert.Equal(t, "", NormalizeProjectID(map[string]string{"project_id": "  "}))
	assert.Equal(t, "", NormalizeProjectID(nil))
}


// quotaStub is a Cloud Code stand-in: it answers the three quota verbs and
// records what the client sent, so request shape and assembly are testable
// without Google.
type quotaStub struct {
	models    string
	summary   string
	plan      string
	modelsRaw int // non-2xx override for fetchAvailableModels
	summarySt int
	planSt    int

	mu       sync.Mutex
	paths    []string
	auths    []string
	projects []string
}

func (s *quotaStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.paths = append(s.paths, r.URL.Path)
		s.auths = append(s.auths, r.Header.Get("Authorization"))
		s.projects = append(s.projects, gjson.GetBytes(body, "project").String())
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, QuotaModelsPath):
			if s.modelsRaw != 0 {
				w.WriteHeader(s.modelsRaw)
				_, _ = w.Write([]byte(`{"error":{"code":403,"message":"forbidden"}}`))
				return
			}
			_, _ = w.Write([]byte(s.models))
		case strings.HasSuffix(r.URL.Path, QuotaSummaryPath):
			if s.summarySt != 0 {
				w.WriteHeader(s.summarySt)
				_, _ = w.Write([]byte(`{"error":{"code":401,"message":"expired"}}`))
				return
			}
			_, _ = w.Write([]byte(s.summary))
		case strings.HasSuffix(r.URL.Path, LoadCodeAssistPath):
			if s.planSt != 0 {
				w.WriteHeader(s.planSt)
				_, _ = w.Write([]byte(`{"error":{"code":403,"message":"nope"}}`))
				return
			}
			_, _ = w.Write([]byte(s.plan))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

const (
	stubModels = `{"models":{"gemini-3.8-flash":{"displayName":"Flash","quotaInfo":{"remainingFraction":0.6,"resetTime":"2026-10-02T00:00:00Z"}}}}`
	stubPlan   = `{"currentTier":{"name":"Google AI Pro"},"paidTier":{"id":"gems-enterprise"}}`
	stubWindow = `{"groups":[{"displayName":"Gemini","buckets":[{"window":"weekly","remainingFraction":0.9}]}]}`
)

// TestFetchQuota_AssemblesAllThreeVerbs pins the wire contract: the right verb is
// called, the project travels in the body, and the token rides as a bearer.
func TestFetchQuota_AssemblesAllThreeVerbs(t *testing.T) {
	t.Parallel()

	stub := &quotaStub{models: stubModels, plan: stubPlan, summary: stubWindow}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	quota := FetchQuota(context.Background(), srv.Client(), srv.URL, "tok-123", "proj-9",
		QuotaOptions{SubscriptionURL: srv.URL + LoadCodeAssistPath})

	require.Equal(t, []string{LoadCodeAssistPath, QuotaModelsPath, QuotaSummaryPath}, stub.paths)
	for _, auth := range stub.auths {
		assert.Equal(t, "Bearer tok-123", auth)
	}
	assert.Equal(t, "proj-9", stub.projects[1], "per-model call carries the project id")
	assert.Equal(t, "proj-9", stub.projects[2], "window call carries the project id")

	assert.Equal(t, "Google AI Pro", quota.Plan)
	assert.Equal(t, "gems-enterprise", quota.PaidTierID)
	assert.False(t, quota.FreeTier)
	assert.Empty(t, quota.Message)
	require.Len(t, quota.Models, 1)
	assert.Equal(t, "gemini-3.8-flash", quota.Models[0].Model)
	assert.InDelta(t, 60.0, quota.Models[0].RemainingPct, 0.01)
	require.Len(t, quota.Windows, 1)
	assert.Equal(t, "gemini", quota.Windows[0].Family)
}

// TestFetchQuota_FreeTierSkipsPerModel pins the free-tier rule: upstream's
// per-model fractions do not describe a free allowance, so they are not shown
// and the call is skipped rather than fetched and discarded.
func TestFetchQuota_FreeTierSkipsPerModel(t *testing.T) {
	t.Parallel()

	stub := &quotaStub{
		models:  stubModels,
		plan:    `{"currentTier":{"name":"Google AI Free"}}`,
		summary: stubWindow,
	}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	quota := FetchQuota(context.Background(), srv.Client(), srv.URL, "tok", "",
		QuotaOptions{SubscriptionURL: srv.URL + LoadCodeAssistPath})

	assert.True(t, quota.FreeTier)
	assert.Empty(t, quota.Models, "no per-model quota on a free tier")
	assert.Contains(t, quota.Message, "Free tier")
	assert.NotContains(t, stub.paths, QuotaModelsPath)
	require.Len(t, quota.Windows, 1)
}

// TestFetchQuota_RefusalsBecomeMessages pins that a refused quota API degrades to
// an explanation, never to a fabricated zero-quota answer.
func TestFetchQuota_RefusalsBecomeMessages(t *testing.T) {
	t.Parallel()

	t.Run("403 on models", func(t *testing.T) {
		t.Parallel()
		stub := &quotaStub{modelsRaw: http.StatusForbidden, plan: stubPlan, summary: stubWindow}
		srv := httptest.NewServer(stub.handler())
		defer srv.Close()

		quota := FetchQuota(context.Background(), srv.Client(), srv.URL, "tok", "",
			QuotaOptions{SubscriptionURL: srv.URL + LoadCodeAssistPath})
		assert.Contains(t, quota.Message, "forbidden")
		assert.Contains(t, quota.Message, "Inference may still work")
		assert.Empty(t, quota.Models)
		assert.Len(t, quota.Windows, 1, "a models refusal does not lose the windows")
	})

	t.Run("401 on windows keeps models", func(t *testing.T) {
		t.Parallel()
		stub := &quotaStub{models: stubModels, plan: stubPlan, summarySt: http.StatusUnauthorized}
		srv := httptest.NewServer(stub.handler())
		defer srv.Close()

		quota := FetchQuota(context.Background(), srv.Client(), srv.URL, "tok", "",
			QuotaOptions{SubscriptionURL: srv.URL + LoadCodeAssistPath})
		assert.Contains(t, quota.Message, "authentication expired")
		assert.Len(t, quota.Models, 1)
		assert.Empty(t, quota.Windows)
	})

	t.Run("plan failure leaves plan unknown", func(t *testing.T) {
		t.Parallel()
		stub := &quotaStub{models: stubModels, summary: stubWindow, planSt: http.StatusForbidden}
		srv := httptest.NewServer(stub.handler())
		defer srv.Close()

		quota := FetchQuota(context.Background(), srv.Client(), srv.URL, "tok", "proj-1",
			QuotaOptions{SubscriptionURL: srv.URL + LoadCodeAssistPath})
		assert.Equal(t, "Unknown", quota.Plan)
		assert.True(t, quota.FreeTier, "no paid tier id means treat as free")
		assert.Equal(t, "proj-1", quota.ProjectID)
	})
}

func TestFetchQuota_RequiresToken(t *testing.T) {
	t.Parallel()

	stub := &quotaStub{}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	quota := FetchQuota(context.Background(), srv.Client(), srv.URL, "  ", "",
		QuotaOptions{SubscriptionURL: srv.URL + LoadCodeAssistPath})
	assert.Empty(t, stub.paths, "no credential means no call")
	assert.Equal(t, "Unknown", quota.Plan)
	assert.NotEmpty(t, quota.Message)
}

