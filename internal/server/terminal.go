package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/coder/websocket"

	"github.com/dickymuliafiqri/firefly/internal/security/auth"
	"github.com/dickymuliafiqri/firefly/internal/supervisor"
)

// This file implements the dashboard web terminal. On Unix the shell runs on
// a real PTY (creack/pty), so password prompts (sudo/su), cursor keys and
// full-screen programs work. Windows uses a pipe-backed fallback: the terminal
// page switches to line-oriented mode there, with a browser-drawn prompt.
// The "ready" frame reports which mode is active.
//
// SECURITY: this endpoint is a remote-code-execution primitive. It is gated by
// the same admin credential as the rest of the dashboard, and the token MUST be
// verified before the WebSocket upgrade — a browser cannot set an Authorization
// header on a WebSocket handshake, so the token arrives in the query string (or
// as a subprotocol). Never log the token or the traffic: slog's logging
// middleware only records r.URL.Path, and no message body is ever logged here.

const (
	terminalSubprotocol  = "firefly-terminal"
	terminalMaxInputSize = 1 << 20 // 1 MiB per client frame; plenty for a shell line
	terminalReadBufSize  = 32 << 10
)

// terminalClientMsg is one frame sent from the browser.
type terminalClientMsg struct {
	Type string `json:"t"`           // "input", "resize", "ping"
	Data string `json:"d,omitempty"` // input bytes (UTF-8), includes trailing newline
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

// terminalServerMsg is one frame sent to the browser.
type terminalServerMsg struct {
	Type string `json:"t"`           // "ready", "data", "exit", "error", "pong"
	Data string `json:"d,omitempty"` // output bytes (UTF-8)
	Code int    `json:"code,omitempty"`
	Msg  string `json:"msg,omitempty"`
	Tty  bool   `json:"tty,omitempty"` // true when a real PTY backs the session
	// Shell, Home, Cwd and Nonce ride on "ready" so the browser can draw a
	// real shell-style prompt in the pipe fallback (see terminalProbe).
	Shell string `json:"shell,omitempty"`
	Home  string `json:"home,omitempty"`
	Cwd   string `json:"cwd,omitempty"`
	Nonce string `json:"nonce,omitempty"`
}

// terminalRequestToken extracts the admin credential from a WebSocket
// handshake. Browsers cannot set headers on new WebSocket connections, so the
// query parameter is the primary path; the header and subprotocol forms are
// accepted for CLI clients and tests.
func terminalRequestToken(r *http.Request) string {
	if tok := r.URL.Query().Get("token"); tok != "" {
		return tok
	}
	if tok, ok := auth.ExtractBearer(r.Header.Get("Authorization")); ok {
		return tok
	}
	const prefix = terminalSubprotocol + "."
	for _, h := range r.Header.Values("Sec-WebSocket-Protocol") {
		if len(h) > len(prefix) && h[:len(prefix)] == prefix {
			return h[len(prefix):]
		}
	}
	return ""
}

// authorizeTerminalCredential applies the strict admin gate. Unlike the bare
// authorizeAdmin helper (which allows an entirely unwired deps struct in unit
// tests), a terminal must never be reachable when no credential is configured.
func (deps RouterDeps) authorizeTerminalCredential(ctx context.Context, token string) bool {
	if token == "" || ctx.Err() != nil {
		return false
	}
	if deps.Auth != nil {
		return deps.Auth.ValidateToken(ctx, token)
	}
	if deps.AdminToken != "" {
		return subtle.ConstantTimeCompare([]byte(token), []byte(deps.AdminToken)) == 1
	}
	return false
}

// terminalWriter serializes all writes to a single WebSocket connection.
// coder/websocket permits only one concurrent writer; stdout, stderr and the
// lifecycle messages all funnel through this mutex.
type terminalWriter struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (tw *terminalWriter) send(msg terminalServerMsg) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	tw.mu.Lock()
	defer tw.mu.Unlock()
	return tw.conn.Write(context.Background(), websocket.MessageText, payload)
}

