package singbox

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

func TestCooldownFor_BackoffCapsAtFiveMinutes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		fails int
		want  time.Duration
	}{
		{0, 5 * time.Second}, // ordinal is clamped to 1
		{1, 5 * time.Second},
		{2, 10 * time.Second},
		{3, 20 * time.Second},
		{4, 40 * time.Second},
		{7, 300 * time.Second}, // 5s<<6 = 320s, capped to 5m
		{50, 300 * time.Second},
	}
	for _, c := range cases {
		if got := cooldownFor(c.fails); got != c.want {
			t.Errorf("cooldownFor(%d) = %v, want %v", c.fails, got, c.want)
		}
	}
}

func TestMarkNodeFailure_CooldownExpiresAndSuccessClears(t *testing.T) {
	t.Parallel()

	m := NewManager(nil)
	m.markNodeFailure("node-a", context.DeadlineExceeded)

	health := m.healthSnapshot()
	h, ok := health["node-a"]
	if !ok {
		t.Fatal("expected node-a to have health state after a failure")
	}
	if h.fails != 1 {
		t.Fatalf("fails = %d, want 1", h.fails)
	}
	if !h.cooling(time.Now()) {
		t.Fatal("node should be cooling immediately after its first failure")
	}
	if h.cooling(time.Now().Add(10 * time.Second)) {
		t.Fatal("5s cooldown should have expired after 10s")
	}

	// A second failure lengthens the backoff: 5s → 10s.
	m.markNodeFailure("node-a", context.DeadlineExceeded)
	health = m.healthSnapshot()
	h = health["node-a"]
	if h.fails != 2 {
		t.Fatalf("fails = %d, want 2", h.fails)
	}
	if !h.cooling(time.Now().Add(8 * time.Second)) {
		t.Fatal("second failure should push the cooldown past 8s (10s window)")
	}

	// A success wipes the entry entirely: no cooldown, no failure history.
	m.markNodeSuccess("node-a")
	health = m.healthSnapshot()
	if _, ok := health["node-a"]; ok {
		t.Fatal("successful dial should delete the node's health entry")
	}
}

func TestNoteNodeOutcome_IgnoresGroupAndUnknownTags(t *testing.T) {
	t.Parallel()

	m := NewManager(nil)
	// No nodes are loaded: auto/select/direct and unknown tags are not nodes,
	// so neither failures nor successes may create health entries for them.
	m.noteNodeOutcome(domain.SingboxDefaultOutboundTag, context.DeadlineExceeded)
	m.noteNodeOutcome(domain.SingboxSelectorOutboundTag, context.DeadlineExceeded)
	m.noteNodeOutcome(domain.SingboxDirectOutboundTag, context.DeadlineExceeded)
	m.noteNodeOutcome("node-not-in-subscription", context.DeadlineExceeded)

	if len(m.healthSnapshot()) != 0 {
		t.Fatalf("group/unknown tags must not accumulate health state, got %v", m.healthSnapshot())
	}
}

func TestPruneHealth_DropsRetiredNodes(t *testing.T) {
	t.Parallel()

	m := NewManager(nil)
	m.markNodeFailure("node-old", context.DeadlineExceeded)
	m.markNodeFailure("node-kept", context.DeadlineExceeded)

	m.pruneHealth([]Node{{Tag: "node-kept"}})

	health := m.healthSnapshot()
	if _, ok := health["node-old"]; ok {
		t.Error("retired node must not keep a cooldown entry")
	}
	if _, ok := health["node-kept"]; !ok {
		t.Error("live node must keep its cooldown entry")
	}
}

func TestStrategy_DefaultsAndNormalizes(t *testing.T) {
	t.Parallel()

	m := NewManager(nil)
	if got := m.Strategy(); got != domain.SingboxStrategyURLTest {
		t.Fatalf("default strategy = %q, want urltest", got)
	}

	m.SetStrategy("ROUND_ROBIN")
	if got := m.Strategy(); got != domain.SingboxStrategyRoundRobin {
		t.Fatalf("strategy after set = %q, want round_robin", got)
	}

	// Invalid input resets to urltest instead of keeping a bogus value.
	m.SetStrategy("least-latency")
	if got := m.Strategy(); got != domain.SingboxStrategyURLTest {
		t.Fatalf("invalid strategy must fall back to urltest, got %q", got)
	}
}

func TestDialRoundRobin_EmptyPoolFailsOpen(t *testing.T) {
	t.Parallel()

	m := NewManager(nil)
	_, _, ok := m.dialRoundRobin(context.Background(), "tcp", "1.2.3.4:443")
	if ok {
		t.Fatal("empty pool must report ok=false so DialContext fails open to direct")
	}
}

func TestStatus_ExposesStrategyAndCooling(t *testing.T) {
	t.Parallel()

	m := NewManager(nil)
	m.SetStrategy(domain.SingboxStrategyRoundRobin)

	m.mu.Lock()
	m.nodes = []Node{{Tag: "node-a", Type: "vless", Server: "a.example.com", Port: 443}}
	m.mu.Unlock()
	m.markNodeFailure("node-a", context.DeadlineExceeded)

	st := m.Status()
	if st.Strategy != domain.SingboxStrategyRoundRobin {
		t.Errorf("strategy = %q, want round_robin", st.Strategy)
	}
	if len(st.Nodes) != 1 {
		t.Fatalf("nodes = %d, want 1", len(st.Nodes))
	}
	if !st.Nodes[0].Cooling {
		t.Error("node under cooldown must be reported as cooling")
	}
	if st.Nodes[0].Failures != 1 {
		t.Errorf("failures = %d, want 1", st.Nodes[0].Failures)
	}
	if st.Nodes[0].CooldownUntil.IsZero() {
		t.Error("cooling node must carry its cooldown expiry")
	}

	// After the success clears the health, the status flips back.
	m.markNodeSuccess("node-a")
	if st := m.Status(); st.Nodes[0].Cooling || st.Nodes[0].Failures != 0 {
		t.Errorf("after success: cooling=%v failures=%d, want false/0",
			st.Nodes[0].Cooling, st.Nodes[0].Failures)
	}
}

// ensure the package's Dialer contract stays satisfied by both Manager and the
// tagged dialer (compile-time assertion, mirrors how the transport uses them).
var (
	_ Dialer = (*Manager)(nil)
	_ Dialer = (*taggedDialer)(nil)
	_        = net.Dialer{}
)
