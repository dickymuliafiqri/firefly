package upstream

import (
	"errors"
	"strings"
	"sync"
	"time"
)

// ErrCircuitOpen is returned when the breaker for an upstream is open and the
// request is rejected without attempting the upstream call.
var ErrCircuitOpen = errors.New("upstream circuit open")

// BreakerState enumerates breaker states for observability.
type BreakerState int

const (
	// StateClosed passes all requests; failures are counted.
	StateClosed BreakerState = iota
	// StateOpen rejects all requests until the cooldown elapses.
	StateOpen
	// StateHalfOpen admits a limited number of probes to test recovery.
	StateHalfOpen
)

func (s BreakerState) String() string {
	switch s {
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "closed"
	}
}

// eventName is the event-type suffix for a state. It deliberately differs from
// String: event types are dotted identifiers, and "breaker.half-open" reads as
// a subtraction to anything parsing the type.
func (s BreakerState) eventName() string {
	switch s {
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half_open"
	default:
		return "closed"
	}
}

// BreakerConfig tunes one breaker.
type BreakerConfig struct {
	// FailureThreshold is the number of consecutive failures that trips open.
	FailureThreshold int
	// SuccessThreshold is the number of consecutive successes in half-open
	// that closes the breaker again.
	SuccessThreshold int
	// Cooldown is how long the breaker stays open before allowing a probe.
	Cooldown time.Duration
	// Now is injectable for tests; defaults to time.Now.
	Now func() time.Time
	// Name identifies the upstream this breaker guards. It is only used for
	// event payloads, so a zero value is harmless.
	Name string
}

func (c BreakerConfig) withDefaults() BreakerConfig {
	if c.FailureThreshold <= 0 {
		c.FailureThreshold = 5
	}
	if c.SuccessThreshold <= 0 {
		c.SuccessThreshold = 2
	}
	if c.Cooldown <= 0 {
		c.Cooldown = 10 * time.Second
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// Breaker is a single circuit breaker. It is safe for concurrent use.
//
// Semantics: a breaker guards UPSTREAM FAILURES, not client errors. Callers
// must classify: Allow() takes a slot; Report(success) records the outcome.
// Only 5xx / transport errors should be reported as failures; 4xx are the
// client's fault and must not trip the breaker.
type Breaker struct {
	cfg BreakerConfig

	mu        sync.Mutex
	state     BreakerState
	failures  int
	successes int
	openedAt  time.Time

	// emitter reports state transitions. Nil means "report nothing", which is
	// the default every existing caller and test runs with.
	emitter EventEmitter
	// deduper suppresses flapping so an oscillating breaker cannot flood the
	// notification channel.
	deduper *eventDeduper
}

// NewBreaker returns a breaker with the given (defaulted) config.
func NewBreaker(cfg BreakerConfig) *Breaker {
	return &Breaker{cfg: cfg.withDefaults(), state: StateClosed}
}

// SetEmitter installs the event emitter for this breaker. Passing nil restores
// the silent default.
func (b *Breaker) SetEmitter(e EventEmitter) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.emitter = e
	if b.deduper == nil {
		b.deduper = newEventDeduper(breakerTransitionWindow)
	}
}

// transition moves to a new state and reports it. The caller must hold b.mu.
// The emit is a non-blocking channel send, so holding the mutex across it is
// safe: a dispatcher with a full queue drops the event rather than parking the
// request path here.
func (b *Breaker) transition(to BreakerState) {
	from := b.state
	b.state = to
	if to == StateOpen {
		b.openedAt = b.cfg.Now()
	}
	if b.emitter != nil && b.deduper != nil {
		emitBreakerTransition(b.emitter, b.deduper, b.cfg.Name, from.String(), to.eventName())
	}
}

// Allow reports whether a request may proceed. It may transition Open->HalfOpen
// when the cooldown has elapsed.
func (b *Breaker) Allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateOpen:
		if b.cfg.Now().Sub(b.openedAt) < b.cfg.Cooldown {
			return ErrCircuitOpen
		}
		// Cooldown elapsed: allow a probe.
		b.transition(StateHalfOpen)
		b.successes = 0
		return nil
	case StateHalfOpen:
		// Let probes through one at a time; concurrency in half-open is
		// bounded by the caller's retry policy in practice. We admit all
		// probes here and rely on Report to converge quickly.
		return nil
	default:
		return nil
	}
}

