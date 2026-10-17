package config

import "testing"

// buildAlwaysAvailable builds a single-upstream catalog from the given upstream
// JSON object and returns the built upstream's AlwaysAvailable flag. It fails
// the test on any build error so each case reads as one assertion.
func buildAlwaysAvailable(t *testing.T, upstreamJSON string, env map[string]string) bool {
	t.Helper()
	res, err := Build(FileSet{
		Upstreams: []byte(`{"upstreams":[` + upstreamJSON + `]}`),
		Models:    []byte(`{"models":[]}`),
		Tenants:   []byte(`{"tenants":[]}`),
	}, fakeEnv(env))
	if err != nil {
		t.Fatalf("unexpected build error: %v", err)
	}
	for _, u := range res.Upstreams {
		return u.AlwaysAvailable
	}
	t.Fatal("no upstream built")
	return false
}

// TestBuild_KeylessOpenCodeDefaultsAlwaysAvailable pins the default that fixes
// the reported 429: the OpenCode free tier authenticates with the literal key
// "public", so its quota follows the caller's egress IP, not the credential.
// Cooldowns and revocation are meaningless there — one shared IP exhausts the
// allowance and every subsequent request was answered locally with "all
// upstream credentials exhausted" even though the provider was reachable.
// The upstream is therefore pinned open by default, and only for keyless
// OpenCode: no other protocol, and no OpenCode upstream that carries a key the
// operator paid for, is affected.
func TestBuild_KeylessOpenCodeDefaultsAlwaysAvailable(t *testing.T) {
	if !buildAlwaysAvailable(t, `{"name":"oc","protocol":"opencode"}`, nil) {
		t.Error("a keyless opencode upstream must default to always_available")
	}
	if !buildAlwaysAvailable(t, `{"name":"oc-go","protocol":"opencode-go"}`, nil) {
		t.Error("a keyless opencode-go upstream must default to always_available")
	}
	// A credential of its own means a real, per-account quota: keep closing it.
	if buildAlwaysAvailable(t, `{"name":"oc","protocol":"opencode","credential_ref":"OC_KEY"}`,
		map[string]string{"OC_KEY": "sk-paid"}) {
		t.Error("an opencode upstream with its own key must not default to always_available")
	}
	if buildAlwaysAvailable(t, `{"name":"oc","protocol":"opencode","credential_pool":[{"ref":"OC_KEY"}]}`,
		map[string]string{"OC_KEY": "sk-paid"}) {
		t.Error("an opencode upstream with a credential pool must not default to always_available")
	}
	// Every other protocol keeps the strict behavior even without a key.
	if buildAlwaysAvailable(t, `{"name":"oa","protocol":"openai","base_url":"https://api.openai.com/v1"}`, nil) {
		t.Error("a keyless non-opencode upstream must not default to always_available")
	}
}

// TestBuild_AlwaysAvailableExplicitOverrideWins pins the tri-state contract: the
// operator's explicit choice is honored in both directions, so a paid OpenCode
// upstream can be pinned open and a free-tier one can be closed.
func TestBuild_AlwaysAvailableExplicitOverrideWins(t *testing.T) {
	if buildAlwaysAvailable(t, `{"name":"oc","protocol":"opencode","always_available":false}`, nil) {
		t.Error("an explicit always_available:false must win over the keyless default")
	}
	if !buildAlwaysAvailable(t, `{"name":"oa","protocol":"openai","base_url":"https://api.openai.com/v1","always_available":true}`, nil) {
		t.Error("an explicit always_available:true must win for any protocol")
	}
}
