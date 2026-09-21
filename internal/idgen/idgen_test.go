package idgen

import (
	"regexp"
	"testing"
)

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestUUIDv4_Shape(t *testing.T) {
	u := UUIDv4()
	if !uuidRe.MatchString(u) {
		t.Fatalf("malformed uuid: %q", u)
	}
	// version nibble == 4
	if u[14] != '4' {
		t.Errorf("version nibble = %c want 4 (%q)", u[14], u)
	}
	// variant char in [89ab]
	switch u[19] {
	case '8', '9', 'a', 'b':
	default:
		t.Errorf("variant char = %c want [89ab] (%q)", u[19], u)
	}
	// uniqueness (sanity)
	if UUIDv4() == u {
		t.Error("two UUIDv4 calls returned identical values")
	}
}

func TestShort(t *testing.T) {
	if got := Short(6); len(got) != 12 {
		t.Errorf("Short(6) len=%d want 12", len(got))
	}
	if got := Short(0); len(got) != 12 {
		t.Errorf("Short(0) default len=%d want 12", len(got))
	}
	if got := Short(3); len(got) != 6 {
		t.Errorf("Short(3) len=%d want 6", len(got))
	}
}

func TestUUIDFromSeed_Deterministic(t *testing.T) {
	a := UUIDFromSeed("hello")
	b := UUIDFromSeed("hello")
	if a != b {
		t.Fatalf("not deterministic: %q vs %q", a, b)
	}
	if !uuidRe.MatchString(a) {
		t.Fatalf("malformed: %q", a)
	}
	if a[14] != '4' {
		t.Errorf("default version nibble = %c want 4", a[14])
	}
	if UUIDFromSeed("world") == a {
		t.Error("different seeds produced same uuid")
	}
}

func TestUUIDFromSeedVersion_Antigravity(t *testing.T) {
	// Antigravity pins the version nibble to 0x50 -> '5'.
	u := UUIDFromSeedVersion("antigravity:conversation:sess-1", 0x50)
	if !uuidRe.MatchString(u) {
		t.Fatalf("malformed: %q", u)
	}
	if u[14] != '5' {
		t.Errorf("version nibble = %c want 5 (%q)", u[14], u)
	}
	// Determinism preserved.
	if UUIDFromSeedVersion("antigravity:conversation:sess-1", 0x50) != u {
		t.Error("not deterministic")
	}
}

func TestHash16(t *testing.T) {
	a := Hash16("prefix", "a", "b")
	if len(a) != 16 {
		t.Fatalf("len=%d want 16", len(a))
	}
	if Hash16("prefix", "a", "b") != a {
		t.Error("not deterministic")
	}
	// Separator prevents collisions between ("ab") and ("a","b").
	if Hash16("prefix", "ab") == a {
		t.Error("collision: nul separator not applied")
	}
}
