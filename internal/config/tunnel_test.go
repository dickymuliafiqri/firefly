package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTunnelRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// A mixed-case mode is normalized on save.
	if err := SaveTunnel(dir, TunnelDTO{Mode: "Named", Token: "token-abc", BinDir: "/opt/bin"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := LoadTunnel(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Mode != "named" {
		t.Fatalf("mode = %q, want named", got.Mode)
	}
	if got.Token != "token-abc" || got.BinDir != "/opt/bin" {
		t.Fatalf("unexpected roundtrip: %+v", got)
	}

	// A token-bearing file must be owner-only.
	info, err := os.Stat(filepath.Join(dir, FileNameTunnel))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != SecretFileMode {
		t.Fatalf("perm = %o, want %o", perm, SecretFileMode)
	}
}

func TestTunnelMissingFileIsZeroValue(t *testing.T) {
	t.Parallel()
	cfg, err := LoadTunnel(t.TempDir())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Mode != "" || cfg.Token != "" || cfg.BinDir != "" {
		t.Fatalf("expected empty config, got %+v", cfg)
	}
}

func TestTunnelQuickModeIsNotSecret(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := SaveTunnel(dir, TunnelDTO{Mode: "quick"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, FileNameTunnel))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Fatalf("perm = %o, want 0644 for a token-free config", perm)
	}
}

func TestTunnelRejectsUnknownField(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileNameTunnel), []byte(`{"mode":"quick","bogus":1}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadTunnel(dir); err == nil {
		t.Fatal("expected strict decode to reject unknown field")
	}
}

func TestTunnelNormalizeMode(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"Named":   "named",
		" QUICK ": "quick",
		"bogus":   "",
		"":        "",
	}
	for in, want := range cases {
		if got := (TunnelDTO{Mode: in}).NormalizeMode(); got != want {
			t.Errorf("NormalizeMode(%q) = %q, want %q", in, got, want)
		}
	}
}
