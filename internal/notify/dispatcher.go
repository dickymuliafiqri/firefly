package notify

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// drainGrace bounds how long a shutting-down worker keeps delivering events
// that were already accepted. It is short on purpose: shutdown must not hang.
const drainGrace = 2 * time.Second

// Dispatcher hands events to background workers over a bounded queue. The
// queue is the whole point: a slow or dead channel must never apply back
// pressure to the request path, so a full queue drops the event and increments
// a counter instead of blocking the caller.
type Dispatcher struct {
	queue    chan Event
	workers  int
	channels func() []Channel
	poster   *Poster
	logger   *slog.Logger

	dropped   atomic.Int64
	delivered atomic.Int64
	failed    atomic.Int64
	running   atomic.Bool

	wg sync.WaitGroup
}

// DispatcherConfig wires the dispatcher. Channels is re-read on every event so
// a settings hot-reload takes effect without restarting the worker.
type DispatcherConfig struct {
	// QueueSize bounds the in-memory backlog. Defaults to 256.
	QueueSize int
	// Workers is the number of concurrent delivery goroutines. Defaults to 1.
	Workers int
	// Channels returns the currently configured channels.
	Channels func() []Channel
	// Poster performs the HTTP delivery. A nil poster makes the dispatcher a
	// no-op sink, which is the default the rest of the gateway runs with.
	Poster *Poster
	// Logger receives delivery failures. Defaults to slog.Default().
	Logger *slog.Logger
}

// NewDispatcher builds a dispatcher. It starts no goroutine; Run does. Events
// emitted before Run are queued rather than dropped, so an alert raised during
// startup is not lost.
func NewDispatcher(cfg DispatcherConfig) *Dispatcher {
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 256
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 1
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	d := &Dispatcher{
		queue:    make(chan Event, cfg.QueueSize),
		workers:  cfg.Workers,
		channels: cfg.Channels,
		poster:   cfg.Poster,
		logger:   cfg.Logger,
	}
	d.running.Store(true)
	return d
}

// Run starts the delivery workers and blocks until ctx is cancelled. The queue
// channel is never closed — closing it would race with Emit and panic on a
// send — so shutdown is signalled purely through the context, and each worker
// drains what it already accepted before exiting.
func (d *Dispatcher) Run(ctx context.Context) {
	d.running.Store(true)
	d.wg.Add(d.workers)
	for i := 0; i < d.workers; i++ {
		go d.work(ctx)
	}
	<-ctx.Done()
	d.running.Store(false)
	d.wg.Wait()
}

func (d *Dispatcher) work(ctx context.Context) {
	defer d.wg.Done()
	for {
		select {
		case <-ctx.Done():
			d.drain()
			return
		case ev := <-d.queue:
			d.deliver(ctx, ev)
		}
	}
}

// drain delivers whatever is already queued, stopping the moment the queue is
// empty or the grace window expires. It uses a detached context because the
// caller's context is already cancelled at this point.
func (d *Dispatcher) drain() {
	deadline := time.Now().Add(drainGrace)
	for {
		select {
		case ev := <-d.queue:
			remaining := time.Until(deadline)
			if remaining <= 0 {
				d.dropped.Add(1)
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), remaining)
			d.deliver(ctx, ev)
			cancel()
		default:
			return
		}
	}
}

func (d *Dispatcher) deliver(ctx context.Context, ev Event) {
	if d.channels == nil || d.poster == nil {
		return
	}
	for _, ch := range d.channels() {
		if !ch.Allows(ev) {
			continue
		}
		if err := d.poster.Post(ctx, ch, ev); err != nil {
			d.failed.Add(1)
			d.logger.Warn("notify delivery failed",
				"event", ev.Type,
				"channel_url", redactURL(ch.URL),
				"error", err)
			continue
		}
		d.delivered.Add(1)
	}
}

// Emit queues an event. It never blocks: when the queue is full the event is
// dropped and the drop counter rises, because losing one alert is strictly
// better than stalling a request.
func (d *Dispatcher) Emit(ev Event) {
	if d == nil {
		return
	}
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = time.Now().UTC()
	}
	if !d.running.Load() {
		d.dropped.Add(1)
		return
	}

	select {
	case d.queue <- ev:
	default:
		d.dropped.Add(1)
	}
}

// Stats is a snapshot of the dispatcher counters, surfaced through telemetry.
type Stats struct {
	Queued    int   `json:"queued"`
	Dropped   int64 `json:"dropped"`
	Delivered int64 `json:"delivered"`
	Failed    int64 `json:"failed"`
}

// Stats reports the current counters.
func (d *Dispatcher) Stats() Stats {
	if d == nil {
		return Stats{}
	}
	return Stats{
		Queued:    len(d.queue),
		Dropped:   d.dropped.Load(),
		Delivered: d.delivered.Load(),
		Failed:    d.failed.Load(),
	}
}
