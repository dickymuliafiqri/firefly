//go:build unix

package watch

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// watchSIGHUP returns a channel that receives on SIGHUP. The channel and the
// signal registration live until ctx is cancelled, at which point the signal
// handler is stopped.
//
// IMPORTANT: callers must invoke this ONCE and reuse the returned channel. It
// must never be called inside a select loop: each call registers a new signal
// handler, so calling it per-iteration leaks a signal registration every time.
func watchSIGHUP(ctx context.Context) <-chan os.Signal {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	// Unregister as soon as ctx is done. We deliberately do NOT close ch:
	// signal.Notify may have a send in flight, and closing a channel the signal
	// package still targets can panic with "send on closed channel". The caller
	// stops selecting on ch once ctx is done, so the GC reclaims it.
	context.AfterFunc(ctx, func() { signal.Stop(ch) })
	return ch
}
