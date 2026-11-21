import { useEffect, useRef, useState } from 'react';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';

import { PageHeader } from '@/components/ui/PageHeader';
import { Button } from '@/components/ui/Button';
import { Badge } from '@/components/ui/Badge';
import { getAdminToken } from '@/state/auth';
import { parsePromptData, renderPrompt, type PromptState } from './terminalPrompt';

/**
 * Terminal — interactive web shell over WebSocket.
 *
 * SECURITY CONTRACT (see server/terminal.go): the shell is reached only after
 * the handshake token is validated. On Unix the backend attaches `bash`/`sh`
 * to a real PTY, so password prompts (sudo/su), cursor keys and full-screen
 * programs work: keystrokes go through verbatim and the shell echoes output.
 * Windows (and hosts where PTY allocation fails) use a pipe-backed fallback,
 * where this page buffers a line locally and draws a shell-style prompt.
 */

interface TerminalFrame {
  t: 'ready' | 'data' | 'exit' | 'error' | 'pong';
  d?: string;
  code?: number;
  msg?: string;
  tty?: boolean;
  shell?: string;
  home?: string;
  cwd?: string;
  nonce?: string;
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
    const host = hostRef.current;
    if (!host) return;

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
    term.open(host);
    fit.fit();
    termRef.current = term;
    fitRef.current = fit;

    // Keep the server-side PTY window in sync with xterm. No-op in pipe mode.
    const sendTerminalResize = () => {
      const ws = wsRef.current;
      if (!ws || ws.readyState !== WebSocket.OPEN) return;
      ws.send(JSON.stringify({ t: 'resize', cols: term.cols, rows: term.rows }));
    };

    // Handle browser window resize while the page is mounted.
    const onResize = () => {
      try {
        fit.fit();
        sendTerminalResize();
      } catch {
        // container may be hidden mid-layout; harmless
      }
    };
    window.addEventListener('resize', onResize);

    // ResizeObserver keeps the terminal fit to its container as the shell
    // panel reflows (sidebar collapse, window resize, etc.). Guarded because
    // ResizeObserver fires once on observe().
    const ro = new ResizeObserver(onResize);
    ro.observe(host);

    // A pipe-backed shell has no TTY: the server injects a nonce-tagged
    // status probe after every command and the browser prints a compact
    // prompt (cwd + shell + exit code) from that real shell state. Buffer
    // the current line locally and hand it to the shell on Enter — that
    // also makes Backspace a real edit instead of a literal DEL byte.
    let prompt: PromptState = { shell: 'bash', cwd: '~', exitCode: 0 };
    let home = '~';
    let nonce = '';
    let dataTail = '';
    let interactive = false;

    const printPrompt = () => term.write(renderPrompt(prompt));
    let pendingLine = '';

    // Registered once, outside connect(): the old code registered a new
    // onData listener per reconnect, so echo multiplied and stale listeners
    // kept holding closed sockets. Reading the socket from the ref keeps one
    // handler pointed at whichever connection is live.
    const inputSub = term.onData((data) => {
      const ws = wsRef.current;
      if (!ws || ws.readyState !== WebSocket.OPEN) return;
      if (interactive) {
        // A PTY owns line editing and echo. Forward every keystroke verbatim:
        // Enter, Ctrl-C, cursor keys and the secret typed at a password
        // prompt all have to reach the program in the tty.
        ws.send(JSON.stringify({ t: 'input', d: data }));
        return;
      }
      // Escape sequences (cursor keys, etc.) carry no meaning without a PTY;
      // drop the whole chunk instead of echoing fragments like "[A".
      if (data.indexOf('\x1b') >= 0) return;

      const chars = Array.from(data);
      for (let i = 0; i < chars.length; i += 1) {
        const ch = chars[i];
        const code = ch.charCodeAt(0);
        if (ch === '\r' || ch === '\n') {
          const line = pendingLine;
          pendingLine = '';
          // convertEol turns '\n' into '\r\n'; write just one '\n' here.
          term.write('\n');
          ws.send(JSON.stringify({ t: 'input', d: line + '\n' }));
          if (ch === '\r' && chars[i + 1] === '\n') i += 1;
          continue;
        }
        if (code === 127 || code === 8) {
          const current = Array.from(pendingLine);
          if (current.length > 0) {
            current.pop();
            pendingLine = current.join('');
            term.write('\b \b');
          }
          continue;
        }
        if (code === 3) {
          // Ctrl-C: clear the local line and forward the interrupt.
          pendingLine = '';
          term.write('^C\n');
          ws.send(JSON.stringify({ t: 'input', d: '\x03' }));
          continue;
        }
        if (code < 32) continue;
        pendingLine += ch;
        term.write(ch);
      }
    });

    // xterm types into a hidden textarea. Without an explicit focus the
    // first keystrokes can land on the page instead of the shell.
    term.focus();
    const focusTerminal = () => term.focus();
    host.addEventListener('mousedown', focusTerminal);

    let disposed = false;

    const connect = () => {
      if (disposed) return;
      setStatus('connecting');
      setLastMsg('');

      dataTail = '';
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
        // Fit on open and send real dims so the PTY starts at the right size.
        try {
          fit.fit();
        } catch {
          // ignore
        }
        sendTerminalResize();
      };

      ws.onmessage = (ev) => {
        try {
          const frame: TerminalFrame = JSON.parse(ev.data);
          switch (frame.t) {
            case 'ready':
              setStatus('connected');
              interactive = frame.tty === true;
              if (frame.home) home = frame.home;
              if (frame.shell) prompt = {
                shell: frame.shell,
                cwd: frame.cwd && home && frame.cwd.startsWith(home)
                  ? '~' + frame.cwd.slice(home.length)
                  : frame.cwd || '~',
                exitCode: 0,
              };
              nonce = frame.nonce ?? '';
              term.focus();
              // The PTY shell draws its own PS1 and echoes input. Only the
              // pipe fallback needs the browser-drawn prompt.
              if (!interactive) printPrompt();
              else sendTerminalResize();
              break;
            case 'data': {
              const parsed = parsePromptData(frame.d ?? '', nonce, dataTail);
              dataTail = parsed.carry;
              if (parsed.text) term.write(parsed.text);
              for (const update of parsed.updates) {
                const cwd = update.cwd && update.cwd === home ? '~' : update.cwd;
                prompt = { ...prompt, ...update, cwd };
                printPrompt();
              }
              break;
            }
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
        interactive = false;
        pendingLine = '';
        setStatus((prev) => (prev === 'connected' ? 'closed' : prev));
        scheduleReconnect();
      };
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
      inputSub.dispose();
      host.removeEventListener('mousedown', focusTerminal);
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
    termRef.current?.focus();
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
        On Linux/macOS this is a real PTY: password prompts (sudo/su), cursor
        keys and full-screen programs work. Windows — or a host where PTY
        allocation fails — falls back to a line-oriented shell with a prompt
        showing the live cwd, shell and last exit code. Shell type is detected
        per host: <code>bash → sh</code> on Linux/macOS,
        <code> PowerShell → cmd</code> on Windows. Output is logged nowhere;
        input/output travel over a WebSocket gated by your admin session token.
      </p>
    </>
  );
}
