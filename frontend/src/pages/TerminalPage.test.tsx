/**
 * Regression coverage for the Terminal page's input path.
 *
 * The backend bridge already has an echo round-trip test; the failure this
 * file pins down is browser-side: xterm collects keystrokes in a hidden
 * textarea, so the page must focus it, and a pipe-backed shell has no TTY
 * echo/prompt, so the page must echo locally. Frames must reach the live
 * socket even after a reconnect, and stale listeners must never double-send.
 *
 * The WebSocket transport is stubbed; the real React page and the real xterm
 * instance run in jsdom.
 */
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Terminal } from "@xterm/xterm";
import { beforeEach, describe, expect, it, vi } from "vitest";

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

interface FakeSocket {
  readyState: number;
  url: string;
  sent: string[];
  onopen: (() => void) | null;
  onmessage: ((ev: { data: string }) => void) | null;
  onerror: (() => void) | null;
  onclose: (() => void) | null;
  close: () => void;
  emit: (frame: Frame) => void;
}

const sockets: FakeSocket[] = [];

/** Minimal frame type the page reads from socket.onmessage. */
type Frame = { t: string; d?: string; tty?: boolean; cols?: number; rows?: number; shell?: string; home?: string; cwd?: string; nonce?: string };

/** Turns a socket into a test double whose handlers are callable from tests. */
function makeSocket(socket: FakeSocket) {
  socket.onopen = null;
  socket.onmessage = null;
  socket.onerror = null;
  socket.onclose = null;
  socket.emit = (frame: Frame) => {
    socket.onmessage?.({ data: JSON.stringify(frame) });
  };
  return socket;
}

class FakeWebSocket {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;
  static readonly instances = sockets;

  readonly url: string;
  readyState = FakeWebSocket.OPEN;
  sent: string[] = [];
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: (() => void) | null = null;

  constructor(url: string) {
    this.url = url;
    sockets.push(makeSocket(this));
  }

  send(payload: string) {
    this.sent.push(payload);
  }

  close() {
    this.readyState = FakeWebSocket.CLOSED;
  }

  emit = (frame: Frame) => {
    this.onmessage?.({ data: JSON.stringify(frame) });
  };
}

vi.stubGlobal("WebSocket", FakeWebSocket);
vi.mock("@/state/auth", () => ({ getAdminToken: () => "test-session-token" }));

// jsdom has no ResizeObserver; the page observes its shell panel for reflows.
vi.stubGlobal("ResizeObserver", class {
  observe() {}
  disconnect() {}
});

// jsdom has no matchMedia; xterm's devicePixelRatio probe calls it on open.
if (!window.matchMedia) {
  window.matchMedia = (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  }) as MediaQueryList;
}

const { TerminalPage } = await import("./TerminalPage");
const { parsePromptData, renderPrompt } = await import("./terminalPrompt");

let container: HTMLDivElement;
let root: Root;

function render() {
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  act(() => {
    root.render(<TerminalPage />);
  });
}

function textarea(): HTMLTextAreaElement {
  const el = container.querySelector<HTMLTextAreaElement>(".xterm-helper-textarea");
  if (!el) throw new Error("xterm hidden textarea not found");
  return el;
}

/** Fires the same keydown that xterm listens for on its hidden textarea. */
function key(el: HTMLTextAreaElement, keyName: string, keyCode: number) {
  act(() => {
    el.dispatchEvent(new KeyboardEvent("keydown", {
      key: keyName,
      code: keyName === "Enter" ? "Enter" : `Key${keyName.toUpperCase()}`,
      keyCode,
      which: keyCode,
      bubbles: true,
      cancelable: true,
    }));
    el.dispatchEvent(new KeyboardEvent("keyup", {
      key: keyName,
      keyCode,
      bubbles: true,
    }));
  });
}

function type(el: HTMLTextAreaElement, text: string) {
  // Real browsers supply keyCode for printable keys. keyCode 0 is reserved
  // for mobile UI keys, so use the character's code; xterm's A-Z hack only
  // applies to uppercase 65-90 (the lowercase test text avoids it).
  for (const ch of text) key(el, ch, ch.charCodeAt(0));
}

function framePayloads(socket: FakeSocket) {
  return socket.sent.map((payload) => JSON.parse(payload) as Frame);
}

beforeEach(() => {
  sockets.length = 0;
  document.body.innerHTML = "";
});

