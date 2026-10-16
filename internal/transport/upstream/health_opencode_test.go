package upstream

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// decodeOpenCodeID unpacks an OpenCode canonical identifier (ses_<26> or msg_<26>)
// into its decoded low-48-bit timestamp field and its 12-bit per-millisecond counter.
func decodeOpenCodeID(id string) (msLow36 int64, counter int64, err error) {
	if len(id) != 30 {
		return 0, 0, fmt.Errorf("invalid id length %d (want 30)", len(id))
	}
	descending := strings.HasPrefix(id, "ses_")
	if !descending && !strings.HasPrefix(id, "msg_") {
		return 0, 0, fmt.Errorf("unknown prefix in %q", id)
	}
	hexPart := id[4:16]
	v, err := strconv.ParseUint(hexPart, 16, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse hex: %w", err)
	}
	const mask48 = 0xFFFFFFFFFFFF
	var raw int64
	if descending {
		raw = int64((^v) & mask48)
	} else {
		raw = int64(v & mask48)
	}
	counter = raw & 0x0FFF
	msLow36 = raw >> 12
	return msLow36, counter, nil
}

func TestOpenCodeID_CounterSequence(t *testing.T) {
	// Generating IDs sequentially within the same millisecond must yield
	// strictly increasing counters starting from 1, matching the official
	// OpenCode CLI client. They must NEVER be random 1…4096 values, which
	// trips OpenCode's edge gate FreeTierError 403.
	now := time.Now().UnixMilli()

	// Direct check against nextOpenCodeIDCounter for deterministic verification.
	c1 := nextOpenCodeIDCounter(now)
	c2 := nextOpenCodeIDCounter(now)
	c3 := nextOpenCodeIDCounter(now)

	if c1 != 1 {
		t.Fatalf("first counter for ms=%d must be 1, got %d", now, c1)
	}
	if c2 != 2 || c3 != 3 {
		t.Fatalf("subsequent counters within ms=%d must increment sequentially (want 2, 3), got %d, %d", now, c2, c3)
	}

	// Moving to the next millisecond must reset the counter back to 1.
	nextMs := now + 1
	cReset := nextOpenCodeIDCounter(nextMs)
	if cReset != 1 {
		t.Fatalf("counter must reset to 1 on a new millisecond (ms=%d), got %d", nextMs, cReset)
	}
}

func TestOpenCodeID_DecodedValuesMatch(t *testing.T) {
	// Verify that GenerateOpenCodeSessionID and GenerateOpenCodeRequestID both
	// produce IDs whose decoded counter is small (<= 10 in a normal loop)
	// and whose timestamp tracks wall-clock time.
	for i := 0; i < 5; i++ {
		sid := GenerateOpenCodeSessionID()
		ms, c, err := decodeOpenCodeID(sid)
		if err != nil {
			t.Fatalf("decode session id %q: %v", sid, err)
		}
		if c < 1 || c > 20 {
			t.Errorf("session id %q has unexpected counter %d (want 1..20)", sid, c)
		}
		nowLow := time.Now().UnixMilli() & 0xFFFFFFFFF
		// Allow small jitter (up to 5 seconds) between generation and check.
		diff := nowLow - ms
		if diff < -1000 || diff > 5000 {
			t.Errorf("session id %q decoded timestamp %d deviates too far from wall clock %d", sid, ms, nowLow)
		}

		rid := GenerateOpenCodeRequestID()
		msR, cR, err := decodeOpenCodeID(rid)
		if err != nil {
			t.Fatalf("decode request id %q: %v", rid, err)
		}
		if cR < 1 || cR > 20 {
			t.Errorf("request id %q has unexpected counter %d (want 1..20)", rid, cR)
		}
		diffR := nowLow - msR
		if diffR < -1000 || diffR > 5000 {
			t.Errorf("request id %q decoded timestamp %d deviates too far from wall clock %d", rid, msR, nowLow)
		}
	}
}

func TestOpenCodeID_ClampAtMaxCounter(t *testing.T) {
	// The free-tier gate validates the counter embedded in a session id
	// ((0xFFFFFFFFFFFF - hex) & 0xFFF) and rejects anything above
	// OpenCodeSessionCounterMax. Under gateway concurrency (>63 IDs in one
	// millisecond) the counter must therefore clamp at that bound rather than
	// carrying into the timestamp field or minting an ID the gate refuses.
	fixedMs := int64(1791895000000)
	openCodeIDCounter.Lock()
	openCodeIDCounter.lastMs = fixedMs
	openCodeIDCounter.counter = OpenCodeSessionCounterMax - 1
	openCodeIDCounter.Unlock()

	c1 := nextOpenCodeIDCounter(fixedMs)
	if c1 != OpenCodeSessionCounterMax {
		t.Fatalf("expected counter to reach %d, got %d", OpenCodeSessionCounterMax, c1)
	}
	c2 := nextOpenCodeIDCounter(fixedMs)
	if c2 != OpenCodeSessionCounterMax {
		t.Fatalf("expected counter to clamp at %d, got %d", OpenCodeSessionCounterMax, c2)
	}

	// The default User-Agent must itself satisfy the gate's end-anchored pattern.
	if !IsOpenCodeUserAgent(OpenCodeUserAgent) {
		t.Fatalf("default User-Agent %q is not accepted by the free-tier gate", OpenCodeUserAgent)
	}
}

func TestIsOpenCodeUserAgent(t *testing.T) {
	accepted := []string{
		"opencode/2.0.29",
		"opencode/1.18.31",
		"opencode/latest/2.0.29/cli",
		"opencode/latest/2.0.29/desktop",
		"OpenCodeClient/1.0 opencode/2.0.29/cli",
		"  opencode/2.0.29  ",
	}
	for _, ua := range accepted {
		if !IsOpenCodeUserAgent(ua) {
			t.Errorf("expected %q to be accepted", ua)
		}
	}
	// The legacy literal carried a trailing ai-sdk/runtime suffix, which breaks
	// the gate's end-anchored match — that is why keyless traffic was answered
	// with 403 FreeTierError even though it started with "opencode/".
	rejected := []string{
		"",
		"opencode/1.18.31 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14",
		"opencode/2.0.29/",
		"opencode/2.0",
		"curl/8.7.1",
		"opencode",
	}
	for _, ua := range rejected {
		if IsOpenCodeUserAgent(ua) {
			t.Errorf("expected %q to be rejected", ua)
		}
	}
}

func TestSetOpenCodeSessionHeaders(t *testing.T) {
	const id = "ses_0fffffffffff00000000000000"
	h := http.Header{}
	SetOpenCodeSessionHeaders(h, id)
	for _, key := range []string{
		OpenCodeSessionIDHeader, "x-opencode-session", "x-session-affinity", "x-session-id",
	} {
		if got := h.Get(key); got != id {
			t.Errorf("header %s = %q, want %q", key, got, id)
		}
	}

	// A missing id must leave the header set untouched instead of writing empty
	// values the gate would read as a malformed attestation.
	empty := http.Header{}
	SetOpenCodeSessionHeaders(empty, "")
	if len(empty) != 0 {
		t.Errorf("expected no headers for an empty id, got %v", empty)
	}
}
