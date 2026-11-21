package server

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// A pipe-backed shell prints no PS1: it has no TTY. To still draw a real,
// informative prompt (cwd + shell + last exit code), the server asks the
// shell to print a short, nonce-tagged status line after every command. The
// browser parses that marker out of the output stream and renders the prompt
// itself. The nonce makes a collision with ordinary command output practically
// impossible.
//
// terminalPrompt describes the marker protocol for one shell session.
// probe is a single shell command line (with trailing newline) that prints
// the marker; it is empty on shells where the POSIX syntax is unavailable.
type terminalPrompt struct {
	shell string
	nonce string
	probe string
}

func newTerminalPrompt(shell string) terminalPrompt {
	nonce := terminalNonce()
	return terminalPrompt{shell: shell, nonce: nonce, probe: terminalProbe(shell, nonce)}
}

func terminalNonce() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "firefly"
	}
	return hex.EncodeToString(b)
}

// terminalProbe builds the shell command that prints the status marker:
//
//	RS <nonce> US <exit code> US <cwd with $HOME shortened to ~> RS LF
//
// Both bash and /bin/sh (including dash and macOS bash-in-POSIX-mode) support
// printf octal escapes and ${VAR/#PATTERN/REPL}. PowerShell/cmd do not, so
// those shells get no probe and the browser falls back to a shell-only prompt.
func terminalProbe(shell, nonce string) string {
	switch shell {
	case "bash", "sh":
		return fmt.Sprintf("printf '\\036%s\\037%%s\\037%%s\\036\\n' \"$?\" \"${PWD/#$HOME/~}\"\n", nonce)
	default:
		return ""
	}
}
