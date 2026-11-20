import { useEffect, useRef, useState } from 'react';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';

import { PageHeader } from '@/components/ui/PageHeader';
import { Button } from '@/components/ui/Button';
import { Badge } from '@/components/ui/Badge';
import { getAdminToken } from '@/state/auth';

/**
 * Terminal — pipe-backed web shell over WebSocket.
 *
 * SECURITY CONTRACT (see server/terminal.go): the shell is reached only after
 * the handshake token is validated. There is NO PTY here — the backend bridges
 * WebSocket frames to a pipe-backed `bash`/`sh` (or PowerShell/cmd on Windows),
 * so this is a line-oriented shell: no cursor keys, no full-screen programs,
 * no job control. Good for quick ops; not a desktop terminal.
 */

interface TerminalFrame {
  t: 'ready' | 'data' | 'exit' | 'error' | 'pong';
  d?: string;
  code?: number;
  msg?: string;
}

const MAX_RECONNECT_ATTEMPTS = 5;
const RECONNECT_DELAY_MS = 1500;

type Status = 'idle' | 'connecting' | 'connected' | 'closed' | 'error';

/** Maps connection status onto the shared Badge tone vocabulary. */
function statusTone(status: Status): 'ok' | 'info' | 'danger' | 'neutral' {
  if (status === 'connected') return 'ok';
  if (status === 'connecting') return 'info';
  if (status === 'error') return 'danger';
  return 'neutral';
}

/** True when the page is served over https so the WS must also be wss://. */
function terminalWsUrl(): string {
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${proto}//${window.location.host}/api/terminal/ws`;
}

