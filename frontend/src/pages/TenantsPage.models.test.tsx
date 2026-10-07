/**
 * Regression coverage for the tenant "Allowed models" picker (`TenantsPage`).
 * The field used to be a free-text input whose comma-separated value reached
 * the gateway verbatim, so a single typo surfaced as an "unknown model"
 * validation failure only at save time. It is now a tag picker driven by the
 * live catalog (models + combos), and the backend's `*` wildcard is rendered
 * as an explicit tag — the backend normalizes an empty `allowed_models` list
 * to `["*"]` anyway, so the picker shows that state instead of an empty box.
 *
 * The settings view and draft store are stubbed (no query client, no network)
 * and `IS_REACT_ACT_ENVIRONMENT` is set so React can be driven synchronously
 * inside a jsdom container.
 */
import { act } from "react";
import { createRoot } from "react-dom/client";
import { describe, expect, it, vi } from "vitest";
import type { SettingsDTO } from "@/services/schema";

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let settingsFixture: SettingsDTO = {
  upstreams: [],
  models: [
    { public_name: "smart", upstream: "u-openai", upstream_model: "gpt-4o" },
    { public_name: "fast", upstream: "u-openai", upstream_model: "gpt-4o-mini" },
  ],
  tenants: [],
  combos: [{ name: "smart-combo", models: ["smart", "fast"] }],
};

const stage = vi.fn();

vi.mock("@/services/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/api")>();
  return {
    ...actual,
    useTopupTenantMutation: () => ({ mutate: vi.fn(), isPending: false }),
  };
});

vi.mock("@/state/draftStore", () => ({
  useSettingsView: () => ({
    data: settingsFixture,
    isLoading: false,
    error: null,
  }),
  useDraftStore: (selector: (s: { stage: typeof stage }) => unknown) =>
    selector({ stage }),
}));

const { TenantsPage } = await import("./TenantsPage");

let container: HTMLDivElement;
function render() {
  stage.mockClear();
  document.body.innerHTML = "";
  container = document.createElement("div");
  document.body.appendChild(container);
  const root = createRoot(container);
  act(() => {
    root.render(<TenantsPage />);
  });
}

