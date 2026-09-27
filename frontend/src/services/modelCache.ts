import { create } from 'zustand';
import { persist, createJSONStorage } from 'zustand/middleware';

interface ModelCacheState {
  /** Map of upstream name -> array of model identifier strings */
  cache: Record<string, string[]>;
  setModels: (upstreamName: string, models: string[]) => void;
  getModels: (upstreamName: string) => string[];
  clearCache: (upstreamName?: string) => void;
}

/**
 * Global reactive model cache backed by localStorage.
 * Prevents redundant network discovery requests across views (Upstreams, Upstream Editor, Models).
 */
export const useModelCacheStore = create<ModelCacheState>()(
  persist(
    (set, get) => ({
      cache: {},
      setModels: (upstreamName: string, models: string[]) => {
        if (!upstreamName) return;
        const clean = Array.from(new Set(models.filter(Boolean))).sort();
        set((state) => ({
          cache: {
            ...state.cache,
            [upstreamName]: clean,
          },
        }));
      },
      getModels: (upstreamName: string) => {
        if (!upstreamName) return [];
        return get().cache[upstreamName] ?? [];
      },
      clearCache: (upstreamName?: string) => {
        if (upstreamName) {
          set((state) => {
            const next = { ...state.cache };
            delete next[upstreamName];
            return { cache: next };
          });
        } else {
          set({ cache: {} });
        }
      },
    }),
    {
      name: 'firefly-model-cache',
      storage: createJSONStorage(() => localStorage),
    },
  ),
);

/** Direct non-reactive getters and setters for utility usage */
export function getCachedModels(upstreamName: string): string[] {
  return useModelCacheStore.getState().getModels(upstreamName);
}

export function setCachedModels(upstreamName: string, models: string[]): void {
  useModelCacheStore.getState().setModels(upstreamName, models);
}

export function getAllCachedModels(): Record<string, string[]> {
  return useModelCacheStore.getState().cache;
}

/**
 * Pick optimal free/lightweight probe model from a list of discovered models.
 * Prioritizes:
 * 1. Contains 'free' (e.g. 'meta-llama/llama-3-8b-instruct:free')
 * 2. Contains 'flash' (e.g. 'gemini-2.0-flash', 'gemini-1.5-flash')
 * 3. Contains 'mini', 'haiku', 'chat' (excluding embedding/moderation models)
 * 4. Fallback: first non-embedding model
 */
export function pickOptimalProbeModel(models: string[]): string | undefined {
  if (!models || models.length === 0) return undefined;

  // Filter out non-chat models (embeddings, moderation, rerank)
  const chatCandidates = models.filter((m) => {
    const lower = m.toLowerCase();
    return !lower.includes('embed') && !lower.includes('moderation') && !lower.includes('rerank');
  });
  const candidates = chatCandidates.length > 0 ? chatCandidates : models;

  // 1. Primary choice: keyword 'free'
  const freeModel = candidates.find((m) => m.toLowerCase().includes('free'));
  if (freeModel) return freeModel;

  // 2. Secondary choice: keyword 'flash'
  const flashModel = candidates.find((m) => m.toLowerCase().includes('flash'));
  if (flashModel) return flashModel;

  // 3. Fallbacks: lightweight standard models
  const miniModel = candidates.find((m) => m.toLowerCase().includes('mini'));
  if (miniModel) return miniModel;
  const haikuModel = candidates.find((m) => m.toLowerCase().includes('haiku'));
  if (haikuModel) return haikuModel;
  const chatModel = candidates.find((m) => m.toLowerCase().includes('chat'));
  if (chatModel) return chatModel;

  return candidates[0];
}
