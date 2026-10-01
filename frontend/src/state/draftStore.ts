import { create } from "zustand";
import { persist, createJSONStorage } from "zustand/middleware";
import {
  fetchSettings,
  hasGateway,
  queryClient,
  saveSettings,
  useSettingsQuery,
} from "@/services/api";
import type { SettingsDTO } from "@/services/schema";

/**
 * Staged-configuration store ("draft first, commit later").
 *
 * Every editing surface writes into `draft` (a full SettingsDTO) instead of
 * calling PUT /api/settings. `base` is the last known server config: it drives
 * the dirty check and the drift detection performed right before a commit, so
 * changes written behind the dashboard's back (key harvester, second operator,
 * hot reload) surface as a conflict instead of being silently overwritten.
 *
 * The persisted slice (base/draft/stagedAt) lives in localStorage, which makes
 * the draft survive reloads and — via the `storage` event listener below —
 * propagate live to every other dashboard tab of the same origin.
 */

export type DraftStatus = "idle" | "saving" | "error" | "conflict";
/** 'saved' = committed to the gateway, 'local' = demo mode (no gateway). */
export type CommitResult = "saved" | "local" | null;

export const DRAFT_STORAGE_KEY = "firefly.settings-draft.v1";

const MANAGE_FLAG = {
  models: "manage_models",
  combos: "manage_combos",
  tenants: "manage_tenants",
  upstreams: "manage_upstreams",
} as const;
type ManagedCollection = keyof typeof MANAGE_FLAG;

interface DraftPersisted {
  base: SettingsDTO | null;
  draft: SettingsDTO | null;
  stagedAt: number | null;
}

interface DraftState extends DraftPersisted {
  status: DraftStatus;
  error: string | null;
  /** Server config that triggered the conflict, kept so "keep server" can adopt it. */
  conflictServer: SettingsDTO | null;

  stage: (patch: Partial<SettingsDTO>) => void;
  replaceAll: (config: SettingsDTO) => void;
  discard: () => void;
  commit: () => Promise<CommitResult>;
  resolveConflict: (mode: "overwrite" | "discard") => Promise<void>;
  syncServer: (server: SettingsDTO) => void;
  applyRemote: (next: DraftPersisted) => void;
}

/** Shallow copy without runtime-only fields that must never reach the server or the diff. */
function stripVolatile<T>(value: T): T {
  if (value && typeof value === "object" && !Array.isArray(value)) {
    const out = { ...(value as Record<string, unknown>) };
    delete out.__mock;
    return out as T;
  }
  return value;
}
const MANAGE_FLAG_KEYS = Object.values(MANAGE_FLAG);
/**
 * Copy without the derived manage_* flags. Used only by the revert-to-base
 * check: staging a collection auto-sets its flag, so an edit whose content
 * ended up identical to base must still be recognized as "nothing pending"
 * instead of lingering as a phantom draft. Explicit intent (replaceAll) and
 * commit drift checks keep comparing the full payload, flags included.
 */
function stripManageFlags(value: SettingsDTO): SettingsDTO {
  const out = { ...value };
  for (const flag of MANAGE_FLAG_KEYS) delete out[flag];
  return out;
}

function cloneJson<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

/** Key-order-insensitive JSON stringify so server payloads and staged edits compare equal. */
function stableStringify(value: unknown): string {
  const seen = new Set<object>();
  const walk = (v: unknown): unknown => {
    if (Array.isArray(v)) return v.map(walk);
    if (v && typeof v === "object") {
      const obj = v as Record<string, unknown>;
      if (seen.has(obj)) return null;
      seen.add(obj);
      const out: Record<string, unknown> = {};
      for (const key of Object.keys(obj).sort()) {
        if (key === "__mock") continue;
        out[key] = walk(obj[key]);
      }
      seen.delete(obj);
      return out;
    }
    return v;
  };
  return JSON.stringify(walk(value));
}

function stableEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (a == null || b == null) return a == null && b == null;
  return stableStringify(a) === stableStringify(b);
}

