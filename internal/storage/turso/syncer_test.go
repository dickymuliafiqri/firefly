package turso

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/registry"
)

// syncerTestSettings is the smallest catalog this build can compile: one
// upstream carrying a resolved secret and one model routing to it.
func syncerTestSettings() config.SettingsDTO {
	enabled := true
	return config.SettingsDTO{
		Upstreams: []config.UpstreamDTO{{
			Name:        "openai",
			Protocol:    "openai",
			BaseURL:     "https://api.openai.com/v1",
			KeyStrategy: "round_robin",
			Enabled:     &enabled,
			CredentialPool: []config.CredentialKeyDTO{
				{Ref: "openai-cred-1", Secret: "sk-syncer-test-secret"},
			},
		}},
		Models: []config.ModelDTO{{
			PublicName:    "gpt-4o",
			Upstream:      "openai",
			UpstreamModel: "gpt-4o",
			Enabled:       &enabled,
			Capabilities:  &config.CapabilitiesDTO{Stream: true},
		}},
	}
}

func TestNextSyncInterval(t *testing.T) {
	const (
		base = 60 * time.Second
		max  = 5 * time.Minute
	)

	tests := []struct {
		name     string
		current  time.Duration
		base     time.Duration
		max      time.Duration
		changed  bool
		expected time.Duration
	}{
		{
			name:     "changed cycle resets to base",
			current:  max,
			base:     base,
			max:      max,
			changed:  true,
			expected: base,
		},
		{
			name:     "idle cycle doubles the delay",
			current:  base,
			base:     base,
			max:      max,
			changed:  false,
			expected: 2 * base,
		},
		{
			name:     "idle backoff stops at the ceiling",
			current:  4 * base,
			base:     base,
			max:      max,
			changed:  false,
			expected: max,
		},
		{
			name:     "delay never drops below base",
			current:  time.Second,
			base:     base,
			max:      max,
			changed:  false,
			expected: base,
		},
		{
			name:     "zero base falls back to the default",
			current:  10 * DefaultSyncInterval,
			base:     0,
			max:      max,
			changed:  false,
			expected: max,
		},
		{
			name:     "ceiling below base is raised to base",
			current:  base,
			base:     base,
			max:      time.Second,
			changed:  false,
			expected: base,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nextSyncInterval(tt.current, tt.base, tt.max, tt.changed)
			if got != tt.expected {
				t.Fatalf("nextSyncInterval(%s, %s, %s, %v) = %s, want %s",
					tt.current, tt.base, tt.max, tt.changed, got, tt.expected)
			}
		})
	}
}

func TestSyncerConfigDefaults(t *testing.T) {
	tests := []struct {
		name     string
		cfg      SyncerConfig
		wantBase time.Duration
		wantMax  time.Duration
	}{
		{
			name:     "zero values use package defaults",
			cfg:      SyncerConfig{},
			wantBase: DefaultSyncInterval,
			wantMax:  DefaultSyncMaxInterval,
		},
		{
			name:     "ceiling below base is raised",
			cfg:      SyncerConfig{Interval: 10 * time.Minute, MaxInterval: time.Minute},
			wantBase: 10 * time.Minute,
			wantMax:  10 * time.Minute,
		},
		{
			name:     "operator values are preserved",
			cfg:      SyncerConfig{Interval: 2 * time.Minute, MaxInterval: 30 * time.Minute},
			wantBase: 2 * time.Minute,
			wantMax:  30 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewSyncer(nil, nil, nil, tt.cfg)
			if s.cfg.Interval != tt.wantBase {
				t.Errorf("Interval = %s, want %s", s.cfg.Interval, tt.wantBase)
			}
			if s.cfg.MaxInterval != tt.wantMax {
				t.Errorf("MaxInterval = %s, want %s", s.cfg.MaxInterval, tt.wantMax)
			}
			if s.cfg.MaxInterval < s.cfg.Interval {
				t.Errorf("MaxInterval %s is below Interval %s; backoff would be inverted",
					s.cfg.MaxInterval, s.cfg.Interval)
			}
		})
	}
}

func TestSyncOnce_PreservesPreviousSnapshotWhenReloadFails(t *testing.T) {
	ctx := context.Background()
	store, db := setupTestDB(t)

	if err := store.SaveSettings(ctx, syncerTestSettings()); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	envLookup := func(string) (string, bool) { return "", false }
	good, _, err := store.LoadCatalogSnapshot(ctx, envLookup)
	if err != nil {
		t.Fatalf("load initial snapshot: %v", err)
	}

	reg := registry.New()
	reg.Store(good)

	rev, err := store.GetCatalogRevision(ctx)
	if err != nil {
		t.Fatalf("read catalog revision: %v", err)
	}

	s := NewSyncer(nil, store, reg, SyncerConfig{
		EnvLookup: envLookup,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	s.SetInitialRevision(rev)

	// Another writer publishes a catalog this build cannot compile (an unknown
	// protocol) and bumps the revision, so this cycle decides to reload.
	if _, err := db.ExecContext(ctx,
		`UPDATE upstreams SET protocol = 'not-a-protocol' WHERE name = 'openai'`); err != nil {
		t.Fatalf("break catalog: %v", err)
	}
	brokenRev, err := store.BumpCatalogRevision(ctx)
	if err != nil {
		t.Fatalf("bump catalog revision: %v", err)
	}

	err = s.SyncOnce(ctx)
	if err == nil {
		t.Fatal("SyncOnce succeeded on an uncompilable catalog; the reload must fail closed")
	}
	if !strings.Contains(err.Error(), "compile snapshot") {
		t.Fatalf("error = %v, want the snapshot-compile failure", err)
	}
	if reg.Current() != good {
		t.Fatal("the failed reload swapped in a snapshot built from a broken catalog")
	}
	if _, ok := reg.Current().Model("gpt-4o"); !ok {
		t.Fatal("the snapshot left serving no longer resolves gpt-4o")
	}

	// A failed cycle must not advance the revision cursor: once the catalog is
	// readable again the next tick has to apply it without another bump.
	if _, err := db.ExecContext(ctx,
		`UPDATE upstreams SET protocol = 'openai' WHERE name = 'openai'`); err != nil {
		t.Fatalf("repair catalog: %v", err)
	}
	if err := s.SyncOnce(ctx); err != nil {
		t.Fatalf("SyncOnce after repair: %v (failed cycle advanced the revision cursor)", err)
	}

	after := reg.Current()
	if after == good {
		t.Fatal("the repaired catalog was never applied")
	}
	if after.Generation() != uint64(brokenRev) {
		t.Fatalf("generation = %d, want the pending revision %d", after.Generation(), brokenRev)
	}
	if _, ok := after.Model("gpt-4o"); !ok {
		t.Fatal("reloaded snapshot lost gpt-4o")
	}
}
