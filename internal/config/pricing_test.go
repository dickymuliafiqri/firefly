package config

import (
	"strings"
	"testing"
)

func TestTranslatePricingValid(t *testing.T) {
	raw := []byte(`{"entries":[
		{"model":"openai/gpt-4o","input_micros_per_m":2500000,"output_micros_per_m":10000000,"source":"models.dev"},
		{"model":"gpt-4o*","input_micros_per_m":100,"output_micros_per_m":200}
	]}`)
	table, err := translatePricing(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if table.Len() != 2 {
		t.Fatalf("len = %d, want 2", table.Len())
	}
	e, ok := table.Lookup("openai/gpt-4o")
	if !ok || e.InputMicrosPerM != 2500000 || e.Source != "models.dev" {
		t.Fatalf("exact entry wrong: %+v ok=%v", e, ok)
	}
	e, ok = table.Lookup("gpt-4o-mini")
	if !ok || e.OutputMicrosPerM != 200 {
		t.Fatalf("wildcard entry wrong: %+v ok=%v", e, ok)
	}
}

func TestTranslatePricingDefaults(t *testing.T) {
	// Missing file and empty file both yield an empty (not nil) table.
	for _, raw := range [][]byte{nil, []byte("  "), []byte("{}")} {
		table, err := translatePricing(raw)
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", raw, err)
		}
		if table == nil || table.Len() != 0 {
			t.Fatalf("expected empty table for %q, got %+v", raw, table)
		}
	}

	// Empty source defaults to manual; whitespace is trimmed.
	table, err := translatePricing([]byte(`{"entries":[{"model":" m ","input_micros_per_m":1}]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	e, ok := table.Lookup("m")
	if !ok {
		t.Fatal("trimmed key must resolve")
	}
	if e.Source != "manual" {
		t.Fatalf("empty source must default to manual, got %q", e.Source)
	}
}

func TestTranslatePricingRejects(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"empty model", `{"entries":[{"model":"","input_micros_per_m":1}]}`, "required"},
		{"negative input", `{"entries":[{"model":"m","input_micros_per_m":-1}]}`, "non-negative"},
		{"negative output", `{"entries":[{"model":"m","output_micros_per_m":-1}]}`, "non-negative"},
		{"negative cache read", `{"entries":[{"model":"m","cache_read_micros_per_m":-1}]}`, "non-negative"},
		{"negative cache write", `{"entries":[{"model":"m","cache_write_micros_per_m":-1}]}`, "non-negative"},
		{"bad source", `{"entries":[{"model":"m","source":"vendor-x"}]}`, "source"},
		{"duplicate key", `{"entries":[{"model":"m"},{"model":"m"}]}`, "duplicate"},
		{"bare wildcard", `{"entries":[{"model":"*"}]}`, "wildcard"},
		{"unknown field", `{"entries":[{"model":"m","price":1}]}`, "unknown field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := translatePricing([]byte(tc.raw))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestNormalizePricing(t *testing.T) {
	if NormalizePricing(nil); false {
		t.Fatal("nil section must be a no-op")
	}

	f := &PricingFile{Entries: []PricingEntryDTO{
		{Model: " zeta ", InputMicrosPerM: 1},
		{Model: "alpha", InputMicrosPerM: 2, Source: " models.dev "},
	}}
	NormalizePricing(f)
	if f.Entries[0].Model != "alpha" {
		t.Fatalf("entries must sort by model, got %q first", f.Entries[0].Model)
	}
	if f.Entries[1].Model != "zeta" {
		t.Fatalf("keys must be trimmed, got %q", f.Entries[1].Model)
	}
	if f.Entries[1].Source != "manual" {
		t.Fatalf("empty source must default to manual, got %q", f.Entries[1].Source)
	}
	if f.Entries[0].Source != "models.dev" {
		t.Fatalf("source must be trimmed, got %q", f.Entries[0].Source)
	}
}

func TestBuildCarriesPricing(t *testing.T) {
	fs := FileSet{
		Upstreams: []byte(`{"upstreams":[{"name":"u","base_url":"https://x/v1","credential_ref":"K"}]}`),
		Models:    []byte(`{"models":[{"public_name":"m","upstream":"u","upstream_model":"m"}]}`),
		Tenants:   []byte(`{"tenants":[{"key_hash":"sha256:` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `","name":"t"}]}`),
		Pricing:   []byte(`{"entries":[{"model":"m","input_micros_per_m":2500,"output_micros_per_m":10000}]}`),
	}
	res, err := Build(fs, func(k string) (string, bool) { return "v", k == "K" })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Pricing == nil || res.Pricing.Len() != 1 {
		t.Fatalf("pricing table missing from build result: %+v", res.Pricing)
	}
	e, ok := res.Pricing.Lookup("m")
	if !ok || e.InputMicrosPerM != 2500 {
		t.Fatalf("pricing entry wrong: %+v ok=%v", e, ok)
	}
}

func TestBuildRejectsBadPricing(t *testing.T) {
	fs := FileSet{
		Upstreams: []byte(`{"upstreams":[{"name":"u","base_url":"https://x/v1","credential_ref":"K"}]}`),
		Models:    []byte(`{"models":[{"public_name":"m","upstream":"u","upstream_model":"m"}]}`),
		Tenants:   []byte(`{"tenants":[{"key_hash":"sha256:` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `","name":"t"}]}`),
		Pricing:   []byte(`{"entries":[{"model":"m","input_micros_per_m":-5}]}`),
	}
	if _, err := Build(fs, func(k string) (string, bool) { return "v", k == "K" }); err == nil {
		t.Fatal("negative price must fail the whole build (fail-closed)")
	}
}
