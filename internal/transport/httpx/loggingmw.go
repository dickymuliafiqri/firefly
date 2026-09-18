package httpx

import (
	"log/slog"
	"net/http"
	"time"
)

// HTTPMetrics is the subset of metrics the request middleware needs. Keeping it
// an interface (rather than *metrics.Metrics) avoids an import cycle and lets
// tests pass a stub. A nil implementation must be tolerated by callers.
type HTTPMetrics interface {
	ObserveHTTP(method, route string, status int, d time.Duration)
	IncInflight()
	DecInflight()
}

// MetricsMiddleware records per-request latency/status and the in-flight gauge.
// It runs OUTSIDE the mux so that r.Pattern (the route template assigned by
// ServeMux) is populated by the time the request completes; we fall back to a
// coarse label for unmatched routes to avoid unbounded cardinality.
func MetricsMiddleware(m HTTPMetrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if m == nil {
				next.ServeHTTP(w, r)
				return
			}
			start := time.Now()
			m.IncInflight()
			defer m.DecInflight()

			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			route := r.Pattern
			if route == "" {
				route = "unmatched"
			}
			m.ObserveHTTP(r.Method, route, rec.status, time.Since(start))
		})
	}
}

// LoggingMiddleware logs one line per request with method, path, status, bytes,
// duration, and correlation/tenant ids. It wraps the writer in a
// statusRecorder, which also lets RecoverMiddleware detect written headers and
// keeps Flush/Hijack working for streaming.
func LoggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.bytes,
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", RequestIDFrom(r.Context()),
			}
			if t := TenantFrom(r.Context()); t != nil {
				attrs = append(attrs, "tenant", t.Name)
			}
			switch {
			case rec.status >= 500:
				logger.Error("request", attrs...)
			case rec.status >= 400:
				logger.Warn("request", attrs...)
			default:
				logger.Info("request", attrs...)
			}
		})
	}
}

// Chain applies middlewares so that the FIRST listed runs OUTERMOST. This makes
// the intended order explicit and easy to read at the call site.
func Chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}
