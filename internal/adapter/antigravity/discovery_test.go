package antigravity

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// cloudCodeStub mimics the one verb Cloud Code serves: a POST to
// /v1internal:generateContent. `ok` lists the models that answer 200; anything
// else is refused with the given status and a Google-shaped error document.
// Every call is recorded so a test can prove the probe really went upstream.
func cloudCodeStub(t *testing.T, ok map[string]bool, refuseStatus int, mu *sync.Mutex, seen *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != GenerateContentPath {
			http.Error(w, `{"error":{"message":"not found"}}`, http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer ya29.test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"missing or invalid credential"}}`)
			return
		}
		if got := r.Header.Get("User-Agent"); got != CloudCodeUserAgent {
			t.Errorf("User-Agent = %q, want %q", got, CloudCodeUserAgent)
		}
		body, _ := io.ReadAll(r.Body)
		var envelope AntigravityEnvelope
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Errorf("probe body is not a Cloud Code envelope: %v", err)
		}
		if envelope.Project != "proj-1" {
			t.Errorf("envelope project = %q, want proj-1", envelope.Project)
		}
		if envelope.Request.GenerationConfig == nil || envelope.Request.GenerationConfig.MaxOutputTokens == nil {
			t.Errorf("probe must pin a maxOutputTokens budget: %s", body)
		} else if *envelope.Request.GenerationConfig.MaxOutputTokens != ProbeMaxOutputTokens {
			t.Errorf("maxOutputTokens = %d, want %d", *envelope.Request.GenerationConfig.MaxOutputTokens, ProbeMaxOutputTokens)
		}
		if len(envelope.Request.Contents) == 0 || len(envelope.Request.Contents[0].Parts) == 0 {
			t.Errorf("probe must carry one content turn: %s", body)
		}
		mu.Lock()
		*seen = append(*seen, envelope.Model)
		mu.Unlock()
		if ok[envelope.Model] {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"response":{"candidates":[{"content":{"parts":[{"text":"pong"}]}}]}}`)
			return
		}
		w.WriteHeader(refuseStatus)
		_, _ = io.WriteString(w, `{"error":{"code":`+strconv.Itoa(refuseStatus)+`,"message":"model is not available for this account"}}`)
	}))
}

func TestNewProbeRequest_UsesCloudCodeVerbAndEnvelope(t *testing.T) {
	req, err := NewProbeRequest(context.Background(), "https://daily-cloudcode-pa.googleapis.com/", "ya29.tok", "proj-9", "gemini-2.5-pro", 0)
	if err != nil {
		t.Fatalf("NewProbeRequest: %v", err)
	}
	if req.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", req.Method)
	}
	if req.URL.String() != "https://daily-cloudcode-pa.googleapis.com/v1internal:generateContent" {
		t.Errorf("url = %s", req.URL.String())
	}
	if got := req.Header.Get("Authorization"); got != "Bearer ya29.tok" {
		t.Errorf("Authorization = %q", got)
	}
	if got := req.Header.Get("User-Agent"); got != CloudCodeUserAgent {
		t.Errorf("User-Agent = %q", got)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
}

func TestNewProbeRequest_RefusesMissingCredentialOrModel(t *testing.T) {
	if _, err := NewProbeRequest(context.Background(), "", "", "p", "gemini-2.5-pro", 1); err == nil {
		t.Error("missing access token must fail instead of sending an anonymous probe")
	}
	if _, err := NewProbeRequest(context.Background(), "", "ya29.tok", "p", "", 1); err == nil {
		t.Error("missing model must fail")
	}
}

func TestProbeModel_ReportsRealOutcome(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := cloudCodeStub(t, map[string]bool{"gemini-2.5-pro": true}, http.StatusNotFound, &mu, &seen)
	defer srv.Close()

	ctx := context.Background()
	ok := ProbeModel(ctx, srv.Client(), srv.URL, "ya29.test-token", "proj-1", "gemini-2.5-pro")
	if !ok.OK {
		t.Fatalf("live probe should be healthy: %s", ok.Message)
	}
	if ok.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", ok.StatusCode)
	}
	if ok.Latency <= 0 {
		t.Errorf("latency = %s, want a measured value", ok.Latency)
	}
	if !strings.Contains(ok.Message, "answered live") {
		t.Errorf("message should state a live answer: %q", ok.Message)
	}

	bad := ProbeModel(ctx, srv.Client(), srv.URL, "ya29.test-token", "proj-1", "gemini-9-ultra")
	if bad.OK {
		t.Fatal("a refused model must not report healthy")
	}
	if bad.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", bad.StatusCode)
	}
	if !strings.Contains(bad.Message, "model is not available for this account") {
		t.Errorf("message should carry the upstream reason: %q", bad.Message)
	}
	if !strings.Contains(bad.Message, "HTTP 404") {
		t.Errorf("message should carry the upstream status: %q", bad.Message)
	}
}

