// Package supervisor keeps the gateway's background workers alive.
//
// A panic inside a goroutine that has no recover crashes the entire process.
// On a container host that surfaces as a bare "container restarted" with no
// clue which loop died — the operator cannot see that it was the WARP rotation
// ticker or the cloudflared pipe reader. Every long-lived loop in Firefly
// therefore runs through this package, which:
//
//   - recovers the panic instead of taking the process down,
//   - logs it with the worker name, the recovered value, the panicking
//     function and file:line, and the full stack trace,
//   - restarts the loop with exponential backoff so a transient fault (a nil
//     field a rotation has not populated yet, a race in a third-party client)
//     heals itself,
//   - records per-worker counters for Prometheus and the telemetry API, so
//     "which worker panics, how often, and where" is answerable from the
//     dashboard without reading container logs.
//
// The state is process-wide because the workers are: main starts a fixed set of
// loops, and several live deep inside packages (the Turso flusher, the quota
// poller) that have no constructor of their own to thread a supervisor
// through. One Configure call from main points them all at the same registry
// and the same metrics instance. Worker names are a bounded, hand-written set,
// so neither the registry nor its metric labels can grow with traffic.
package supervisor

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"
)

// Default backoff bounds for a restarting worker. The floor keeps a hot
// crash-loop from spinning the CPU; the ceiling keeps a worker whose fault is
// persistent from being retried faster than that is useful.
const (
	DefaultMinBackoff = time.Second
	DefaultMaxBackoff = time.Minute

	// DefaultResetAfter is the uptime after which a restarted worker counts as
	// healthy again and its backoff returns to the floor, so a worker that
	// panics once an hour is not punished with a minute-long stall each time.
	DefaultResetAfter = 5 * time.Minute
)

// modulePrefix is stripped from stack frames so a reported location reads
// "internal/transport/warp/manager.go:320" instead of an absolute build path.
const modulePrefix = "github.com/dickymuliafiqri/firefly/"

// maxPanicText bounds the recovered value copied into Status.LastPanic and the
// log record, so a value whose formatting is enormous cannot flood the sink.
const maxPanicText = 512

// Policy bounds how a panicking worker is restarted.
type Policy struct {
	// MinBackoff is the delay before the first restart. Zero means
	// DefaultMinBackoff.
	MinBackoff time.Duration
	// MaxBackoff caps the exponential growth of that delay. Zero means
	// DefaultMaxBackoff.
	MaxBackoff time.Duration
	// ResetAfter is the uptime after which the backoff resets to MinBackoff.
	// Zero means DefaultResetAfter.
	ResetAfter time.Duration
	// MaxRestarts caps how many times the worker is restarted before the
	// supervisor leaves it disabled. Zero means unlimited: the worker keeps
	// retrying at MaxBackoff forever. A ticker with no useful work while down
	// should stay unlimited so it recovers once the cause is gone.
	MaxRestarts uint32
}

// DefaultPolicy is the restart policy used by every supervised loop unless a
// call site has a reason to deviate.
func DefaultPolicy() Policy {
	return Policy{
		MinBackoff: DefaultMinBackoff,
		MaxBackoff: DefaultMaxBackoff,
		ResetAfter: DefaultResetAfter,
	}
}

// withDefaults fills the unset fields so a zero Policy is usable.
func (p Policy) withDefaults() Policy {
	if p.MinBackoff <= 0 {
		p.MinBackoff = DefaultMinBackoff
	}
	if p.MaxBackoff <= 0 {
		p.MaxBackoff = DefaultMaxBackoff
	}
	if p.MaxBackoff < p.MinBackoff {
		p.MaxBackoff = p.MinBackoff
	}
	if p.ResetAfter <= 0 {
		p.ResetAfter = DefaultResetAfter
	}
	return p
}

// Observer receives worker lifecycle events. *metrics.Metrics implements this
// interface; the supervisor deliberately does not import it, so a nil Observer
// (or a metrics-less test) simply drops the events.
type Observer interface {
	// WorkerPanic counts one recovered panic for a worker.
	WorkerPanic(worker string)
	// WorkerRestart counts one restart scheduled after a panic.
	WorkerRestart(worker string)
}

// Status is the current state of one supervised worker, as reported on
// GET /api/telemetry.
type Status struct {
	// Worker is the stable name of the loop, e.g. "warp.auto-rotate".
	Worker string `json:"worker"`
	// Supervised reports whether a panic restarts the worker. Tasks guarded
	// with Guard are recorded but never restarted.
	Supervised bool `json:"supervised"`
	// Running is false once the worker has exited (context cancellation, or a
	// normal return).
	Running bool `json:"running"`
	// Panics and Restarts are lifetime counters since process start.
	Panics   uint64 `json:"panics"`
	Restarts uint64 `json:"restarts"`
	// LastPanicAt is the Unix millisecond timestamp of the most recent panic;
	// zero when the worker has never panicked.
	LastPanicAt int64 `json:"last_panic_at,omitempty"`
	// LastPanic is the recovered value, formatted and truncated for the wire.
	LastPanic string `json:"last_panic,omitempty"`
	// Location is the panicking function and source position, e.g.
	// "internal/transport/warp/manager.go:320 warp.(*Manager).autoRotationLoop".
	Location string `json:"location,omitempty"`
	// BackoffMs is the delay the supervisor is currently waiting out before
	// the next attempt; zero when the worker is not waiting.
	BackoffMs int64 `json:"backoff_ms,omitempty"`
	// Disabled is true when MaxRestarts was exceeded and the supervisor gave
	// up on the worker. The process stays up with that one loop dead.
	Disabled bool `json:"disabled"`
}

