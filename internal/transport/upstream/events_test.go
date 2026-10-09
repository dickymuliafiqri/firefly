package upstream

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/notify"
	"github.com/dickymuliafiqri/firefly/internal/ports"
)

// recordingEmitter captures emitted events for assertion.
type recordingEmitter struct {
	mu     sync.Mutex
	events []notify.Event
}

func (r *recordingEmitter) Emit(ev notify.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recordingEmitter) count(eventType string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, ev := range r.events {
		if ev.Type == eventType {
			n++
		}
	}
	return n
}

func (r *recordingEmitter) first(eventType string) (notify.Event, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ev := range r.events {
		if ev.Type == eventType {
			return ev, true
		}
	}
	return notify.Event{}, false
}

func (r *recordingEmitter) all() []notify.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]notify.Event, len(r.events))
	copy(out, r.events)
	return out
}

func testUpstream() *domain.Upstream {
	return &domain.Upstream{
		Name:              "openai",
		KeyErrorThreshold: 3,
		KeyErrorAction:    string(ports.KeyActionDeactivate),
		KeyRing:           domain.NewKeyRing(domain.KeyStrategyRoundRobin, nil),
	}
}

func TestHandleKeyOutcome_NoEmitterIsSilent(t *testing.T) {
	u := testUpstream()
	slot := &domain.KeySlot{Ref: "sk-openai-1234567890"}
	for i := 0; i < 10; i++ {
		action, failover := HandleKeyOutcome(u, slot, http.StatusTooManyRequests, "30", nil, nil)
		if !failover {
			t.Fatalf("429 must stay failover-eligible")
		}
		_ = action
	}
}

func TestHandleKeyOutcome_CooldownEventCoalesced(t *testing.T) {
	emitter := &recordingEmitter{}
	u := testUpstream()
	slot := &domain.KeySlot{Ref: "sk-openai-abcdefgh"}

	for i := 0; i < 10; i++ {
		HandleKeyOutcome(u, slot, http.StatusTooManyRequests, "30", nil, nil, emitter)
	}

	// A burst of 429s must collapse to one event inside the 30s window.
	if got := emitter.count("key.cooldown"); got != 1 {
		t.Fatalf("cooldown events = %d, want 1 (coalesced)", got)
	}
	ev, ok := emitter.first("key.cooldown")
	if !ok {
		t.Fatal("no cooldown event")
	}
	if ev.Severity != notify.SeverityWarning {
		t.Fatalf("severity = %q", ev.Severity)
	}
	if ev.Data["upstream"] != "openai" {
		t.Fatalf("upstream = %v", ev.Data["upstream"])
	}
	if ev.Data["key_hint"] != "sk***gh" {
		t.Fatalf("key_hint = %v, want a masked hint", ev.Data["key_hint"])
	}
}

func TestHandleKeyOutcome_RevokedEvent(t *testing.T) {
	emitter := &recordingEmitter{}
	u := testUpstream()
	slot := &domain.KeySlot{Ref: "sk-openai-xyzxyz12"}

	HandleKeyOutcome(u, slot, http.StatusUnauthorized, "", nil, nil, emitter)

	if got := emitter.count("key.revoked"); got != 1 {
		t.Fatalf("revoked events = %d, want 1", got)
	}
	ev, _ := emitter.first("key.revoked")
	if ev.Severity != notify.SeverityCritical {
		t.Fatalf("severity = %q", ev.Severity)
	}
	if ev.Data["status"] != 401 {
		t.Fatalf("status = %v", ev.Data["status"])
	}
}

func TestHandleKeyOutcome_ThresholdEvent(t *testing.T) {
	emitter := &recordingEmitter{}
	u := testUpstream()
	u.KeyErrorThreshold = 2
	slot := &domain.KeySlot{Ref: "sk-openai-threshold"}

	HandleKeyOutcome(u, slot, http.StatusForbidden, "", nil, nil, emitter)
	HandleKeyOutcome(u, slot, http.StatusForbidden, "", nil, nil, emitter)

	if got := emitter.count("key.threshold_action"); got != 1 {
		t.Fatalf("threshold events = %d, want 1", got)
	}
	ev, _ := emitter.first("key.threshold_action")
	if ev.Data["action"] != string(ports.KeyActionDeactivate) {
		t.Fatalf("action = %v", ev.Data["action"])
	}
	if ev.Data["upstream"] != "openai" {
		t.Fatalf("upstream = %v", ev.Data["upstream"])
	}
}

func TestHandleKeyOutcome_NoSecretsInPayload(t *testing.T) {
	emitter := &recordingEmitter{}
	u := testUpstream()
	secret := "sk-super-secret-value-do-not-leak"
	slot := &domain.KeySlot{Ref: secret, APIKeyID: 42}

	HandleKeyOutcome(u, slot, http.StatusUnauthorized, "", nil, nil, emitter)
	HandleKeyOutcome(u, slot, http.StatusTooManyRequests, "30", nil, nil, emitter)

	for _, ev := range emitter.all() {
		for _, field := range []string{ev.Subject, ev.Body} {
			if strings.Contains(field, secret) {
				t.Fatalf("secret leaked into %s: %q", ev.Type, field)
			}
		}
		for k, v := range ev.Data {
			s, ok := v.(string)
			if !ok {
				continue
			}
			if strings.Contains(s, secret) {
				t.Fatalf("secret leaked into data[%s] of %s: %q", k, ev.Type, s)
			}
		}
	}
}

