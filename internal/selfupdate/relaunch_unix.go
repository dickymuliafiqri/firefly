//go:build !windows

package selfupdate

import (
	"log/slog"
	"os"
	"syscall"
)

// relaunchProcess replaces the current image with the updated binary, keeping
// the process ID, its open file descriptors, and its controlling terminal.
func relaunchProcess(env Environment, logger *slog.Logger) error {
	argv := append([]string{env.Executable}, os.Args[1:]...)
	if logger != nil {
		logger.Info("re-executing the updated binary", "path", env.Executable)
	}
	return syscall.Exec(env.Executable, argv, os.Environ())
}
