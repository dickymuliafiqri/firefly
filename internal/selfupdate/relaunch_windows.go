//go:build windows

package selfupdate

import (
	"log/slog"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// relaunchProcess starts the updated binary as a child of this process. The
// caller is expected to exit immediately afterwards, so the child is released
// rather than waited on and the current console (if any) stays attached.
func relaunchProcess(env Environment, logger *slog.Logger) error {
	cmd := exec.Command(env.Executable, os.Args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	// A new process group keeps a Ctrl+C in the shared console from being
	// delivered to both the departing parent and the fresh child.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}

	if logger != nil {
		logger.Info("starting the updated binary", "path", env.Executable)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
