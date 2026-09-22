package turso

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEnsureLocalDirIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not representable on Windows")
	}

	root := t.TempDir()

	fresh := filepath.Join(root, "nested", "data", "firefly.db")
	dir, err := ensureLocalDir(fresh, nil)
	if err != nil {
		t.Fatalf("ensureLocalDir(fresh): %v", err)
	}
	if want := filepath.Join(root, "nested", "data"); dir != want {
		t.Fatalf("dir = %q, want %q", dir, want)
	}
	freshInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat fresh dir: %v", err)
	}
	if got := freshInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("fresh dir mode = %o, want 700", got)
	}

	// A directory created by an older release at 0755 must be tightened, since
	// the replica inside mirrors raw upstream credentials.
	legacy := filepath.Join(root, "legacy", "data")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatalf("seed legacy dir: %v", err)
	}
	if err := os.Chmod(legacy, 0o755); err != nil {
		t.Fatalf("chmod legacy dir: %v", err)
	}
	if _, err := ensureLocalDir(filepath.Join(legacy, "firefly.db"), nil); err != nil {
		t.Fatalf("ensureLocalDir(legacy): %v", err)
	}
	legacyInfo, err := os.Stat(legacy)
	if err != nil {
		t.Fatalf("stat legacy dir: %v", err)
	}
	if got := legacyInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("legacy dir mode = %o, want 700", got)
	}
}
