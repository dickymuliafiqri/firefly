package config

import (
	"os"
	"path/filepath"
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

func TestWriteSecretFileIsOwnerOnly(t *testing.T) {
	dir := t.TempDir()

	created := filepath.Join(dir, "created.json")
	if err := WriteSecretFile(created, []byte(`{"upstreams":[]}`)); err != nil {
		t.Fatalf("WriteSecretFile(create): %v", err)
	}
	if got := fileMode(t, created); got != SecretFileMode {
		t.Errorf("new secret file mode = %o, want %o", got, SecretFileMode)
	}

	// A file written by an older release at the default umask must be tightened,
	// not left world-readable: os.WriteFile ignores perm for existing files.
	existing := filepath.Join(dir, "existing.json")
	if err := os.WriteFile(existing, []byte(`{"upstreams":[]}`), 0o644); err != nil {
		t.Fatalf("seed world-readable file: %v", err)
	}
	if err := WriteSecretFile(existing, []byte(`{"upstreams":[{"name":"u1"}]}`)); err != nil {
		t.Fatalf("WriteSecretFile(tighten): %v", err)
	}
	if got := fileMode(t, existing); got != SecretFileMode {
		t.Errorf("existing secret file mode = %o, want %o", got, SecretFileMode)
	}
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
	for _, name := range []string{FileNameUpstreams, FileNameTenants} {
		if got := fileMode(t, filepath.Join(dir, name)); got != SecretFileMode {
			t.Errorf("%s mode = %o, want %o", name, got, SecretFileMode)
		}
	}

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
	if got := fileMode(t, upstreams); got != SecretFileMode {
		t.Errorf("re-run %s mode = %o, want %o", FileNameUpstreams, got, SecretFileMode)
	}
	raw, err := os.ReadFile(upstreams)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(raw) != `{"upstreams":[{"name":"kept"}]}` {
		t.Errorf("existing content overwritten: %s", raw)
	}
}
