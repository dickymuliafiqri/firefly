import { useCallback, useMemo } from 'react';
import {
  useCombos,
  useModels,
  useUpstreamBreakers,
  useUpstreams,
} from '@/core/state/store';
import type { ComboDTO, ModelDTO } from '@/services/schema';

export interface ModelOption {
  id: string;
  kind: 'combo' | 'model';
  available: boolean;
}

export interface ModelOptions {
  models: ModelDTO[];
  combos: ComboDTO[];
  enabledModels: ModelDTO[];
  enabledCombos: ComboDTO[];
  isModelAvailable: (model: ModelDTO) => boolean;
  isComboAvailable: (combo: ComboDTO) => boolean;
  options: ModelOption[];
  firstAvailableId: string | null;
}

/**
 * useModelOptions
 * Single source of truth for what this gateway can actually serve: a model is
 * available when it is enabled and at least one of its upstreams is enabled with a
 * circuit breaker that is not OPEN; a combo is available when at least one member
 * model is. Extracted verbatim from ChatWindow so the Benchmark tool can reuse it.
 */
export function useModelOptions(): ModelOptions {
  const models = useModels();
  const combos = useCombos();
  const upstreams = useUpstreams();
  const upstreamBreakers = useUpstreamBreakers();

  const isUpstreamActive = useCallback(
    (name: string) => {
      const u = upstreams.find((up) => up.name === name);
      if (u && u.enabled === false) return false;
      const st = upstreamBreakers[name] || 'CLOSED';
      return st !== 'OPEN';
    },
    [upstreams, upstreamBreakers]
  );

  const isModelAvailable = useCallback(
    (model: ModelDTO) => {
      if (model.enabled === false) return false;
      const candidates = [model.upstream, ...(model.fallback_upstreams || [])].filter(Boolean);
      if (candidates.length === 0) return false;
      return candidates.some(isUpstreamActive);
    },
    [isUpstreamActive]
  );

  const isComboAvailable = useCallback(
    (combo: ComboDTO) => {
      if (combo.enabled === false) return false;
      if (!combo.models || combo.models.length === 0) return false;
      return combo.models.some((memberPublicName) => {
        const m = models.find((mod) => mod.public_name === memberPublicName);
        return m ? isModelAvailable(m) : false;
      });
    },
    [models, isModelAvailable]
  );

  const enabledModels = useMemo(() => models.filter((m) => m.enabled !== false), [models]);
  const enabledCombos = useMemo(() => combos.filter((c) => c.enabled !== false), [combos]);

  const options = useMemo<ModelOption[]>(
    () => [
      ...enabledCombos.map((c) => ({
        id: c.name,
        kind: 'combo' as const,
        available: isComboAvailable(c),
      })),
      ...enabledModels.map((m) => ({
        id: m.public_name,
        kind: 'model' as const,
        available: isModelAvailable(m),
      })),
    ],
    [enabledCombos, enabledModels, isComboAvailable, isModelAvailable]
  );

  // Falls back to the first configured option when nothing is available, matching the
  // old auto-pick, so the dropdown is never empty while the catalog is.
  const firstAvailableId = useMemo(
    () => options.find((o) => o.available)?.id ?? options[0]?.id ?? null,
    [options]
  );

  return {
    models,
    combos,
    enabledModels,
    enabledCombos,
    isModelAvailable,
    isComboAvailable,
    options,
    firstAvailableId,
  };
}
