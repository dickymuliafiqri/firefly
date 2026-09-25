package httpx

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/limits"
)

const (
	// DefaultGlobalMaxInflight caps concurrent in-flight requests across the entire server.
	DefaultGlobalMaxInflight = 1500

	// DefaultGlobalWaitTimeout is the maximum time a request will wait in the bounded
	// admission queue when capacity is saturated before failing fast with 429.
	DefaultGlobalWaitTimeout = 1500 * time.Millisecond
)

// GlobalInflightObserver receives notifications when global requests are admitted or finish.
type GlobalInflightObserver interface {
	IncGlobalInflight()
	DecGlobalInflight()
}

// GlobalLimiter manages server-wide in-flight concurrency with a bounded wait queue.
type GlobalLimiter struct {
	sem         chan struct{}
	waitTimeout time.Duration
	maxInflight int
	observer    GlobalInflightObserver
}

// NewGlobalLimiter constructs a GlobalLimiter with the given max concurrent in-flight
// capacity and bounded queue wait duration.
func NewGlobalLimiter(maxInflight int, waitTimeout time.Duration) *GlobalLimiter {
	if maxInflight <= 0 {
		maxInflight = DefaultGlobalMaxInflight
	}
	if waitTimeout <= 0 {
		waitTimeout = DefaultGlobalWaitTimeout
	}
	return &GlobalLimiter{
		sem:         make(chan struct{}, maxInflight),
		waitTimeout: waitTimeout,
		maxInflight: maxInflight,
	}
}

// Inflight returns the current count of acquired in-flight slots.
func (g *GlobalLimiter) Inflight() int {
	return len(g.sem)
}

// Capacity returns the maximum configured in-flight capacity.
func (g *GlobalLimiter) Capacity() int {
	return g.maxInflight
}

// SetObserver configures a metric observer for global in-flight concurrency.
func (g *GlobalLimiter) SetObserver(obs GlobalInflightObserver) {
	if g != nil {
		g.observer = obs
	}
}

// isBypassAdmission checks whether an endpoint should bypass global data-plane admission.
func isBypassAdmission(path string) bool {
	return path == "/healthz" ||
		path == "/" ||
		path == "/overview" ||
		path == "/telemetry" ||
		path == "/usage" ||
		path == "/upstreams" ||
		path == "/providers" ||
		path == "/models" ||
		path == "/tenants" ||
		path == "/settings" ||
		path == "/chat" ||
		path == "/benchmark" ||
		path == "/quota" ||
		path == "/console" ||
		path == "/playground" ||
		strings.HasPrefix(path, "/api/") ||
		strings.HasPrefix(path, "/assets/")
}

// Middleware returns an HTTP middleware that enforces global in-flight concurrency
// limits with bounded waiting.
func (g *GlobalLimiter) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Health checks, internal admin/settings/telemetry APIs, and frontend assets
			// bypass global admission limits so management and monitoring probes are never
			// throttled or counted towards model admission capacity.
			if isBypassAdmission(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			// Fast-path: try to acquire a slot without blocking.
			select {
			case g.sem <- struct{}{}:
				if g.observer != nil {
					g.observer.IncGlobalInflight()
				}
				defer func() {
					if g.observer != nil {
						g.observer.DecGlobalInflight()
					}
					<-g.sem
				}()
				next.ServeHTTP(w, r)
				return
			default:
			}

			// Bounded wait path: slot is full; wait up to waitTimeout or context cancellation.
			timer := time.NewTimer(g.waitTimeout)
			defer timer.Stop()

			select {
			case g.sem <- struct{}{}:
				if g.observer != nil {
					g.observer.IncGlobalInflight()
				}
				defer func() {
					if g.observer != nil {
						g.observer.DecGlobalInflight()
					}
					<-g.sem
				}()
				next.ServeHTTP(w, r)
			case <-r.Context().Done():
				// Client hung up while waiting in queue.
				return
			case <-timer.C:
				// Queue wait timeout expired: fail-fast with 429 and Retry-After: 2.
				w.Header().Set("Retry-After", "2")
				openai.WriteError(w, http.StatusTooManyRequests, openai.TypeRateLimit,
					"server is at capacity; retry shortly")
			}
		})
	}
}

// GlobalAdmissionMiddleware is a convenience constructor for NewGlobalLimiter(maxInflight, waitTimeout).Middleware().
func GlobalAdmissionMiddleware(maxInflight int, waitTimeout time.Duration) func(http.Handler) http.Handler {
	return NewGlobalLimiter(maxInflight, waitTimeout).Middleware()
}

// AdmissionMiddleware enforces per-tenant (and per-credential aggregate) limits
// after authentication. It acquires capacity for the request duration and
// releases on completion, so long streaming responses hold a concurrency slot.
//
// It must be placed after AuthMiddleware (needs the tenant in context) and
// before the handler. Over-limit requests get 429 with a Retry-After header.
func AdmissionMiddleware(lim *limits.Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenant := TenantFrom(r.Context())
			if tenant == nil {
				// Should not happen; AuthMiddleware runs first.
				openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthenticated")
				return
			}

			// Pre-flight Commercial Checks: Expiration & Quota
			now := time.Now().UnixMilli()
			if tenant.IsExpired(now) || tenant.Status == domain.TenantStatusExpired {
				openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication,
					"Your API key has expired. Please renew your plan.")
				return
			}

			if tenant.IsQuotaExceeded() || tenant.Status == domain.TenantStatusExhausted {
				openai.WriteError(w, http.StatusTooManyRequests, "insufficient_quota",
					"You have exceeded your token quota. Please top up your account.")
				return
			}

			// A nil limiter means "unlimited" (mis-wire or explicit opt-out).
			// Tolerating it keeps a config error from panicking the data path;
			// the wiring layer is responsible for supplying a real limiter.
			if lim == nil {
				next.ServeHTTP(w, r)
				return
			}

			// Credential aggregate limiting: the true credential is resolved per
			// target (tenant override, else upstream's ref) and is only known
			// after model routing. At admission time we have two options:
			//   (a) approximate with the tenant's declared override, or
			//   (b) defer aggregate limiting to the handler once the target is
			//       resolved.
			// We choose (b): admission only enforces the tenant-level policy
			// here; the handler (Phase 4) calls Limiter.AcquireCredential after
			// ResolveTarget so the aggregate key is exact. This avoids
			// double-limiting and guarantees the correct shared-key ceiling.
			release, err := lim.Acquire(limits.AcquireInput{
				TenantName:    tenant.Name,
				RPS:           tenant.RateLimit.RPS,
				Burst:         tenant.RateLimit.Burst,
				MaxConcurrent: tenant.RateLimit.MaxConcurrent,
			})
			if err != nil {
				if errors.Is(err, limits.ErrRateLimited) {
					w.Header().Set("Retry-After", "1")
					openai.WriteError(w, http.StatusTooManyRequests, openai.TypeRateLimit,
						"rate limit exceeded for tenant "+tenant.Name)
					return
				}
				openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, "admission control error")
				return
			}
			defer release()

			next.ServeHTTP(w, r)
		})
	}
}
