package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"sync"

	"github.com/coder/websocket"

	"github.com/dickymuliafiqri/firefly/internal/security/auth"
	"github.com/dickymuliafiqri/firefly/internal/supervisor"
)

// This file implements the dashboard web terminal. It is deliberately a
// *pipe-backed* shell, not a PTY: the gateway must run on Windows hosts as well
// as on the Linux container image, and creack/pty has no Windows backend.
// Consequences are documented on the Terminal dashboard page: no cursor keys,
// no full-screen programs, no job control — just a persistent shell that reads
// lines from stdin and writes output to stdout/stderr.
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

	// Wait for the shell so we can report its exit code. Cancel first so the
	// input pump's blocking Read is released.
	go func() {
		defer supervisor.Guard("server.terminal-wait")()
		err := sh.cmd.Wait()
		_ = tw.send(terminalServerMsg{Type: "exit", Code: shellExitCode(err)})
		cancel()
	}()

	// Two output pumps: stdout and stderr are separate os.Pipes from exec.Cmd.
	go pumpOutput(ctx, sh.stdout, tw)
	go pumpOutput(ctx, sh.stderr, tw)

	_ = tw.send(terminalServerMsg{Type: "ready"})

	// The request goroutine is the single input pump.
	defer func() {
		cancel() // kills the shell (CommandContext) and releases the pumps
		_ = sh.stdin.Close()
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
			if _, err := sh.stdin.Write([]byte(msg.Data)); err != nil {
				return
			}
		case "resize":
			// No PTY: terminal dimensions are a browser-side concern only.
			// Accept the frame for forward compatibility without touching the
			// process (there is no TTY to resize).
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
