package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"
	"golang.org/x/term"
)

// terminalDial dials the handler over a real HTTP server so the full WS
// handshake — including the query-param token path — is exercised.
func terminalDial(t *testing.T, h http.Handler, token string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	u.Scheme = "ws"
	u.Path = "/api/terminal/ws"
	dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	wsURL := u.String()
	if token != "" {
		wsURL += "?token=" + url.QueryEscape(token)
	}
	// nolint:bodyclose // websocket.Dial closes the body itself
	return websocket.Dial(dctx, wsURL, &websocket.DialOptions{
		Subprotocols: []string{terminalSubprotocol},
		HTTPHeader:   http.Header{"Origin": []string{"http://" + u.Host}},
	})
}

// TestTerminalWS_RejectsAnonymous verifies the endpoint fails closed with no
// credential. There is no "unwired deps" bypass here: a web terminal must never
// be reachable without an admin credential.
func TestTerminalWS_RejectsAnonymous(t *testing.T) {
	deps := RouterDeps{AdminToken: "test-terminal-secret"}
	// Dial with a bad token: server must answer 401 before upgrading, so Dial
	// fails with a non-101 response.
	_, resp, err := terminalDial(t, http.HandlerFunc(deps.handleTerminalWS), "wrong-token")
	if err == nil {
		t.Fatal("expected dial to fail for a bad token")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

// TestTerminalWS_RejectsUnconfigured runs the same check when no credential is
// configured at all — a default-allow would turn the endpoint into an open
// webshell on a gateway whose operator never set a password.
func TestTerminalWS_RejectsUnconfigured(t *testing.T) {
	deps := RouterDeps{}
	_, _, err := terminalDial(t, http.HandlerFunc(deps.handleTerminalWS), "anything")
	if err == nil {
		t.Fatal("expected dial to fail when no credential is configured")
	}
}

// TestTerminalWS_EchoRoundTrip drives a real shell over the socket: send a
// command, then wait for the output frame. Skipped when no shell is on PATH.
func TestTerminalWS_EchoRoundTrip(t *testing.T) {
	if _, err := startShell(context.Background()); err != nil {
		t.Skip("no shell on PATH")
	}
	deps := RouterDeps{AdminToken: "test-terminal-secret"}
	conn, resp, err := terminalDial(t, http.HandlerFunc(deps.handleTerminalWS), "test-terminal-secret")
	if err != nil {
		t.Fatalf("dial: %v (status %d)", err, respStatus(resp))
	}
	defer conn.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Server sends "ready" first.
	_, payload, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read ready: %v", err)
	}
	var ready terminalServerMsg
	if json.Unmarshal(payload, &ready) != nil || ready.Type != "ready" {
		t.Fatalf("expected ready frame, got %s", payload)
	}

	// 2. Echo a distinctive marker.
	marker := "FIREFLY_TERMINAL_ECHO_" + strings.ToUpper(strings.Repeat("x", 8))
	input := "echo " + marker + "\n"
	payload, _ = json.Marshal(terminalClientMsg{Type: "input", Data: input})
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatalf("write input: %v", err)
	}

	// 3. Read until the marker shows up in an output frame. The context
	// deadline above bounds the whole read loop (coder/websocket has no
	// SetReadDeadline; deadlines are carried on the read context).
	var buf strings.Builder
	for ctx.Err() == nil {
		_, payload, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read output: %v (got: %q)", err, buf.String())
		}
		var msg terminalServerMsg
		if json.Unmarshal(payload, &msg) != nil {
			continue
		}
		buf.WriteString(msg.Data)
		if strings.Contains(buf.String(), marker) {
			return
		}
	}
	t.Fatalf("marker %q never appeared; collected: %q", marker, buf.String())
}

func respStatus(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}

const terminalSecretHelperEnv = "FIREFLY_TERMINAL_SECRET_HELPER"

// TestTerminal_PTYSecretInput drives a real PTY with a re-executed copy of
// this test binary (Go's standard helper-process pattern): the child disables
// tty echo, exactly like the password prompt of sudo/su, and must still read
// the secret the parent writes. That is the primitive the WebSocket bridge
// forwards bytes to — a pipe-backed shell cannot provide it.
func TestTerminal_PTYSecretInput(t *testing.T) {
	if os.Getenv(terminalSecretHelperEnv) == "1" {
		terminalSecretHelper()
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// No shell interpreter and no user-derived arguments: re-exec this test
	// binary with a fixed flag and an env switch.
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTerminal_PTYSecretInput$")
	cmd.Env = append(os.Environ(), terminalSecretHelperEnv+"=1")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Skipf("PTY unavailable on this host: %v", err)
	}
	defer ptmx.Close()

	buf := make([]byte, 256)
	var preamble string
	for !strings.Contains(preamble, "READY:") {
		n, err := ptmx.Read(buf)
		if n > 0 {
			preamble += string(buf[:n])
		}
		if err != nil {
			t.Fatalf("read ready marker: %v (got %q)", err, preamble)
		}
	}

	if _, err := ptmx.Write([]byte("hunter2\n")); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	var output string
	deadline := time.After(3 * time.Second)
	for !strings.Contains(output, "OK:[hunter2]") {
		select {
		case <-deadline:
			t.Fatalf("secret never reached program; got %q", output)
		default:
		}
		n, err := ptmx.Read(buf)
		if n > 0 {
			output += string(buf[:n])
		}
		if err != nil {
			t.Fatalf("read after secret: %v (got %q)", err, output)
		}
	}
	if strings.Count(output, "hunter2") != 1 {
		t.Fatalf("secret was echoed by the tty: %q", output)
	}
	_ = cmd.Wait()
}

// terminalSecretHelper runs in the PTY child: disable echo first, announce
// readiness, then read one line and echo it back prefixed so the parent can
// assert both delivery and the absence of tty echo.
func terminalSecretHelper() {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		fmt.Fprintf(os.Stdout, "ERR:%v\n", err)
		return
	}
	defer func() { _ = term.Restore(fd, old) }()
	fmt.Fprint(os.Stdout, "READY:")
	var secret []byte
	one := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(one)
		if err != nil || n == 0 || one[0] == '\n' || one[0] == '\r' {
			break
		}
		secret = append(secret, one[0])
	}
	fmt.Fprintf(os.Stdout, "\nOK:[%s]\n", secret)
}
