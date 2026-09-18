package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

// fakeEnv returns a lookup func backed by a map.
func fakeEnv(vars map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := vars[k]
		return v, ok
	}
}

const validUpstreams = `{
  "upstreams": [
    {"name":"openai-main","base_url":"https://api.openai.com/v1","credential_ref":"OPENAI_KEY"}
  ]
}`

const validModels = `{
  "models": [
    {"public_name":"gpt-4o","upstream":"openai-main","upstream_model":"gpt-4o","enabled":true},
    {"public_name":"gpt-4o-mini","upstream":"openai-main","upstream_model":"gpt-4o-mini","enabled":true},
    {"public_name":"hidden","upstream":"openai-main","upstream_model":"gpt-4o","enabled":false}
  ]
}`

const validTenants = `{
  "tenants": [
    {"key_hash":"sha256:` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `","name":"alpha","allowed_models":["gpt-4o-mini"],
     "rate_limit":{"rps":20,"burst":40,"max_concurrent":20}},
    {"key_hash":"sha256:` + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" + `","name":"internal","allowed_models":["*"]}
  ]
}`

func TestBuildValid(t *testing.T) {
	env := fakeEnv(map[string]string{"OPENAI_KEY": "sk-test"})
	res, err := Build(FileSet{
		Upstreams: []byte(validUpstreams),
		Models:    []byte(validModels),
		Tenants:   []byte(validTenants),
	}, env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := len(res.Upstreams); got != 1 {
		t.Fatalf("upstreams = %d, want 1", got)
	}
	if got := len(res.EnabledModelIDs); got != 2 {
		t.Fatalf("enabled models = %v, want 2 entries", res.EnabledModelIDs)
	}
	// Defaults applied on internal tenant: rps=20, burst=40, maxConcurrent=20.
	internal := res.TenantsByHash["sha256:"+strings.Repeat("b", 64)]
	if internal.RateLimit.RPS != DefaultTenantRPS {
		t.Errorf("default rps = %v, want %v", internal.RateLimit.RPS, DefaultTenantRPS)
	}
	if internal.RateLimit.MaxConcurrent != DefaultTenantMaxConcurrent {
		t.Errorf("default max_concurrent = %v, want %v", internal.RateLimit.MaxConcurrent, DefaultTenantMaxConcurrent)
	}
}

func TestBuildAppliesStreamIdleTimeout(t *testing.T) {
	env := fakeEnv(map[string]string{"OPENAI_KEY": "sk-test"})

	// Default applied when the field is omitted.
	res, err := Build(FileSet{
		Upstreams: []byte(validUpstreams),
		Models:    []byte(validModels),
		Tenants:   []byte(validTenants),
	}, env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := res.Upstreams["openai-main"].StreamIdleTimeoutMs; got != DefaultStreamIdleTimeoutMs {
		t.Errorf("default stream idle = %d, want %d", got, DefaultStreamIdleTimeoutMs)
	}

	// Explicit override respected.
	override := `{"upstreams":[{"name":"openai-main","base_url":"https://api.openai.com/v1","credential_ref":"OPENAI_KEY","stream_idle_timeout_ms":5000}]}`
	res2, err := Build(FileSet{Upstreams: []byte(override), Models: []byte(validModels), Tenants: []byte(validTenants)}, env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := res2.Upstreams["openai-main"].StreamIdleTimeoutMs; got != 5000 {
		t.Errorf("override stream idle = %d, want 5000", got)
	}
}

func TestBuildRejectsMissingEnv(t *testing.T) {
	// OPENAI_KEY not present in env -> upstream credential_ref presence fails.
	_, err := Build(FileSet{
		Upstreams: []byte(validUpstreams),
		Models:    []byte(validModels),
		Tenants:   []byte(validTenants),
	}, fakeEnv(map[string]string{"OTHER": "x"}))
	if err == nil {
		t.Fatal("expected error for missing ENV var")
	}
	if _, ok := err.(*ValidationError); !ok {
		t.Fatalf("want *ValidationError, got %T: %v", err, err)
	}
}

func TestBuildRejectsUnknownField(t *testing.T) {
	bad := `{"upstreams":[{"name":"u1","base_url":"https://x/v1","credential_ref":"K","bogus":1}]}`
	_, err := Build(FileSet{
		Upstreams: []byte(bad),
		Models:    []byte(`{"models":[]}`),
		Tenants:   []byte(`{"tenants":[]}`),
	}, fakeEnv(map[string]string{"K": "v"}))
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
	if _, ok := err.(*ParseError); !ok {
		t.Fatalf("want *ParseError, got %T: %v", err, err)
	}
}

func TestBuildRejectsModelUnknownUpstream(t *testing.T) {
	badModels := `{"models":[{"public_name":"m1","upstream":"nope","upstream_model":"m1"}]}`
	_, err := Build(FileSet{
		Upstreams: []byte(validUpstreams),
		Models:    []byte(badModels),
		Tenants:   []byte(`{"tenants":[]}`),
	}, fakeEnv(map[string]string{"OPENAI_KEY": "sk"}))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if _, ok := err.(*ValidationError); !ok {
		t.Fatalf("want *ValidationError, got %T: %v", err, err)
	}
}

func TestBuildRejectsDuplicateNames(t *testing.T) {
	dup := `{"upstreams":[
	  {"name":"u1","base_url":"https://x/v1","credential_ref":"K"},
	  {"name":"u1","base_url":"https://y/v1","credential_ref":"K"}
	]}`
	_, err := Build(FileSet{
		Upstreams: []byte(dup),
		Models:    []byte(`{"models":[]}`),
		Tenants:   []byte(`{"tenants":[]}`),
	}, fakeEnv(map[string]string{"K": "v"}))
	if err == nil {
		t.Fatal("expected duplicate-name error")
	}
}

func TestBuildRejectsInvalidKeyHash(t *testing.T) {
	badTenants := `{"tenants":[{"key_hash":"deadbeef","name":"t"}]}`
	_, err := Build(FileSet{
		Upstreams: []byte(validUpstreams),
		Models:    []byte(`{"models":[]}`),
		Tenants:   []byte(badTenants),
	}, fakeEnv(map[string]string{"OPENAI_KEY": "sk"}))
	if err == nil {
		t.Fatal("expected invalid key_hash error")
	}
}

func TestBuildRespectsAllowInsecure(t *testing.T) {
	insecure := `{"upstreams":[{"name":"local","base_url":"http://localhost:8080/v1","credential_ref":"K","allow_insecure":true}]}`
	_, err := Build(FileSet{
		Upstreams: []byte(insecure),
		Models:    []byte(`{"models":[]}`),
		Tenants:   []byte(`{"tenants":[]}`),
	}, fakeEnv(map[string]string{"K": "v"}))
	if err != nil {
		t.Fatalf("allow_insecure should permit http: %v", err)
	}
	// Without allow_insecure, http must be rejected.
	insecureNo := `{"upstreams":[{"name":"local","base_url":"http://localhost:8080/v1","credential_ref":"K"}]}`
	_, err = Build(FileSet{
		Upstreams: []byte(insecureNo),
		Models:    []byte(`{"models":[]}`),
		Tenants:   []byte(`{"tenants":[]}`),
	}, fakeEnv(map[string]string{"K": "v"}))
	if err == nil {
		t.Fatal("http without allow_insecure must be rejected")
	}
}

func TestBuildCredentialPoolValid(t *testing.T) {
	cfg := `{"upstreams":[{
		"name":"openai-pool",
		"base_url":"https://api.openai.com/v1",
		"key_strategy":"least_inflight",
		"credential_pool":[
			{"ref":"KEY_1","rps":50,"max_concurrent":10},
			{"ref":"KEY_2","rps":30,"max_concurrent":5}
		]
	}]}`
	env := fakeEnv(map[string]string{"KEY_1": "secret-1", "KEY_2": "secret-2"})
	res, err := Build(FileSet{
		Upstreams: []byte(cfg),
		Models:    []byte(`{"models":[]}`),
		Tenants:   []byte(`{"tenants":[]}`),
	}, env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	u := res.Upstreams["openai-pool"]
	if u.KeyRing == nil {
		t.Fatal("expected KeyRing to be non-nil")
	}
	if u.KeyRing.SlotCount() != 2 {
		t.Fatalf("expected 2 slots, got %d", u.KeyRing.SlotCount())
	}
	if u.KeyStrategy != "least_inflight" {
		t.Fatalf("expected least_inflight, got %s", u.KeyStrategy)
	}
	if u.KeyRing.Slots[0].Ref != "KEY_1" || u.KeyRing.Slots[0].Secret != "secret-1" || u.KeyRing.Slots[0].RPS != 50 || u.KeyRing.Slots[0].MaxConcurrent != 10 {
		t.Fatalf("slot 0 mismatch: %+v", u.KeyRing.Slots[0])
	}
	if u.KeyRing.Slots[1].Ref != "KEY_2" || u.KeyRing.Slots[1].Secret != "secret-2" || u.KeyRing.Slots[1].RPS != 30 || u.KeyRing.Slots[1].MaxConcurrent != 5 {
		t.Fatalf("slot 1 mismatch: %+v", u.KeyRing.Slots[1])
	}
	if u.CredentialRef != "KEY_1" {
		t.Fatalf("expected CredentialRef KEY_1, got %s", u.CredentialRef)
	}
	if u.CredentialRPS != 50 || u.CredentialMaxConcurrent != 10 {
		t.Fatalf("expected primary rps/concurrency on upstream, got rps=%v max_concurrent=%d", u.CredentialRPS, u.CredentialMaxConcurrent)
	}
}

func TestBuildCredentialRefBackwardCompatibility(t *testing.T) {
	cfg := `{"upstreams":[{
		"name":"openai-legacy",
		"base_url":"https://api.openai.com/v1",
		"credential_ref":"LEGACY_KEY",
		"credential_rps":15,
		"credential_max_concurrent":8
	}]}`
	env := fakeEnv(map[string]string{"LEGACY_KEY": "secret-leg"})
	res, err := Build(FileSet{
		Upstreams: []byte(cfg),
		Models:    []byte(`{"models":[]}`),
		Tenants:   []byte(`{"tenants":[]}`),
	}, env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	u := res.Upstreams["openai-legacy"]
	if u.KeyRing == nil {
		t.Fatal("expected KeyRing to be non-nil")
	}
	if u.KeyRing.SlotCount() != 1 {
		t.Fatalf("expected 1 slot, got %d", u.KeyRing.SlotCount())
	}
	slot := u.KeyRing.PrimarySlot()
	if slot.Ref != "LEGACY_KEY" || slot.Secret != "secret-leg" || slot.RPS != 15 || slot.MaxConcurrent != 8 {
		t.Fatalf("slot mismatch: %+v", slot)
	}
	if u.KeyStrategy != "round_robin" {
		t.Fatalf("default strategy should be round_robin, got %s", u.KeyStrategy)
	}
}

func TestBuildAllowsEmptyCredentialPoolAndRef(t *testing.T) {
	// An upstream may be created without any credential material; operators can
	// add keys later. It must build successfully with an empty key ring rather
	// than being rejected.
	cfg := `{"upstreams":[{"name":"u1","base_url":"https://api.openai.com/v1"}]}`
	res, err := Build(FileSet{
		Upstreams: []byte(cfg),
		Models:    []byte(`{"models":[]}`),
		Tenants:   []byte(`{"tenants":[]}`),
	}, fakeEnv(map[string]string{}))
	if err != nil {
		t.Fatalf("expected credential-less upstream to be accepted, got: %v", err)
	}
	u, ok := res.Upstreams["u1"]
	if !ok || u == nil {
		t.Fatalf("upstream u1 not built: %+v", res.Upstreams)
	}
	if u.KeyRing != nil && u.KeyRing.SlotCount() != 0 {
		t.Fatalf("expected empty key ring, got %d slots", u.KeyRing.SlotCount())
	}
}

func TestBuildRejectsMissingEnvInPool(t *testing.T) {
	cfg := `{"upstreams":[{
		"name":"u1",
		"base_url":"https://api.openai.com/v1",
		"credential_pool":[{"ref":"MISSING_KEY"}]
	}]}`
	_, err := Build(FileSet{
		Upstreams: []byte(cfg),
		Models:    []byte(`{"models":[]}`),
		Tenants:   []byte(`{"tenants":[]}`),
	}, fakeEnv(map[string]string{"OTHER": "v"}))
	if err == nil {
		t.Fatal("expected error for missing ENV var in pool")
	}
}

func TestBuildRejectsDuplicateKeyLogInPool(t *testing.T) {
	cfg := `{"upstreams":[{
		"name":"u1",
		"base_url":"https://api.openai.com/v1",
		"credential_pool":[{"ref":"DUP_KEY"},{"ref":"DUP_KEY"}]
	}]}`
	_, err := Build(FileSet{
		Upstreams: []byte(cfg),
		Models:    []byte(`{"models":[]}`),
		Tenants:   []byte(`{"tenants":[]}`),
	}, fakeEnv(map[string]string{"DUP_KEY": "secret"}))
	if err == nil {
		t.Fatal("expected error for duplicate key ref in pool")
	}
}

func TestBuildRejectsInvalidKeyStrategy(t *testing.T) {
	cfg := `{"upstreams":[{
		"name":"u1",
		"base_url":"https://api.openai.com/v1",
		"key_strategy":"random_bogus",
		"credential_ref":"K"
	}]}`
	_, err := Build(FileSet{
		Upstreams: []byte(cfg),
		Models:    []byte(`{"models":[]}`),
		Tenants:   []byte(`{"tenants":[]}`),
	}, fakeEnv(map[string]string{"K": "v"}))
	if err == nil {
		t.Fatal("expected error for invalid key_strategy")
	}
}

func TestBuildCombos_Success(t *testing.T) {
	upstreams := `{"upstreams":[
		{"name":"u1","base_url":"https://u1.com/v1","credential_ref":"K"},
		{"name":"u2","base_url":"https://u2.com/v1","credential_ref":"K"}
	]}`
	models := `{"models":[
		{"public_name":"m1","upstream":"u1","upstream_model":"m1-real"},
		{"public_name":"m2","upstream":"u2","upstream_model":"m2-real"}
	]}`
	combos := `{"combos":[
		{"name":"combo-all","strategy":"least_inflight","models":["m1","m2"],"enabled":true}
	]}`
	res, err := Build(FileSet{
		Upstreams: []byte(upstreams),
		Models:    []byte(models),
		Tenants:   []byte(`{"tenants":[]}`),
		Combos:    []byte(combos),
	}, fakeEnv(map[string]string{"K": "v"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c := res.Combos["combo-all"]
	if c == nil || c.Strategy != domain.RoutingStrategyLeastInflight || len(c.Models) != 2 {
		t.Fatalf("combo = %+v, want least_inflight with 2 models", c)
	}
}

func TestBuildCombos_RejectsUnknownModel(t *testing.T) {
	upstreams := `{"upstreams":[{"name":"u1","base_url":"https://u1.com/v1","credential_ref":"K"}]}`
	models := `{"models":[{"public_name":"m1","upstream":"u1","upstream_model":"m1-real"}]}`
	combos := `{"combos":[{"name":"c1","models":["m1","nonexistent"]}]}`
	_, err := Build(FileSet{
		Upstreams: []byte(upstreams),
		Models:    []byte(models),
		Tenants:   []byte(`{"tenants":[]}`),
		Combos:    []byte(combos),
	}, fakeEnv(map[string]string{"K": "v"}))
	if err == nil {
		t.Fatal("expected error for unknown model in combo")
	}
}

func TestBuildCombos_RejectsNameCollisionWithModel(t *testing.T) {
	upstreams := `{"upstreams":[{"name":"u1","base_url":"https://u1.com/v1","credential_ref":"K"}]}`
	models := `{"models":[{"public_name":"m1","upstream":"u1","upstream_model":"m1-real"}]}`
	combos := `{"combos":[{"name":"m1","models":["m1"]}]}`
	_, err := Build(FileSet{
		Upstreams: []byte(upstreams),
		Models:    []byte(models),
		Tenants:   []byte(`{"tenants":[]}`),
		Combos:    []byte(combos),
	}, fakeEnv(map[string]string{"K": "v"}))
	if err == nil {
		t.Fatal("expected error for combo name collision with model")
	}
}

func TestBuildDirectAPIKey(t *testing.T) {
	cfg := `{"upstreams":[{"name":"u-direct","base_url":"https://api.openai.com/v1","api_key":"sk-direct-secret"}]}`
	res, err := Build(FileSet{
		Upstreams: []byte(cfg),
		Models:    []byte(`{"models":[]}`),
		Tenants:   []byte(`{"tenants":[]}`),
	}, fakeEnv(map[string]string{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	u := res.Upstreams["u-direct"]
	if u.KeyRing == nil || u.KeyRing.SlotCount() != 1 {
		t.Fatalf("want 1 slot, got %v", u.KeyRing)
	}
	if u.KeyRing.Slots[0].Secret != "sk-direct-secret" {
		t.Errorf("want secret sk-direct-secret, got %q", u.KeyRing.Slots[0].Secret)
	}
}

func TestBuildDirectAPIKeys(t *testing.T) {
	cfg := `{"upstreams":[{"name":"u-multi","base_url":"https://api.openai.com/v1","api_keys":["sk-key1","sk-key2"]}]}`
	res, err := Build(FileSet{
		Upstreams: []byte(cfg),
		Models:    []byte(`{"models":[]}`),
		Tenants:   []byte(`{"tenants":[]}`),
	}, fakeEnv(map[string]string{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	u := res.Upstreams["u-multi"]
	if u.KeyRing == nil || u.KeyRing.SlotCount() != 2 {
		t.Fatalf("want 2 slots, got %v", u.KeyRing)
	}
	if u.KeyRing.Slots[0].Secret != "sk-key1" || u.KeyRing.Slots[1].Secret != "sk-key2" {
		t.Errorf("slots secrets mismatch: %+v", u.KeyRing.Slots)
	}
}

func TestBuildMultipleBaseURLs(t *testing.T) {
	cfg := `{"upstreams":[{"name":"u-urls","base_urls":["https://api1.openai.com/v1","https://api2.openai.com/v1"],"api_key":"sk-test"}]}`
	res, err := Build(FileSet{
		Upstreams: []byte(cfg),
		Models:    []byte(`{"models":[]}`),
		Tenants:   []byte(`{"tenants":[]}`),
	}, fakeEnv(map[string]string{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	u := res.Upstreams["u-urls"]
	if len(u.BaseURLs) != 2 {
		t.Fatalf("want 2 BaseURLs, got %v", u.BaseURLs)
	}
	if u.BaseURL != "https://api1.openai.com/v1" {
		t.Errorf("want primary BaseURL https://api1.openai.com/v1, got %q", u.BaseURL)
	}
	if got := u.URLForAttempt(0); got != "https://api1.openai.com/v1" {
		t.Errorf("attempt 0 url = %q", got)
	}
	if got := u.URLForAttempt(1); got != "https://api2.openai.com/v1" {
		t.Errorf("attempt 1 url = %q", got)
	}
	if got := u.URLForAttempt(2); got != "https://api1.openai.com/v1" {
		t.Errorf("attempt 2 url = %q", got)
	}
}

func TestBuildTenantDirectAPIKey(t *testing.T) {
	tenants := `{"tenants":[{"api_key":"sk-gw-secret-token","name":"my-tenant","allowed_models":["*"]}]}`
	res, err := Build(FileSet{
		Upstreams: []byte(`{"upstreams":[]}`),
		Models:    []byte(`{"models":[]}`),
		Tenants:   []byte(tenants),
	}, fakeEnv(map[string]string{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.TenantsByHash) != 1 {
		t.Fatalf("want 1 tenant, got %d", len(res.TenantsByHash))
	}
	for hash, tenant := range res.TenantsByHash {
		if !strings.HasPrefix(hash, "sha256:") {
			t.Errorf("hash does not have sha256: prefix: %q", hash)
		}
		if tenant.Name != "my-tenant" {
			t.Errorf("tenant name = %q, want my-tenant", tenant.Name)
		}
	}
}

func TestBuildEmptyConfigSet(t *testing.T) {
	// Empty or nil files should decode cleanly with 0 upstreams/models/tenants
	res, err := Build(FileSet{
		Upstreams: []byte(""),
		Models:    []byte("{}"),
		Tenants:   nil,
	}, fakeEnv(map[string]string{}))
	if err != nil {
		t.Fatalf("unexpected error for empty FileSet: %v", err)
	}
	if len(res.Upstreams) != 0 || len(res.Models) != 0 || len(res.TenantsByHash) != 0 {
		t.Fatalf("want empty result, got %+v", res)
	}
}

var _ = os.Getenv

func TestEnsureConfigFiles(t *testing.T) {
	dir := t.TempDir()

	// 1. First run: directory has no files -> EnsureConfigFiles creates them
	if err := EnsureConfigFiles(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, name := range []string{FileNameUpstreams, FileNameModels, FileNameTenants} {
		p := filepath.Join(dir, name)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("expected file %s to be created: %v", name, err)
		}
		if !strings.Contains(string(b), "[]") {
			t.Fatalf("expected file %s to have empty array template, got: %s", name, string(b))
		}
	}

	// 2. Modify one file and run EnsureConfigFiles again -> existing file must NOT be overwritten!
	customContent := []byte(`{"upstreams":[{"name":"custom"}]}`)
	_ = os.WriteFile(filepath.Join(dir, FileNameUpstreams), customContent, 0o644)

	if err := EnsureConfigFiles(dir); err != nil {
		t.Fatalf("unexpected error on second run: %v", err)
	}

	after, _ := os.ReadFile(filepath.Join(dir, FileNameUpstreams))
	if string(after) != string(customContent) {
		t.Fatalf("existing file was overwritten! got %s, want %s", string(after), string(customContent))
	}
}

func TestBuild_OAuthProtocolsAndDynamicRefs(t *testing.T) {
	upstreamsJSON := `{
		"upstreams": [
			{
				"name": "cline-upstream",
				"protocol": "cline",
				"base_url": "https://api.cline.bot/api/v1",
				"credential_pool": [
					{"ref": "oauth:cline-mulyono@gmail.com"}
				]
			},
			{
				"name": "antigravity-upstream",
				"protocol": "antigravity",
				"base_url": "https://cloudsandbox-pa.googleapis.com",
				"credential_ref": "oauth:antigravity"
			},
			{
				"name": "codebuddy-upstream",
				"protocol": "codebuddy_cn",
				"base_url": "https://copilot.tencent.com",
				"credential_pool": [
					{"ref": "oauth:codebuddy-1"}
				]
			}
		]
	}`

	modelsJSON := `{"models": []}`
	tenantsJSON := `{"tenants": []}`

	res, err := Build(FileSet{
		Upstreams: []byte(upstreamsJSON),
		Models:    []byte(modelsJSON),
		Tenants:   []byte(tenantsJSON),
	}, fakeEnv(nil))
	if err != nil {
		t.Fatalf("unexpected error building oauth upstreams: %v", err)
	}

	if len(res.Upstreams) != 3 {
		t.Fatalf("expected 3 upstreams, got %d", len(res.Upstreams))
	}

	clineUp := res.Upstreams["cline-upstream"]
	if clineUp == nil {
		t.Fatal("expected cline-upstream to exist")
	}
	if clineUp.Protocol != domain.ProtocolCline {
		t.Errorf("expected protocol 'cline', got %s", clineUp.Protocol)
	}
	if clineUp.KeyRing == nil || len(clineUp.KeyRing.Slots) != 1 {
		t.Fatalf("expected 1 slot in keyring, got %v", clineUp.KeyRing)
	}
	if clineUp.KeyRing.Slots[0].Ref != "oauth:cline-mulyono@gmail.com" {
		t.Errorf("expected ref 'oauth:cline-mulyono@gmail.com', got %s", clineUp.KeyRing.Slots[0].Ref)
	}

	cbUp := res.Upstreams["codebuddy-upstream"]
	if cbUp == nil {
		t.Fatal("expected codebuddy-upstream to exist")
	}
	if cbUp.Protocol != domain.ProtocolCodeBuddyCN {
		t.Errorf("expected normalized protocol 'codebuddy-cn', got %s", cbUp.Protocol)
	}
}

func TestBuild_EgressModeAndProxy(t *testing.T) {
	t.Parallel()

	modelsJSON := `{"models": []}`
	tenantsJSON := `{"tenants": []}`

	// 1. Valid Direct, Warp, and Proxy upstreams
	validJSON := `{
		"upstreams": [
			{
				"name": "u-direct",
				"base_url": "https://api.openai.com/v1",
				"api_key": "sk-test",
				"egress_mode": "direct"
			},
			{
				"name": "u-warp",
				"base_url": "https://opencode.ai/zen/go/v1",
				"api_key": "public",
				"egress_mode": "warp",
				"warp_auto_rotate_on_429": true
			},
			{
				"name": "u-proxy",
				"base_url": "https://api.anthropic.com/v1",
				"api_key": "sk-ant",
				"egress_mode": "proxy",
				"proxy_url": "socks5://127.0.0.1:1080"
			}
		]
	}`

	res, err := Build(FileSet{
		Upstreams: []byte(validJSON),
		Models:    []byte(modelsJSON),
		Tenants:   []byte(tenantsJSON),
	}, fakeEnv(nil))
	if err != nil {
		t.Fatalf("unexpected error building valid egress configs: %v", err)
	}

	uDirect := res.Upstreams["u-direct"]
	if uDirect.EgressMode != "direct" {
		t.Errorf("expected direct egress, got %s", uDirect.EgressMode)
	}

	uWarp := res.Upstreams["u-warp"]
	if uWarp.EgressMode != "warp" || !uWarp.WarpAutoRotateOn429 {
		t.Errorf("expected warp with auto-rotate, got mode=%s autoRotate=%v", uWarp.EgressMode, uWarp.WarpAutoRotateOn429)
	}

	uProxy := res.Upstreams["u-proxy"]
	if uProxy.EgressMode != "proxy" || uProxy.ProxyURL != "socks5://127.0.0.1:1080" {
		t.Errorf("expected proxy with socks5 URL, got mode=%s url=%s", uProxy.EgressMode, uProxy.ProxyURL)
	}

	// 2. Reject invalid egress_mode
	invalidModeJSON := `{
		"upstreams": [
			{"name": "u-bad", "base_url": "https://a.com", "api_key": "k", "egress_mode": "tor"}
		]
	}`
	_, err = Build(FileSet{Upstreams: []byte(invalidModeJSON), Models: []byte(modelsJSON), Tenants: []byte(tenantsJSON)}, fakeEnv(nil))
	if err == nil {
		t.Fatal("expected error for invalid egress_mode 'tor', got nil")
	}

	// 3. Reject proxy without proxy_url
	missingProxyJSON := `{
		"upstreams": [
			{"name": "u-bad", "base_url": "https://a.com", "api_key": "k", "egress_mode": "proxy"}
		]
	}`
	_, err = Build(FileSet{Upstreams: []byte(missingProxyJSON), Models: []byte(modelsJSON), Tenants: []byte(tenantsJSON)}, fakeEnv(nil))
	if err == nil {
		t.Fatal("expected error for missing proxy_url, got nil")
	}

	// 4. Reject invalid scheme for proxy_url
	badSchemeJSON := `{
		"upstreams": [
			{"name": "u-bad", "base_url": "https://a.com", "api_key": "k", "egress_mode": "proxy", "proxy_url": "ftp://127.0.0.1:21"}
		]
	}`
	_, err = Build(FileSet{Upstreams: []byte(badSchemeJSON), Models: []byte(modelsJSON), Tenants: []byte(tenantsJSON)}, fakeEnv(nil))
	if err == nil {
		t.Fatal("expected error for invalid proxy scheme ftp, got nil")
	}
}

func TestBuild_OpenCode_AutoProvisionsPublicKeySlot(t *testing.T) {
	upstreamsJSON := `{
		"upstreams": [
			{
				"name": "opencode-free",
				"protocol": "opencode",
				"base_url": "https://opencode.ai/zen/v1",
				"probe_model": "muse-spark-1.3"
			}
		]
	}`
	modelsJSON := `{
		"models": [
			{
				"public_name": "muse-spark-1.3",
				"upstream": "opencode-free",
				"upstream_model": "muse-spark-1.3"
			}
		]
	}`
	tenantsJSON := `{
		"tenants": [
			{
				"name": "t1",
				"api_key": "sk-t1",
				"allowed_models": ["*"]
			}
		]
	}`

	res, err := Build(FileSet{
		Upstreams: []byte(upstreamsJSON),
		Models:    []byte(modelsJSON),
		Tenants:   []byte(tenantsJSON),
	}, fakeEnv(nil))
	if err != nil {
		t.Fatalf("unexpected error building OpenCode free upstream: %v", err)
	}

	u := res.Upstreams["opencode-free"]
	if u == nil {
		t.Fatal("expected opencode-free upstream")
	}
	if u.KeyRing == nil {
		t.Fatal("expected non-nil KeyRing")
	}
	if len(u.KeyRing.Slots) != 1 {
		t.Fatalf("expected 1 auto-provisioned key slot, got %d", len(u.KeyRing.Slots))
	}
	slot := u.KeyRing.Slots[0]
	if slot.Ref != "opencode-free-public" {
		t.Errorf("expected slot ref 'opencode-free-public', got %s", slot.Ref)
	}
	if slot.Secret != "public" {
		t.Errorf("expected slot secret 'public', got %s", slot.Secret)
	}
	if u.CredentialRef != "opencode-free-public" {
		t.Errorf("expected upstream CredentialRef 'opencode-free-public', got %s", u.CredentialRef)
	}
}
