package upstream

import (
	"context"
	mrand "math/rand"
	"time"
)

// RetryPolicy controls pre-first-byte retries. Retries are ONLY safe before any
// response byte has been written to the client; once we start relaying an SSE
// stream a retry would corrupt the response (duplicate partial data). The relay
// therefore signals "committed" back to the retry loop.
type RetryPolicy struct {
	// MaxAttempts is the total number of tries (1 = no retry).
	MaxAttempts int
	// BaseBackoff is the first backoff delay; doubled each attempt.
	BaseBackoff time.Duration
	// MaxBackoff caps the backoff.
	MaxBackoff time.Duration
	// Jitter returns a random duration in [0, d). It defaults to a global,
	// concurrency-safe source; tests inject a deterministic one.
	Jitter func(d time.Duration) time.Duration
}

// DefaultRetryPolicy is a conservative default: 3 attempts, 100ms base.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts: 3,
		BaseBackoff: 100 * time.Millisecond,
		MaxBackoff:  2 * time.Second,
	}
}

func (p RetryPolicy) normalized() RetryPolicy {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = 1
	}
	if p.BaseBackoff <= 0 {
		p.BaseBackoff = 100 * time.Millisecond
	}
	if p.MaxBackoff <= 0 {
		p.MaxBackoff = 2 * time.Second
	}
	if p.Jitter == nil {
		p.Jitter = defaultJitter
	}
	return p
}

// defaultJitter returns a value in [0, d). The top-level math/rand functions are
// safe for concurrent use; a shared *rand.Rand would NOT be, which matters here
// because many requests back off concurrently.
func defaultJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(mrand.Int63n(int64(d)))
}

// backoff returns the jittered delay before attempt n (1-based, n>=2).
func (p RetryPolicy) backoff(n int) time.Duration {
	d := p.BaseBackoff
	for i := 2; i < n; i++ {
		d *= 2
		if d >= p.MaxBackoff {
			d = p.MaxBackoff
			break
		}
	}
	// Full jitter in [d/2, d] to avoid thundering herds.
	return d/2 + p.Jitter(d/2)
}

// sleepBackoff waits for the backoff before attempt n, honoring ctx cancel.
// Returns false if the context was cancelled during the wait.
func (p RetryPolicy) sleepBackoff(ctx context.Context, n int) bool {
	p = p.normalized()
	d := p.backoff(n)
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Attempts reports the maximum number of tries, satisfying openai.RetryPolicy.
func (p RetryPolicy) Attempts() int { return p.normalized().MaxAttempts }

// Wait blocks for the backoff before attempt n, satisfying openai.RetryPolicy.
func (p RetryPolicy) Wait(ctx context.Context, attempt int) bool {
	return p.normalized().sleepBackoff(ctx, attempt)
}
