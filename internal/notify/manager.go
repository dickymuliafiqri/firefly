package notify

import (
	"context"
	"log/slog"
)

// Manager owns the process-wide dispatcher and the channel list it delivers to.
// It exists so the gateway has exactly one thing to hand to the breaker, the
// health checker, and the request path, and so the dispatcher's lifetime is
// tied to the root context rather than to whichever component happened to
// construct it.
type Manager struct {
	dispatcher *Dispatcher
}

// ManagerConfig wires the manager.
type ManagerConfig struct {
	// Channels returns the currently configured channels. It is re-read on
	// every delivery, so a settings hot-reload takes effect without a restart.
	// A nil func means "no channels", which is the default.
	Channels func() []Channel
	// QueueSize bounds the in-memory backlog. Defaults to 256.
	QueueSize int
	// Workers is the number of concurrent delivery goroutines. Defaults to 1.
	Workers int
	// AllowPrivate lifts the SSRF guard for local development and tests.
	AllowPrivate bool
	// Logger receives delivery failures. Defaults to slog.Default().
	Logger *slog.Logger
}

// NewManager builds a manager and its dispatcher. No goroutine starts until
// Run is called.
func NewManager(cfg ManagerConfig) *Manager {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	dispatcher := NewDispatcher(DispatcherConfig{
		QueueSize: cfg.QueueSize,
		Workers:   cfg.Workers,
		Channels:  cfg.Channels,
		Poster:    NewPoster(cfg.AllowPrivate),
		Logger:    cfg.Logger,
	})
	return &Manager{dispatcher: dispatcher}
}

// Run starts the delivery workers and blocks until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) {
	if m == nil || m.dispatcher == nil {
		return
	}
	m.dispatcher.Run(ctx)
}

// Emit queues an event. It never blocks.
func (m *Manager) Emit(ev Event) {
	if m == nil || m.dispatcher == nil {
		return
	}
	m.dispatcher.Emit(ev)
}

// Stats reports the dispatcher counters.
func (m *Manager) Stats() Stats {
	if m == nil || m.dispatcher == nil {
		return Stats{}
	}
	return m.dispatcher.Stats()
}
