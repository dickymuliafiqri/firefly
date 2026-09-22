package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/observability/usage"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/dickymuliafiqri/firefly/internal/registry"
	"github.com/dickymuliafiqri/firefly/internal/security/auth"
	"github.com/dickymuliafiqri/firefly/internal/server"
)

func TestEndToEndAuthAndModels(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "active", []string{"gpt-4o-mini"})
	reg := registry.New()
	build(t, reg, dir)
	base := setupServer(t, reg)

	// 1. Unauthenticated -> 401.
	resp, err := http.Get(base + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no auth = %d, want 401", resp.StatusCode)
	}

	// 2. Authenticated -> 200, only the allowed model is visible.
	req, _ := http.NewRequest("GET", base+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+gatewayKey)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authed = %d, want 200", resp.StatusCode)
	}
	var body server.ModelsResponse
	_ = json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	ids := map[string]bool{}
	for _, m := range body.Data {
		ids[m.ID] = true
	}
	if !ids["gpt-4o-mini"] || ids["gpt-4o"] {
		t.Fatalf("models = %v, want only gpt-4o-mini", ids)
	}

	// 3. /v1/chat/completions with a missing model param is a client error
	//    (400 invalid_request_error), not a 501: the endpoint is live.
	req2, _ := http.NewRequest("POST", base+"/v1/chat/completions", strings.NewReader("{}"))
	req2.Header.Set("Authorization", "Bearer "+gatewayKey)
	resp, err = http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("chat (no model) = %d, want 400", resp.StatusCode)
	}

	// 4. A model the tenant may not use is a 403 from the routing layer.
	req3, _ := http.NewRequest("POST", base+"/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o"}`))
	req3.Header.Set("Authorization", "Bearer "+gatewayKey)
	resp, err = http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("chat (forbidden model) = %d, want 403", resp.StatusCode)
	}
}

func TestHotReloadChangesLiveAuth(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "active", []string{"gpt-4o-mini"})
	reg := registry.New()
	build(t, reg, dir)
	base := setupServer(t, reg)

	status := func() int {
		req, _ := http.NewRequest("GET", base+"/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+gatewayKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	modelCount := func() int {
		req, _ := http.NewRequest("GET", base+"/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+gatewayKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var b server.ModelsResponse
		_ = json.NewDecoder(resp.Body).Decode(&b)
		return len(b.Data)
	}

	if got := status(); got != http.StatusOK {
		t.Fatalf("before reload = %d, want 200", got)
	}

	// Suspend the tenant and reload: live server must reject.
	writeConfig(t, dir, "suspended", []string{"gpt-4o-mini"})
	build(t, reg, dir)
	if got := status(); got != http.StatusUnauthorized {
		t.Fatalf("after suspend reload = %d, want 401", got)
	}

	// Re-activate with an expanded allow-list: new model must appear.
	writeConfig(t, dir, "active", []string{"gpt-4o", "gpt-4o-mini"})
	build(t, reg, dir)
	if got := modelCount(); got != 2 {
		t.Fatalf("after reactivate: %d models, want 2", got)
	}
}

// TestReloadFailClosedKeepsServing verifies a broken reload leaves the previous
// snapshot (and thus live auth) intact.
func TestReloadFailClosedKeepsServing(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "active", []string{"gpt-4o-mini"})
	reg := registry.New()
	build(t, reg, dir)
	genBefore := reg.CurrentGeneration()
	base := setupServer(t, reg)

	// Corrupt one file and attempt a reload; it must fail, not swap.
	if err := os.WriteFile(filepath.Join(dir, config.FileNameModels), []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.BuildAndStore(context.Background(), config.NewFileConfigSource(dir), envOK); err == nil {
		t.Fatal("expected reload to fail on corrupt config")
	}
	if reg.CurrentGeneration() != genBefore {
		t.Fatalf("generation advanced on failed reload: %d -> %d", genBefore, reg.CurrentGeneration())
	}

	req, _ := http.NewRequest("GET", base+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+gatewayKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("live auth broke after failed reload: %d", resp.StatusCode)
	}
}

type fakeEchoAdapter struct{}

func (f *fakeEchoAdapter) Protocol() domain.Protocol { return domain.ProtocolOpenAI }
func (f *fakeEchoAdapter) Forward(ctx context.Context, tgt *domain.Target, req ports.ForwardRequest, respW io.Writer) error {
	_, err := respW.Write([]byte(`{"id":"chatcmpl-123","choices":[{"message":{"role":"assistant","content":"hello"}}]}`))
	return err
}

func TestDirectConnectionRejectedWhenUpstreamDisabled(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test-12345")
	dir := t.TempDir()
	writeConfig(t, dir, "active", []string{"gpt-4o-mini"})
	reg := registry.New()
	build(t, reg, dir)

	deps := server.RouterDeps{
		Snapshots:   reg,
		Registry:    reg,
		ConfigDir:   dir,
		TenantStore: auth.NewStore(reg),
		Limiter:     limits.New(),
		Adapter:     &fakeEchoAdapter{},
		Usage:       usage.NewCounters(),
		Logger:      discardLogger(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := server.New(server.Config{Addr: "127.0.0.1:0", ShutdownGrace: 100 * time.Millisecond}, deps, ctx, discardLogger())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		_ = s.ServeOnListener(ln)
	}()
	defer func() {
		cancel()
		<-serverDone
	}()
	base := "http://" + ln.Addr().String()

	client := &http.Client{
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
	}

	// 1. Direct connection to /v1/chat/completions while upstream is enabled -> 200 OK.
	req, _ := http.NewRequest("POST", base+"/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+gatewayKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK when enabled, got %d: %s", resp.StatusCode, string(body))
	}

	// 2. Disable upstream via POST /api/settings (exactly as the frontend does when toggling upstream)
	disablePayload := fmt.Sprintf(`{"upstreams":[{"name":"openai","base_url":"https://api.openai.com/v1","credential_ref":"OPENAI_API_KEY","protocol":"openai","enabled":false}],"models":[{"public_name":"gpt-4o-mini","upstream":"openai","upstream_model":"gpt-4o-mini","enabled":true}],"tenants":[{"name":"demo-tenant","status":"active","key_hash":%q,"allowed_models":["gpt-4o-mini"]}]}`, auth.HashKey(gatewayKey))
	saveReq, _ := http.NewRequest("POST", base+"/api/settings", strings.NewReader(disablePayload))
	saveReq.Header.Set("Content-Type", "application/json")
	saveResp, err := client.Do(saveReq)
	if err != nil {
		t.Fatal(err)
	}
	saveBody, _ := io.ReadAll(saveResp.Body)
	saveResp.Body.Close()
	if saveResp.StatusCode != http.StatusOK {
		t.Fatalf("failed to save settings: %d body=%s", saveResp.StatusCode, string(saveBody))
	}

	// 3. Direct connection to /v1/chat/completions after upstream disabled -> MUST BE REJECTED with 503 Service Unavailable!
	req2, _ := http.NewRequest("POST", base+"/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`))
	req2.Header.Set("Authorization", "Bearer "+gatewayKey)
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable when upstream disabled, got %d: %s", resp2.StatusCode, string(body2))
	}
	if !strings.Contains(string(body2), "upstream disabled or circuit open") {
		t.Fatalf("expected error body to mention upstream disabled, got: %s", string(body2))
	}

	// 4. Re-enable upstream via POST /api/settings
	enablePayload := fmt.Sprintf(`{"upstreams":[{"name":"openai","base_url":"https://api.openai.com/v1","credential_ref":"OPENAI_API_KEY","protocol":"openai","enabled":true}],"models":[{"public_name":"gpt-4o-mini","upstream":"openai","upstream_model":"gpt-4o-mini","enabled":true}],"tenants":[{"name":"demo-tenant","status":"active","key_hash":%q,"allowed_models":["gpt-4o-mini"]}]}`, auth.HashKey(gatewayKey))
	saveReq2, _ := http.NewRequest("POST", base+"/api/settings", strings.NewReader(enablePayload))
	saveReq2.Header.Set("Content-Type", "application/json")
	saveResp2, err := client.Do(saveReq2)
	if err != nil {
		t.Fatal(err)
	}
	saveResp2.Body.Close()
	if saveResp2.StatusCode != http.StatusOK {
		t.Fatalf("failed to re-enable settings: %d", saveResp2.StatusCode)
	}

	// 5. Direct connection to /v1/chat/completions succeeds again -> 200 OK!
	req3, _ := http.NewRequest("POST", base+"/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`))
	req3.Header.Set("Authorization", "Bearer "+gatewayKey)
	req3.Header.Set("Content-Type", "application/json")
	resp3, err := client.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK after re-enabling upstream, got %d", resp3.StatusCode)
	}
}
