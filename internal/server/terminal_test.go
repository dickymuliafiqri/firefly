package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
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
