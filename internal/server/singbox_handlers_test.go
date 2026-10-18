package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/transport/singbox"
)

// newSingboxStrategyRequest builds an authorized strategy request.
func newSingboxStrategyRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPut, "/api/singbox/strategy", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret-admin-token")
	return req
}

func TestHandleSetSingboxStrategy_PersistsAndApplies(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := singbox.NewManager(nil)
	deps := RouterDeps{
		AdminToken:     "secret-admin-token",
		SingboxManager: mgr,
		ConfigDir:      dir,
	}

	rr := httptest.NewRecorder()
	deps.handleSetSingboxStrategy(rr, newSingboxStrategyRequest(`{"strategy":"round_robin"}`))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}

	var st singbox.Status
	if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if st.Strategy != domain.SingboxStrategyRoundRobin {
		t.Errorf("status strategy = %q, want round_robin", st.Strategy)
	}
	// The engine must carry it, not just the response body.
	if got := mgr.Strategy(); got != domain.SingboxStrategyRoundRobin {
		t.Errorf("engine strategy = %q, want round_robin", got)
	}
	cfg, err := config.LoadSingbox(dir)
	if err != nil {
		t.Fatalf("load persisted config: %v", err)
	}
	if cfg.Strategy != domain.SingboxStrategyRoundRobin {
		t.Errorf("persisted strategy = %q, want round_robin", cfg.Strategy)
	}
}

