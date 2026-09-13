import type { StateCreator } from 'zustand';
import type { LiveConnectionLog } from '@/services/schema';
import type { TelemetryStats } from '@/core/layout/StatsFooter';

export interface TelemetrySlice {
  stats: TelemetryStats;
  recentLogs: LiveConnectionLog[];

  updateStats: (partial: Partial<TelemetryStats>) => void;
  incrementUsage: (inputTokens: number, outputTokens: number, costUsd: number) => void;
  addLog: (log: LiveConnectionLog) => void;
  setRecentLogs: (logs: LiveConnectionLog[]) => void;
  clearLogs: () => void;
}

const INITIAL_STATS: TelemetryStats = {
  inputTokens: 0,
  outputTokens: 0,
  totalTokens: 0,
  totalRequests: 0,
  estimatedCostUsd: 0,
  activeStreams: 0,
  p95LatencyMs: 0,
};

const MAX_LOGS = 100;

export const createTelemetrySlice: StateCreator<TelemetrySlice, [], [], TelemetrySlice> = (set) => ({
  stats: INITIAL_STATS,
  recentLogs: [],

  updateStats: (partial) =>
    set((state) => ({
      stats: { ...state.stats, ...partial },
    })),

  incrementUsage: (inTokens, outTokens, costUsd) =>
    set((state) => ({
      stats: {
        ...state.stats,
        inputTokens: state.stats.inputTokens + inTokens,
        outputTokens: state.stats.outputTokens + outTokens,
        totalTokens: state.stats.totalTokens + inTokens + outTokens,
        totalRequests: state.stats.totalRequests + 1,
        estimatedCostUsd: state.stats.estimatedCostUsd + costUsd,
      },
    })),

  addLog: (log) =>
    set((state) => {
      // If log already exists (e.g. updating in-flight log upon completion), update in place
      const existingIdx = state.recentLogs.findIndex((l) => l.id === log.id);
      let nextLogs: LiveConnectionLog[];
      const nextStats = { ...state.stats };

      if (existingIdx >= 0) {
        const prev = state.recentLogs[existingIdx];
        nextLogs = [...state.recentLogs];
        nextLogs[existingIdx] = { ...prev, ...log };

        // If transitioning from in-flight (0) to completed (> 0)
        if (prev.status === 0 && log.status > 0) {
          const outTok = log.tokensOut || 0;
          const inTok = log.tokensIn || prev.tokensIn || 0;
          const cost =
            log.estimatedCost ||
            inTok * 0.0000025 + outTok * 0.00001;
          nextStats.outputTokens += outTok;
          nextStats.totalTokens = nextStats.inputTokens + nextStats.outputTokens;
          nextStats.estimatedCostUsd += cost;
          nextStats.activeStreams = Math.max(0, nextStats.activeStreams - 1);
        }
      } else {
        nextLogs = [log, ...state.recentLogs];
        if (nextLogs.length > MAX_LOGS) {
          nextLogs.length = MAX_LOGS;
        }

        // New request arrived
        nextStats.totalRequests += 1;
        const inTok = log.tokensIn || 0;
        nextStats.inputTokens += inTok;
        nextStats.totalTokens += inTok;
        if (log.status === 0) {
          nextStats.activeStreams += 1;
        } else {
          const outTok = log.tokensOut || 0;
          nextStats.outputTokens += outTok;
          nextStats.totalTokens = nextStats.inputTokens + nextStats.outputTokens;
          nextStats.estimatedCostUsd +=
            log.estimatedCost || inTok * 0.0000025 + outTok * 0.00001;
        }
      }

      return { recentLogs: nextLogs, stats: nextStats };
    }),

  setRecentLogs: (logs) =>
    set((state) => {
      if (!logs || logs.length === 0) return state;
      const map = new Map<string, LiveConnectionLog>();
      for (const l of state.recentLogs) {
        map.set(l.id, l);
      }
      for (const l of logs) {
        const existing = map.get(l.id);
        if (existing) {
          map.set(l.id, { ...existing, ...l });
        } else {
          map.set(l.id, l);
        }
      }
      const merged = Array.from(map.values()).sort((a, b) => b.timestamp - a.timestamp);
      return { recentLogs: merged.slice(0, MAX_LOGS) };
    }),

  clearLogs: () => set(() => ({ recentLogs: [] })),
});
