package config

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

// nestedComboBaseUpstreams and nestedComboBaseModels are the shared fixture for
// the nested-combo build tests: two upstreams, two concrete models.
const nestedComboBaseUpstreams = `{"upstreams":[
	{"name":"u1","base_url":"https://u1.com/v1","credential_ref":"K"},
	{"name":"u2","base_url":"https://u2.com/v1","credential_ref":"K"}
]}`

const nestedComboBaseModels = `{"models":[
	{"public_name":"m1","upstream":"u1","upstream_model":"m1-real"},
	{"public_name":"m2","upstream":"u2","upstream_model":"m2-real"}
]}`

// buildNestedCombos builds a catalog from the given combos JSON against the
// two-model, two-upstream base.
func buildNestedCombos(t *testing.T, combos string) (*BuildResult, error) {
	t.Helper()
	return Build(FileSet{
		Upstreams: []byte(nestedComboBaseUpstreams),
		Models:    []byte(nestedComboBaseModels),
		Tenants:   []byte(`{"tenants":[]}`),
		Combos:    []byte(combos),
	}, fakeEnv(map[string]string{"K": "v"}))
}

// TestBuildCombos_NestedCombosAccepted pins the capability itself: a combo may
// list another combo as a member, and a forward reference to a combo declared
// later in the file must validate too.
func TestBuildCombos_NestedCombosAccepted(t *testing.T) {
	res, err := buildNestedCombos(t, `{"combos":[
		{"name":"outer","strategy":"failover","models":["inner","m2"]},
		{"name":"inner","strategy":"round_robin","models":["m1","m2"]}
	]}`)
	if err != nil {
		t.Fatalf("nested combos must be accepted, got: %v", err)
	}
	if res.Combos["outer"] == nil || res.Combos["inner"] == nil {
		t.Fatalf("both combos must be built, got %v", res.ComboOrder)
	}
	if got := res.Combos["outer"].Models; len(got) != 2 || got[0] != "inner" {
		t.Fatalf("outer members = %v, want [inner m2]", got)
	}
}

// TestBuildCombos_RejectsSelfReference pins that a combo naming itself fails at
// build time instead of at request time.
func TestBuildCombos_RejectsSelfReference(t *testing.T) {
	_, err := buildNestedCombos(t, `{"combos":[{"name":"c1","models":["c1"]}]}`)
	if err == nil {
		t.Fatal("expected error for self-referencing combo")
	}
	if !strings.Contains(err.Error(), "cannot include itself") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestBuildCombos_RejectsIndirectCycle pins two-combo and three-combo cycles.
func TestBuildCombos_RejectsIndirectCycle(t *testing.T) {
	cases := map[string]string{
		"two": `{"combos":[
			{"name":"a","models":["b"]},
			{"name":"b","models":["a"]}
		]}`,
		"three": `{"combos":[
			{"name":"a","models":["b"]},
			{"name":"b","models":["c"]},
			{"name":"c","models":["a"]}
		]}`,
	}
	for name, body := range cases {
		_, err := buildNestedCombos(t, body)
		if err == nil {
			t.Fatalf("%s-cycle: expected error", name)
		}
		if !strings.Contains(err.Error(), "cycle") {
			t.Fatalf("%s-cycle: unexpected error: %v", name, err)
		}
	}
}

// TestBuildCombos_RejectsExcessiveNesting pins the MaxComboDepth ceiling: a
// chain deeper than the limit is rejected at build time, so no snapshot can
// ever hold a graph the resolver has to refuse at request time.
func TestBuildCombos_RejectsExcessiveNesting(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"combos":[`)
	for i := 0; i <= domain.MaxComboDepth; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		next := fmt.Sprintf("c%d", i+1)
		if i == domain.MaxComboDepth {
			next = "m1"
		}
		fmt.Fprintf(&b, `{"name":"c%d","models":["%s"]}`, i, next)
	}
	b.WriteString(`]}`)

	_, err := buildNestedCombos(t, b.String())
	if err == nil {
		t.Fatal("expected error for over-deep combo nesting")
	}
	if !strings.Contains(err.Error(), "maximum depth") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestBuildCombos_AllowsNestingAtTheLimit pins that exactly MaxComboDepth
// levels are accepted, so the ceiling is off-by-one free.
func TestBuildCombos_AllowsNestingAtTheLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"combos":[`)
	for i := 0; i < domain.MaxComboDepth; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		next := fmt.Sprintf("c%d", i+1)
		if i == domain.MaxComboDepth-1 {
			next = "m1"
		}
		fmt.Fprintf(&b, `{"name":"c%d","models":["%s"]}`, i, next)
	}
	b.WriteString(`]}`)

	if _, err := buildNestedCombos(t, b.String()); err != nil {
		t.Fatalf("%d nested levels must be accepted: %v", domain.MaxComboDepth, err)
	}
}

// TestBuildCombos_RejectsUnknownMember pins that a member which is neither a
// model nor a combo is still rejected, now with the widened wording.
func TestBuildCombos_RejectsUnknownMember(t *testing.T) {
	_, err := buildNestedCombos(t, `{"combos":[{"name":"c1","models":["m1","ghost"]}]}`)
	if err == nil {
		t.Fatal("expected error for unknown combo member")
	}
	if !strings.Contains(err.Error(), "unknown model or combo") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestBuildCombos_DisabledComboMayBeMember pins that a disabled combo is still a
// legal member: the resolver skips it at request time, so the graph must build.
func TestBuildCombos_DisabledComboMayBeMember(t *testing.T) {
	res, err := buildNestedCombos(t, `{"combos":[
		{"name":"off","models":["m1"],"enabled":false},
		{"name":"outer","models":["off","m2"]}
	]}`)
	if err != nil {
		t.Fatalf("disabled nested combo must be accepted: %v", err)
	}
	if res.Combos["off"].Enabled {
		t.Fatal("disabled combo must stay disabled")
	}
}

// TestBuildCombos_TenantMayBeAllowedCombo pins that tenant allowed_models may
// still name a combo (existing behaviour preserved by the new validation).
func TestBuildCombos_TenantMayBeAllowedCombo(t *testing.T) {
	tenants := `{"tenants":[{"name":"t1","api_key":"sk-gw-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","allowed_models":["c1"]}]}`
	if _, err := Build(FileSet{
		Upstreams: []byte(nestedComboBaseUpstreams),
		Models:    []byte(nestedComboBaseModels),
		Tenants:   []byte(tenants),
		Combos:    []byte(`{"combos":[{"name":"c1","models":["m1"]}]}`),
	}, fakeEnv(map[string]string{"K": "v"})); err != nil {
		t.Fatalf("tenant allowed_models naming a combo must be accepted: %v", err)
	}
}