func TestKeyHint(t *testing.T) {
	cases := []struct{ in, want string }{
		{"sk-openai-1234567890", "sk***90"},
		{"abcd", "***"},
		{"", "(unknown)"},
		{"   ", "(unknown)"},
	}
	for _, tc := range cases {
		if got := keyHint(tc.in); got != tc.want {
			t.Errorf("keyHint(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEventDeduper_Window(t *testing.T) {
	now := time.Now()
	d := &eventDeduper{last: map[string]time.Time{}, window: time.Minute, lastSweep: now}

	if !d.allow("k") {
		t.Fatal("first event must pass")
	}
	if d.allow("k") {
		t.Fatal("second event inside the window must be suppressed")
	}
	if !d.allow("other") {
		t.Fatal("a different key must not be suppressed")
	}

	// Age the entry past the window.
	d.mu.Lock()
	d.last["k"] = now.Add(-2 * time.Minute)
	d.mu.Unlock()
	if !d.allow("k") {
		t.Fatal("event after the window must pass")
	}
}

func TestEventDeduper_NilIsPassThrough(t *testing.T) {
	var d *eventDeduper
	if !d.allow("k") {
		t.Fatal("a nil deduper must never suppress")
	}
}

func TestEventDeduper_SweepDoesNotGrow(t *testing.T) {
	d := newEventDeduper(50 * time.Millisecond)
	for i := 0; i < 200; i++ {
		d.allow(string(rune('a' + i%26)))
	}
	time.Sleep(80 * time.Millisecond)
	d.allow("trigger-sweep")

	d.mu.Lock()
	size := len(d.last)
	d.mu.Unlock()
	if size > 30 {
		t.Fatalf("deduper map grew to %d entries; the sweep is not reclaiming", size)
	}
}

func TestBreaker_EmitsTransitions(t *testing.T) {
	emitter := &recordingEmitter{}
	b := NewBreaker(BreakerConfig{
		FailureThreshold: 2,
		SuccessThreshold: 1,
		Cooldown:         time.Millisecond,
		Name:             "openai",
	})
	b.SetEmitter(emitter)

	b.Report(false)
	if got := emitter.count("breaker.open"); got != 0 {
		t.Fatalf("opened too early: %d", got)
	}
	b.Report(false)
	if got := emitter.count("breaker.open"); got != 1 {
		t.Fatalf("breaker.open events = %d, want 1", got)
	}

	// Cooldown elapses on the next Allow.
	time.Sleep(5 * time.Millisecond)
	if err := b.Allow(); err != nil {
		t.Fatalf("probe should be admitted: %v", err)
	}
	if got := emitter.count("breaker.half_open"); got != 1 {
		t.Fatalf("breaker.half_open events = %d, want 1", got)
	}

	b.Report(true)
	if got := emitter.count("breaker.closed"); got != 1 {
		t.Fatalf("breaker.closed events = %d, want 1", got)
	}

	ev, _ := emitter.first("breaker.open")
	if ev.Severity != notify.SeverityCritical {
		t.Fatalf("open severity = %q, want critical", ev.Severity)
	}
	if ev.Data["upstream"] != "openai" {
		t.Fatalf("upstream = %v", ev.Data["upstream"])
	}
	if ev.Data["to"] != "open" {
		t.Fatalf("to = %v", ev.Data["to"])
	}
}

func TestBreaker_FlappingIsDeduplicated(t *testing.T) {
	emitter := &recordingEmitter{}
	b := NewBreaker(BreakerConfig{
		FailureThreshold: 1,
		SuccessThreshold: 1,
		Cooldown:         time.Millisecond,
		Name:             "openai",
	})
	b.SetEmitter(emitter)

	// Ten open/half-open/closed cycles inside the dedupe window.
	for i := 0; i < 10; i++ {
		b.Report(false) // -> open
		time.Sleep(2 * time.Millisecond)
		_ = b.Allow()  // -> half_open
		b.Report(true) // -> closed
	}

	if got := emitter.count("breaker.open"); got != 1 {
		t.Fatalf("breaker.open events = %d, want 1 (flapping must be deduplicated)", got)
	}
}

func TestBreaker_NoEmitterIsSilent(t *testing.T) {
	b := NewBreaker(BreakerConfig{FailureThreshold: 1, Name: "openai"})
	b.Report(false)
	if b.State() != StateOpen {
		t.Fatalf("state = %v, want open", b.State())
	}
}

func TestBreakerRegistry_PropagatesEmitter(t *testing.T) {
	emitter := &recordingEmitter{}
	r := NewBreakerRegistry(BreakerConfig{FailureThreshold: 1, Name: "openai"})
	r.SetEmitter(emitter)

	r.For("openai").Report(false)
	if got := emitter.count("breaker.open"); got != 1 {
		t.Fatalf("breaker.open events = %d, want 1", got)
	}
}
