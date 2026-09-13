package limits

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

func TestConcurrencyLimit(t *testing.T) {
	l := New()
	in := AcquireInput{TenantName: "t", RPS: 0, MaxConcurrent: 2} // RPS unlimited

	r1, err := l.Acquire(in)
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	r2, err := l.Acquire(in)
	if err != nil {
		t.Fatalf("acquire 2: %v", err)
	}
	if _, err := l.Acquire(in); err != ErrRateLimited {
		t.Fatalf("3rd acquire = %v, want ErrRateLimited", err)
	}
	r1()
	r3, err := l.Acquire(in)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	r2()
	r3()
}

func TestReleaseIsIdempotent(t *testing.T) {
	l := New()
	in := AcquireInput{TenantName: "t", MaxConcurrent: 1}
	rel, err := l.Acquire(in)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	rel()
	rel() // must not free a slot twice / panic
	rel2, err := l.Acquire(in)
	if err != nil {
		t.Fatalf("acquire after double release: %v", err)
	}
	rel2()
}

func TestRPSLimit(t *testing.T) {
	l := New()
	// Very low RPS so refill during the test is negligible; burst 1.
	in := AcquireInput{TenantName: "t", RPS: 0.001, Burst: 1}

	if _, err := l.Acquire(in); err != nil {
		t.Fatalf("first acquire should pass: %v", err)
	}
	if _, err := l.Acquire(in); err != ErrRateLimited {
		t.Fatalf("second acquire = %v, want ErrRateLimited", err)
	}
}

func TestReloadDoesNotOverAdmitInFlight(t *testing.T) {
	l := New()
	base := AcquireInput{TenantName: "t", MaxConcurrent: 2} // RPS unlimited

	// Fill the cap.
	r1, err := l.Acquire(base)
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	r2, err := l.Acquire(base)
	if err != nil {
		t.Fatalf("acquire 2: %v", err)
	}

	// Reload with the SAME maxConcurrent while two are in flight. Before the fix
	// this built a fresh empty semaphore channel, so the next two acquires would
	// have been admitted on top of the two in-flight = 4 concurrent, exceeding
	// the cap of 2. The reload-stable counter must keep rejecting.
	rel, err := l.Acquire(AcquireInput{TenantName: "t", MaxConcurrent: 2})
	if err != ErrRateLimited {
		if err == nil {
			rel()
		}
		t.Fatalf("reload over-admitted: 3rd acquire = %v, want ErrRateLimited", err)
	}

	// Raising the cap must actually take effect, but exactly to the new cap.
	r3, err := l.Acquire(AcquireInput{TenantName: "t", MaxConcurrent: 3})
	if err != nil {
		t.Fatalf("acquire under raised cap: %v", err)
	}
	if _, err := l.Acquire(AcquireInput{TenantName: "t", MaxConcurrent: 3}); err != ErrRateLimited {
		t.Fatalf("raised cap over-admitted: got %v, want ErrRateLimited", err)
	}

	// Releases must decrement the shared counter so capacity frees up.
	r1()
	if r4, err := l.Acquire(AcquireInput{TenantName: "t", MaxConcurrent: 3}); err != nil {
		t.Fatalf("acquire after release post-reload: %v", err)
	} else {
		r4()
	}
	r2()
	r3()
}

func TestPolicyChangePreservesBucketOnReload(t *testing.T) {
	l := New()
	in := AcquireInput{TenantName: "t", RPS: 0.001, Burst: 1}

	if _, err := l.Acquire(in); err != nil {
		t.Fatalf("first: %v", err)
	}
	// Same policy: bucket still empty.
	if _, err := l.Acquire(in); err != ErrRateLimited {
		t.Fatalf("want rate limited, got %v", err)
	}
	// Raise the limit on the same tenant. The bucket is REUSED, and crucially a
	// reload must NOT gift free tokens (otherwise reloading in a loop would be a
	// rate-limit bypass). So the still-indebted bucket keeps rejecting.
	if _, err := l.Acquire(AcquireInput{TenantName: "t", RPS: 100, Burst: 100}); err != ErrRateLimited {
		t.Fatalf("reload must not bypass limit; got %v", err)
	}
	// A fresh tenant with the raised policy is allowed immediately.
	if _, err := l.Acquire(AcquireInput{TenantName: "u", RPS: 100, Burst: 100}); err != nil {
		t.Fatalf("fresh tenant: %v", err)
	}
}

