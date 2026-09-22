import { useCallback } from 'react';
import { benchmarkRequest } from './benchmarkRunner';
import {
  useBenchmarkActions,
  useBenchmarkConcurrency,
  useBenchmarkModel,
  useBenchmarkPrompt,
  useBenchmarkRequests,
  usePlaygroundApiKey,
} from '@/core/state/store';

// Module scope, not React state: a run must survive switching tools (the tool
// component unmounts) and Stop must still reach it after coming back.
let activeRunId = 0;
let activeController: AbortController | null = null;

/**
 * useBenchmarkRun
 * Owns the worker pool: min(concurrency, requests) workers pull request indices from
 * a shared cursor, and every finished request is appended to the slice immediately so
 * results stream into the UI. The run is started by a click handler — never by an
 * effect — and it never touches `playgroundIsGenerating`, so chat and benchmark
 * cannot interfere with each other.
 */
export function useBenchmarkRun() {
  const model = useBenchmarkModel();
  const prompt = useBenchmarkPrompt();
  const requests = useBenchmarkRequests();
  const concurrency = useBenchmarkConcurrency();
  const apiKey = usePlaygroundApiKey();
  const { startBenchmark, appendBenchmarkResult, finishBenchmark } = useBenchmarkActions();

  const start = useCallback(async () => {
    if (activeController && !activeController.signal.aborted) return;
    const trimmedKey = apiKey.trim();
    if (!model || !trimmedKey || !prompt.trim()) return;

    const controller = new AbortController();
    activeController = controller;
    const runId = ++activeRunId;
    const isCurrentRun = () => runId === activeRunId;

    startBenchmark(requests);
    const startedAt = performance.now();

    let nextIndex = 1;
    const worker = async () => {
      while (!controller.signal.aborted && isCurrentRun()) {
        const index = nextIndex++;
        if (index > requests) return;

        const result = await benchmarkRequest({
          index,
          apiKey: trimmedKey,
          model,
          prompt,
          signal: controller.signal,
        });

        // A newer run has replaced this one: drop the stale result instead of
        // appending it to a fresh run's list.
        if (!isCurrentRun()) return;
        appendBenchmarkResult(result);
      }
    };

    await Promise.all(Array.from({ length: Math.min(concurrency, requests) }, worker));

    if (isCurrentRun()) {
      activeController = null;
      finishBenchmark(
        controller.signal.aborted ? 'aborted' : 'done',
        Math.round(performance.now() - startedAt)
      );
    }
  }, [
    model,
    prompt,
    requests,
    concurrency,
    apiKey,
    startBenchmark,
    appendBenchmarkResult,
    finishBenchmark,
  ]);

  const stop = useCallback(() => {
    activeController?.abort();
  }, []);

  return { start, stop };
}
