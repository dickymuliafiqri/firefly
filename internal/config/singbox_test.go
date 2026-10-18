package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

func TestLoadSaveSingbox(t *testing.T) {
	tmp := t.TempDir()
	cfg, err := LoadSingbox(tmp)
	if err != nil {
		t.Fatalf("LoadSingbox: %v", err)
	}
	if cfg.SubscriptionURL != "" || cfg.RefreshIntervalS != 0 {
		t.Fatalf("empty file should give zero values: %+v", cfg)
	}

	toSave := SingboxDTO{
		SubscriptionURL:    "https://example.com/subs.txt",
		RefreshIntervalS:   600,
		DefaultOutboundTag: "auto",
	}
	if err := SaveSingbox(tmp, toSave); err != nil {
		t.Fatalf("SaveSingbox: %v", err)
	}
	cfg2, err := LoadSingbox(tmp)
	if err != nil {
		t.Fatalf("LoadSingbox2: %v", err)
	}
	if cfg2.SubscriptionURL != toSave.SubscriptionURL {
		t.Errorf("url = %q", cfg2.SubscriptionURL)
	}
	if cfg2.RefreshIntervalS != 600 {
		t.Errorf("interval = %d", cfg2.RefreshIntervalS)
	}
	if cfg2.DefaultOutboundTag != "auto" {
		t.Errorf("default tag = %q", cfg2.DefaultOutboundTag)
	}
}

func TestSingboxNormalizeDefaultTag(t *testing.T) {
	var d SingboxDTO
	if d.NormalizeDefaultTag() != domain.SingboxDefaultOutboundTag {
		t.Fatalf("expected auto default")
	}
	d.DefaultOutboundTag = "node-1"
	if d.NormalizeDefaultTag() != "node-1" {
		t.Fatalf("expected node-1")
	}
	d.DefaultOutboundTag = "bad tag" // has space
	if d.NormalizeDefaultTag() != domain.SingboxDefaultOutboundTag {
		t.Fatalf("expected auto fallback")
	}
}

func TestSingboxValidate(t *testing.T) {
	d := SingboxDTO{SubscriptionURL: "https://example.com/subs", RefreshIntervalS: 3600, DefaultOutboundTag: "auto"}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	d.SubscriptionURL = "http://example.com/subs"
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	d.SubscriptionURL = "ftp://example.com/subs"
	if err := d.Validate(); err == nil {
		t.Fatal("expected error for non-http(s)")
	}
	d.SubscriptionURL = "https://example.com/subs"
	d.RefreshIntervalS = -1
	if err := d.Validate(); err == nil {
		t.Fatal("expected error for negative interval")
	}
	d.RefreshIntervalS = 0
	d.DefaultOutboundTag = "bad tag"
	if err := d.Validate(); err == nil {
		t.Fatal("expected error for invalid default tag")
	}
}

func TestSingboxMissingFile(t *testing.T) {
	_, err := LoadSingbox(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("LoadSingbox should treat missing file as zero: %v", err)
	}
}

// The strategy field must round-trip through save/load (a partial dashboard
// save cannot silently revert the selection mode) and validate to exactly the
// two supported values.
func TestSingboxStrategyRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	toSave := SingboxDTO{
		SubscriptionURL:    "https://example.com/subs.txt",
		RefreshIntervalS:   600,
		DefaultOutboundTag: "auto",
		Strategy:           domain.SingboxStrategyRoundRobin,
	}
	if err := SaveSingbox(tmp, toSave); err != nil {
		t.Fatalf("SaveSingbox: %v", err)
	}
	cfg, err := LoadSingbox(tmp)
	if err != nil {
		t.Fatalf("LoadSingbox: %v", err)
	}
	if cfg.Strategy != domain.SingboxStrategyRoundRobin {
		t.Errorf("strategy = %q, want round_robin", cfg.Strategy)
	}

	// A legacy file without a strategy field loads as zero, which validates
	// (empty means "use the urltest default").
	if err := os.WriteFile(filepath.Join(tmp, FileNameSingbox), []byte(`{"subscription_url":"https://example.com/s"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	legacy, err := LoadSingbox(tmp)
	if err != nil {
		t.Fatalf("legacy load: %v", err)
	}
	if legacy.Strategy != "" {
		t.Errorf("legacy strategy = %q, want empty", legacy.Strategy)
	}
}

func TestSingboxValidateStrategy(t *testing.T) {
	for _, valid := range []string{"", "urltest", "round_robin", " URLTEST ", "Round_Robin"} {
		d := SingboxDTO{Strategy: valid}
		if err := d.Validate(); err != nil {
			t.Errorf("Strategy %q should validate: %v", valid, err)
		}
	}
	for _, invalid := range []string{"roundrobin", "least_latency", "random"} {
		d := SingboxDTO{Strategy: invalid}
		if err := d.Validate(); err == nil {
			t.Errorf("Strategy %q should be rejected", invalid)
		}
	}
}
