package server

import (
	"os/exec"
)

// wireShell connects the three standard pipes of an already-configured
// exec.Cmd and starts the process. It is shared by the Unix and Windows
// startShell implementations so pipe/process lifecycle logic stays in one
// place.
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
