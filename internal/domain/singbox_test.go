package domain

import "testing"

func TestValidSingboxTag(t *testing.T) {
	cases := []struct {
		tag  string
		want bool
	}{
		{"auto", true},
		{"node-hk-1", true},
		{"Node_1.2", true},
		{"9lives", true},
		{"", false},
		{"-leading", false},
		{".leading", false},
		{"_leading", false},
		{"has space", false},
		{"has:colon", false},
		{"has/slash", false},
		{"  spaced  ", true}, // trimmed before validation
		{string(make([]byte, MaxSingboxTagLen+1)), false},
	}
	for _, tc := range cases {
		if got := ValidSingboxTag(tc.tag); got != tc.want {
			t.Errorf("ValidSingboxTag(%q) = %v, want %v", tc.tag, got, tc.want)
		}
	}
}

func TestValidSingboxTagMaxLength(t *testing.T) {
	exact := make([]byte, MaxSingboxTagLen)
	for i := range exact {
		exact[i] = 'a'
	}
	if !ValidSingboxTag(string(exact)) {
		t.Errorf("tag of exactly %d bytes should be valid", MaxSingboxTagLen)
	}
}

func TestSanitizeSingboxTag(t *testing.T) {
	cases := []struct {
		label string
		want  string
	}{
		{"Hong Kong 01", "Hong-Kong-01"},
		{"node/hk#1", "node-hk-1"},
		{"  ...trim...  ", "trim"},
		{"", ""},
		{"!!!", ""},
		{"--.-", ""},
	}
	for _, tc := range cases {
		if got := SanitizeSingboxTag(tc.label); got != tc.want {
			t.Errorf("SanitizeSingboxTag(%q) = %q, want %q", tc.label, got, tc.want)
		}
	}
}

func TestSanitizeSingboxTagTruncates(t *testing.T) {
	long := make([]byte, MaxSingboxTagLen+20)
	for i := range long {
		long[i] = 'a'
	}
	got := SanitizeSingboxTag(string(long))
	if len(got) != MaxSingboxTagLen {
		t.Fatalf("len = %d, want %d", len(got), MaxSingboxTagLen)
	}
	if !ValidSingboxTag(got) {
		t.Fatalf("truncated tag %q should be valid", got)
	}
}

func TestClampSingboxRefreshIntervalS(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, DefaultSingboxRefreshIntervalS},
		{-5, DefaultSingboxRefreshIntervalS},
		{1, MinSingboxRefreshIntervalS},
		{MinSingboxRefreshIntervalS, MinSingboxRefreshIntervalS},
		{3600, 3600},
		{MaxSingboxRefreshIntervalS, MaxSingboxRefreshIntervalS},
		{MaxSingboxRefreshIntervalS + 1, MaxSingboxRefreshIntervalS},
	}
	for _, tc := range cases {
		if got := ClampSingboxRefreshIntervalS(tc.in); got != tc.want {
			t.Errorf("ClampSingboxRefreshIntervalS(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestSingboxEgressModeConstants(t *testing.T) {
	// The mode literal lives in domain so config, transport, and dashboard
	// compare the same string.
	if EgressModeSingbox != "singbox" {
		t.Fatalf("EgressModeSingbox = %q, want singbox", EgressModeSingbox)
	}
}
