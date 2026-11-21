//go:build windows

package server

import (
	"context"
	"io"
	"os/exec"
)

// startShell spawns a pipe-backed shell. Windows has no POSIX PTY in this
// build, so the candidates are PowerShell (Core first, then Windows
// PowerShell) and finally cmd.exe. The browser falls back to line-oriented
// mode: no cursor keys or hidden password prompts there.
func startShell(ctx context.Context) (*shellProcess, error) {
	var cmd *exec.Cmd
	var name string
	for _, candidate := range []string{"pwsh", "powershell", "cmd"} {
		if path, err := exec.LookPath(candidate); err == nil {
			cmd = exec.CommandContext(ctx, path)
			name = candidate
			break
		}
	}
	if cmd == nil {
		return nil, io.ErrUnexpectedEOF
	}
	sh, err := wireShell(cmd)
	if err != nil {
		return nil, err
	}
	sh.name = name
	return sh, nil
}

// resizeShell is a no-op on Windows' pipe-backed shell.
func resizeShell(_ *shellProcess, _, _ int) {}
