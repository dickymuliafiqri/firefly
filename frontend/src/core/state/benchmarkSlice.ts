import type { StateCreator } from 'zustand';
import type { BenchmarkRequestResult } from '@/modules/7-tools/benchmark/benchmarkRunner';

export type BenchmarkRunState = 'idle' | 'running' | 'done' | 'aborted';

export const BENCHMARK_MIN_REQUESTS = 1;
export const BENCHMARK_MAX_REQUESTS = 100;
export const BENCHMARK_MIN_CONCURRENCY = 1;
export const BENCHMARK_MAX_CONCURRENCY = 20;

const clamp = (value: number, min: number, max: number) =>
  Math.min(Math.max(Math.round(value) || min, min), max);

export interface BenchmarkSlice {
  benchmarkModel: string;
  benchmarkPrompt: string;
  benchmarkRequests: number;
  benchmarkConcurrency: number;
  benchmarkStatus: BenchmarkRunState;
  benchmarkTotal: number;
  benchmarkCompleted: number;
  benchmarkResults: BenchmarkRequestResult[];
  benchmarkWallClockMs: number | null;

  setBenchmarkModel: (model: string) => void;
  setBenchmarkPrompt: (prompt: string) => void;
  setBenchmarkRequests: (count: number) => void;
  setBenchmarkConcurrency: (count: number) => void;
  startBenchmark: (total: number) => void;
  appendBenchmarkResult: (result: BenchmarkRequestResult) => void;
  finishBenchmark: (status: BenchmarkRunState, wallClockMs: number) => void;
  resetBenchmark: () => void;
}

export const createBenchmarkSlice: StateCreator<BenchmarkSlice, [], [], BenchmarkSlice> = (set) => ({
  benchmarkModel: '',
  benchmarkPrompt: 'Write a short paragraph about a firefly.',
  benchmarkRequests: 10,
  benchmarkConcurrency: 2,
  benchmarkStatus: 'idle',
  benchmarkTotal: 0,
  benchmarkCompleted: 0,
  benchmarkResults: [],
  benchmarkWallClockMs: null,

  setBenchmarkModel: (model) => set(() => ({ benchmarkModel: model })),
  setBenchmarkPrompt: (prompt) => set(() => ({ benchmarkPrompt: prompt })),
  setBenchmarkRequests: (count) =>
    set(() => ({ benchmarkRequests: clamp(count, BENCHMARK_MIN_REQUESTS, BENCHMARK_MAX_REQUESTS) })),
  setBenchmarkConcurrency: (count) =>
    set(() => ({
      benchmarkConcurrency: clamp(count, BENCHMARK_MIN_CONCURRENCY, BENCHMARK_MAX_CONCURRENCY),
    })),

  startBenchmark: (total) =>
    set(() => ({
      benchmarkStatus: 'running',
      benchmarkTotal: total,
      benchmarkCompleted: 0,
      benchmarkResults: [],
      benchmarkWallClockMs: null,
    })),

  appendBenchmarkResult: (result) =>
    set((state) => ({
      benchmarkResults: [...state.benchmarkResults, result],
      benchmarkCompleted: state.benchmarkCompleted + 1,
    })),

  finishBenchmark: (status, wallClockMs) =>
    set(() => ({ benchmarkStatus: status, benchmarkWallClockMs: wallClockMs })),

  resetBenchmark: () =>
    set(() => ({
      benchmarkStatus: 'idle',
      benchmarkTotal: 0,
      benchmarkCompleted: 0,
      benchmarkResults: [],
      benchmarkWallClockMs: null,
    })),
});
