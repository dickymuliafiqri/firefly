import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/services/api", async () => {
  const { QueryClient } = await import("@tanstack/react-query");
  return {
    queryClient: new QueryClient({
      defaultOptions: { queries: { retry: false } },
    }),
    hasGateway: vi.fn(async () => true),
    fetchSettings: vi.fn(),
    saveSettings: vi.fn(async () => ({ status: "ok" })),
    useSettingsQuery: () => ({
      data: undefined,
      isLoading: false,
      error: null,
    }),
  };
});

import {
  fetchSettings,
  hasGateway,
  queryClient,
  saveSettings,
} from "@/services/api";
import type { SettingsDTO } from "@/services/schema";
import {
  DRAFT_STORAGE_KEY,
  resetDraftStoreForTests,
  useDraftStore,
} from "@/state/draftStore";

function makeSettings(): SettingsDTO {
  return {
    upstreams: [{ name: "openai-main", protocol: "openai" }],
    models: [
      {
        public_name: "gpt-test",
        upstream: "openai-main",
        upstream_name: "gpt-4o",
      },
    ],
    tenants: [{ name: "work", status: "active" }],
  } as unknown as SettingsDTO;
}

const otherModels = [
  { public_name: "m2", upstream: "openai-main", upstream_name: "gpt-4o" },
] as unknown as SettingsDTO["models"];

const otherTenants = [
  { name: "play", status: "suspended" },
] as unknown as SettingsDTO["tenants"];

beforeEach(() => {
  resetDraftStoreForTests();
  queryClient.clear();
  vi.mocked(hasGateway).mockResolvedValue(true);
  vi.mocked(saveSettings).mockResolvedValue({ status: "ok" });
  vi.mocked(fetchSettings).mockImplementation(async () => makeSettings());
});

describe("draftStore.stage", () => {
  it("stages a collection edit and marks its manage flag", () => {
    useDraftStore.setState({ base: makeSettings() });
    useDraftStore.getState().stage({ models: otherModels });

    const s = useDraftStore.getState();
    expect(s.draft).not.toBeNull();
    expect(s.draft?.models).toEqual(otherModels);
    expect(s.draft?.manage_models).toBe(true);
    expect(s.draft?.manage_upstreams).toBeUndefined();
    expect(s.stagedAt).not.toBeNull();
  });

  it("accumulates edits across collections", () => {
    useDraftStore.setState({ base: makeSettings() });
    const stage = useDraftStore.getState().stage;
    stage({ models: otherModels });
    stage({ tenants: otherTenants });

    const s = useDraftStore.getState();
    expect(s.draft?.models).toEqual(otherModels);
    expect(s.draft?.tenants).toEqual(otherTenants);
    expect(s.draft?.manage_models).toBe(true);
    expect(s.draft?.manage_tenants).toBe(true);
  });

  it("clears the draft when staged edits revert to base", () => {
    const base = makeSettings();
    useDraftStore.setState({ base });
    useDraftStore.getState().stage({ models: [...base.models] });

    expect(useDraftStore.getState().draft).toBeNull();
    expect(useDraftStore.getState().stagedAt).toBeNull();
  });

  it("stages an emptied collection authoritatively", () => {
    useDraftStore.setState({ base: makeSettings() });
    useDraftStore.getState().stage({ models: [] });

    const s = useDraftStore.getState();
    expect(s.draft?.models).toEqual([]);
    expect(s.draft?.manage_models).toBe(true);
  });
});

describe("draftStore.discard", () => {
  it("drops every staged edit", () => {
    useDraftStore.setState({ base: makeSettings() });
    const store = useDraftStore.getState();
    store.stage({ models: otherModels });
    store.discard();

    const s = useDraftStore.getState();
    expect(s.draft).toBeNull();
    expect(s.stagedAt).toBeNull();
    expect(s.status).toBe("idle");
  });
});

describe("draftStore.replaceAll", () => {
  it("replaces the working copy with a full config", () => {
    const base = makeSettings();
    useDraftStore.setState({ base });
    const replaced = {
      ...base,
      token_saver: { enabled: true },
    } as unknown as SettingsDTO;
    useDraftStore.getState().replaceAll(replaced);

    const s = useDraftStore.getState();
    expect(s.draft).toBe(replaced);
    expect(s.stagedAt).not.toBeNull();
  });

  it("clears the draft when the config equals base", () => {
    const base = makeSettings();
    useDraftStore.setState({ base });
    useDraftStore.getState().stage({ models: otherModels });
    useDraftStore.getState().replaceAll(base);

    expect(useDraftStore.getState().draft).toBeNull();
  });
});

describe("draftStore.syncServer", () => {
  it("adopts server changes while no draft is pending", () => {
    useDraftStore.setState({ base: makeSettings() });
    const server = { ...makeSettings(), tenants: [] };
    useDraftStore.getState().syncServer(server);

    expect(useDraftStore.getState().base).toEqual(server);
  });

  it("ignores server changes while a draft is pending", () => {
    useDraftStore.setState({ base: makeSettings() });
    const serverA = { ...makeSettings(), tenants: [] };
    useDraftStore.getState().syncServer(serverA);
    useDraftStore.getState().stage({ models: otherModels });

    const serverB = { ...makeSettings(), tenants: otherTenants };
    useDraftStore.getState().syncServer(serverB);

    const s = useDraftStore.getState();
    expect(s.base).toEqual(serverA);
    expect(s.draft).not.toBeNull();
  });
});

