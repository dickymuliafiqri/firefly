// Package limits implements admission control: per-tenant rate limiting (token
// bucket + concurrency cap) and a per-credential aggregate limiter that guards a
// shared upstream key against the collective load of all tenants using it.
//
// Design notes:
//   - Limiters are keyed by stable identity (tenant name / credential ref) and
//     survive config reloads, so a token bucket is not reset on every reload.
//   - Acquire is non-blocking: over-limit requests get ErrRateLimited
//     immediately rather than queueing (protects tail latency and memory).
//   - Release must always be called for a successful Acquire; the returned
//     ReleaseFunc is safe to call exactly once (guarded by sync.Once).
package limits

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"golang.org/x/time/rate"
)

// ErrRateLimited is returned when RPS or concurrency ceilings are exceeded.
var ErrRateLimited = errors.New("rate limit exceeded")

// ReleaseFunc releases a concurrency slot. Call it exactly once per Acquire.
type ReleaseFunc func()

// inflight is a per-identity, reload-stable admission counter. It is the
// AUTHORITATIVE concurrency gate: unlike a buffered channel (which a reload
// replaces with a fresh empty one, transiently over-admitting), this counter
// survives limiter swaps, so in-flight requests are always accounted for even
// as maxConcurrent changes.
type inflight struct {
	n atomic.Int64
}

