// Package watch implements config hot-reload: fsnotify events as the primary
// trigger, a periodic poll as a backstop (bind mounts / Docker), and a debounce
// window to coalesce editor write bursts. It is context-bound for clean exit.
package watch

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/registry"
	"github.com/fsnotify/fsnotify"
)

// Options configures the watcher.
type Options struct {
	// Dir is the directory containing the JSON config files.
	Dir string
	// PollInterval is the backstop poll period. Zero uses the default.
	PollInterval time.Duration
	// Debounce coalesces bursts of filesystem events. Zero uses the default.
	Debounce time.Duration
	// Logger receives structured reload logs. Nil uses slog.Default().
	Logger *slog.Logger
}

// Default timing values.
const (
	DefaultPollInterval = 10 * time.Second
	DefaultDebounce     = 500 * time.Millisecond
)

// Watcher drives registry reloads. It is safe to run as a single goroutine and
// exits when ctx is cancelled.
type Watcher struct {
	opts   Options
	reg    *registry.Registry
	logger *slog.Logger
}

// New builds a Watcher.
func New(opts Options, reg *registry.Registry) *Watcher {
	if opts.PollInterval <= 0 {
		opts.PollInterval = DefaultPollInterval
	}
	if opts.Debounce <= 0 {
		opts.Debounce = DefaultDebounce
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Watcher{opts: opts, reg: reg, logger: logger}
}

// trigger carries the reason a reload was requested (for logs); it travels over
// the internal debounce channel (triggers, buffered size 1).
type trigger struct{ reason string }

// Run blocks until ctx is cancelled, reloading the registry on events, polls,
// and SIGHUP. Reload errors never terminate the loop; they are logged and the
// previous snapshot keeps serving.
func (w *Watcher) Run(ctx context.Context, reload func(context.Context) error) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer fsw.Close()

	// Ensure directory exists so fsnotify can watch it.
	_ = os.MkdirAll(w.opts.Dir, 0o755)

	if err := fsw.Add(w.opts.Dir); err != nil {
		w.logger.Warn("could not add directory to fsnotify watcher", "dir", w.opts.Dir, "err", err)
	}

	triggers := make(chan trigger, 1)
	// Debounce worker: coalesces rapid triggers into a single reload.
	go w.debounce(ctx, triggers, reload)

	// SIGHUP setup is hoisted OUT of the loop on purpose: watchSIGHUP registers a
	// signal handler, so calling it per-iteration would leak a registration on
	// every event. Register once, reuse the channel.
	hup := watchSIGHUP(ctx)

	poll := time.NewTicker(w.opts.PollInterval)
	defer poll.Stop()

	// Baseline for the poll backstop. The poll exists for environments where
	// inotify events never arrive (bind mounts, Docker volumes), but an idle
	// gateway must not rebuild its whole catalog on every tick — that caused a
	// reload (and a full KeyRing rebuild) every PollInterval even with nothing
	// changed. Only a fingerprint change — a writer actually touching one of the
	// tracked files — justifies a reload. fsnotify-served changes refresh the
	// baseline too, so they are not reloaded a second time by the next poll.
	lastPoll := pollFingerprint(w.opts.Dir)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-fsw.Events:
			if !ok {
				return nil
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) != 0 {
				// Ignore unrelated files in the config directory (log files,
				// editor swap/temp files) so we do not rebuild on noise.
				if !config.IsConfigFile(filepath.Base(ev.Name)) {
					continue
				}
				lastPoll = pollFingerprint(w.opts.Dir)
				w.signal(triggers, "fsnotify:"+ev.Name)
			}
		case err, ok := <-fsw.Errors:
			if !ok {
				return nil
			}
			w.logger.Error("fsnotify error", "err", err)
		case <-poll.C:
			if fp := pollFingerprint(w.opts.Dir); fp != lastPoll {
				lastPoll = fp
				w.signal(triggers, "poll")
			}
		case <-hup:
			w.signal(triggers, "sighup")
		}
	}
}

// pollFingerprint hashes the identity (presence, size, mtime) of every tracked
// config file. It deliberately does not read file contents: the fingerprint only
// answers "did a writer touch the directory since the last sample", which is
// what gates the poll-triggered reload.
func pollFingerprint(dir string) uint64 {
	h := fnv.New64a()
	for _, name := range config.ConfigFileNames() {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			_, _ = fmt.Fprintf(h, "%s|missing\n", name)
			continue
		}
		_, _ = fmt.Fprintf(h, "%s|%d|%d\n", name, fi.Size(), fi.ModTime().UnixNano())
	}
	return h.Sum64()
}

// generation returns the registry's current generation, or 0 if no registry is
// attached (e.g. in unit tests).
func (w *Watcher) generation() uint64 {
	if w.reg == nil {
		return 0
	}
	return w.reg.CurrentGeneration()
}

// signal pushes a trigger without blocking; if one is pending it is superseded.
func (w *Watcher) signal(ch chan<- trigger, reason string) {
	select {
	case ch <- trigger{reason: reason}:
	default:
		// A trigger is already pending; drop this one (debounce will cover it).
	}
}

// debounce waits for quiet time before invoking reload.
//
// It coalesces a burst of file-change triggers: each new trigger restarts the
// quiet window, and reload runs once when the window elapses. We allocate a
// FRESH timer per trigger rather than calling Reset on a reused one — the
// time.Timer Reset contract requires the channel to have been drained (or the
// timer stopped, with a race check) before reuse, and a stale tick leaking into
// the next window would fire a premature reload. A per-burst timer sidesteps
// that contract entirely and is cheap (one alloc per event burst, not per file).
func (w *Watcher) debounce(ctx context.Context, in <-chan trigger, reload func(context.Context) error) {
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-in:
			timer := time.NewTimer(w.opts.Debounce)
			lastReason := t.reason
		drain:
			for {
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case next := <-in:
					lastReason = next.reason
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					timer.Reset(w.opts.Debounce)
				case <-timer.C:
					break drain
				}
			}

			if err := reload(ctx); err != nil {
				w.logger.Error("config reload failed; keeping previous snapshot", "reason", lastReason, "err", err)
			} else {
				w.logger.Info("config reloaded", "reason", lastReason, "generation", w.generation())
			}
		}
	}
}
