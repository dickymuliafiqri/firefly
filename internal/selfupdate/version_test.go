package selfupdate

import (
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		ok   bool
		want Version
	}{
		{in: "v1.44.13", ok: true, want: Version{Major: 1, Minor: 44, Patch: 13}},
		{in: "1.44.13", ok: true, want: Version{Major: 1, Minor: 44, Patch: 13}},
		{in: " v2.0.0 ", ok: true, want: Version{Major: 2, Minor: 0, Patch: 0}},
		{in: "v1.44.13-beta.1", ok: true, want: Version{Major: 1, Minor: 44, Patch: 13, Pre: "beta.1"}},
		{in: "v1.44.13+meta", ok: true, want: Version{Major: 1, Minor: 44, Patch: 13}},
		{in: "v3", ok: true, want: Version{Major: 3}},
		{in: "v1.2", ok: true, want: Version{Major: 1, Minor: 2}},
		// A build that never went through a release tag.
		{in: "dev", ok: false},
		{in: "dev-abc1234", ok: false},
		{in: "", ok: false},
		{in: "v1.44.13.7", ok: false},
		{in: "v1.x.0", ok: false},
	}

	for _, tc := range cases {
		got, ok := ParseVersion(tc.in)
		if ok != tc.ok {
			t.Fatalf("ParseVersion(%q) ok = %v, want %v", tc.in, ok, tc.ok)
		}
		if !ok {
			continue
		}
		if got.Major != tc.want.Major || got.Minor != tc.want.Minor || got.Patch != tc.want.Patch || got.Pre != tc.want.Pre {
			t.Fatalf("ParseVersion(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
		if got.Raw != strings.TrimSpace(tc.in) {
			t.Fatalf("ParseVersion(%q).Raw = %q, want %q", tc.in, got.Raw, strings.TrimSpace(tc.in))
		}
	}
}

func TestVersionString(t *testing.T) {
	v, ok := ParseVersion("1.44.13")
	if !ok {
		t.Fatal("ParseVersion failed")
	}
	if got := v.String(); got != "v1.44.13" {
		t.Fatalf("String() = %q, want %q", got, "v1.44.13")
	}

	pre, _ := ParseVersion("v1.44.13-rc.2")
	if got := pre.String(); got != "v1.44.13-rc.2" {
		t.Fatalf("String() = %q, want %q", got, "v1.44.13-rc.2")
	}
}

func TestVersionCompare(t *testing.T) {
	// Ordered from lowest to highest; every adjacent pair must order strictly.
	ordered := []string{
		"v1.44.13-alpha",
		"v1.44.13-alpha.1",
		"v1.44.13-alpha.2",
		"v1.44.13-beta",
		"v1.44.13-beta.2",
		"v1.44.13-beta.10",
		"v1.44.13-rc.1",
		"v1.44.13",
		"v1.44.14",
		"v1.45.0",
		"v2.0.0",
	}

	for i := 0; i < len(ordered); i++ {
		for j := 0; j < len(ordered); j++ {
			a, _ := ParseVersion(ordered[i])
			b, _ := ParseVersion(ordered[j])
			got := a.Compare(b)
			want := compareInt(i, j)
			if got != want {
				t.Fatalf("Compare(%s, %s) = %d, want %d", ordered[i], ordered[j], got, want)
			}
		}
	}
}

func TestIsNewer(t *testing.T) {
	cases := []struct {
		candidate string
		current   string
		want      bool
	}{
		{candidate: "v1.45.0", current: "v1.44.13", want: true},
		{candidate: "v1.44.13", current: "v1.44.13", want: false},
		{candidate: "v1.44.12", current: "v1.44.13", want: false},
		{candidate: "v1.44.13", current: "v1.44.13-beta.1", want: true},
		{candidate: "v1.44.13-beta.1", current: "v1.44.13", want: false},
		// A local build is never newer than a release, so it always updates.
		{candidate: "v1.44.13", current: "dev", want: true},
		{candidate: "v1.44.13", current: "", want: true},
		// A tag that cannot be parsed must not trigger an update.
		{candidate: "nightly", current: "v1.0.0", want: false},
		{candidate: "", current: "v1.0.0", want: false},
	}

	for _, tc := range cases {
		if got := IsNewer(tc.candidate, tc.current); got != tc.want {
			t.Fatalf("IsNewer(%q, %q) = %v, want %v", tc.candidate, tc.current, got, tc.want)
		}
	}
}