describe("draftStore.commit", () => {
  it("saves the draft and adopts it as the new base", async () => {
    const base = makeSettings();
    useDraftStore.setState({ base });
    useDraftStore.getState().stage({ models: otherModels });

    const result = await useDraftStore.getState().commit();

    expect(result).toBe("saved");
    expect(saveSettings).toHaveBeenCalledOnce();
    expect(vi.mocked(saveSettings).mock.calls[0][0]).toEqual(
      expect.objectContaining({ manage_models: true }),
    );
    const s = useDraftStore.getState();
    expect(s.draft).toBeNull();
    expect(s.status).toBe("idle");
    expect(s.base).toEqual({
      ...base,
      models: otherModels,
      manage_models: true,
    });
  });

  it("surfaces server drift as a conflict without saving", async () => {
    const base = makeSettings();
    useDraftStore.setState({ base });
    useDraftStore.getState().stage({ models: otherModels });

    const serverNow = { ...base, storage_engine: "turso" };
    vi.mocked(fetchSettings).mockResolvedValue(serverNow);

    const result = await useDraftStore.getState().commit();

    expect(result).toBeNull();
    expect(saveSettings).not.toHaveBeenCalled();
    const s = useDraftStore.getState();
    expect(s.status).toBe("conflict");
    expect(s.conflictServer).toEqual(serverNow);
    expect(s.draft).not.toBeNull();
  });

  it("resolves a conflict by overwriting the server", async () => {
    const base = makeSettings();
    useDraftStore.setState({ base });
    useDraftStore.getState().stage({ models: otherModels });
    vi.mocked(fetchSettings).mockResolvedValue({
      ...base,
      storage_engine: "turso",
    });
    await useDraftStore.getState().commit();

    await useDraftStore.getState().resolveConflict("overwrite");

    expect(saveSettings).toHaveBeenCalledOnce();
    const s = useDraftStore.getState();
    expect(s.status).toBe("idle");
    expect(s.conflictServer).toBeNull();
    expect(s.draft).toBeNull();
    expect(s.base).toEqual(
      expect.objectContaining({ models: otherModels, manage_models: true }),
    );
  });

  it("resolves a conflict by adopting the server config", async () => {
    const base = makeSettings();
    useDraftStore.setState({ base });
    useDraftStore.getState().stage({ models: otherModels });
    const serverNow = { ...base, storage_engine: "turso" };
    vi.mocked(fetchSettings).mockResolvedValue(serverNow);
    await useDraftStore.getState().commit();

    await useDraftStore.getState().resolveConflict("discard");

    expect(saveSettings).not.toHaveBeenCalled();
    const s = useDraftStore.getState();
    expect(s.status).toBe("idle");
    expect(s.draft).toBeNull();
    expect(s.base).toEqual(serverNow);
  });

  it("keeps the draft when the drift check fails", async () => {
    useDraftStore.setState({ base: makeSettings() });
    useDraftStore.getState().stage({ models: otherModels });
    vi.mocked(fetchSettings).mockRejectedValue(new Error("boom"));

    const result = await useDraftStore.getState().commit();

    expect(result).toBeNull();
    const s = useDraftStore.getState();
    expect(s.status).toBe("error");
    expect(s.error).toBe("boom");
    expect(s.draft).not.toBeNull();
  });

  it("applies locally in demo mode without touching the server", async () => {
    vi.mocked(hasGateway).mockResolvedValue(false);
    useDraftStore.setState({ base: makeSettings() });
    useDraftStore.getState().stage({ models: otherModels });

    const result = await useDraftStore.getState().commit();

    expect(result).toBe("local");
    expect(saveSettings).not.toHaveBeenCalled();
    const s = useDraftStore.getState();
    expect(s.draft).toBeNull();
    expect(s.base).toEqual(
      expect.objectContaining({ models: otherModels, manage_models: true }),
    );
    expect(queryClient.getQueryData<SettingsDTO>(["settings"])).toEqual(
      expect.objectContaining({ manage_models: true }),
    );
  });
});

describe("draftStore.applyRemote", () => {
  it("mirrors remote staged state", () => {
    const base = makeSettings();
    useDraftStore.setState({ base });
    const remoteDraft = { ...base, models: otherModels };

    useDraftStore
      .getState()
      .applyRemote({ base, draft: remoteDraft, stagedAt: 42 });

    const s = useDraftStore.getState();
    expect(s.base).toBe(base);
    expect(s.draft).toBe(remoteDraft);
    expect(s.stagedAt).toBe(42);
  });

  it("invalidates the settings query when another tab commits", async () => {
    const base = makeSettings();
    useDraftStore.setState({ base });
    useDraftStore.getState().stage({ models: otherModels });
    const invalidate = vi
      .spyOn(queryClient, "invalidateQueries")
      .mockResolvedValue(undefined);

    useDraftStore.getState().applyRemote({ base, draft: null, stagedAt: null });

    expect(invalidate).toHaveBeenCalled();
    invalidate.mockRestore();
  });
});

describe("draftStore cross-tab storage events", () => {
  it("applies the persisted payload from another tab", () => {
    const base = makeSettings();
    useDraftStore.setState({ base });
    const remoteDraft = { ...base, models: otherModels };

    window.dispatchEvent(
      new StorageEvent("storage", {
        key: DRAFT_STORAGE_KEY,
        newValue: JSON.stringify({
          state: { base, draft: remoteDraft, stagedAt: 7 },
        }),
      }),
    );

    const s = useDraftStore.getState();
    expect(s.draft).toEqual(remoteDraft);
    expect(s.stagedAt).toBe(7);
  });

  it("ignores unrelated keys", () => {
    useDraftStore.setState({ base: makeSettings() });
    useDraftStore.getState().stage({ models: otherModels });
    const before = useDraftStore.getState().draft;

    window.dispatchEvent(
      new StorageEvent("storage", { key: "unrelated", newValue: "{}" }),
    );

    expect(useDraftStore.getState().draft).toBe(before);
  });
});
