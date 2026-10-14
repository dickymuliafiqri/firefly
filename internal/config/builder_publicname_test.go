package config

import (
	"strings"
	"testing"
)

// Two upstreams may legitimately expose the same provider model id (`gpt-4o`):
// the catalog keys models by the globally-unique public_name, so each route gets
// its own distinct public name while the provider-side id stays identical.
func TestBuildRoutesSameModelOnDifferentUpstreamsWithDistinctPublicNames(t *testing.T) {
	upstreams := `{"upstreams":[
		{"name":"upstream-a","base_url":"https://a.example/v1"},
		{"name":"upstream-b","base_url":"https://b.example/v1"}
	]}`
	models := `{"models":[
		{"public_name":"gpt-4o","upstream":"upstream-a","upstream_model":"gpt-4o","enabled":true},
		{"public_name":"upstream-b--gpt-4o","upstream":"upstream-b","upstream_model":"gpt-4o","enabled":true}
	]}`

	res, err := Build(FileSet{
		Upstreams: []byte(upstreams),
		Models:    []byte(models),
	}, fakeEnv(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := len(res.Models); got != 2 {
		t.Fatalf("models = %d, want 2", got)
	}
	if got := res.Models["gpt-4o"].Upstream; got != "upstream-a" {
		t.Errorf("gpt-4o upstream = %q, want upstream-a", got)
	}
	if got := res.Models["upstream-b--gpt-4o"].Upstream; got != "upstream-b" {
		t.Errorf("namespaced upstream = %q, want upstream-b", got)
	}
	if got := res.Models["upstream-b--gpt-4o"].UpstreamModel; got != "gpt-4o" {
		t.Errorf("namespaced upstream_model = %q, want gpt-4o", got)
	}
}

// A duplicate public_name stays rejected (it is the gateway-wide identity), but
// the error must name both mappings and point at the namespacing remedy so the
// operator is not left with an index-based complaint.
func TestBuildRejectsDuplicatePublicNameAcrossUpstreams(t *testing.T) {
	upstreams := `{"upstreams":[
		{"name":"upstream-a","base_url":"https://a.example/v1"},
		{"name":"upstream-b","base_url":"https://b.example/v1"}
	]}`
	models := `{"models":[
		{"public_name":"gpt-4o","upstream":"upstream-a","upstream_model":"gpt-4o","enabled":true},
		{"public_name":"gpt-4o","upstream":"upstream-b","upstream_model":"gpt-4o","enabled":true}
	]}`

	_, err := Build(FileSet{
		Upstreams: []byte(upstreams),
		Models:    []byte(models),
	}, fakeEnv(nil))
	if err == nil {
		t.Fatal("expected duplicate public_name error")
	}
	msg := err.Error()
	for _, want := range []string{
		"duplicate public_name",
		`upstream-a`,
		`upstream-b`,
		"upstream-b--gpt-4o",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

func TestSuggestNamespacedPublicName(t *testing.T) {
	cases := []struct {
		upstream string
		name     string
		want     string
	}{
		{"upstream-b", "gpt-4o", "upstream-b--gpt-4o"},
		{"upstream b", "gpt-4o", "upstream-b--gpt-4o"},
		{"upstream.b", "vendor/model", "upstream.b--vendor-model"},
		{"", "", ""},
	}
	for _, tc := range cases {
		if got := suggestNamespacedPublicName(tc.upstream, tc.name); got != tc.want {
			t.Errorf("suggestNamespacedPublicName(%q, %q) = %q, want %q",
				tc.upstream, tc.name, got, tc.want)
		}
	}

	long := suggestNamespacedPublicName(strings.Repeat("a", 200), "gpt-4o")
	if len(long) != 128 {
		t.Errorf("long suggestion length = %d, want 128", len(long))
	}
	if !modelNameRe.MatchString(long) {
		t.Errorf("long suggestion %q does not match modelNameRe", long)
	}
}
