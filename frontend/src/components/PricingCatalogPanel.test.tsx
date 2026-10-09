/**
 * Regression coverage for the models.dev catalog picker (`PricingCatalogPanel`).
 *
 * The panel used to read a `model` field that GET /api/pricing/catalog never
 * emitted — the handler serves `model_id` and `name` — so every row rendered an
 * empty Model cell while still showing its provider and price. The rows looked
 * populated, which is why it survived review: only the one column a human
 * actually reads was blank.
 *
 * These tests decode the fixture through the same field names the endpoint
 * emits and assert the rendered cell is non-empty, so a future rename on either
 * side fails here instead of on the Pricing page.
 */
import { act } from "react";
import { createRoot } from "react-dom/client";
import { describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let catalogFixture = {
  status: "ok",
  fetched_at: "2026-10-09T00:00:00Z",
  providers: ["google", "openai"],
  total: 3,
  entries: [
    {
      key: "google/gemini-2.5-pro",
      provider: "google",
      model_id: "gemini-2.5-pro",
      name: "Gemini 2.5 Pro",
      input_micros_per_m: 1_250_000,
      output_micros_per_m: 10_000_000,
      cache_read_micros_per_m: 310_000,
      cache_write_micros_per_m: 1_250_000,
      context_limit: 1_048_576,
      output_limit: 65_536,
    },
    {
      key: "openai/gpt-4o",
      provider: "openai",
      model_id: "gpt-4o",
      name: "GPT-4o",
      input_micros_per_m: 2_500_000,
      output_micros_per_m: 10_000_000,
      cache_read_micros_per_m: 1_250_000,
    },
    {
      key: "openai/gpt-4o-mini",
      provider: "openai",
      model_id: "gpt-4o-mini",
      name: "GPT-4o mini",
      input_micros_per_m: 150_000,
      output_micros_per_m: 600_000,
    },
  ],
};

const pushToast = vi.fn();

vi.mock("@/services/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/api")>();
  return {
    ...actual,
    fetchPricingCatalog: vi.fn(async () => catalogFixture),
    refreshPricingCatalog: vi.fn(async () => catalogFixture),
    importPricing: vi.fn(async () => ({
      status: "ok",
      added: 1,
      skipped: 0,
      total: 1,
    })),
  };
});

vi.mock("@/state/store", () => ({
  useUiStore: (selector: (s: { pushToast: typeof pushToast }) => unknown) =>
    selector({ pushToast }),
}));

const { PricingCatalogPanel } = await import("./PricingCatalogPanel");

let container: HTMLDivElement;

function render() {
  pushToast.mockClear();
  document.body.innerHTML = "";
  container = document.createElement("div");
  document.body.appendChild(container);
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  const root = createRoot(container);
  act(() => {
    root.render(
      <QueryClientProvider client={qc}>
        <PricingCatalogPanel />
      </QueryClientProvider>,
    );
  });
}

function buttonByText(label: string): HTMLButtonElement {
  const btn = [...container.querySelectorAll("button")].find(
    (b) => b.textContent?.trim() === label,
  );
  if (!btn) throw new Error(`button "${label}" not found`);
  return btn as HTMLButtonElement;
}

/** Flushes pending microtasks/timers so an enabled React Query can settle. */
async function flush() {
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0));
  });
}

/** Opens the picker and flushes the catalog query it enables. */
async function openBrowser() {
  await act(async () => {
    buttonByText("Browse & import").click();
  });
  await flush();
  await flush();
}

function rows(): Element[] {
  return [...container.querySelectorAll("tbody tr")];
}

/** The Model cell is the second column, after the selection checkbox. */
function modelCell(row: Element): string {
  return row.querySelectorAll("td")[1]?.textContent?.trim() ?? "";
}

function searchBox(): HTMLInputElement {
  return container.querySelector<HTMLInputElement>(
    'input[aria-label="Search catalog models"]',
  )!;
}

async function typeSearch(value: string) {
  const input = searchBox();
  const setter = Object.getOwnPropertyDescriptor(
    HTMLInputElement.prototype,
    "value",
  )?.set;
  await act(async () => {
    setter?.call(input, value);
    input.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

describe("PricingCatalogPanel", () => {
  it("renders a non-empty Model cell for every catalog row", async () => {
    render();
    await openBrowser();

    const body = rows();
    expect(body).toHaveLength(3);
    for (const row of body) {
      expect(modelCell(row)).not.toBe("");
    }
  });

  it("shows the display name with the model id beneath it", async () => {
    render();
    await openBrowser();

    const gemini = rows().find((r) => r.textContent?.includes("Gemini 2.5 Pro"));
    expect(gemini).toBeDefined();
    const cell = modelCell(gemini!);
    expect(cell).toContain("Gemini 2.5 Pro");
    expect(cell).toContain("gemini-2.5-pro");
  });

  it("still renders provider and price columns", async () => {
    render();
    await openBrowser();

    const gemini = rows().find((r) => r.textContent?.includes("gemini-2.5-pro"))!;
    expect(gemini.textContent).toContain("google");
    expect(gemini.textContent).toContain("$1.250");
    expect(gemini.textContent).toContain("$10.000");
  });

  it("filters on the display name", async () => {
    render();
    await openBrowser();
    await typeSearch("gemini");

    expect(rows()).toHaveLength(1);
    expect(modelCell(rows()[0])).toContain("Gemini 2.5 Pro");
  });

  it("filters on the model id too", async () => {
    render();
    await openBrowser();
    await typeSearch("gpt-4o-mini");

    expect(rows()).toHaveLength(1);
    expect(modelCell(rows()[0])).toContain("GPT-4o mini");
  });

  it("keeps the picker closed until it is opened", () => {
    render();
    expect(rows()).toHaveLength(0);
    expect(container.textContent).toContain("Seed the local sheet");
  });
});
