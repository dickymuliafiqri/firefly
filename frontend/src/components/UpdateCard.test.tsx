/**
 * Contract coverage for the self-update card (`UpdateCard`).
 *
 * The card is the only place an operator sees which release is deployed, which
 * one is installable, and why an instance refuses to update itself. These cases
 * pin the four states that matter — up to date, update available, an instance
 * that must not self-update, and a failed/running install — plus the confirm
 * gate in front of the only destructive action in the dashboard (it replaces
 * the running binary and restarts the gateway).
 *
 * The API surface is stubbed (no query client, no network) and
 * `IS_REACT_ACT_ENVIRONMENT` is set so React can be driven synchronously.
 */
import { act } from "react";
import { createRoot } from "react-dom/client";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { UpdateStatusDTO } from "@/services/api";

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function status(overrides: Partial<UpdateStatusDTO> = {}): UpdateStatusDTO {
  return {
    repository: "dickymuliafiqri/firefly",
    current_version: "v1.44.13",
    current_commit: "abc1234",
    build_date: "2026-01-01T00:00:00Z",
    platform: "linux/amd64",
    update_available: false,
    environment: {
      available: true,
      container: false,
      writable: true,
      executable: "/usr/local/bin/firefly",
      dir: "/usr/local/bin",
      supervisor: "systemd",
      restart_behavior: "exits and systemd starts it again with the new binary",
    },
    checked_at: "2026-01-02T00:00:00Z",
    ...overrides,
  };
}

let statusData: UpdateStatusDTO | undefined;
let statusError: unknown = null;
const refetch = vi.fn();
const mutate = vi.fn();

vi.mock("@/services/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/api")>();
  return {
    ...actual,
    useUpdateStatusQuery: () => ({
      data: statusData,
      error: statusError,
      isPending: statusData === undefined && statusError === null,
      isFetching: false,
      refetch,
    }),
    useApplyUpdateMutation: () => ({ mutate, isPending: false }),
  };
});

const { UpdateCard } = await import("./UpdateCard");

let container: HTMLDivElement;

function render(data: UpdateStatusDTO | undefined, error: unknown = null) {
  statusData = data;
  statusError = error;
  refetch.mockClear();
  mutate.mockClear();
  document.body.innerHTML = "";
  container = document.createElement("div");
  document.body.appendChild(container);
  const root = createRoot(container);
  act(() => {
    root.render(<UpdateCard />);
  });
}

function text(): string {
  return container.textContent ?? "";
}

function button(label: string): HTMLButtonElement {
  const found = [...container.querySelectorAll("button")].find((b) =>
    b.textContent?.includes(label),
  );
  if (!found) {
    throw new Error(`button ${label} not found in: ${text()}`);
  }
  return found as HTMLButtonElement;
}

function click(el: Element) {
  act(() => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

beforeEach(() => {
  statusData = undefined;
  statusError = null;
});

describe("UpdateCard", () => {
  it("reports up to date and disables the apply action", () => {
    render(status({ update_available: false }));

    expect(text()).toContain("UP TO DATE");
    expect(text()).toContain("v1.44.13");
    expect(button("Up to date").disabled).toBe(true);
    // Nothing to install, so no confirmation is ever staged.
    expect(mutate).not.toHaveBeenCalled();
  });

  it("offers the newest release when one is available", () => {
    render(
      status({
        update_available: true,
        latest: {
          tag: "v1.45.0",
          url: "https://github.com/dickymuliafiqri/firefly/releases/tag/v1.45.0",
          prerelease: false,
          published_at: "2026-01-02T00:00:00Z",
          asset: "firefly_v1.45.0_linux_amd64.tar.gz",
          asset_size: 21 * 1024 * 1024,
          notes: "Fixes the tunnel switch.",
        },
      }),
    );

    expect(text()).toContain("UPDATE AVAILABLE");
    expect(text()).toContain("v1.45.0");
    expect(text()).toContain("21 MB");
    expect(button("Update to v1.45.0").disabled).toBe(false);
  });

  it("keeps a disallowed instance informed but refuses to act", () => {
    render(
      status({
        update_available: true,
        latest: { tag: "v1.45.0", prerelease: false, asset_size: 100 },
        environment: {
          available: false,
          container: true,
          writable: false,
          reason: "Firefly is running in a container; update the image instead",
          restart_behavior: "Firefly relaunches itself with the new binary",
        },
      }),
    );

    // The release is still shown: the operator must learn what they are behind.
    expect(text()).toContain("v1.45.0");
    expect(text()).toContain("running in a container");
    expect(button("Update to v1.45.0").disabled).toBe(true);
  });

  it("asks for confirmation before replacing the running binary", () => {
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
    render(
      status({
        update_available: true,
        latest: { tag: "v1.45.0", prerelease: false, asset_size: 100 },
      }),
    );

    click(button("Update to v1.45.0"));

    expect(confirmSpy).toHaveBeenCalledTimes(1);
    expect(String(confirmSpy.mock.calls[0][0])).toContain("v1.45.0");
    expect(mutate).toHaveBeenCalledTimes(1);
    confirmSpy.mockRestore();
  });

  it("sends nothing when the confirmation is declined", () => {
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(false);
    render(
      status({
        update_available: true,
        latest: { tag: "v1.45.0", prerelease: false, asset_size: 100 },
      }),
    );

    click(button("Update to v1.45.0"));

    expect(mutate).not.toHaveBeenCalled();
    confirmSpy.mockRestore();
  });

  it("shows live progress while an install runs", () => {
    render(
      status({
        update_available: true,
        latest: { tag: "v1.45.0", prerelease: false, asset_size: 100 },
        install: {
          running: true,
          done: false,
          percent: 42,
          stage: "downloading",
          message: "42 of 100 bytes",
          total: 100,
          written: 42,
          restarting: false,
        },
      }),
    );

    expect(text()).toContain("UPDATING");
    expect(text()).toContain("downloading");
    const bar = container.querySelector(".progress .fill") as HTMLElement;
    expect(bar.style.width).toBe("42%");
    // One install at a time: the action is locked while it runs.
    expect(button("Updating…").disabled).toBe(true);
  });

  it("surfaces a failed install without hiding the retry action", () => {
    render(
      status({
        update_available: true,
        latest: { tag: "v1.45.0", prerelease: false, asset_size: 100 },
        install: {
          running: false,
          done: false,
          percent: 0,
          error: "checksum mismatch: got aa, expected bb",
          restarting: false,
        },
      }),
    );

    expect(text()).toContain("UPDATE FAILED");
    expect(text()).toContain("checksum mismatch");
    expect(button("Update to v1.45.0").disabled).toBe(false);
  });

  it("keeps the last status visible when a check fails, noting it is stale", () => {
    render(
      status({
        update_available: false,
        check_error: "GitHub API rate limit exceeded",
        stale: true,
      }),
    );

    expect(text()).toContain("last known release");
    expect(text()).toContain("rate limit");
  });

  it("renders a load failure instead of a fabricated version when nothing is known", () => {
    render(undefined, new Error("gateway unreachable"));

    expect(text()).toContain("Failed to load from gateway");
    expect(text()).toContain("gateway unreachable");
  });
});