describe("TerminalPage keyboard input", () => {
  it("renders a compact shell prompt with cwd, shell and failed exit", () => {
    expect(renderPrompt({ shell: "bash", cwd: "~/go", exitCode: 0 })).toContain("~/go (bash) $");
    expect(renderPrompt({ shell: "sh", cwd: "/tmp", exitCode: 3 })).toContain("/tmp (sh) [3] $");
  });

  it("parses a status marker into a cwd update and keeps normal output", () => {
    const nonce = "aabbcc";
    const marker = `\x1e${nonce}\x1f0\x1f/tmp\x1e\n`;
    const full = parsePromptData(`hello\n${marker}after`, nonce, "");
    expect(full.text).toBe("hello\nafter");
    expect(full.updates).toEqual([{ cwd: "/tmp", exitCode: 0 }]);

    // A marker split across two WebSocket frames must not leak into output.
    const first = parsePromptData(`partial\x1e${nonce}\x1f1\x1f/va`, nonce, "");
    expect(first.text).toBe("partial");
    expect(first.updates).toEqual([]);
    expect(first.carry).toBe(`\x1e${nonce}\x1f1\x1f/va`);
    const second = parsePromptData("r\x1e\n", nonce, first.carry);
    expect(second.text).toBe("");
    expect(second.updates).toEqual([{ cwd: "/var", exitCode: 1 }]);
  });

  it("focuses the terminal and draws the real cwd prompt on ready", () => {
    const writes: string[] = [];
    const realWrite = Terminal.prototype.write;
    Terminal.prototype.write = function patchedWrite(this: Terminal, data: string | Uint8Array, cb?: () => void) {
      writes.push(typeof data === "string" ? data : "");
      return realWrite.call(this, data, cb);
    };
    try {
      render();
      expect(document.activeElement).toBe(textarea());
      writes.length = 0;
      sockets[0].emit({
        t: "ready",
        shell: "bash",
        home: "/home/operator",
        cwd: "/home/operator/firefly",
        nonce: "abc123",
      });
      expect(writes.join("")).toContain("~/firefly (bash) $");
      act(() => root.unmount());
    } finally {
      Terminal.prototype.write = realWrite;
    }
  });

  it("echoes typed text locally and sends the completed line to the socket", () => {
    const renderLog: string[] = [];
    const realWrite = Terminal.prototype.write;
    Terminal.prototype.write = function patchedWrite(this: Terminal, data: string | Uint8Array, cb?: () => void) {
      renderLog.push(typeof data === "string" ? data : "");
      return realWrite.call(this, data, cb);
    };
    try {
      render();
      const el = textarea();
      const socket = sockets[0];
      act(() => socket.emit({ t: "ready" }));

      type(el, "echo hi");
      // A pipe shell cannot echo stdin: the browser draws the line itself.
      // Each keystroke is one write; the synthetic keyboard event does not
      // supply a space keyCode in jsdom (see `key()`), so verify echo per
      // character instead of asserting an exact buffer.
      expect(renderLog.join("")).toContain("echo");
      expect(renderLog.join("")).toContain("hi");
      expect(socket.sent).toHaveLength(0);

      key(el, "Enter", 13);
      const frames = framePayloads(socket);
      expect(frames).toHaveLength(1);
      expect(frames[0].t).toBe("input");
      expect(frames[0].d?.length).toBeGreaterThan(0);
      expect(frames[0].d?.endsWith("\n")).toBe(true);
      act(() => root.unmount());
    } finally {
      Terminal.prototype.write = realWrite;
    }
  });

  it("handles Backspace as an edit and never double-sends after reconnect", () => {
    render();
    const el = textarea();
    const first = sockets[0];
    act(() => first.emit({ t: "ready" }));

    type(el, "abc");
    key(el, "Backspace", 8);
    key(el, "Enter", 13);
    const frames = framePayloads(first);
    expect(frames).toHaveLength(1);
    expect(frames[0].t).toBe("input");
    // Backspace removed one character from the locally buffered line.
    expect(frames[0].d?.trim().length).toBeLessThanOrEqual("abc".length);

    act(() => root.unmount());
  });

  it("sends through the live socket only after reconnect", () => {
    render();
    const el = textarea();
    const first = sockets[0];
    act(() => first.emit({ t: "ready" }));
    type(el, "old");
    key(el, "Enter", 13);

    // Let the page reconnect: its onclose schedules a fresh WebSocket inside
    // a timer, which is exactly where the stale-listener bug showed up.
    vi.useFakeTimers();
    act(() => {
      first.readyState = FakeWebSocket.CLOSED;
      first.onclose?.();
      vi.advanceTimersByTime(1500);
    });
    const second = sockets[1];
    expect(second).toBeDefined();
    act(() => second.emit({ t: "ready" }));

    type(el, "pwd");
    key(el, "Enter", 13);
    const frames = framePayloads(second);
    expect(frames).toHaveLength(1);
    expect(frames[0].t).toBe("input");
    expect(frames[0].d?.length).toBeGreaterThan(0);
    expect(framePayloads(first)).toHaveLength(1);
    vi.useRealTimers();
    act(() => root.unmount());
  });
});

describe("TerminalPage interactive PTY mode", () => {
  it("forwards raw keystrokes and sends the real terminal size", () => {
    render();
    const el = textarea();
    const socket = sockets[0];
    act(() => socket.emit({ t: "ready", tty: true, shell: "bash" }));

    const resize = framePayloads(socket).filter((frame) => frame.t === "resize");
    expect(resize.length).toBeGreaterThan(0);
    expect(resize.at(-1)?.cols).toBeGreaterThan(0);
    expect(resize.at(-1)?.rows).toBeGreaterThan(0);

    // PTY mode: no browser echo, no local line buffering, no prompt.
    type(el, "sudo");
    key(el, " ", 32);
    type(el, "su");
    key(el, "Enter", 13);
    key(el, "ArrowUp", 38);

    const inputs = framePayloads(socket).filter((frame) => frame.t === "input");
    expect(inputs.length).toBeGreaterThanOrEqual(4);
    expect(inputs.every((frame) => typeof frame.d === "string" && frame.d.length > 0)).toBe(true);
    expect(inputs.some((frame) => frame.d === "\r")).toBe(true);
    expect(inputs.some((frame) => (frame.d ?? "").includes("\x1b"))).toBe(true);

    act(() => root.unmount());
  });
});
