// Package selfupdate lets a running Firefly check GitHub Releases for a newer
// build and replace its own executable in place.
//
// It exists because the alternative — asking an operator to re-run the install
// script on every release — is the reason gateways like this one drift several
// versions behind. The whole feature is deliberately small and boring: one
// upstream source, one artifact per platform, one checksum, one atomic swap.
//
// Three rules shape the implementation:
//
//   - The upstream is fixed. The repository, the API host, and the asset naming
//     are compiled in, because a configurable "update source" is a
//     remote-code-execution vector with a settings page attached. Only tests and
//     forks that call the package directly can point it elsewhere.
//   - Verification always happens. An archive is only allowed near the
//     executable after its SHA-256 matches the digest published in the same
//     release's checksums.txt; a release without that file is not installable.
//   - The process never replaces a file it may not own. A read-only or
//     container-provided filesystem is reported as unavailable up front, with
//     the reason, instead of failing halfway through a download.
package selfupdate

import (
	"strconv"
	"strings"
)

// Version is a parsed semantic version as produced by the release pipeline's
// git tags ("v1.44.13").
//
// A build made outside a release carries something unparseable — "dev",
// "dev-abc1234", or an empty string — which parses as the zero Version with
// ok=false. That is deliberately asymmetric: an unparseable current version
// compares older than every release, so a local build is always told an update
// exists instead of being mistaken for the newest release. The reverse case
// cannot occur, since a candidate version always comes from a release tag.
type Version struct {
	Major int
	Minor int
	Patch int
	// Pre is the pre-release suffix without the leading dash, e.g. "beta.1" in
	// v1.44.13-beta.1. Empty for a final release.
	Pre string
	// Raw is the version string as supplied, with surrounding whitespace removed.
	Raw string
}

// ParseVersion parses "v1.44.13" and "1.44.13", with an optional "-pre"
// suffix and any "+build" metadata. Build metadata is ignored for ordering, as
// SemVer requires. Missing minor/patch components are treated as zero, so a
// major-only tag like "v2" still parses.
func ParseVersion(s string) (Version, bool) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return Version{}, false
	}

	body := strings.TrimPrefix(strings.TrimPrefix(raw, "v"), "V")

	// Build metadata never affects comparison.
	if i := strings.IndexByte(body, '+'); i >= 0 {
		body = body[:i]
	}

	pre := ""
	if i := strings.IndexByte(body, '-'); i >= 0 {
		pre = body[i+1:]
		body = body[:i]
	}

	parts := strings.Split(body, ".")
	if len(parts) > 3 {
		return Version{}, false
	}

	var nums [3]int
	for i, part := range parts {
		if part == "" {
			return Version{}, false
		}
		// A leading zero is only legal for "0" itself under SemVer, but
		// rejecting it would make "01" an error instead of the version it
		// obviously means; the release pipeline never emits one.
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return Version{}, false
		}
		nums[i] = n
	}
	if len(parts) == 0 {
		return Version{}, false
	}

	return Version{
		Major: nums[0],
		Minor: nums[1],
		Patch: nums[2],
		Pre:   pre,
		Raw:   raw,
	}, true
}

// String renders the version in canonical form, always with a leading "v" so it
// compares visually with the release tags the dashboard links to.
func (v Version) String() string {
	out := "v" + strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
	if v.Pre != "" {
		out += "-" + v.Pre
	}
	return out
}

// Compare returns -1 if v sorts before o, 0 if they are equal, and 1 if it
// sorts after. Pre-release ordering follows SemVer: a pre-release sorts below
// the final release of the same version, and identifiers are compared
// field-by-field (numeric identifiers numerically, so beta.2 < beta.10).
func (v Version) Compare(o Version) int {
	if c := compareInt(v.Major, o.Major); c != 0 {
		return c
	}
	if c := compareInt(v.Minor, o.Minor); c != 0 {
		return c
	}
	if c := compareInt(v.Patch, o.Patch); c != 0 {
		return c
	}

	// A release outranks any of its own pre-releases.
	switch {
	case v.Pre == "" && o.Pre == "":
		return 0
	case v.Pre == "":
		return 1
	case o.Pre == "":
		return -1
	}

	a := strings.Split(v.Pre, ".")
	b := strings.Split(o.Pre, ".")
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := comparePrereleaseIdent(a[i], b[i]); c != 0 {
			return c
		}
	}
	// "beta.1" sorts below "beta.1.1": fewer identifiers is smaller.
	return compareInt(len(a), len(b))
}

// comparePrereleaseIdent orders one dotted pre-release identifier. Numeric
// identifiers compare numerically and always rank below alphanumeric ones.
func comparePrereleaseIdent(a, b string) int {
	an, aErr := strconv.Atoi(a)
	bn, bErr := strconv.Atoi(b)
	switch {
	case aErr == nil && bErr == nil:
		return compareInt(an, bn)
	case aErr == nil:
		return -1
	case bErr == nil:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// IsNewer reports whether candidate is a later version than current. An
// unparseable candidate is never newer (protects against a malformed tag
// triggering an update), while an unparseable current version loses to any
// parseable candidate (see Version).
func IsNewer(candidate, current string) bool {
	next, ok := ParseVersion(candidate)
	if !ok {
		return false
	}
	cur, ok := ParseVersion(current)
	if !ok {
		return true
	}
	return next.Compare(cur) > 0
}
