package config

import "testing"

func TestAutoTLSPersistence(t *testing.T) {
	dir := t.TempDir()
	want := AutoTLSDTO{Enabled: true, Domain: "ai.example.com", Email: "ops@example.com"}
	if err := SaveAutoTLS(dir, want); err != nil {
		t.Fatalf("SaveAutoTLS() error = %v", err)
	}
	got, err := LoadAutoTLS(dir)
	if err != nil {
		t.Fatalf("LoadAutoTLS() error = %v", err)
	}
	if got != want {
		t.Fatalf("LoadAutoTLS() = %#v, want %#v", got, want)
	}
}

func TestLoadAutoTLSMissingIsDisabled(t *testing.T) {
	got, err := LoadAutoTLS(t.TempDir())
	if err != nil {
		t.Fatalf("LoadAutoTLS() error = %v", err)
	}
	if got.Enabled || got.Domain != "" || got.Email != "" {
		t.Fatalf("LoadAutoTLS() = %#v, want disabled default", got)
	}
}
