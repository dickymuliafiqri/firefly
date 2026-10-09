/**
 * Regression coverage for the Cloudflare Tunnel card's Quick | Named mode switch
 * (`SettingsPage`): the switch must reconnect a running tunnel in place, keep the
 * selection pending while the tunnel is stopped, never fire the request a Named
 * switch without a token would fail on, and revert to the mode the server really
 * accepted when a reconnect fails.
 *
 * The API surface is stubbed (no query client, no network) and `IS_REACT_ACT_ENVIRONMENT`
 * is set so React can be driven synchronously inside a jsdom container.
 */
import { act } from "react";
import { createRoot } from "react-dom/client";
import { beforeEach, describe, expect, it, vi } from "vitest";

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

interface Status {
  running: boolean;
  mode: string;
  public_url: string;
  local_url: string;
  token_configured: boolean;
  downloading: boolean;
}

const emptyStatus: Status = {
  running: false,
  mode: "",
  public_url: "",
  local_url: "http://127.0.0.1:8080",
  token_configured: false,
  downloading: false,
};

let tunnelStatus: Status = { ...emptyStatus };
const mutate = vi.fn();

vi.mock("@/services/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/api")>();
  return {
    ...actual,
    useTunnelStatusQuery: () => ({ data: tunnelStatus, isPending: false }),
    useToggleTunnelMutation: () => ({ mutate, isPending: false }),
    useSettingsQuery: () => ({ data: undefined, isPending: false, error: null }),
    useWarpStatusQuery: () => ({ data: undefined, isPending: false }),
    useTestTursoMutation: () => ({ mutate: vi.fn(), isPending: false }),
    useRotateWarpMutation: () => ({ mutate: vi.fn(), isPending: false }),
    useTestNotificationMutation: () => ({ mutate: vi.fn(), isPending: false }),
  };
});

const { SettingsPage } = await import("./SettingsPage");

let container: HTMLDivElement;

function render(status: Partial<Status>) {
  tunnelStatus = { ...emptyStatus, ...status };
  mutate.mockClear();
  document.body.innerHTML = "";
  container = document.createElement("div");
  document.body.appendChild(container);
  const root = createRoot(container);
  act(() => {
    root.render(<SettingsPage />);
  });
}

function click(el: Element) {
  act(() => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

function modeRow(): HTMLElement {
  const row = [...container.querySelectorAll(".switch-row")].find((r) =>
    r.textContent?.includes("Tunnel Mode"),
  );
  if (!row) throw new Error("Tunnel Mode switch row not found");
  return row as HTMLElement;
}

function switchButton(label: string): HTMLButtonElement {
  const btn = [...modeRow().querySelectorAll(".segmented button")].find(
    (b) => b.textContent?.trim() === label,
  );
  if (!btn) throw new Error(`segmented button ${label} not found`);
  return btn as HTMLButtonElement;
}

function activeMode(): string {
  return [...modeRow().querySelectorAll(".segmented button.active")]
    .map((b) => b.textContent?.trim())
    .join(",");
}

function buttonByText(label: string): HTMLButtonElement | undefined {
  return [...container.querySelectorAll("button")].find(
    (b) => b.textContent?.trim() === label,
  ) as HTMLButtonElement | undefined;
}

function typeToken(value: string) {
  const input = container.querySelector("#tunnel-token") as HTMLInputElement;
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
  act(() => {
    setter?.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

beforeEach(() => {
  document.body.innerHTML = "";
});

describe("Cloudflare Tunnel mode switch", () => {
  it("offers a Quick | Named switch inside the tunnel card body", () => {
    render({});
    expect(modeRow().querySelectorAll(".segmented button")).toHaveLength(2);
    expect(activeMode()).toBe("Quick");
    expect(modeRow().textContent).toContain("Applied when the tunnel is enabled.");
    expect(modeRow().textContent).toContain("trycloudflare.com");
  });

  it("keeps the selection pending (no request) while the tunnel is stopped", () => {
    render({ mode: "named", token_configured: true });
    click(switchButton("Named"));
    expect(mutate).not.toHaveBeenCalled();
    expect(activeMode()).toBe("Named");
  });

  it("guards a Named switch with no stored token: no request, drawer revealed", () => {
    render({ running: true, mode: "quick", public_url: "https://x.trycloudflare.com" });
    click(switchButton("Named"));
    expect(mutate).not.toHaveBeenCalled();
    expect(container.querySelector("#tunnel-token")).not.toBeNull();
    expect(container.textContent).toContain("needs a Cloudflare Tunnel token");
    expect(modeRow().textContent).toContain("waiting for a tunnel token");
  });

  it("reconnects in place when switching to Named while running", () => {
    render({
      running: true,
      mode: "quick",
      public_url: "https://x.trycloudflare.com",
      token_configured: true,
    });
    click(switchButton("Named"));
    expect(mutate).toHaveBeenCalledTimes(1);
    expect(mutate.mock.calls[0][0]).toEqual({ enabled: true, mode: "named", token: undefined });
  });

  it("reverts the switch when the server rejects the new mode", () => {
    render({ running: true, mode: "named", token_configured: true });
    click(switchButton("Quick"));
    expect(mutate.mock.calls[0][0]).toEqual({ enabled: true, mode: "quick", token: undefined });
    const onError = mutate.mock.calls[0][1].onError as (e: unknown) => void;
    act(() => {
      onError(new Error("cloudflared binary not found"));
    });
    expect(activeMode()).toBe("Named");
    expect(container.textContent).toContain("cloudflared binary not found");
  });

  it("rotates the token through Apply & Reconnect while running", () => {
    render({ running: true, mode: "named", token_configured: true });
    click(buttonByText("Tunnel Token Settings…") as Element);
    typeToken("eyJh.rotated");
    const apply = buttonByText("Apply & Reconnect");
    expect(apply).toBeDefined();
    click(apply as Element);
    expect(mutate.mock.calls[0][0]).toEqual({
      enabled: true,
      mode: "named",
      token: "eyJh.rotated",
    });
    const onSuccess = mutate.mock.calls[0][1].onSuccess as (r: unknown) => void;
    act(() => {
      onSuccess({ running: true, mode: "named" });
    });
    const input = container.querySelector("#tunnel-token") as HTMLInputElement;
    expect(input.value).toBe("");
  });

  it("labels the drawer action Save & Enable while stopped", () => {
    render({ mode: "named", token_configured: true });
    click(buttonByText("Tunnel Token Settings…") as Element);
    expect(buttonByText("Save & Enable")).toBeDefined();
  });

  it("makes the Enable switch refuse a token-less Named start as well", () => {
    render({ mode: "named" });
    const enableSwitch = container.querySelector(
      'input[aria-label="Toggle Cloudflare Tunnel"]',
    ) as HTMLInputElement;
    click(enableSwitch);
    expect(mutate).not.toHaveBeenCalled();
    expect(container.textContent).toContain("needs a Cloudflare Tunnel token");
    expect(container.querySelector("#tunnel-token")).not.toBeNull();
  });
});
