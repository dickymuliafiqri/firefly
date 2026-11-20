//go:build !windows

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

// startShell spawns a pipe-backed shell: bash when available, otherwise sh.
// There is no PTY on any platform in this feature; see terminal.go.
//
// SECURITY NOTE (Semgrep dangerous-exec-command): the exec path is a FIXED
// allowlist resolved via LookPath — it is never derived from request data. User
// input flows only into the shell's stdin (the documented purpose of a web
// terminal). This endpoint is gated by authorizeTerminalCredential before the
// WebSocket upgrade, so arbitrary command execution is reachable only by an
// authenticated dashboard admin — which is the explicit feature contract.
func startShell(ctx context.Context) (*shellProcess, error) {
	var cmd *exec.Cmd
	for _, name := range []string{"bash", "sh"} {
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
