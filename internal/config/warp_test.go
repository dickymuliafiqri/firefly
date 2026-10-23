package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWarpRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	if err := SaveWarp(dir, WarpDTO{PoolSize: 25}); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := LoadWarp(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.PoolSize != 25 {
		t.Fatalf("pool_size = %d, want 25", got.PoolSize)
	}

	// The file holds tuning only, so it must not be owner-only like a
	// credential-bearing config. Windows ignores POSIX mode bits (Chmod only
	// toggles the read-only flag), so the check is skipped there.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, FileNameWarp))
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o644 {
			t.Fatalf("perm = %o, want %o", perm, 0o644)
		}
	}
}

func TestWarpMissingFileIsZeroValue(t *testing.T) {
	t.Parallel()

	cfg, err := LoadWarp(t.TempDir())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.PoolSize != 0 {
		t.Fatalf("expected unset pool size, got %d", cfg.PoolSize)
	}
}

func TestWarpRejectsNegativePoolSize(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileNameWarp), []byte(`{"pool_size":-4}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := LoadWarp(dir); err == nil || !strings.Contains(err.Error(), "must not be negative") {
		t.Fatalf("expected negative rejection, got %v", err)
	}
}

func TestWarpRejectsUnknownField(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileNameWarp), []byte(`{"poolsize":3}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := LoadWarp(dir); err == nil {
		t.Fatal("expected an unknown-field error from the strict decoder")
	}
}

func TestWarpOutOfRangeIsLoadedNotRejected(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// The 1..64 window is a dashboard policy owned by the warp package; the
	// loader must hand an out-of-range value to the caller so it can be ignored
	// with a warning instead of failing the boot.
	if err := os.WriteFile(filepath.Join(dir, FileNameWarp), []byte(`{"pool_size":900}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := LoadWarp(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.PoolSize != 900 {
		t.Fatalf("pool_size = %d, want 900", cfg.PoolSize)
	}
}

func TestWarpPreferredColoRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	if err := SaveWarp(dir, WarpDTO{PoolSize: 10, PreferredColo: "SIN"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := LoadWarp(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.PreferredColo != "SIN" {
		t.Fatalf("preferred_colo = %q, want SIN", got.PreferredColo)
	}
}
