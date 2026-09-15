// Package server wires the HTTP transport: server construction with
// streaming-safe timeouts, route registration, and graceful shutdown.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/openai"
)

// Config holds server-level tuning. Zero values fall back to defaults.
type Config struct {
	Addr string

	// AdminAddr is the listen address for the admin plane (/metrics, /debug/*).
	// Empty disables the admin listener entirely.
	AdminAddr string
	// AdminToken guards the admin plane. When empty, /debug/* endpoints are
	// DISABLED (metrics stays open) — fail-closed for the sensitive endpoints.
	AdminToken string

	// ReadHeaderTimeout bounds how long a client may take to send headers.
	ReadHeaderTimeout time.Duration
	// IdleTimeout bounds keep-alive idle time between requests.
	IdleTimeout time.Duration
	// ShutdownGrace bounds how long in-flight requests may finish on shutdown.
	ShutdownGrace time.Duration

	// NOTE: WriteTimeout is deliberately NOT set on the server. It would cap the
	// total response duration and kill long SSE streams. Streaming-safe budgets
	// are enforced per-request instead (TTFB + idle-stream timeouts) in Phase 4.
}

// Default timeouts.
const (
	DefaultReadHeaderTimeout = 10 * time.Second
	DefaultIdleTimeout       = 120 * time.Second
	DefaultShutdownGrace     = 30 * time.Second
)

// Server bundles an *http.Server with graceful-shutdown helpers.
type Server struct {
	http         *http.Server
	logger       *slog.Logger
	grace        time.Duration
	baseCtx      context.Context
	streamCtx    context.Context
	shuttingDown atomic.Bool
	streamCancel context.CancelFunc
}

// New constructs a Server from router dependencies, assembling the middleware
// chain and routes internally.
func New(cfg Config, deps RouterDeps, baseCtx context.Context, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if deps.Logger == nil {
		deps.Logger = logger
	}
	if deps.LiveLogs == nil {
		deps.LiveLogs = NewLiveLogHub()
	}
	if deps.Analytics != nil {
		deps.LiveLogs.AttachStore(deps.Analytics)
	}
	if deps.TursoManager == nil {
		deps.TursoManager = NewTursoManager(deps.ConfigDir, deps.TursoStore, logger)
	}
	if deps.Registry != nil {
		deps.TursoManager.AttachRegistry(deps.Registry)
	}
	s := newServer(cfg, baseCtx, logger)
	s.http.Handler = s.buildHandler(deps)
	return s
}

// NewWithHandler constructs a Server from a pre-built handler (used in tests and
// when the caller wants full control of routing).
func NewWithHandler(cfg Config, handler http.Handler, baseCtx context.Context, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	s := newServer(cfg, baseCtx, logger)
	s.http.Handler = s.drainGuard(handler)
	return s
}

func newServer(cfg Config, baseCtx context.Context, logger *slog.Logger) *Server {
	if cfg.Addr == "" {
		cfg.Addr = "0.0.0.0:8080"
	}
	if cfg.ReadHeaderTimeout <= 0 {
		cfg.ReadHeaderTimeout = DefaultReadHeaderTimeout
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = DefaultIdleTimeout
	}
	if cfg.ShutdownGrace <= 0 {
		cfg.ShutdownGrace = DefaultShutdownGrace
	}
	// streamCtx decouples in-flight streaming requests from premature signal context cancellation,
	// allowing active streams to drain naturally during the shutdown grace period.
	streamCtx, streamCancel := context.WithCancel(context.Background())
	return &Server{
		http: &http.Server{
			Addr:              cfg.Addr,
			Handler:           nil, // set by callers
			ReadHeaderTimeout: cfg.ReadHeaderTimeout,
			IdleTimeout:       cfg.IdleTimeout,
			// WriteTimeout intentionally unset (streaming-safe).
			BaseContext: func(_ net.Listener) context.Context { return streamCtx },
		},
		logger:       logger,
		grace:        cfg.ShutdownGrace,
		baseCtx:      baseCtx,
		streamCtx:    streamCtx,
		streamCancel: streamCancel,
	}
}

// Handler exposes the underlying http.Server's handler (used in tests).
func (s *Server) Handler() http.Handler { return s.http.Handler }

// AttachAutoTLS binds an AutoTLS controller to this server's middleware chain,
// stream context, and graceful-shutdown budget. Binding does not enable TLS;
// callers must explicitly apply an enabled AutoTLSConfig.
func (s *Server) AttachAutoTLS(autoTLS *AutoTLS) {
	if s == nil || autoTLS == nil {
		return
	}
	autoTLS.bind(s.http.Handler, s.streamCtx, s.grace)
}

// ShuttingDown reports whether the server has received a shutdown signal and is in draining mode.
func (s *Server) ShuttingDown() bool {
	return s.shuttingDown.Load()
}

// drainGuard intercepts new incoming requests during graceful shutdown and fails fast with 503.
func (s *Server) drainGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.shuttingDown.Load() {
			w.Header().Set("Retry-After", "5")
			w.Header().Set("Connection", "close")
			openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI, "server is shutting down")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ServeOnListener runs the server on a caller-provided listener until the
// server's base context is cancelled, then shuts down gracefully. Useful for
// pre-bound sockets (e.g. systemd socket activation) and tests.
func (s *Server) ServeOnListener(ln net.Listener) error {
	return s.serveUntilDone(ln, s.baseCtx)
}

// Serve runs the server until ctx is cancelled, then gracefully shuts down
// within the grace period. It returns nil on clean shutdown and a non-nil error
// on unexpected failure.
func (s *Server) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return err
	}
	return s.serveUntilDone(ln, ctx)
}

func (s *Server) serveUntilDone(ln net.Listener, ctx context.Context) error {
	defer func() {
		if s.streamCancel != nil {
			s.streamCancel()
		}
	}()
	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("http server listening", "addr", ln.Addr().String())
		err := s.http.Serve(ln)
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
		s.shuttingDown.Store(true)
		s.logger.Info("shutdown signal received; draining", "grace", s.grace.String())
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.grace)
		defer cancel()
		if err := s.http.Shutdown(shutdownCtx); err != nil {
			s.logger.Warn("graceful shutdown incomplete; forcing close", "err", err)
			if closeErr := s.http.Close(); closeErr != nil {
				return errors.Join(err, fmt.Errorf("force close: %w", closeErr))
			}
			return err
		}
		s.logger.Info("graceful shutdown complete")
		return <-errCh
	}
}
