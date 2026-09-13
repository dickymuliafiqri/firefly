//go:build !unix

package watch

import (
	"context"
	"os"
)

// watchSIGHUP returns a channel that never fires; used on platforms without
// SIGHUP. It allocates no goroutine, so hoisting it out of the watcher loop is
// free. Callers must still invoke it once and pass the same channel to Run.
func watchSIGHUP(ctx context.Context) <-chan os.Signal {
	_ = ctx
	return make(chan os.Signal)
}
