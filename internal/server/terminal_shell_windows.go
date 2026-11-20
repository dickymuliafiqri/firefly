//go:build windows

package server

import (
	"context"
	"io"
	"os/exec"
)

// shellProcess holds the running shell and the pipes the WebSocket handler
// bridges. exec.Cmd with CommandContext kills the process when the request
// context is canceled (browser disconnect or graceful shutdown).
type shellProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
}

// startShell spawns a pipe-backed shell. Windows has no POSIX PTY and no
// /bin/sh, so the candidates are PowerShell (Core first, then Windows
// PowerShell) and finally cmd.exe. There is no PTY on any platform in this
// feature; see terminal.go.
func startShell(ctx context.Context) (*shellProcess, error) {
	var cmd *exec.Cmd
	for _, name := range []string{"pwsh", "powershell", "cmd"} {
		if path, err := exec.LookPath(name); err == nil {
			cmd = exec.CommandContext(ctx, path)
			break
		}
	}
	if cmd == nil {
		return nil, io.ErrUnexpectedEOF
	}
	return wireShell(cmd)
}