function click(el: Element) {
  act(() => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

function buttonByText(label: string): HTMLButtonElement {
  const btn = [...container.querySelectorAll("button")].find(
    (b) => b.textContent?.trim() === label,
  );
  if (!btn) throw new Error(`button "${label}" not found`);
  return btn as HTMLButtonElement;
}

function rowFor(name: string): HTMLElement {
  const row = [...container.querySelectorAll("tbody tr")].find((tr) =>
    tr.textContent?.includes(name),
  );
  if (!row) throw new Error(`tenant row "${name}" not found`);
  return row as HTMLElement;
}

function openEditor(name: string) {
  const row = rowFor(name);
  const btn = [...row.querySelectorAll("button")].find(
    (b) => b.textContent?.trim() === "Edit",
  );
  if (!btn) throw new Error(`edit button for "${name}" not found`);
  click(btn);
}

/** React's onChange for form controls: set the value through the prototype
 *  setter (bypassing React's value tracker) and dispatch the native event. */
function setControlValue(
  el: HTMLInputElement | HTMLSelectElement,
  value: string,
) {
  const proto =
    el instanceof HTMLSelectElement
      ? HTMLSelectElement.prototype
      : HTMLInputElement.prototype;
  const setter = Object.getOwnPropertyDescriptor(proto, "value")?.set;
  setter?.call(el, value);
  act(() => {
    el.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

function tags(): string[] {
  return [...container.querySelectorAll(".tag")].map(
    (t) => t.textContent?.trim() ?? "",
  );
}

function optionValues(): string[] {
  return [
    ...(container.querySelector("#t-models") as HTMLSelectElement).options,
  ].map((o) => o.value);
}

/** Tag remove buttons are icon-only; they carry `Remove <name>` as aria-label. */
function removeButtonFor(name: string): HTMLButtonElement {
  const btn = [...container.querySelectorAll("button")].find(
    (b) => b.getAttribute("aria-label") === `Remove ${name}`,
  );
  if (!btn) throw new Error(`remove button for "${name}" not found`);
  return btn as HTMLButtonElement;
}

describe("TenantsPage allowed-models picker", () => {
  it("defaults a new tenant to the explicit wildcard tag", () => {
    render();
    click(buttonByText("Create Tenant"));
    expect(tags()).toEqual(["All models (*)"]);
    const picker = container.querySelector("#t-models") as HTMLSelectElement;
    expect(picker.disabled).toBe(true);
    // The wildcard state offers nothing to add — the tag already grants
    // everything, so the dropdown holds only its placeholder.
    expect(optionValues()).toEqual([""]);
    expect(buttonByText("Restrict to list")).toBeTruthy();
  });

  it("stages picked catalog entries and honors tag removal", () => {
    render();
    click(buttonByText("Create Tenant"));
    click(buttonByText("Restrict to list"));

    expect(container.querySelector(".tag-picker-empty")?.textContent).toContain(
      "No restriction",
    );
    expect(optionValues()).toEqual(["", "*", "smart", "fast", "smart-combo"]);

    setControlValue(
      container.querySelector("#t-models") as HTMLSelectElement,
      "smart",
    );
    expect(tags()).toEqual(["smart"]);

    setControlValue(
      container.querySelector("#t-models") as HTMLSelectElement,
      "smart-combo",
    );
    expect(tags()).toEqual(["smart", "smart-combo"]);
    // A picked entry leaves the dropdown, so it cannot be added twice.
    expect(optionValues()).toEqual(["", "*", "fast"]);

    click(removeButtonFor("smart"));
    expect(tags()).toEqual(["smart-combo"]);

    setControlValue(
      container.querySelector("#t-name") as HTMLInputElement,
      "work",
    );
    click(buttonByText("Create tenant"));

    expect(stage).toHaveBeenCalledTimes(1);
    const payload = stage.mock.calls[0][0];
    expect(payload.tenants).toHaveLength(1);
    expect(payload.tenants[0].name).toBe("work");
    expect(payload.tenants[0].allowed_models).toEqual(["smart-combo"]);
  });

  it("editing a tenant shows its stored list as removable tags", () => {
    settingsFixture = {
      ...settingsFixture,
      tenants: [
        {
          name: "work",
          api_key: "sk-gw-test",
          status: "active",
          allowed_models: ["smart", "smart-combo"],
          rate_limit: { rps: 10, max_concurrent: 8 },
        },
      ],
    };
    render();
    openEditor("work");
    expect(tags()).toEqual(["smart", "smart-combo"]);

    click(removeButtonFor("smart"));
    expect(tags()).toEqual(["smart-combo"]);
    click(buttonByText("Save changes"));

    expect(stage).toHaveBeenCalledTimes(1);
    const payload = stage.mock.calls[0][0];
    expect(payload.tenants[0].name).toBe("work");
    expect(payload.tenants[0].allowed_models).toEqual(["smart-combo"]);
  });

  it("editing a tenant with an empty list shows the wildcard tag", () => {
    settingsFixture = {
      ...settingsFixture,
      tenants: [
        {
          name: "work",
          api_key: "sk-gw-test",
          status: "active",
          allowed_models: [],
          rate_limit: { rps: 10, max_concurrent: 8 },
        },
      ],
    };
    render();
    openEditor("work");
    expect(tags()).toEqual(["All models (*)"]);
    expect(
      (container.querySelector("#t-models") as HTMLSelectElement).disabled,
    ).toBe(true);
  });

  it("editing a tenant with a stored wildcard shows the wildcard tag", () => {
    settingsFixture = {
      ...settingsFixture,
      tenants: [
        {
          name: "work",
          api_key: "sk-gw-test",
          status: "active",
          allowed_models: ["*"],
          rate_limit: { rps: 10, max_concurrent: 8 },
        },
      ],
    };
    render();
    openEditor("work");
    expect(tags()).toEqual(["All models (*)"]);
  });
});

