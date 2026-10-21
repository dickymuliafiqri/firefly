/**
 * Contract coverage for the WARP pool-size setting on the Warp page.
 *
 * Pool size is a steady-state ceiling, not a dial: the value an operator saves
 * is applied by the engine and persisted, and slot count then grows one slot per
 * rotation. What matters here is that the field reports the engine's own window
 * (pool_size_min/max) rather than a copy, that an invalid value can never be
 * submitted, and that the 5-second status poll cannot erase an in-progress edit.
 *
 * The API surface is stubbed (no query client, no network) and
 * `IS_REACT_ACT_ENVIRONMENT` is set so React can be driven synchronously.
 */
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WarpStatusDTO } from "@/services/schema";

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function status(overrides: Partial<WarpStatusDTO> = {}): WarpStatusDTO {
  return {
    enabled: true,
    pool_size: 25,
    pool_size_min: 1,
    pool_size_max: 64,
    active_sessions: 3,
    egress_ips: ["203.0.113.7"],
    ...overrides,
  };
}

let statusData: WarpStatusDTO | undefined;
const setPoolSize = vi.fn();
const pushToast = vi.fn();

vi.mock("@/services/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/api")>();
  return {
    ...actual,
    useWarpStatusQuery: () => ({
      data: statusData,
      error: null,
      isLoading: false,
      isFetching: false,
      refetch: vi.fn(),
    }),
    useRotateWarpMutation: () => ({ mutate: vi.fn(), isPending: false }),
    useCheckWarpMutation: () => ({ mutate: vi.fn(), isPending: false }),
    useSetWarpPoolSizeMutation: () => ({ mutate: setPoolSize, isPending: false }),
  };
});

vi.mock("@/state/store", () => ({
  useUiStore: (selector: (s: { pushToast: typeof pushToast }) => unknown) =>
    selector({ pushToast }),
}));

const { WarpPage } = await import("./WarpPage");

let container: HTMLDivElement;
let root: Root;

function render(data: WarpStatusDTO) {
  statusData = data;
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  act(() => {
    root.render(<WarpPage />);
  });
}

/** Re-renders the same tree, as the 5s status poll does. */
function repoll(data: WarpStatusDTO) {
  statusData = data;
  act(() => {
    root.render(<WarpPage />);
  });
}

function poolInput(): HTMLInputElement {
  const el = container.querySelector<HTMLInputElement>("#warp-pool-size");
  if (!el) {
    throw new Error(`pool-size input not found in: ${container.textContent ?? ""}`);
  }
  return el;
}

function saveButton(): HTMLButtonElement {
  const el = container.querySelector<HTMLButtonElement>('button[type="submit"]');
  if (!el) {
    throw new Error("save button not found");
  }
  return el;
}

/** Types into the controlled input the way a real user does. */
function type(el: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
  act(() => {
    setter?.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

function submit() {
  const form = container.querySelector("form");
  if (!form) {
    throw new Error("form not found");
  }
  act(() => {
    form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
  });
}

beforeEach(() => {
  statusData = undefined;
  setPoolSize.mockClear();
  pushToast.mockClear();
  document.body.innerHTML = "";
});

describe("WarpPage pool size", () => {
  it("reports the engine's configured ceiling and its window", () => {
    render(status({ pool_size: 12, pool_size_min: 1, pool_size_max: 64 }));

    expect(poolInput().value).toBe("12");
    expect(poolInput().min).toBe("1");
    expect(poolInput().max).toBe("64");
    expect(container.textContent).toContain("1–64");
    // Unchanged, so there is nothing to save yet.
    expect(saveButton().disabled).toBe(true);
  });

  it("submits the parsed value once it differs from the running pool", () => {
    render(status({ pool_size: 25 }));

    type(poolInput(), "40");
    expect(saveButton().disabled).toBe(false);

    submit();
    expect(setPoolSize).toHaveBeenCalledTimes(1);
    expect(setPoolSize.mock.calls[0][0]).toBe(40);
  });

  it("refuses a value outside the engine's window", () => {
    render(status({ pool_size: 25 }));

    type(poolInput(), "900");
    expect(saveButton().disabled).toBe(true);
    expect(container.textContent).toContain("Must be between 1 and 64.");

    submit();
    expect(setPoolSize).not.toHaveBeenCalled();
  });

  it("refuses a fractional value", () => {
    render(status({ pool_size: 25 }));

    type(poolInput(), "7.5");
    expect(saveButton().disabled).toBe(true);
    expect(container.textContent).toContain("Enter a whole number.");

    submit();
    expect(setPoolSize).not.toHaveBeenCalled();
  });

  it("keeps an in-progress edit across a status re-poll", () => {
    render(status({ pool_size: 25 }));

    type(poolInput(), "40");
    // The next poll reports the still-unchanged server value; copying it back
    // into the field would wipe what the operator is typing.
    repoll(status({ pool_size: 25 }));

    expect(poolInput().value).toBe("40");
    expect(saveButton().disabled).toBe(false);
  });

  it("falls back to a 1–64 window when the server omits the bounds", () => {
    render(status({ pool_size: 25, pool_size_min: undefined, pool_size_max: undefined }));

    expect(poolInput().min).toBe("1");
    expect(poolInput().max).toBe("64");
  });

  it("treats an unset pool size as an empty field that still validates", () => {
    render(status({ pool_size: 0 }));

    expect(poolInput().value).toBe("");
    expect(saveButton().disabled).toBe(true);
    expect(container.textContent).toContain("Enter a value between 1 and 64.");
  });
});
