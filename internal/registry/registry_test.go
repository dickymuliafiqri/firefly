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

// TestBuildAndStoreCarriesTokenSaver guards the reload path: TokenSaver used to
// be attached only by the settings-update handler, so a process that had not
// saved settings since boot (or that reloaded afterwards) ran every chat
// completion without the configured compression.
func TestBuildAndStoreCarriesTokenSaver(t *testing.T) {
	files := validFiles()
	files["tokensaver"] = []byte(`{"enabled":true,"compress_tool_output":true,` +
		`"terse_output":false,"minimal_code":false,"compress_context":false,` +
		`"max_tool_output_chars":12000,"context_threshold":32000}`)

	r := New()
	if _, err := r.BuildAndStore(context.Background(), &mapSource{files: files}, env); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ts := r.Current().TokenSaver()
	if !ts.Enabled || !ts.CompressToolOutput {
		t.Fatalf("TokenSaver config missing from the built snapshot: %+v", ts)
	}
}

var _ = config.FileSetFromMap

// TestBuildAndStoreCarriesPricing guards the reload path for the price sheet:
// the table must reach the snapshot on startup and on every watcher reload,
// otherwise per-model cost tracking silently reverts to the flat-rate
// fallback until the next settings save.
func TestBuildAndStoreCarriesPricing(t *testing.T) {
	files := validFiles()
	files["pricing"] = []byte(`{"entries":[
		{"model":"m","input_micros_per_m":2500,"output_micros_per_m":10000,"source":"models.dev"},
		{"model":"m-mini*","input_micros_per_m":150,"output_micros_per_m":600}
	]}`)

	r := New()
	if _, err := r.BuildAndStore(context.Background(), &mapSource{files: files}, env); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	table := r.Current().Pricing()
	if table == nil || table.Len() != 2 {
		t.Fatalf("pricing table missing from the built snapshot: %+v", table)
	}
	e, ok := table.Lookup("m")
	if !ok || e.InputMicrosPerM != 2500 || e.Source != "models.dev" {
		t.Fatalf("exact pricing entry wrong: %+v ok=%v", e, ok)
	}
	if _, ok := table.Lookup("m-mini-2024"); !ok {
		t.Fatal("wildcard pricing entry must resolve")
	}

	// A malformed sheet fails the reload and keeps the previous snapshot.
	bad := validFiles()
	bad["pricing"] = []byte(`{"entries":[{"model":"m","input_micros_per_m":-1}]}`)
	if _, err := r.BuildAndStore(context.Background(), &mapSource{files: bad}, env); err == nil {
		t.Fatal("negative price must fail the reload")
	}
	if r.Current().Pricing() == nil || r.Current().Pricing().Len() != 2 {
		t.Fatal("failed reload must keep the previous pricing table")
	}
}

// TestBuildAndStoreCarriesVisualizer guards the reload path for the private
// visualizer: the recorder bounds must reach the snapshot, otherwise the
// recorder silently falls back to defaults after every config reload.
func TestBuildAndStoreCarriesVisualizer(t *testing.T) {
	files := validFiles()
	files["visualizer"] = []byte(`{"enabled":true,"retention":42,"keep_events":7}`)

	r := New()
	if _, err := r.BuildAndStore(context.Background(), &mapSource{files: files}, env); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	viz := r.Current().Visualizer()
	if !viz.Enabled || viz.Retention != 42 || viz.KeepEvents != 7 {
		t.Fatalf("visualizer config missing from the built snapshot: %+v", viz)
	}
}