// Report records the outcome of an allowed request.
func (b *Breaker) Report(ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateClosed:
		if ok {
			b.failures = 0
			return
		}
		b.failures++
		if b.failures >= b.cfg.FailureThreshold {
			b.transition(StateOpen)
		}
	case StateHalfOpen:
		if ok {
			b.successes++
			if b.successes >= b.cfg.SuccessThreshold {
				b.transition(StateClosed)
				b.failures = 0
				b.successes = 0
			}
			return
		}
		// A probe failed: immediately re-open.
		b.transition(StateOpen)
		b.successes = 0
	case StateOpen:
		// Stale report from a request that started before the trip. Refresh
		// the cooldown so we do not flap.
		b.openedAt = b.cfg.Now()
	}
}

// State returns the current state (for metrics/tests).
func (b *Breaker) State() BreakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// ForceState forcibly sets the breaker into a target state, resetting failure counters.
func (b *Breaker) ForceState(state BreakerState) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state = state
	b.failures = 0
	b.successes = 0
	if state == StateOpen {
		b.openedAt = b.cfg.Now()
	}
}

// BreakerRegistry holds one breaker per upstream name.
type BreakerRegistry struct {
	cfg BreakerConfig

	mu       sync.Mutex
	breakers map[string]*Breaker
	// emitter is stamped onto every breaker the registry creates.
	emitter EventEmitter
}

// NewBreakerRegistry returns a registry that stamps new breakers with cfg.
func NewBreakerRegistry(cfg BreakerConfig) *BreakerRegistry {
	return &BreakerRegistry{cfg: cfg.withDefaults(), breakers: make(map[string]*Breaker)}
}

// SetEmitter installs the emitter on every breaker the registry hands out,
// including ones created later by For.
func (r *BreakerRegistry) SetEmitter(e EventEmitter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.emitter = e
	for _, b := range r.breakers {
		b.SetEmitter(e)
	}
}

// For returns the breaker for an upstream name, creating it on first use.
func (r *BreakerRegistry) For(name string) *Breaker {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b, ok := r.breakers[name]; ok {
		return b
	}
	b := NewBreaker(r.cfg)
	if r.emitter != nil {
		b.SetEmitter(r.emitter)
	}
	r.breakers[name] = b
	return b
}

// Allow satisfies openai.BreakerLookup by delegating to the named breaker.
func (r *BreakerRegistry) Allow(name string) error { return r.For(name).Allow() }

// Report satisfies openai.BreakerLookup.
func (r *BreakerRegistry) Report(name string, ok bool) { r.For(name).Report(ok) }

// StateFor returns the current BreakerState for the named upstream, or StateClosed if not yet created.
func (r *BreakerRegistry) StateFor(name string) BreakerState {
	if r == nil {
		return StateClosed
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.breakers[name]
	if !ok {
		return StateClosed
	}
	return b.State()
}

// BreakerStateString returns "closed", "open", or "half-open" for the named upstream.
func (r *BreakerRegistry) BreakerStateString(name string) string {
	return r.StateFor(name).String()
}

// SetState forces the named breaker into the target state.
func (r *BreakerRegistry) SetState(name string, state BreakerState) {
	if r == nil {
		return
	}
	r.For(name).ForceState(state)
}

// AllStates returns a snapshot map of all tracked breaker states.
func (r *BreakerRegistry) AllStates() map[string]BreakerState {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]BreakerState, len(r.breakers))
	for name, b := range r.breakers {
		out[name] = b.State()
	}
	return out
}

// ParseBreakerState parses a string into a BreakerState.
func ParseBreakerState(s string) (BreakerState, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "closed":
		return StateClosed, true
	case "open":
		return StateOpen, true
	case "half-open", "halfopen", "half_open":
		return StateHalfOpen, true
	default:
		return StateClosed, false
	}
}