func TestProbeModel_UnauthorizedIsNotHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"credentials expired"}}`)
	}))
	defer srv.Close()

	res := ProbeModel(context.Background(), srv.Client(), srv.URL, "ya29.stale", "proj-1", "gemini-2.5-pro")
	if res.OK {
		t.Fatal("an expired credential must not report healthy")
	}
	if !strings.Contains(res.Message, "credentials expired") {
		t.Errorf("message = %q, want the upstream reason", res.Message)
	}
}

func TestProbeModel_TransportFailureIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening any more

	res := ProbeModel(context.Background(), &http.Client{Timeout: 2 * time.Second}, url, "ya29.tok", "proj-1", "gemini-2.5-pro")
	if res.OK {
		t.Fatal("an unreachable host must not report healthy")
	}
	if res.StatusCode != 0 {
		t.Errorf("status = %d, want 0 for a transport failure", res.StatusCode)
	}
	if res.Message == "" {
		t.Error("a transport failure must carry a message")
	}
}

func TestDiscoverModels_VerifiesEveryCandidateLive(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := cloudCodeStub(t, map[string]bool{
		"gemini-2.5-pro":   true,
		"gemini-2.5-flash": true,
	}, http.StatusNotFound, &mu, &seen)
	defer srv.Close()

	candidates := []string{"gemini-2.5-pro", "gemini-2.5-flash", "gemini-legacy", "gemini-2.5-pro", "  "}
	results := DiscoverModels(context.Background(), srv.Client(), srv.URL, "ya29.test-token", "proj-1", candidates, 2)

	if len(results) != 3 {
		t.Fatalf("results = %d, want 3 (deduped, blanks dropped): %+v", len(results), results)
	}
	var okIDs []string
	for _, r := range results {
		if r.OK {
			okIDs = append(okIDs, r.Model)
		}
	}
	if strings.Join(okIDs, ",") != "gemini-2.5-flash,gemini-2.5-pro" {
		t.Errorf("verified models = %v, want the two the host served (sorted)", okIDs)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 3 {
		t.Errorf("upstream saw %d probes (%v), want one per candidate", len(seen), seen)
	}
}

func TestDiscoverModels_EmptyCandidatesMakeNoCalls(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := cloudCodeStub(t, nil, http.StatusNotFound, &mu, &seen)
	defer srv.Close()

	if got := DiscoverModels(context.Background(), srv.Client(), srv.URL, "ya29.test-token", "proj-1", nil, 0); got != nil {
		t.Errorf("empty candidate list must not sweep: %+v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 0 {
		t.Errorf("upstream must not be called for an empty sweep: %v", seen)
	}
}

func TestUpstreamErrorMessage(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		want   string
	}{
		"error message":  {http.StatusNotFound, `{"error":{"message":"model is not available"}}`, "model is not available"},
		"error status":   {http.StatusForbidden, `{"error":{"status":"PERMISSION_DENIED"}}`, "PERMISSION_DENIED"},
		"top level":      {http.StatusBadRequest, `{"message":"bad envelope"}`, "bad envelope"},
		"plain text":     {http.StatusBadGateway, "upstream exploded", "upstream exploded"},
		"empty fallback": {http.StatusInternalServerError, "", http.StatusText(http.StatusInternalServerError)},
	}
	for name, tc := range cases {
		if got := UpstreamErrorMessage(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("%s: UpstreamErrorMessage = %q, want %q", name, got, tc.want)
		}
	}
	long := UpstreamErrorMessage(http.StatusBadRequest, []byte(`{"error":{"message":"`+strings.Repeat("x", 900)+`"}}`))
	if len(long) > maxErrorMessageChars+8 {
		t.Errorf("error message not bounded: %d chars", len(long))
	}
}

func TestCuratedModels_IsACopy(t *testing.T) {
	first := CuratedModels()
	if len(first) == 0 {
		t.Fatal("curated seed must not be empty")
	}
	first[0] = "mutated"
	if CuratedModels()[0] == "mutated" {
		t.Error("CuratedModels must return a copy, not the shared slice")
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	cases := map[string]string{
		"":                                         DefaultBaseURL(),
		"https://daily-cloudcode-pa.googleapis.com": DefaultBaseURL(),
		"https://daily-cloudcode-pa.googleapis.com/": "https://daily-cloudcode-pa.googleapis.com",
		" https://host/ ": "https://host",
	}
	for in, want := range cases {
		if got := NormalizeBaseURL(in); got != want {
			t.Errorf("NormalizeBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}
