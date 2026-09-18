package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	npprof "net/http/pprof"
	rpprof "runtime/pprof"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/transport/httpx"
)

// Admin bundles the admin-plane server (/metrics + guarded /debug/*). It is a
// separate listener from the data plane so a flood of metrics scrapes cannot
// starve proxied traffic, and so the debug surface can be firewalled off.
type Admin struct {
	http   *http.Server
	logger *slog.Logger
	grace  time.Duration
}

// NewAdmin builds the admin server. metricsHandler is required (/metrics);
// token guards the sensitive /debug/* routes — when token is empty those routes
// are NOT registered (fail-closed), but /metrics stays available.
// grace optionally configures the shutdown drain timeout.
func NewAdmin(addr, token string, metricsHandler http.Handler, logger *slog.Logger, grace ...time.Duration) *Admin {
	if logger == nil {
		logger = slog.Default()
	}
	mux := http.NewServeMux()

	// /metrics and /healthz are always available on the admin plane.
	if metricsHandler != nil {
		mux.Handle("GET /metrics", metricsHandler)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Sensitive debug endpoints: guarded and only registered when a token is set.
	if token != "" {
		guard := requireToken(token)
		mux.Handle("GET /debug/pprof/", guard(http.HandlerFunc(npprof.Index)))
		mux.Handle("GET /debug/pprof/cmdline", guard(http.HandlerFunc(npprof.Cmdline)))
		mux.Handle("GET /debug/pprof/profile", guard(http.HandlerFunc(npprof.Profile)))
		mux.Handle("GET /debug/pprof/symbol", guard(http.HandlerFunc(npprof.Symbol)))
		mux.Handle("GET /debug/pprof/trace", guard(http.HandlerFunc(npprof.Trace)))
		mux.Handle("GET /debug/goroutines", guard(http.HandlerFunc(goroutineDump)))
	}

	g := DefaultShutdownGrace
	if len(grace) > 0 && grace[0] > 0 {
		g = grace[0]
	}

	return &Admin{
		http: &http.Server{
			Addr:              addr,
			Handler:           httpx.RecoverMiddleware(logger)(mux),
			ReadHeaderTimeout: DefaultReadHeaderTimeout,
			IdleTimeout:       DefaultIdleTimeout,
			// No WriteTimeout: /debug/pprof/profile runs for seconds by design.
		},
		logger: logger,
		grace:  g,
	}
}

// SetShutdownGrace configures the draining timeout for the admin plane on shutdown.
func (a *Admin) SetShutdownGrace(grace time.Duration) {
	if a != nil && grace > 0 {
		a.grace = grace
	}
}

// Handler exposes the admin handler (for tests).
func (a *Admin) Handler() http.Handler { return a.http.Handler }

// Serve runs the admin server until ctx is cancelled, then drains it. Returns
// nil on clean shutdown.
func (a *Admin) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", a.http.Addr)
	if err != nil {
		return err
	}
	return a.ServeOnListener(ctx, ln)
}

// ServeOnListener runs the admin server on a caller-provided listener until ctx
// is cancelled, then drains it within the grace period.
func (a *Admin) ServeOnListener(ctx context.Context, ln net.Listener) error {
	errCh := make(chan error, 1)
	go func() {
		a.logger.Info("admin server listening", "addr", ln.Addr().String(), "path", "/metrics")
		err := a.http.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), a.grace)
		defer cancel()
		if err := a.http.Shutdown(shutdownCtx); err != nil {
			if closeErr := a.http.Close(); closeErr != nil {
				return errors.Join(err, fmt.Errorf("force close: %w", closeErr))
			}
			return err
		}
		return <-errCh
	}
}

// requireToken returns middleware enforcing a constant-time bearer-token check.
// Accepted forms: "Authorization: Bearer <token>" or "X-Admin-Token: <token>".
func requireToken(token string) func(http.Handler) http.Handler {
	want := []byte(token)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := r.Header.Get("X-Admin-Token")
			if got == "" {
				if h := r.Header.Get("Authorization"); len(h) > 7 && h[:7] == "Bearer " {
					got = h[7:]
				}
			}
			if subtle.ConstantTimeCompare([]byte(got), want) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// goroutineDump writes a full goroutine stack dump — the fastest way to spot a
// leak in production (stack count per state). Guarded by the admin token.
func goroutineDump(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	p := rpprof.Lookup("goroutine")
	if p == nil {
		http.Error(w, "goroutine profile not found", http.StatusInternalServerError)
		return
	}
	_ = p.WriteTo(w, 2)
}