type registry struct {
	mu     sync.Mutex
	appLog *slog.Logger
	obs    Observer
	state  map[string]*Status
}

// defaultRegistry is the process-wide supervisor. main configures it once;
// packages call the package-level Go/Guard helpers.
var defaultRegistry = &registry{state: map[string]*Status{}}

// Configure points the process-wide supervisor at the application logger and
// metrics instance. It is safe to call more than once (the latest call wins),
// which keeps tests independent. A nil logger falls back to slog.Default().
func Configure(logger *slog.Logger, obs Observer) {
	defaultRegistry.mu.Lock()
	defer defaultRegistry.mu.Unlock()
	defaultRegistry.appLog = logger
	defaultRegistry.obs = obs
}

// Snapshot returns the state of every worker started so far, ordered by name so
// the telemetry payload is stable across polls.
func Snapshot() []Status {
	return defaultRegistry.snapshot()
}

// Go launches fn in a goroutine that is restarted with backoff whenever it
// panics, until ctx is cancelled or fn returns normally. A normal return means
// the loop decided it was done (ctx.Done() observed, or a terminal condition),
// so it is never restarted.
//
// The returned channel is closed once the supervisor stops driving fn: fn
// returned, ctx was cancelled, or the worker was disabled after exceeding
// MaxRestarts. A caller that must join its workers on shutdown waits on it
// instead of pairing a sync.WaitGroup with a loop that may run many times.
func Go(ctx context.Context, worker string, pol Policy, fn func(context.Context)) <-chan struct{} {
	return defaultRegistry.launch(ctx, worker, pol, fn)
}

// Guard returns a function to defer inside a one-shot background goroutine. It
// recovers a panic, logs it with the stack trace, and records it, but does not
// restart the goroutine: use it for per-request work (a rotation triggered by a
// 429, a health probe, an async quota fetch) where retrying is the caller's
// decision. Usage:
//
//	go func() {
//	    defer supervisor.Guard("upstream.health-probe")()
//	    ...
//	}()
func Guard(worker string) func() {
	return func() {
		if rec := recover(); rec != nil {
			defaultRegistry.report(worker, false, rec, debug.Stack())
		}
	}
}

// launch runs fn under supervision and returns a channel closed when the
// supervisor stops driving it.
func (r *registry) launch(ctx context.Context, worker string, pol Policy, fn func(context.Context)) <-chan struct{} {
	done := make(chan struct{})
	if fn == nil {
		close(done)
		return done
	}
	pol = pol.withDefaults()

	// Register eagerly so the worker is visible on GET /api/telemetry from the
	// moment it is started, not only after its first panic.
	r.mark(worker, func(s *Status) {
		s.Supervised = true
		s.Running = true
	})

	go func() {
		defer close(done)
		backoff := pol.MinBackoff
		for {
			if ctx.Err() != nil {
				break
			}
			started := time.Now()
			rec, stack := r.invoke(ctx, worker, fn)
			if rec == nil {
				// fn returned on its own: a deliberate stop, not a failure.
				break
			}

			status := r.report(worker, true, rec, stack)
			if pol.MaxRestarts > 0 && status.Panics > uint64(pol.MaxRestarts) {
				r.mark(worker, func(s *Status) {
					s.Disabled = true
					s.Running = false
					s.BackoffMs = 0
				})
				r.logger().Error("worker disabled after repeated panics",
					"worker", worker, "restarts", status.Restarts,
					"max_restarts", pol.MaxRestarts)
				return
			}

			if time.Since(started) >= pol.ResetAfter {
				backoff = pol.MinBackoff
			}
			r.mark(worker, func(s *Status) { s.BackoffMs = backoff.Milliseconds() })
			r.logger().Warn("restarting worker after panic",
				"worker", worker,
				"backoff", backoff.String(),
				"panic_number", status.Panics)

			select {
			case <-ctx.Done():
				r.mark(worker, func(s *Status) {
					s.Running = false
					s.BackoffMs = 0
				})
				return
			case <-time.After(backoff):
			}

			// Count the restart only once the attempt is really about to
			// happen: a shutdown that lands inside the backoff window did not
			// restart anything, and must not inflate the counter.
			r.mark(worker, func(s *Status) {
				s.Restarts++
				s.BackoffMs = 0
			})
			if obs := r.observer(); obs != nil {
				obs.WorkerRestart(worker)
			}

			if next := backoff * 2; next < pol.MaxBackoff {
				backoff = next
			} else {
				backoff = pol.MaxBackoff
			}
		}
		r.mark(worker, func(s *Status) {
			s.Running = false
			s.BackoffMs = 0
		})
	}()
	return done
}