export const useDraftStore = create<DraftState>()(
  persist(
    (set, get) => {
      const finishCommit = (draft: SettingsDTO) => {
        // Optimistic: show the committed config immediately; the invalidated
        // query refetch then replaces it with the server-normalized payload.
        queryClient.setQueryData<SettingsDTO>(["settings"], draft);
        set({
          base: draft,
          draft: null,
          stagedAt: null,
          status: "idle",
          error: null,
          conflictServer: null,
        });
        void queryClient.invalidateQueries({ queryKey: ["settings"] });
      };

      return {
        base: null,
        draft: null,
        stagedAt: null,
        status: "idle",
        error: null,
        conflictServer: null,

        stage: (patch) => {
          const { base, draft, stagedAt } = get();
          const source = draft ?? base;
          if (!source) return;
          // Shallow merge (no deep clone): untouched branches keep their
          // object identity, so editor forms keyed on an upstream entry do not
          // reset just because models were staged elsewhere.
          const next: SettingsDTO = { ...source, ...patch };
          for (const key of Object.keys(MANAGE_FLAG) as ManagedCollection[]) {
            if (patch[key] !== undefined) next[MANAGE_FLAG[key]] = true;
          }
          if (
            base &&
            stableEqual(stripManageFlags(next), stripManageFlags(base))
          ) {
            // The edit was undone — nothing pending anymore.
            set({
              draft: null,
              stagedAt: null,
              status: "idle",
              error: null,
              conflictServer: null,
            });
            return;
          }
          set({ draft: next, stagedAt: stagedAt ?? Date.now() });
        },

        replaceAll: (config) => {
          const { base, stagedAt } = get();
          if (base && stableEqual(config, base)) {
            set({
              draft: null,
              stagedAt: null,
              status: "idle",
              error: null,
              conflictServer: null,
            });
            return;
          }
          set({ draft: config, stagedAt: stagedAt ?? Date.now() });
        },

        discard: () => {
          set({
            draft: null,
            stagedAt: null,
            status: "idle",
            error: null,
            conflictServer: null,
          });
        },

        commit: async () => {
          const { draft, base, status } = get();
          if (!draft || status === "saving") return null;
          set({ status: "saving", error: null });

          if (!(await hasGateway())) {
            // Demo mode: no gateway reachable from this origin — apply to the
            // local query cache only, exactly like the legacy smart save.
            finishCommit(draft);
            return "local";
          }

          // Drift check against the live server (bypasses the query cache).
          let server: SettingsDTO;
          try {
            server = stripVolatile(await fetchSettings());
          } catch (err) {
            set({
              status: "error",
              error:
                err instanceof Error
                  ? err.message
                  : "Could not verify the current server configuration.",
            });
            return null;
          }
          if (base && !stableEqual(server, base)) {
            set({ status: "conflict", conflictServer: server });
            return null;
          }

          try {
            await saveSettings(draft);
          } catch (err) {
            set({
              status: "error",
              error: err instanceof Error ? err.message : "Saving failed.",
            });
            return null;
          }
          finishCommit(draft);
          return "saved";
        },

        resolveConflict: async (mode) => {
          const { draft, conflictServer, status } = get();
          if (mode === "discard") {
            set({
              base: conflictServer ?? get().base,
              draft: null,
              stagedAt: null,
              status: "idle",
              error: null,
              conflictServer: null,
            });
            void queryClient.invalidateQueries({ queryKey: ["settings"] });
            return;
          }
          if (!draft || status === "saving") return;
          set({ status: "saving", error: null });
          try {
            await saveSettings(draft);
          } catch (err) {
            set({
              status: "error",
              error: err instanceof Error ? err.message : "Saving failed.",
            });
            return;
          }
          finishCommit(draft);
        },

        syncServer: (server) => {
          const { draft, base } = get();
          // While a draft exists the server snapshot is deliberately ignored:
          // only the commit path arbitrates server changes (drift check).
          if (!draft && !stableEqual(stripVolatile(server), base)) {
            set({ base: stripVolatile(server) });
          }
        },

        applyRemote: (next) => {
          const { base, draft } = get();
          if (stableEqual(next.base, base) && stableEqual(next.draft, draft))
            return;
          const hadDraft = draft != null;
          set({
            base: next.base,
            draft: next.draft,
            stagedAt: next.stagedAt ?? (next.draft ? Date.now() : null),
            status: "idle",
            error: null,
            conflictServer: null,
          });
          // Another tab committed — refresh this tab's server view too.
          if (hadDraft && next.draft == null) {
            void queryClient.invalidateQueries({ queryKey: ["settings"] });
          }
        },
      };
    },
    {
      name: DRAFT_STORAGE_KEY,
      version: 1,
      storage: createJSONStorage(() => localStorage),
      // Status/transients stay in memory; only the replayable state persists.
      partialize: (s): DraftPersisted => ({
        base: s.base,
        draft: s.draft,
        stagedAt: s.stagedAt,
      }),
    },
  ),
);

// Multi-tab: every other open dashboard tab mirrors the staged draft live.
if (typeof window !== "undefined") {
  window.addEventListener("storage", (event) => {
    if (event.key !== DRAFT_STORAGE_KEY || event.newValue == null) return;
    try {
      const parsed = JSON.parse(event.newValue) as {
        state?: Partial<DraftPersisted>;
      };
      if (!parsed?.state) return;
      useDraftStore.getState().applyRemote({
        base: parsed.state.base ?? null,
        draft: parsed.state.draft ?? null,
        stagedAt: parsed.state.stagedAt ?? null,
      });
    } catch {
      // Ignore malformed cross-tab payloads.
    }
  });
}

/** Draft-aware settings view: editing surfaces render the staged config when one exists. */
export function useSettingsView() {
  const query = useSettingsQuery();
  const draft = useDraftStore((s) => s.draft);
  if (!draft) return query;
  return { ...query, data: draft, isLoading: false, error: null };
}

/** Test helper: fully reset in-memory and persisted draft state. */
export function resetDraftStoreForTests(): void {
  useDraftStore.setState({
    base: null,
    draft: null,
    stagedAt: null,
    status: "idle",
    error: null,
    conflictServer: null,
  });
  try {
    localStorage.removeItem(DRAFT_STORAGE_KEY);
  } catch {
    // Non-browser environment.
  }
}

export { cloneJson, stableEqual, stableStringify };
