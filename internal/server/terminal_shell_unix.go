//go:build !windows

package server

import (
	"context"
	"io"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// startShell spawns an interactive shell on a real PTY: bash when available,
// otherwise sh. A PTY is what makes password prompts (sudo/su), cursor keys,
// and full-screen programs work — the slave presents itself as a terminal, so
// programs can disable echo for secrets. exec.Cmd with CommandContext kills the
// process when the request context is canceled (browser disconnect or graceful
// shutdown). If PTY allocation fails, we fall back to a pipe-backed shell so
// the terminal stays usable (line-oriented) instead of dead.
//
// SECURITY NOTE (Semgrep dangerous-exec-command): the exec path is a FIXED
// allowlist resolved via LookPath — it is never derived from request data. User
// input flows only into the shell's stdin (the documented purpose of a web
// terminal). This endpoint is gated by authorizeTerminalCredential before the
// WebSocket upgrade, so arbitrary command execution is reachable only by an
// authenticated dashboard admin — which is the explicit feature contract.
func startShell(ctx context.Context) (*shellProcess, error) {
	var cmd *exec.Cmd
	var name string
	for _, candidate := range []string{"bash", "sh"} {
		if path, err := exec.LookPath(candidate); err == nil {
			cmd = exec.CommandContext(ctx, path)
			name = candidate
			break
		}
	}
	if cmd == nil {
		return nil, io.ErrUnexpectedEOF
	}
	// Watchdog-friendly default size; the browser sends a real resize frame
	// as soon as xterm fits itself (see the frontend resize handler).
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err == nil {
		return &shellProcess{
			cmd:    cmd,
			name:   name,
			tty:    true,
			stdin:  ptmx,
			stdout: ptmx,
		}, nil
	}

	// No PTY on this host (e.g. restricted container). Fall back to pipes:
	// lose interactive password prompts but keep the shell reachable.
	sh, err := wireShell(exec.CommandContext(ctx, cmd.Path))
	if err != nil {
		return nil, err
	}
	sh.name = name
	return sh, nil
}

// resizeShell adjusts the PTY window size to the browser terminal's dimensions.
// It is a no-op on the pipe fallback.
func resizeShell(sh *shellProcess, cols, rows int) {
	if sh == nil || !sh.tty || cols <= 0 || rows <= 0 {
		return
	}
	// Guard the uint16 conversion: oversized frames are clamped, not wrapped.
	if cols > 0xffff {
		cols = 0xffff
	}
	if rows > 0xffff {
		rows = 0xffff
	}
	_ = pty.Setsize(sh.stdout.(*os.File), &pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
	})
}
