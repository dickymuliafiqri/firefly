/**
 * Regression coverage for the Models tab's catalog status: registration is
 * per-upstream. The same provider id (`gpt-4o`) routed on upstream-a must NOT
 * mark upstream-b's row REGISTERED — that global check used to dead-end the
 * operator on "Already registered" and block a legitimate second route.
 */
import { act } from "react";
import { createRoot } from "react-dom/client";
import { beforeEach, describe, expect, it, vi } from "vitest";

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const pushToast = vi.fn();

vi.mock("@/services/api", () => ({
  useCheckUpstreamMutation: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useUpstreamModelsMutation: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));

vi.mock("@/state/draftStore", () => ({
  useSettingsView: () => ({ data: { upstreams: [] } }),
}));

vi.mock("@/state/store", () => ({
  useUiStore: (sel: (s: { pushToast: typeof pushToast }) => unknown) =>
    sel({ pushToast }),
}));

vi.mock("@/services/modelCache", () => ({
  useModelCacheStore: (
    sel: (s: {
      cache: Record<string, string[]>;
      setModels: () => void;
    }) => unknown,
  ) => sel({ cache: {}, setModels: vi.fn() }),
  pickOptimalProbeModel: () => null,
}));

const { ModelsTab } = await import("@/components/upstream/ModelsTab");

type CatalogModel = {
  public_name: string;
  upstream: string;
  upstream_model?: string;
};

function render(catalogModels: CatalogModel[]): HTMLElement {
  document.body.innerHTML = "";
  const container = document.createElement("div");
  document.body.appendChild(container);
  act(() => {
    createRoot(container).render(
      <ModelsTab
        upstreamName="upstream-b"
        protocol="openai"
        baseUrl="https://b.example/v1"
        egressMode="direct"
        probeModel=""
        discoveredModels={["gpt-4o"]}
        latencyMs={null}
        catalogModels={catalogModels}
        onProbeModelChange={() => {}}
        onDiscover={() => {}}
        onCreateRoute={() => {}}
        onCreateAll={() => {}}
      />,
    );
  });
  return container;
}

beforeEach(() => {
  pushToast.mockClear();
});

describe("ModelsTab catalog status", () => {
  it("leaves the row UNMAPPED when only another upstream routes the id", () => {
    const container = render([
      { public_name: "gpt-4o", upstream: "upstream-a", upstream_model: "gpt-4o" },
    ]);

    expect(container.textContent).toContain("UNMAPPED");
    expect(container.textContent).not.toContain("REGISTERED");
    // The operator still learns it is routed elsewhere, and what adding here does.
    expect(container.textContent).toContain("upstream-a");
    expect(container.textContent).toContain("namespace");
  });

  it("marks the row REGISTERED when this upstream routes the id", () => {
    const container = render([
      { public_name: "gpt-4o", upstream: "upstream-a", upstream_model: "gpt-4o" },
      {
        public_name: "upstream-b--gpt-4o",
        upstream: "upstream-b",
        upstream_model: "gpt-4o",
      },
    ]);

    expect(container.textContent).toContain("REGISTERED");
    expect(container.textContent).toContain("Mapped as: upstream-b--gpt-4o");
  });
});