// pumpOutput relays one shell output stream to the WebSocket until EOF, an
// error, or the connection is closed. Every goroutine that touches the process
// or the socket is supervised so a panic cannot take down the gateway (§7 of
// AGENTS.md).
func pumpOutput(ctx context.Context, src io.Reader, tw *terminalWriter) {
	defer supervisor.Guard("server.terminal-output")()
	buf := make([]byte, terminalReadBufSize)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if tw.send(terminalServerMsg{Type: "data", Data: string(buf[:n])}) != nil {
				return
			}
		}
		if err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

func shellExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// handleTerminalWS upgrades an authenticated request into a shell session.
// The process is bound to the request context: when the browser disconnects or
// the server starts draining, CommandContext kills the shell and every pipe
// reader unblocks with EOF.
func (deps RouterDeps) handleTerminalWS(w http.ResponseWriter, r *http.Request) {
	token := terminalRequestToken(r)
	if !deps.authorizeTerminalCredential(r.Context(), token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{terminalSubprotocol},
	})
	if err != nil {
		return // Accept already wrote the HTTP error
	}
	defer conn.CloseNow()

	conn.SetReadLimit(terminalMaxInputSize)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	sh, err := startShell(ctx)
	if err != nil {
		_ = conn.Write(ctx, websocket.MessageText, mustTerminalJSON(terminalServerMsg{
			Type: "error", Msg: "shell unavailable: " + err.Error(),
		}))
		return
	}

	tw := &terminalWriter{conn: conn}

	// The pipe fallback prints no prompt. The browser draws one from the
	// initial cwd on "ready" and from a marker the shell emits after every
	// command, so `cd` is reflected and the exit code is the last command's.
	// A PTY session has a real shell prompt and needs no marker injection.
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	var prompt terminalPrompt
	if !sh.tty {
		prompt = newTerminalPrompt(sh.name)
	}

	// Wait for the shell so we can report its exit code. Cancel first so the
	// input pump's blocking Read is released.
	go func() {
		defer supervisor.Guard("server.terminal-wait")()
		err := sh.cmd.Wait()
		_ = tw.send(terminalServerMsg{Type: "exit", Code: shellExitCode(err)})
		cancel()
	}()

	// A PTY merges stdout and stderr into one stream; the pipe fallback has
	// two separate os.Pipes.
	go pumpOutput(ctx, sh.stdout, tw)
	if sh.stderr != nil {
		go pumpOutput(ctx, sh.stderr, tw)
	}

	_ = tw.send(terminalServerMsg{
		Type: "ready", Tty: sh.tty, Shell: sh.name, Home: home, Cwd: cwd, Nonce: prompt.nonce,
	})

	// The request goroutine is the single input pump.
	defer func() {
		cancel() // kills the shell (CommandContext) and releases the pumps
		closeShell(sh)
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}()

	for {
		mt, payload, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if mt != websocket.MessageText {
			continue
		}
		var msg terminalClientMsg
		if json.Unmarshal(payload, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "input":
			if msg.Data == "" {
				continue
			}
			line := msg.Data
			// A PTY runs in raw mode: the browser forwards keystrokes verbatim,
			// including Enter, Ctrl-C and escape sequences. Only the pipe
			// fallback gets a status probe after each completed line.
			if !sh.tty && strings.HasSuffix(line, "\n") && prompt.probe != "" {
				line += prompt.probe
			}
			if _, err := sh.stdin.Write([]byte(line)); err != nil {
				return
			}
		case "resize":
			// Keep the PTY window size in sync with xterm so programs that
			// reflow (editors, password prompts, progress bars) render right.
			resizeShell(sh, msg.Cols, msg.Rows)
		case "ping":
			if tw.send(terminalServerMsg{Type: "pong"}) != nil {
				return
			}
		}
	}
}

func mustTerminalJSON(msg terminalServerMsg) []byte {
	b, _ := json.Marshal(msg)
	return b
}