func TestCredentialAggregateLimit(t *testing.T) {
	l := New()

	// Two DIFFERENT tenants share one credential; the aggregate max-concurrent=1
	// must block the second tenant even though each tenant is unbounded.
	base := AcquireInput{
		RPS: 0, MaxConcurrent: 0,
		CredentialRef: "OPENAI_KEY", CredentialMaxConcurrent: 1,
	}
	a := base
	a.TenantName = "alpha"
	b := base
	b.TenantName = "beta"

	relA, err := l.Acquire(a)
	if err != nil {
		t.Fatalf("tenant alpha: %v", err)
	}
	if _, err := l.Acquire(b); err != ErrRateLimited {
		t.Fatalf("tenant beta should hit aggregate limit, got %v", err)
	}
	relA()
	if _, err := l.Acquire(b); err != nil {
		t.Fatalf("tenant beta after release should pass: %v", err)
	}
}

func TestCredentialRollbackOnFailure(t *testing.T) {
	l := New()

	// Tenant concurrency cap 1: second acquire fails on tenant and must NOT leak
	// a credential slot.
	in := AcquireInput{TenantName: "t", MaxConcurrent: 1, CredentialRef: "K", CredentialMaxConcurrent: 5}
	rel, err := l.Acquire(in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := l.Acquire(in); err != ErrRateLimited {
		t.Fatalf("second should be tenant-limited: %v", err)
	}
	rel()
	// Credential semaphore must be back to 0 used; we can still acquire.
	if _, err := l.Acquire(in); err != nil {
		t.Fatalf("after release: %v", err)
	}
}

func TestConcurrentAcquireRelease(t *testing.T) {
	l := New()
	in := AcquireInput{TenantName: "t", RPS: 0, MaxConcurrent: 8}

	var inflight, maxSeen atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := l.Acquire(in)
			if err != nil {
				return // expected when saturated
			}
			defer rel()
			n := inflight.Add(1)
			for {
				m := maxSeen.Load()
				if n <= m || maxSeen.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			inflight.Add(-1)
		}()
	}
	wg.Wait()
	if got := maxSeen.Load(); got > 8 {
		t.Fatalf("observed %d concurrent, cap is 8", got)
	}
}

func TestAcquireKeySlot_Basics(t *testing.T) {
	l := New()
	slot := &domain.KeySlot{Ref: "KEY_A", MaxConcurrent: 2}

	r1, err := l.AcquireKeySlot(slot)
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	if slot.Inflight.Load() != 1 {
		t.Fatalf("expected slot inflight 1, got %d", slot.Inflight.Load())
	}
	if l.InflightForKey("KEY_A") != 1 {
		t.Fatalf("expected limiter inflight 1, got %d", l.InflightForKey("KEY_A"))
	}

	r2, err := l.AcquireKeySlot(slot)
	if err != nil {
		t.Fatalf("acquire 2: %v", err)
	}

	// 3rd acquire exceeds MaxConcurrent=2
	_, err = l.AcquireKeySlot(slot)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited, got %v", err)
	}

	// Release 1 and double-release to test idempotency
	r1()
	r1() // must be no-op via sync.OnceFunc

	if slot.Inflight.Load() != 1 {
		t.Fatalf("expected slot inflight 1 after release, got %d", slot.Inflight.Load())
	}

	// Now 3rd acquire succeeds
	r3, err := l.AcquireKeySlot(slot)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}

	r2()
	r3()

	if slot.Inflight.Load() != 0 {
		t.Fatalf("expected slot inflight 0 after all releases, got %d", slot.Inflight.Load())
	}
	if l.InflightForKey("KEY_A") != 0 {
		t.Fatalf("expected limiter inflight 0 after all releases, got %d", l.InflightForKey("KEY_A"))
	}
}

func TestAcquireKeySlot_CooldownAndRevoked(t *testing.T) {
	l := New()

	// Revoked slot
	revokedSlot := &domain.KeySlot{Ref: "REVOKED_KEY"}
	revokedSlot.Revoked.Store(true)
	_, err := l.AcquireKeySlot(revokedSlot)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited for revoked slot, got %v", err)
	}

	// In cooldown slot
	cooldownSlot := &domain.KeySlot{Ref: "COOLDOWN_KEY"}
	cooldownSlot.CooldownUntil.Store(time.Now().Add(10 * time.Second).UnixNano())
	_, err = l.AcquireKeySlot(cooldownSlot)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited for cooldown slot, got %v", err)
	}
}

