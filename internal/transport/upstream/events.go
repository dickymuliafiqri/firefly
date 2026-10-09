package upstream

import (
	"strings"
	"sync"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/notify"
)

// EventEmitter is the minimal surface this package needs to report lifecycle
// events. It is satisfied by *notify.Dispatcher; a nil emitter means "report
// nothing", which is the default every existing caller and test runs with, so
// adding instrumentation cannot change runtime behaviour on its own.
type EventEmitter interface {
	Emit(notify.Event)
}

// sharedKeyDeduper coalesces key events process-wide. It is a package-level
// singleton because HandleKeyOutcome is a free function called from eight
// adapters; the alternative — threading a deduper through every call site —
// would buy nothing but churn.
var sharedKeyDeduper = newEventDeduper(keyCooldownWindow)

// firstEmitter returns the first non-nil emitter from a variadic list, or nil
// when none was supplied. A nil emitter is the no-op default.
func firstEmitter(emitters []EventEmitter) EventEmitter {
	for _, e := range emitters {
		if e != nil {
			return e
		}
	}
	return nil
}

// keyCooldownWindow coalesces 429 bursts: a key that is rate-limited on every
// request would otherwise emit one event per attempt.
const keyCooldownWindow = 30 * time.Second

// breakerTransitionWindow suppresses flapping. A breaker that opens, half-opens,
// and closes inside a minute is one incident, not three.
const breakerTransitionWindow = 60 * time.Second

// eventDeduper rate-limits events by key. It is deliberately tiny: a map, a
// mutex, and a timestamp per key. The gateway's event volume is low enough
// that a bounded sweep on write is cheaper than a background reaper.
type eventDeduper struct {
	mu        sync.Mutex
	last      map[string]time.Time
	window    time.Duration
	lastSweep time.Time
}

func newEventDeduper(window time.Duration) *eventDeduper {
	return &eventDeduper{last: make(map[string]time.Time), window: window}
}

// allow reports whether an event for key may be emitted now, recording the
// emission when it may.
func (d *eventDeduper) allow(key string) bool {
	if d == nil {
		return true
	}
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()

	// Opportunistic sweep so a long-lived process does not accumulate entries
	// for keys that stopped failing.
	if now.Sub(d.lastSweep) > d.window {
		for k, t := range d.last {
			if now.Sub(t) > d.window {
				delete(d.last, k)
			}
		}
		d.lastSweep = now
	}

	if prev, ok := d.last[key]; ok && now.Sub(prev) < d.window {
		return false
	}
	d.last[key] = now
	return true
}

// keyHint renders a credential reference for an event payload. Only the hint
// ever leaves the process — the same rule the logger follows.
func keyHint(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "(unknown)"
	}
	if len(ref) <= 4 {
		return "***"
	}
	return ref[:2] + "***" + ref[len(ref)-2:]
}

// emitKeyCooldown reports a key entering cooldown after a 429.
func emitKeyCooldown(emitter EventEmitter, deduper *eventDeduper, upstream, ref string, retryAfter string) {
	if emitter == nil || deduper == nil || !deduper.allow("cooldown\x00"+upstream+"\x00"+ref) {
		return
	}
	emitter.Emit(notify.Event{
		Type:     "key.cooldown",
		Severity: notify.SeverityWarning,
		Subject:  "Key entered cooldown",
		Body:     "Upstream " + upstream + " key " + keyHint(ref) + " is rate-limited; retry after " + retryAfter + ".",
		Data: map[string]any{
			"upstream":    upstream,
			"key_hint":    keyHint(ref),
			"retry_after": retryAfter,
		},
	})
}

// emitKeyRevoked reports a key revoked after a 401.
func emitKeyRevoked(emitter EventEmitter, upstream, ref string) {
	if emitter == nil {
		return
	}
	emitter.Emit(notify.Event{
		Type:     "key.revoked",
		Severity: notify.SeverityCritical,
		Subject:  "Key revoked",
		Body:     "Upstream " + upstream + " key " + keyHint(ref) + " was rejected with 401 and is now revoked.",
		Data: map[string]any{
			"upstream": upstream,
			"key_hint": keyHint(ref),
			"status":   401,
		},
	})
}

// emitKeyThreshold reports an automated threshold action (deactivate/delete).
func emitKeyThreshold(emitter EventEmitter, upstream, ref, action, reason string) {
	if emitter == nil {
		return
	}
	emitter.Emit(notify.Event{
		Type:     "key.threshold_action",
		Severity: notify.SeverityCritical,
		Subject:  "Key " + action,
		Body:     "Upstream " + upstream + " key " + keyHint(ref) + " " + action + ": " + reason + ".",
		Data: map[string]any{
			"upstream": upstream,
			"key_hint": keyHint(ref),
			"action":   action,
			"reason":   reason,
		},
	})
}

// emitBreakerTransition reports a circuit breaker state change, deduplicated so
// a flapping breaker cannot flood the channel.
func emitBreakerTransition(emitter EventEmitter, deduper *eventDeduper, upstream, from, to string) {
	if emitter == nil || deduper == nil || !deduper.allow("breaker\x00"+upstream+"\x00"+to) {
		return
	}
	severity := notify.SeverityWarning
	if to == "open" {
		severity = notify.SeverityCritical
	}
	emitter.Emit(notify.Event{
		Type:     "breaker." + to,
		Severity: severity,
		Subject:  "Breaker " + to,
		Body:     "Upstream " + upstream + " circuit breaker moved from " + from + " to " + to + ".",
		Data: map[string]any{
			"upstream": upstream,
			"from":     from,
			"to":       to,
		},
	})
}

// emitHealthFailed reports a health probe failure.
func emitHealthFailed(emitter EventEmitter, upstream, reason string) {
	if emitter == nil {
		return
	}
	emitter.Emit(notify.Event{
		Type:     "upstream.health_failed",
		Severity: notify.SeverityWarning,
		Subject:  "Upstream health check failed",
		Body:     "Upstream " + upstream + " failed its health probe: " + reason + ".",
		Data: map[string]any{
			"upstream": upstream,
			"reason":   reason,
		},
	})
}

// emitHealthRecovered reports a health probe succeeding again.
func emitHealthRecovered(emitter EventEmitter, upstream string) {
	if emitter == nil {
		return
	}
	emitter.Emit(notify.Event{
		Type:     "upstream.health_recovered",
		Severity: notify.SeverityInfo,
		Subject:  "Upstream health check recovered",
		Body:     "Upstream " + upstream + " is healthy again.",
		Data: map[string]any{
			"upstream": upstream,
		},
	})
}
