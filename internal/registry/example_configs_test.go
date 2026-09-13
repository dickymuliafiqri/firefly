package registry_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/registry"
)

// TestExampleConfigsLoad ensures the shipped example configs are valid so the
// project can actually be started with them.
func TestExampleConfigsLoad(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	// internal/registry -> repo root is two levels up.
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")
	dir := filepath.Join(root, "configs")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("configs dir not found: %v", err)
	}

	// The example references OPENAI_API_KEY; inject it for the check.
	env := func(k string) (string, bool) {
		if k == "OPENAI_API_KEY" {
			return "sk-test", true
		}
		return os.LookupEnv(k)
	}

	r := registry.New()
	if _, err := r.BuildAndStore(context.Background(), config.NewFileConfigSource(dir), env); err != nil {
		t.Fatalf("example configs invalid: %v", err)
	}
	s := r.Current()
	if s.Generation() != 1 {
		t.Fatalf("generation = %d", s.Generation())
	}
	modelName := "gpt-4o-mini"
	if _, ok := s.Model(modelName); !ok {
		if enabled := s.EnabledModels(); len(enabled) > 0 {
			modelName = enabled[0]
		} else {
			t.Fatal("no models found in catalog")
		}
	}
	hashes := s.TenantHashes()
	if len(hashes) == 0 {
		t.Fatal("no tenants found in catalog")
	}
	tenant, ok := s.TenantByHash(hashes[0])
	if !ok {
		t.Fatal("tenant missing")
	}
	tgt, err := s.ResolveTarget(tenant, modelName)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if tgt.Upstream.Name == "" {
		t.Fatal("upstream name is empty")
	}
}