// A strategy save must not drop the subscription URL or default tag a previous
// save wrote: the handler rebuilds the full DTO from the manager.
func TestHandleSetSingboxStrategy_PreservesOtherFields(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := singbox.NewManager(nil)
	mgr.SetSubscriptionURL("https://example.com/sub?token=x", 0)
	mgr.SetDefaultTag("node-pinned")
	deps := RouterDeps{
		AdminToken:     "secret-admin-token",
		SingboxManager: mgr,
		ConfigDir:      dir,
	}

	rr := httptest.NewRecorder()
	deps.handleSetSingboxStrategy(rr, newSingboxStrategyRequest(`{"strategy":"round_robin"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}

	cfg, err := config.LoadSingbox(dir)
	if err != nil {
		t.Fatalf("load persisted config: %v", err)
	}
	if cfg.SubscriptionURL != "https://example.com/sub?token=x" {
		t.Errorf("subscription_url = %q, want preserved", cfg.SubscriptionURL)
	}
	if cfg.DefaultOutboundTag != "node-pinned" {
		t.Errorf("default_outbound_tag = %q, want preserved", cfg.DefaultOutboundTag)
	}
}

func TestHandleSetSingboxStrategy_RejectsInvalid(t *testing.T) {
	t.Parallel()

	for _, body := range []string{`{"strategy":"random"}`, `{"strategy":""}`, `{"strategy":"roundrobin"}`} {
		dir := t.TempDir()
		mgr := singbox.NewManager(nil)
		deps := RouterDeps{
			AdminToken:     "secret-admin-token",
			SingboxManager: mgr,
			ConfigDir:      dir,
		}

		rr := httptest.NewRecorder()
		deps.handleSetSingboxStrategy(rr, newSingboxStrategyRequest(body))

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("body %s: expected 400, got %d (%s)", body, rr.Code, rr.Body.String())
		}
		if got := mgr.Strategy(); got != domain.SingboxStrategyURLTest {
			t.Errorf("body %s: engine strategy changed to %q on a rejected request", body, got)
		}
		if _, err := os.Stat(filepath.Join(dir, config.FileNameSingbox)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("body %s: singbox.json must not be written for a rejected value", body)
		}
	}
}

func TestHandleSetSingboxStrategy_Unauthorized(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	deps := RouterDeps{
		AdminToken:     "secret-admin-token",
		SingboxManager: singbox.NewManager(nil),
		ConfigDir:      dir,
	}

	req := httptest.NewRequest(http.MethodPut, "/api/singbox/strategy", strings.NewReader(`{"strategy":"round_robin"}`))
	rr := httptest.NewRecorder()
	deps.handleSetSingboxStrategy(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, config.FileNameSingbox)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("singbox.json must not be written after a rejected request")
	}
}

func TestHandleSetSingboxStrategy_RejectsUninitializedEngine(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	deps := RouterDeps{
		AdminToken: "secret-admin-token",
		ConfigDir:  dir,
	}

	rr := httptest.NewRecorder()
	deps.handleSetSingboxStrategy(rr, newSingboxStrategyRequest(`{"strategy":"round_robin"}`))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, config.FileNameSingbox)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("singbox.json must not be written when the engine is missing")
	}
}

// The public status endpoint must never serve the subscription URL: it is a
// token-bearing credential, protected everywhere else (0600 file, admin-only
// GET /api/singbox/subscription). An unauthenticated caller learns only that
// one is configured.
func TestHandleGetSingboxStatus_HidesSubscriptionURLWhenUnauthenticated(t *testing.T) {
	t.Parallel()

	const subURL = "https://example.com/sub?token=supersecret"
	mgr := singbox.NewManager(nil)
	mgr.SetSubscriptionURL(subURL, 0)
	deps := RouterDeps{
		AdminToken:     "secret-admin-token",
		SingboxManager: mgr,
	}

	// Unauthenticated.
	rr := httptest.NewRecorder()
	deps.handleGetSingboxStatus(rr, httptest.NewRequest(http.MethodGet, "/api/singbox/status", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if body := rr.Body.String(); strings.Contains(body, "supersecret") || strings.Contains(body, subURL) {
		t.Fatalf("unauthenticated status leaked the subscription URL: %s", body)
	}
	var anon singbox.Status
	if err := json.Unmarshal(rr.Body.Bytes(), &anon); err != nil {
		t.Fatalf("unmarshal anon status: %v", err)
	}
	if anon.SubscriptionURL != "" {
		t.Errorf("anon subscription_url = %q, want empty", anon.SubscriptionURL)
	}
	if !anon.SubscriptionConfigured {
		t.Error("anon subscription_configured = false, want true")
	}

	// Authenticated: the dashboard poll carries the admin session token, so the
	// form can still prefill the URL.
	authedReq := httptest.NewRequest(http.MethodGet, "/api/singbox/status", nil)
	authedReq.Header.Set("Authorization", "Bearer secret-admin-token")
	rrAuthed := httptest.NewRecorder()
	deps.handleGetSingboxStatus(rrAuthed, authedReq)
	var authed singbox.Status
	if err := json.Unmarshal(rrAuthed.Body.Bytes(), &authed); err != nil {
		t.Fatalf("unmarshal authed status: %v", err)
	}
	if authed.SubscriptionURL != subURL {
		t.Errorf("authed subscription_url = %q, want %q", authed.SubscriptionURL, subURL)
	}
}

// A fetch error can embed the subscription URL; the public status must redact
// it. Admin calls keep the full error for debugging.
func TestHandleGetSingboxStatus_RedactsRefreshErrorForUnauthenticated(t *testing.T) {
	t.Parallel()

	// Port 1 on loopback refuses immediately, so Refresh fails fast with a
	// transport error that embeds the URL.
	const subURL = "http://127.0.0.1:1/sub?token=supersecret"
	mgr := singbox.NewManager(nil)
	mgr.SetSubscriptionURL(subURL, 0)
	if err := mgr.Refresh(context.Background()); err == nil {
		t.Fatal("expected Refresh to fail against a closed port")
	}

	deps := RouterDeps{AdminToken: "secret-admin-token", SingboxManager: mgr}
	rr := httptest.NewRecorder()
	deps.handleGetSingboxStatus(rr, httptest.NewRequest(http.MethodGet, "/api/singbox/status", nil))
	if body := rr.Body.String(); strings.Contains(body, "supersecret") {
		t.Fatalf("unauthenticated status leaked the subscription URL through an error: %s", body)
	}
}
