package server

import (
	"errors"
	"io"
	"os/exec"
)

// shellProcess holds the running shell and the streams the WebSocket handler
// bridges. An interactive session uses a PTY: stdin and stdout are the SAME
// *os.File (the pty master), which is what lets password prompts disable echo,
// cursor keys work, and tools like sudo/su see a real terminal. exec.Cmd with
// CommandContext kills the process when the request context is canceled
// (browser disconnect or graceful shutdown).
type shellProcess struct {
	cmd    *exec.Cmd
	name   string // bash/sh or pwsh/powershell/cmd; reported to the browser
	tty    bool   // true when started on a real PTY (interactive input accepted)
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser // nil on a PTY: the slave merges stdout and stderr
}

// wireShell connects the three standard pipes of an already-configured
// exec.Cmd and starts the process. It is the non-interactive fallback for
// platforms where no PTY is available (notably Windows in this build).
func wireShell(cmd *exec.Cmd) (*shellProcess, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	// CommandContext kills the process when ctx is canceled. There is no PTY,
	// so stdout/stderr are separate pipes and must be pumped independently.
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, err
	}
	return &shellProcess{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr}, nil
}

// closeShell releases every stream the process owns. A PTY master is both
// stdin and stdout; close it once. CommandContext is the process owner, so
// there is no ad-hoc Kill here — cancel() does that before this call.
func closeShell(sh *shellProcess) {
	if sh == nil {
		return
	}
	if sh.tty {
		_ = sh.stdin.Close()
		return
	}
	_ = sh.stdin.Close()
	_ = sh.stdout.Close()
	if sh.stderr != nil {
		_ = sh.stderr.Close()
	}
}

// readShellOutput copies one chunk from the shell stream. A PTY master is a
// single duplex stream; on a pipe shell stdout and stderr are separate.
func readShellOutput(sh *shellProcess, stderr bool, buf []byte) (int, error) {
	if stderr {
		if sh.stderr == nil {
			return 0, io.EOF
		}
		return sh.stderr.Read(buf)
	}
	return sh.stdout.Read(buf)
}

var errNoTTY = errors.New("no pseudo-terminal available")