// tryAcquire attempts to reserve one slot under cap. Returns false if full.
func (f *inflight) tryAcquire(cap int64) bool {
	for {
		cur := f.n.Load()
		if cur >= cap {
			return false
		}
		if f.n.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

// release returns one slot. It never goes below zero even on a double release.
func (f *inflight) release() {
	for {
		cur := f.n.Load()
		if cur <= 0 {
			return
		}
		if f.n.CompareAndSwap(cur, cur-1) {
			return
		}
	}
}

// tenantLimiter holds the per-tenant buckets. Its fields are immutable once
// published: a policy change builds a NEW tenantLimiter and swaps the pointer
// atomically, so acquire() reads it without a lock and without a race.
//
// The concurrency gate is a shared *inflight counter rather than a channel: a
// channel would be recreated (empty) on every maxConcurrent change, so slots
// held by in-flight requests made on the old channel would be invisible to the
// new one — a transient over-admission bug. The counter is shared across
// limiter generations (see tenantLimiter/credLimiter) precisely to avoid that.
type tenantLimiter struct {
	rps    *rate.Limiter // nil when rps <= 0 (unlimited)
	flight *inflight     // nil when maxConcurrent <= 0 (unlimited)
	semCap int           // effective maxConcurrent when flight != nil
}

// Limiter owns all limiter state. It is safe for concurrent use.
type Limiter struct {
	mu sync.RWMutex

	// tenants maps tenant name -> *atomic.Pointer[tenantLimiter]. The map itself
	// is guarded by mu; the pointed-to limiter is read/updated atomically.
	tenants map[string]*atomic.Pointer[tenantLimiter]

	// creds maps credential ref -> *atomic.Pointer[tenantLimiter].
	creds map[string]*atomic.Pointer[tenantLimiter]

	// flight maps identity -> *inflight. These counters are NEVER reset by a
	// reload; they outlive limiter generations so in-flight requests keep being
	// counted as maxConcurrent changes.
	flight map[string]*inflight
}

// New returns an empty Limiter.
func New() *Limiter {
	return &Limiter{
		tenants: make(map[string]*atomic.Pointer[tenantLimiter]),
		creds:   make(map[string]*atomic.Pointer[tenantLimiter]),
		flight:  make(map[string]*inflight),
	}
}

// AcquireInput describes the limits to enforce for one request.
type AcquireInput struct {
	TenantName string

	// Tenant RPS policy.
	RPS           float64
	Burst         int
	MaxConcurrent int

	// Optional per-credential aggregate policy (0 = unbounded).
	CredentialRef           string
	CredentialRPS           float64
	CredentialMaxConcurrent int
}

// AcquireCredential reserves only the per-credential aggregate slot. Handlers
// call this AFTER resolving the target (so the credential ref is exact) and
// AFTER AdmissionMiddleware has taken the tenant slot. Returns a no-op release
// and nil error when the credential has no aggregate policy.
//
// A nil *Limiter means "unlimited": the call is a no-op. This makes the zero
// value usable and keeps a missing limiter from panicking the request path.
func (l *Limiter) AcquireCredential(credentialRef string, rps float64, maxConcurrent int) (ReleaseFunc, error) {
	if l == nil {
		return noopRelease, nil
	}
	if credentialRef == "" || (rps <= 0 && maxConcurrent <= 0) {
		return noopRelease, nil
	}
	cl := l.credLimiter(credentialRef, rps, maxConcurrent)
	return cl.acquire()
}

// AcquireKeySlot reserves capacity for an individual upstream key slot.
// It guards against revoked keys, keys in cooldown, and enforces both RPS
// and concurrency limits (via CAS loops) on the slot.
//
// Concurrency is tracked both on the slot instance itself and via a reload-stable
// counter in the Limiter, preventing transient over-admission during config reloads.
// The returned ReleaseFunc is guaranteed idempotent via sync.OnceFunc.
func (l *Limiter) AcquireKeySlot(slot *domain.KeySlot) (ReleaseFunc, error) {
	if l == nil || slot == nil {
		return noopRelease, nil
	}
	if slot.Revoked.Load() {
		return noopRelease, ErrRateLimited
	}
	if slot.IsInCooldown(time.Now().UnixNano()) {
		return noopRelease, ErrRateLimited
	}

	// 1. Enforce RPS if configured
	if slot.RPS > 0 {
		cl := l.credLimiter(slot.Ref, slot.RPS, 0)
		if cl.rps != nil && !cl.rps.Allow() {
			return noopRelease, ErrRateLimited
		}
	}

	// 2. Enforce Concurrency via CAS loop
	if slot.MaxConcurrent > 0 {
		f := l.flightFor(slot.Ref)
		capVal := int64(slot.MaxConcurrent)
		if !f.tryAcquire(capVal) {
			return noopRelease, ErrRateLimited
		}

		// Slot-level CAS loop
		for {
			cur := slot.Inflight.Load()
			if cur >= capVal {
				f.release()
				return noopRelease, ErrRateLimited
			}
			if slot.Inflight.CompareAndSwap(cur, cur+1) {
				break
			}
		}

		return sync.OnceFunc(func() {
			for {
				cur := slot.Inflight.Load()
				if cur <= 0 {
					break
				}
				if slot.Inflight.CompareAndSwap(cur, cur-1) {
					break
				}
			}
			f.release()
		}), nil
	}

	// No concurrency ceiling, but track in-flight count for metrics and LeastInflight
	f := l.flightFor(slot.Ref)
	f.n.Add(1)
	slot.Inflight.Add(1)

	return sync.OnceFunc(func() {
		for {
			cur := slot.Inflight.Load()
			if cur <= 0 {
				break
			}
			if slot.Inflight.CompareAndSwap(cur, cur-1) {
				break
			}
		}
		f.release()
	}), nil
}

// noopRelease is a shared, allocation-free release for the "nothing reserved"
// cases. It is safe to call any number of times.
func noopRelease() {}

// Acquire reserves tenant capacity, and optionally credential-aggregate capacity
// when CredentialRef/Credential* fields are set.
//
// Routing handlers that resolve the upstream credential from the model should
// NOT set the Credential* fields here (the credential is unknown until after
// ResolveTarget); instead call AcquireCredential once the target is known.
// Setting them is intended for callers/tests that already know the credential.
//
// On success it returns a ReleaseFunc that MUST be called when the request
// finishes. On failure it returns ErrRateLimited and a no-op ReleaseFunc (safe
// to defer unconditionally).
func (l *Limiter) Acquire(in AcquireInput) (ReleaseFunc, error) {
	if l == nil {
		return noopRelease, nil
	}
	rel, err := l.acquireTenant(in)
	if err != nil {
		return noopRelease, err
	}
	credRel, err := l.acquireCredential(in)
	if err != nil {
		rel() // roll back tenant reservation
		return noopRelease, err
	}
	return func() {
		credRel()
		rel()
	}, nil
}

func (l *Limiter) acquireTenant(in AcquireInput) (ReleaseFunc, error) {
	tl := l.tenantLimiter(in.TenantName, in.RPS, in.Burst, in.MaxConcurrent)
	return tl.acquire()
}

func (l *Limiter) acquireCredential(in AcquireInput) (ReleaseFunc, error) {
	if in.CredentialRef == "" || (in.CredentialRPS <= 0 && in.CredentialMaxConcurrent <= 0) {
		return noopRelease, nil
	}
	cl := l.credLimiter(in.CredentialRef, in.CredentialRPS, in.CredentialMaxConcurrent)
	return cl.acquire()
}

// tenantLimiter returns the limiter for name, rebuilding it only if the policy
// changed. Rebuilds preserve an existing token bucket when only the concurrency
// cap changed (and vice versa), so limits do not silently reset on reload.
func (l *Limiter) tenantLimiter(name string, rps float64, burst, maxConcurrent int) *tenantLimiter {
	l.mu.Lock()
	defer l.mu.Unlock()

	slot, ok := l.tenants[name]
	if !ok {
		slot = &atomic.Pointer[tenantLimiter]{}
		l.tenants[name] = slot
	}
	f := l.flightForLocked(name)
	cur := slot.Load()
	if cur == nil {
		cur = &tenantLimiter{}
	}
	next := cur.reconfigure(f, rps, burst, maxConcurrent)
	if next != cur {
		slot.Store(next)
	}
	return next
}

func (l *Limiter) credLimiter(ref string, rps float64, maxConcurrent int) *tenantLimiter {
	l.mu.Lock()
	defer l.mu.Unlock()

	slot, ok := l.creds[ref]
	if !ok {
		slot = &atomic.Pointer[tenantLimiter]{}
		l.creds[ref] = slot
	}
	f := l.flightForLocked(ref)
	cur := slot.Load()
	if cur == nil {
		cur = &tenantLimiter{}
	}
	next := cur.reconfigure(f, rps, 0, maxConcurrent)
	if next != cur {
		slot.Store(next)
	}
	return next
}

// flightForLocked returns the reload-stable in-flight counter for an identity,
// creating it on first use. Caller must hold l.mu.
func (l *Limiter) flightForLocked(identity string) *inflight {
	f, ok := l.flight[identity]
	if !ok {
		f = &inflight{}
		l.flight[identity] = f
	}
	return f
}

// flightFor returns the reload-stable in-flight counter for an identity,
// creating it on first use if needed.
func (l *Limiter) flightFor(identity string) *inflight {
	l.mu.RLock()
	f, ok := l.flight[identity]
	l.mu.RUnlock()
	if ok {
		return f
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	return l.flightForLocked(identity)
}

// InflightForKey returns the reload-stable in-flight count for a credential or key ref.
func (l *Limiter) InflightForKey(identity string) int64 {
	if l == nil {
		return 0
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if f, ok := l.flight[identity]; ok {
		return f.n.Load()
	}
	return 0
}

// reconfigure returns a limiter reflecting the desired policy. It returns the
// receiver unchanged when nothing needs to change, so the hot-path pointer is
// stable across no-op reloads.
//
// IMPORTANT: rate limiter fields are NOT mutated in place. A *rate.Limiter is
// itself internally synchronized, but to keep acquire() lock-free we only ever
// swap whole limiter values. When the RPS policy is unchanged we keep the exact
// same *rate.Limiter so bucket state (tokens) persists.
//
// The concurrency gate keeps the caller-supplied *inflight pointer (reload
// stable) and only the *cap* changes; this is what prevents over-admission when
// maxConcurrent is retuned while requests are in flight.
func (tl *tenantLimiter) reconfigure(f *inflight, rps float64, burst, maxConcurrent int) *tenantLimiter {
	wantRPS := rps > 0
	haveRPS := tl.rps != nil
	wantSem := maxConcurrent > 0
	haveSem := tl.flight != nil

	// Fast path: RPS presence unchanged and (no concurrency cap, or the cap is
	// identical). We can reuse the receiver. We only ever mutate the
	// *rate.Limiter internals (internally synchronized), never struct fields, so
	// concurrent acquire() on this pointer is race-free.
	if wantRPS == haveRPS && wantSem == haveSem && (!wantSem || tl.semCap == maxConcurrent) {
		if wantRPS {
			tl.rps.SetLimit(rate.Limit(rps))
			tl.rps.SetBurst(burstOrDefault(burst, rps))
		}
		return tl
	}

	next := &tenantLimiter{}
	if wantRPS {
		if haveRPS {
			next.rps = tl.rps // reuse bucket to preserve tokens
			next.rps.SetLimit(rate.Limit(rps))
			next.rps.SetBurst(burstOrDefault(burst, rps))
		} else {
			next.rps = rate.NewLimiter(rate.Limit(rps), burstOrDefault(burst, rps))
		}
	}
	if wantSem {
		// CRITICAL: reuse the caller-supplied shared counter, NOT a new one.
		next.flight = f
		next.semCap = maxConcurrent
	}
	return next
}

// acquire enforces RPS (non-blocking) then concurrency (non-blocking). It uses
// the real wall clock because rate.Limiter's internal accounting does too.
func (tl *tenantLimiter) acquire() (ReleaseFunc, error) {
	if tl.rps != nil && !tl.rps.Allow() {
		return nil, ErrRateLimited
	}
	if tl.flight != nil {
		if !tl.flight.tryAcquire(int64(tl.semCap)) {
			return nil, ErrRateLimited
		}
	}
	return tl.releaseFunc(), nil
}

func (tl *tenantLimiter) releaseFunc() ReleaseFunc {
	f := tl.flight
	if f == nil {
		return func() {}
	}
	return sync.OnceFunc(f.release)
}

// burstOrDefault returns burst if > 0, else a sensible multiple of rps.
func burstOrDefault(burst int, rps float64) int {
	if burst > 0 {
		return burst
	}
	b := int(rps * 2)
	if b < 1 {
		b = 1
	}
	return b
}