// invoke runs fn once, converting a panic into its recovered value plus stack.
// A nil recovered value means fn returned normally.
func (r *registry) invoke(ctx context.Context, worker string, fn func(context.Context)) (rec any, stack []byte) {
	defer func() {
		if v := recover(); v != nil {
			rec = v
			stack = debug.Stack()
		}
	}()
	r.mark(worker, func(s *Status) { s.Running = true })
	fn(ctx)
	return nil, nil
}

// report logs the panic, records it against the worker, and notifies the
// observer. It returns a copy of the updated status.
func (r *registry) report(worker string, supervised bool, rec any, stack []byte) Status {
	text := formatPanic(rec)
	location := PanicLocation(stack)

	status := r.mark(worker, func(s *Status) {
		s.Supervised = s.Supervised || supervised
		s.Panics++
		s.LastPanicAt = time.Now().UnixMilli()
		s.LastPanic = text
		s.Location = location
	})

	r.logger().Error("recovered from panic in background worker",
		"worker", worker,
		"panic", text,
		"location", location,
		"panics", status.Panics,
		"stack", string(stack),
	)
	if obs := r.observer(); obs != nil {
		obs.WorkerPanic(worker)
	}
	return status
}

// mark applies mutate to the worker's status under the registry lock and
// returns a copy of the result.
func (r *registry) mark(worker string, mutate func(*Status)) Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.state[worker]
	if !ok {
		s = &Status{Worker: worker}
		r.state[worker] = s
	}
	if mutate != nil {
		mutate(s)
	}
	return *s
}

func (r *registry) snapshot() []Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Status, 0, len(r.state))
	for _, s := range r.state {
		out = append(out, *s)
	}
	slices.SortFunc(out, func(a, b Status) int { return cmp.Compare(a.Worker, b.Worker) })
	return out
}

func (r *registry) logger() *slog.Logger {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.appLog != nil {
		return r.appLog
	}
	return slog.Default()
}

func (r *registry) observer() Observer {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.obs
}

// formatPanic renders a recovered value for logs and telemetry, bounded so a
// value whose formatting is enormous cannot flood the log sink.
func formatPanic(rec any) string {
	text := fmt.Sprint(rec)
	if len(text) > maxPanicText {
		text = text[:maxPanicText] + "…(truncated)"
	}
	return text
}

// PanicLocation extracts "file:line func" for the frame that panicked out of a
// debug.Stack() dump. The dump starts at the recovering function, passes
// through the runtime's panic frame, then reaches the faulting call — so the
// first frame below "panic(" is the one that matters.
func PanicLocation(stack []byte) string {
	lines := strings.Split(string(stack), "\n")

	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "panic(") {
			start = i + 1
			break
		}
	}
	if start >= 0 {
		for i := start; i < len(lines); i++ {
			line := lines[i]
			if line == "" || strings.HasPrefix(line, "\t") {
				continue
			}
			// The frame line is "pkg.Func(...)"; the next line holds its position.
			if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "\t") {
				return framePosition(lines[i+1], line)
			}
			return trimFunc(line)
		}
	}

	// A runtime fault that bypassed panic(), or an unexpected dump shape: fall
	// back to the first frame on the dump that carries a source position.
	for i, line := range lines {
		if !strings.HasPrefix(line, "\t") || !strings.Contains(line, ".go:") {
			continue
		}
		frame := ""
		if i > 0 {
			frame = lines[i-1]
		}
		return framePosition(line, frame)
	}
	return "unknown"
}

// framePosition renders "internal/pkg/file.go:42 pkg.Func" from a raw runtime
// position line ("\t/path/file.go:42 +0x1a4") and its frame label.
func framePosition(position, frame string) string {
	position = strings.TrimSpace(position)
	if sp := strings.IndexByte(position, ' '); sp > 0 {
		position = position[:sp]
	}
	name := trimFunc(frame)
	if name == "" {
		return shortenPath(position)
	}
	return shortenPath(position) + " " + name
}

// trimFunc strips the module prefix and the trailing argument list from a
// stack frame label, leaving "internal/transport/warp.(*Manager).autoRotationLoop".
func trimFunc(frame string) string {
	frame = strings.TrimSpace(frame)
	frame = strings.TrimPrefix(frame, modulePrefix)
	if open := strings.LastIndexByte(frame, '('); open > 0 && strings.HasSuffix(frame, ")") {
		frame = frame[:open]
	}
	return frame
}

// shortenPath drops the build directory so a location stays readable on the
// dashboard, keeping the module-relative tail.
func shortenPath(path string) string {
	if idx := strings.Index(path, modulePrefix); idx >= 0 {
		return path[idx+len(modulePrefix):]
	}
	if idx := strings.Index(path, "/internal/"); idx >= 0 {
		return path[idx+1:]
	}
	if idx := strings.Index(path, "/cmd/"); idx >= 0 {
		return path[idx+1:]
	}
	return path
}
