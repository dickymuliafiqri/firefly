package registry

import (
	"context"
	"errors"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/config"
)

// mapSource is an in-memory ConfigSource for tests.
type mapSource struct {
	files map[string][]byte
	err   error
}

func (m *mapSource) Load(ctx context.Context) (map[string][]byte, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.files, nil
}

func validFiles() map[string][]byte {
	return map[string][]byte{
		"upstreams": []byte(`{"upstreams":[{"name":"openai-main","base_url":"https://x/v1","credential_ref":"K"}]}`),
		"models":    []byte(`{"models":[{"public_name":"m","upstream":"openai-main","upstream_model":"m"}]}`),
		"tenants":   []byte(`{"tenants":[{"key_hash":"sha256:` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `","name":"t"}]}`),
	}
}

func env(k string) (string, bool) { return "v", k == "K" }

func TestBuildAndStore(t *testing.T) {
	r := New()
	if r.Current() != nil {
		t.Fatal("fresh registry must have no snapshot")
	}
	warns, err := r.BuildAndStore(context.Background(), &mapSource{files: validFiles()}, env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = warns
	s := r.Current()
	if s == nil || s.Generation() != 1 {
		t.Fatalf("expected generation 1 snapshot, got %+v", s)
	}
	if _, ok := s.Model("m"); !ok {
		t.Fatal("model m missing")
	}
}

func TestBuildAndStoreFailClosed(t *testing.T) {
	r := New()
	if _, err := r.BuildAndStore(context.Background(), &mapSource{files: validFiles()}, env); err != nil {
		t.Fatalf("seed failed: %v", err)
	}
	first := r.Current()

	// Second load fails at the source.
	if _, err := r.BuildAndStore(context.Background(), &mapSource{err: errors.New("disk gone")}, env); err == nil {
		t.Fatal("expected error")
	}
	if r.Current() != first {
		t.Fatal("failed reload must keep previous snapshot")
	}
	if r.CurrentGeneration() != 1 {
		t.Fatalf("generation advanced on failure: %d", r.CurrentGeneration())
	}

	// Second load fails at validation (unknown upstream).
	bad := validFiles()
	bad["models"] = []byte(`{"models":[{"public_name":"m","upstream":"nope","upstream_model":"m"}]}`)
	if _, err := r.BuildAndStore(context.Background(), &mapSource{files: bad}, env); err == nil {
		t.Fatal("expected validation error")
	}
	if r.Current() != first {
		t.Fatal("validation failure must keep previous snapshot")
	}
}

func TestGenerationMonotonic(t *testing.T) {
	r := New()
	for i := 1; i <= 3; i++ {
		if _, err := r.BuildAndStore(context.Background(), &mapSource{files: validFiles()}, env); err != nil {
			t.Fatalf("load %d: %v", i, err)
		}
		if got := r.Current().Generation(); got != uint64(i) {
			t.Fatalf("generation = %d, want %d", got, i)
		}
	}
}

var _ = config.FileSetFromMap
