package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

// skipUnlessPosixModes skips the enclosing subtest on Windows, where os.Chmod
// only toggles the read-only flag and Stat reports 0666/0777 for every file, so
// an owner-only assertion can never hold there. Call it from inside a t.Run so
// the parent test's platform-independent checks still run; CI exercises the
// real assertion on Linux, the platform the service ships on.
func skipUnlessPosixModes(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not representable on Windows")
	}
}

func TestWriteSecretFileIsOwnerOnly(t *testing.T) {
	dir := t.TempDir()

	created := filepath.Join(dir, "created.json")
	if err := WriteSecretFile(created, []byte(`{"upstreams":[]}`)); err != nil {
		t.Fatalf("WriteSecretFile(create): %v", err)
	}
	t.Run("new file is owner-only", func(t *testing.T) {
		skipUnlessPosixModes(t)
		if got := fileMode(t, created); got != SecretFileMode {
			t.Errorf("new secret file mode = %o, want %o", got, SecretFileMode)
		}
	})

	// A file written by an older release at the default umask must be tightened,
	// not left world-readable: os.WriteFile ignores perm for existing files.
	existing := filepath.Join(dir, "existing.json")
	if err := os.WriteFile(existing, []byte(`{"upstreams":[]}`), 0o644); err != nil {
		t.Fatalf("seed world-readable file: %v", err)
	}
	if err := WriteSecretFile(existing, []byte(`{"upstreams":[{"name":"u1"}]}`)); err != nil {
		t.Fatalf("WriteSecretFile(tighten): %v", err)
	}
	t.Run("existing file is tightened", func(t *testing.T) {
		skipUnlessPosixModes(t)
		if got := fileMode(t, existing); got != SecretFileMode {
			t.Errorf("existing secret file mode = %o, want %o", got, SecretFileMode)
		}
	})
	raw, err := os.ReadFile(existing)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(raw) != `{"upstreams":[{"name":"u1"}]}` {
		t.Errorf("content = %s, want the new payload", raw)
	}
}

func TestEnsureConfigFilesPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "configs")
	if err := EnsureConfigFiles(dir); err != nil {
		t.Fatalf("EnsureConfigFiles: %v", err)
	}

	// Non-secret files (models/combos/tokensaver/tls) intentionally keep the
	// default 0644-ish mode and are not asserted here, since the process umask
	// decides their exact bits.
	t.Run("credential files are owner-only", func(t *testing.T) {
		skipUnlessPosixModes(t)
		for _, name := range []string{FileNameUpstreams, FileNameTenants} {
			if got := fileMode(t, filepath.Join(dir, name)); got != SecretFileMode {
				t.Errorf("%s mode = %o, want %o", name, got, SecretFileMode)
			}
		}
	})

	// Startup must also fix up credential files left world-readable by an older
	// release, without touching their contents.
	upstreams := filepath.Join(dir, FileNameUpstreams)
	if err := os.Chmod(upstreams, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := os.WriteFile(upstreams, []byte(`{"upstreams":[{"name":"kept"}]}`), 0o644); err != nil {
		t.Fatalf("seed world-readable upstreams: %v", err)
	}
	if err := EnsureConfigFiles(dir); err != nil {
		t.Fatalf("EnsureConfigFiles (second run): %v", err)
	}
	t.Run("re-run tightens a world-readable file", func(t *testing.T) {
		skipUnlessPosixModes(t)
		if got := fileMode(t, upstreams); got != SecretFileMode {
			t.Errorf("re-run %s mode = %o, want %o", FileNameUpstreams, got, SecretFileMode)
		}
	})
	raw, err := os.ReadFile(upstreams)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(raw) != `{"upstreams":[{"name":"kept"}]}` {
		t.Errorf("existing content overwritten: %s", raw)
	}
}
