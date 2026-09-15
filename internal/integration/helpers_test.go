// Package integration exercises the full Phase-3 request path: config files on
// disk -> registry snapshot -> auth -> admission -> HTTP server, including a
// hot reload that changes live behavior.
package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/auth"
	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/dickymuliafiqri/firefly/internal/registry"
	"github.com/dickymuliafiqri/firefly/internal/server"
	"github.com/dickymuliafiqri/firefly/internal/usage"
)

const gatewayKey = "sk-gw-demo-000000000000000000000000"

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// writeConfig writes a coherent 3-file config set into dir.
func writeConfig(t *testing.T, dir, status string, allowedModels []string) {
	t.Helper()

	upstreams := []byte(`{"upstreams":[{"name":"openai","base_url":"https://api.openai.com/v1",` +
		`"credential_ref":"OPENAI_API_KEY","protocol":"openai"}]}`)

	mods, _ := json.Marshal(map[string]any{"models": []map[string]any{
		{"public_name": "gpt-4o", "upstream": "openai", "upstream_model": "gpt-4o", "enabled": true},
		{"public_name": "gpt-4o-mini", "upstream": "openai", "upstream_model": "gpt-4o-mini", "enabled": true},
	}})

	tenants, _ := json.Marshal(map[string]any{"tenants": []map[string]any{{
		"key_hash":       auth.HashKey(gatewayKey),
		"name":           "demo-tenant",
		"status":         status,
		"allowed_models": allowedModels,
		"rate_limit":     map[string]any{"rps": 500, "burst": 500, "max_concurrent": 50},
	}}})

	for name, data := range map[string][]byte{
		config.FileNameUpstreams: upstreams,
		config.FileNameModels:    mods,
		config.FileNameTenants:   tenants,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func envOK(string) (string, bool) { return "value", true }

// setupServer starts a live HTTP server wired to reg and returns its base URL.
func setupServer(t *testing.T, reg *registry.Registry) string {
	t.Helper()
	deps := server.RouterDeps{
		Snapshots:   reg,
		TenantStore: auth.NewStore(reg),
		Limiter:     limits.New(),
		Usage:       usage.NewCounters(),
		Logger:      discardLogger(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := server.New(server.Config{Addr: "127.0.0.1:0", ShutdownGrace: 2 * time.Second}, deps, ctx, discardLogger())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.ServeOnListener(ln) }()
	return "http://" + ln.Addr().String()
}

func build(t *testing.T, reg *registry.Registry, dir string) {
	t.Helper()
	if _, err := reg.BuildAndStore(context.Background(), config.NewFileConfigSource(dir), envOK); err != nil {
		t.Fatalf("build: %v", err)
	}
}

// writeConfigWithUpstream writes a config set whose "openai" upstream points at
// baseURL, so tests can target a local fake upstream.
func writeConfigWithUpstream(t *testing.T, dir, baseURL string, allowedModels []string) {
	t.Helper()
	upstreams := []byte(`{"upstreams":[{"name":"openai","base_url":"` + baseURL + `",` +
		`"credential_ref":"OPENAI_API_KEY","protocol":"openai","allow_insecure":true}]}`)
	mods, _ := json.Marshal(map[string]any{"models": []map[string]any{
		{"public_name": "gpt-4o", "upstream": "openai", "upstream_model": "gpt-4o", "enabled": true},
		{"public_name": "gpt-4o-mini", "upstream": "openai", "upstream_model": "gpt-4o-mini", "enabled": true},
	}})
	tenants, _ := json.Marshal(map[string]any{"tenants": []map[string]any{{
		"key_hash":       auth.HashKey(gatewayKey),
		"name":           "demo-tenant",
		"status":         "active",
		"allowed_models": allowedModels,
		"rate_limit":     map[string]any{"rps": 500, "burst": 500, "max_concurrent": 50},
	}}})
	for name, data := range map[string][]byte{
		config.FileNameUpstreams: upstreams,
		config.FileNameModels:    mods,
		config.FileNameTenants:   tenants,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// writeConfigWithConcurrency writes config with custom tenant RPS, Burst, and MaxConcurrent.
func writeConfigWithConcurrency(t *testing.T, dir, baseURL string, allowedModels []string, maxConcurrent int) {
	t.Helper()
	upstreams := []byte(`{"upstreams":[{"name":"openai","base_url":"` + baseURL + `",` +
		`"credential_ref":"OPENAI_API_KEY","protocol":"openai","allow_insecure":true,"max_idle_conns_per_host":2000,"max_conns_per_host":2000}]}`)
	mods, _ := json.Marshal(map[string]any{"models": []map[string]any{
		{"public_name": "gpt-4o", "upstream": "openai", "upstream_model": "gpt-4o", "enabled": true},
		{"public_name": "gpt-4o-mini", "upstream": "openai", "upstream_model": "gpt-4o-mini", "enabled": true},
	}})
	tenants, _ := json.Marshal(map[string]any{"tenants": []map[string]any{{
		"key_hash":       auth.HashKey(gatewayKey),
		"name":           "demo-tenant",
		"status":         "active",
		"allowed_models": allowedModels,
		"rate_limit":     map[string]any{"rps": 50000, "burst": 50000, "max_concurrent": maxConcurrent},
	}}})
	for name, data := range map[string][]byte{
		config.FileNameUpstreams: upstreams,
		config.FileNameModels:    mods,
		config.FileNameTenants:   tenants,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}


// setupServerWithAdapter starts a live server from reg with a caller-supplied
// adapter, so tests can point the gateway at a fake upstream.
func setupServerWithAdapter(t *testing.T, reg *registry.Registry, adapter ports.UpstreamAdapter) string {
	t.Helper()
	deps := server.RouterDeps{
		Snapshots:   reg,
		TenantStore: auth.NewStore(reg),
		Limiter:     limits.New(),
		Adapter:     adapter,
		Usage:       usage.NewCounters(),
		Logger:      discardLogger(),
	}
	return serve(t, deps)
}

func serve(t *testing.T, deps server.RouterDeps) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := server.New(server.Config{Addr: "127.0.0.1:0", ShutdownGrace: 2 * time.Second}, deps, ctx, discardLogger())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.ServeOnListener(ln) }()
	return "http://" + ln.Addr().String()
}