func TestAcquireKeySlot_ReloadPreservesInflight(t *testing.T) {
	l := New()
	slotV1 := &domain.KeySlot{Ref: "POOL_KEY_1", MaxConcurrent: 2}

	// Fill slotV1 capacity
	r1, err := l.AcquireKeySlot(slotV1)
	if err != nil {
		t.Fatalf("acquire v1.1: %v", err)
	}
	r2, err := l.AcquireKeySlot(slotV1)
	if err != nil {
		t.Fatalf("acquire v1.2: %v", err)
	}

	// Simulate config reload: a brand new *domain.KeySlot struct is allocated with Inflight=0
	slotV2 := &domain.KeySlot{Ref: "POOL_KEY_1", MaxConcurrent: 2}

	// Trying to acquire on slotV2 MUST fail because the reload-stable counter in Limiter
	// remembers the 2 in-flight requests on POOL_KEY_1.
	rel, err := l.AcquireKeySlot(slotV2)
	if !errors.Is(err, ErrRateLimited) {
		if err == nil {
			rel()
		}
		t.Fatalf("expected ErrRateLimited across reload, got %v", err)
	}

	// Release one from the previous generation
	r1()

	// Now slotV2 should succeed
	r3, err := l.AcquireKeySlot(slotV2)
	if err != nil {
		t.Fatalf("acquire v2 after release: %v", err)
	}

	r2()
	r3()
}

func TestLimiterConcurrent(t *testing.T) {
	l := New()

	const (
		numTenants     = 5
		numKeys        = 3
		keyCap         = 6
		tenantCap      = 10
		totalRoutines  = 120
		iterations     = 50
	)

	// Keys shared across tenants
	keys := make([]*domain.KeySlot, numKeys)
	for i := range keys {
		keys[i] = &domain.KeySlot{
			Ref:           fmt.Sprintf("KEY_%d", i),
			MaxConcurrent: keyCap,
		}
	}

	var totalTenantAdmitted atomic.Int64
	var totalKeyAdmitted atomic.Int64
	var keyViolations atomic.Int64

	var wg sync.WaitGroup
	for w := 0; w < totalRoutines; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			tenantName := fmt.Sprintf("tenant_%d", workerID%numTenants)
			key := keys[workerID%numKeys]

			for it := 0; it < iterations; it++ {
				// Tier 1: Tenant admission
				tenantRel, err := l.Acquire(AcquireInput{
					TenantName:    tenantName,
					MaxConcurrent: tenantCap,
				})
				if err != nil {
					continue // over tenant cap
				}
				totalTenantAdmitted.Add(1)

				// Tier 2: Key slot admission
				keyRel, err := l.AcquireKeySlot(key)
				if err != nil {
					tenantRel() // rollback tenant reservation
					continue
				}
				totalKeyAdmitted.Add(1)

				// Check invariant: key inflight must not exceed cap
				inflight := key.Inflight.Load()
				if inflight > int64(keyCap) {
					keyViolations.Add(1)
				}

				// Simulate tiny in-flight duration
				time.Sleep(10 * time.Microsecond)

				// Release both slots
				keyRel()
				tenantRel()

				// Verify double release is idempotent
				keyRel()
				tenantRel()
			}
		}(w)
	}

	wg.Wait()

	if v := keyViolations.Load(); v > 0 {
		t.Fatalf("detected %d concurrency cap violations on keys", v)
	}

	t.Logf("TestLimiterConcurrent finished: tenant admissions=%d, key admissions=%d",
		totalTenantAdmitted.Load(), totalKeyAdmitted.Load())

	// Verify all counters return to 0
	for _, k := range keys {
		if cur := k.Inflight.Load(); cur != 0 {
			t.Errorf("key %s has non-zero final inflight: %d", k.Ref, cur)
		}
		if cur := l.InflightForKey(k.Ref); cur != 0 {
			t.Errorf("key %s limiter has non-zero final inflight: %d", k.Ref, cur)
		}
	}
}