export function TerminalPage() {
  const hostRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const reconnectTimer = useRef<number | null>(null);
  const reconnectAttempts = useRef(0);
  const [status, setStatus] = useState<Status>('idle');
  const [lastMsg, setLastMsg] = useState('');
  const [lastExitCode, setLastExitCode] = useState<number | null>(null);

  // Mount once: create the xterm instance and open the WebSocket. The token
  // arrives as a query param because browsers cannot send an Authorization
  // header on a WebSocket handshake; the server validates it before upgrading.
  useEffect(() => {
    if (!hostRef.current) return;

    const term = new Terminal({
      cursorBlink: true,
      convertEol: true,
      fontFamily: "'JetBrains Mono', ui-monospace, monospace",
      fontSize: 13,
      theme: {
        background: '#050b18',
        foreground: '#e8eef9',
        cursor: '#bef264',
        green: '#bef264',
        blue: '#06b6d4',
        red: '#f43f5e',
      },
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(hostRef.current);
    fit.fit();
    termRef.current = term;
    fitRef.current = fit;

    // Handle browser window resize while the page is mounted.
    const onResize = () => {
      try {
        fit.fit();
      } catch {
        // container may be hidden mid-layout; harmless
      }
    };
    window.addEventListener('resize', onResize);

    // ResizeObserver keeps the terminal fit to its container as the shell
    // panel reflows (sidebar collapse, window resize, etc.). Guarded because
    // ResizeObserver fires once on observe().
    const ro = new ResizeObserver(onResize);
    ro.observe(hostRef.current);

    let disposed = false;

    const connect = () => {
      if (disposed) return;
      setStatus('connecting');
      setLastMsg('');

      const token = getAdminToken();
      if (!token) {
        setStatus('error');
        setLastMsg('No session token. Sign in again.');
        term.writeln('\x1b[31mNo session token. Sign in again.\x1b[0m');
        return;
      }

      const ws = new WebSocket(`${terminalWsUrl()}?token=${encodeURIComponent(token)}`);
      wsRef.current = ws;

      ws.onopen = () => {
        setStatus('connected');
        setLastExitCode(null);
        reconnectAttempts.current = 0;
        // Fit on open so the server-side resize handler receives real dims.
        try {
          fit.fit();
        } catch {
          // ignore
        }
      };

      ws.onmessage = (ev) => {
        try {
          const frame: TerminalFrame = JSON.parse(ev.data);
          switch (frame.t) {
            case 'ready':
              setStatus('connected');
              break;
            case 'data':
              term.write(frame.d ?? '');
              break;
            case 'exit':
              setStatus('closed');
              setLastExitCode(frame.code ?? null);
              term.writeln(`\r\n\x1b[33m[shell exited, code ${frame.code ?? '?'}]\x1b[0m`);
              break;
            case 'error':
              setStatus('error');
              setLastMsg(frame.msg ?? 'terminal error');
              term.writeln(`\r\n\x1b[31m[error: ${frame.msg ?? 'unknown'}]\x1b[0m`);
              break;
            case 'pong':
              // liveness signal; no UI change
              break;
          }
        } catch {
          // ignore malformed frame
        }
      };

      ws.onerror = () => {
        setStatus('error');
        setLastMsg('WebSocket error');
        term.writeln('\r\n\x1b[31m[WebSocket error]\x1b[0m');
      };

      ws.onclose = () => {
        if (disposed) return;
        setStatus((prev) => (prev === 'connected' ? 'closed' : prev));
        scheduleReconnect();
      };

      // Wire xterm input to the socket.
      term.onData((data) => {
        if (ws.readyState !== WebSocket.OPEN) return;
        ws.send(JSON.stringify({ t: 'input', d: data }));
      });
    };

    const scheduleReconnect = () => {
      // Reconnect only after a clean exit, not on user-initiated teardown.
      // Auto-retry is bounded so a permanently broken endpoint cannot spin
      // forever; a successful open resets the counter.
      if (disposed) return;
      if (reconnectTimer.current) return;
      if (reconnectAttempts.current >= MAX_RECONNECT_ATTEMPTS) {
        setStatus('error');
        setLastMsg('Connection lost. Use Reconnect to try again.');
        return;
      }
      reconnectAttempts.current += 1;
      reconnectTimer.current = window.setTimeout(() => {
        reconnectTimer.current = null;
        connect();
      }, RECONNECT_DELAY_MS);
    };

    connect();

    return () => {
      disposed = true;
      if (reconnectTimer.current) {
        window.clearTimeout(reconnectTimer.current);
        reconnectTimer.current = null;
      }
      wsRef.current?.close();
      wsRef.current = null;
      window.removeEventListener('resize', onResize);
      ro.disconnect();
      term.dispose();
      termRef.current = null;
      fitRef.current = null;
    };
  }, []);

  const reconnectNow = () => {
    // Force reconnect: reset the bounded counter and close the socket; the
    // normal onclose path then schedules a fresh connection.
    reconnectAttempts.current = 0;
    wsRef.current?.close();
  };

  const clearTerminal = () => {
    termRef.current?.clear();
    termRef.current?.focus();
  };

  return (
    <>
      <PageHeader
        title="Terminal"
        description="Pipe-backed web shell. No PTY: line-oriented commands only — no vim/top/cursor keys."
        actions={
          <>
            <Button variant="ghost" onClick={clearTerminal}>
              Clear
            </Button>
            <Button variant="secondary" onClick={reconnectNow}>
              Reconnect
            </Button>
          </>
        }
      />

      <div className="card">
        <div className="card-header">
          <h2>Shell session</h2>
          <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
            <Badge
              tone={statusTone(status)}
            >
              {status}
            </Badge>
            {lastExitCode !== null && (
              <span className="faint">last exit: {lastExitCode}</span>
            )}
            {lastMsg && <span className="muted">{lastMsg}</span>}
          </div>
        </div>
        <div
          className="card-body"
          style={{
            padding: 0,
            background: '#050b18',
            borderBottomLeftRadius: 'var(--radius, 8px)',
            borderBottomRightRadius: 'var(--radius, 8px)',
            overflow: 'hidden',
          }}
        >
          <div
            ref={hostRef}
            style={{
              height: 'calc(100vh - 260px)',
              minHeight: 360,
              padding: 8,
              boxSizing: 'border-box',
            }}
          />
        </div>
      </div>

      <p className="faint" style={{ marginTop: 12, fontSize: 12 }}>
        Shell type is detected per host: <code>bash → sh</code> on Linux/macOS,
        PowerShell → cmd on Windows. Output is logged nowhere; input/output travel
        over a WebSocket gated by your admin session token.
      </p>
    </>
  );
}
