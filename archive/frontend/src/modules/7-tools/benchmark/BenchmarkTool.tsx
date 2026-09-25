import React, { useEffect, useMemo } from 'react';
import { BenchmarkForm } from './BenchmarkForm';
import { BenchmarkResults } from './BenchmarkResults';
import { useBenchmarkRun } from './useBenchmarkRun';
import { summarize } from './benchmarkStats';
import { MetricPill } from '../shared/MetricPill';
import { useModelOptions } from '../shared/useModelOptions';
import {
  useBenchmarkActions,
  useBenchmarkCompleted,
  useBenchmarkModel,
  useBenchmarkResults,
  useBenchmarkStatus,
  useBenchmarkTotal,
  useBenchmarkWallClockMs,
} from '@/core/state/store';
import type { BenchmarkRunState } from '@/core/state/benchmarkSlice';

const STATUS_LABEL: Record<BenchmarkRunState, string> = {
  idle: 'Idle',
  running: 'Running',
  done: 'Completed',
  aborted: 'Stopped',
};

/**
 * BenchmarkTool
 * Fires N requests at concurrency C through the gateway and reports the same
 * latency/token metrics the chat waterfall shows, aggregated over the run.
 *
 * Vercel React Best Practices:
 * - rerender-memo
 * - rerender-defer-reads: atomic selector hooks
 * - rendering-conditional-render
 */
export default React.memo(function BenchmarkTool() {
  const model = useBenchmarkModel();
  const results = useBenchmarkResults();
  const status = useBenchmarkStatus();
  const total = useBenchmarkTotal();
  const completed = useBenchmarkCompleted();
  const wallClockMs = useBenchmarkWallClockMs();
  const { options, firstAvailableId } = useModelOptions();
  const { setBenchmarkModel } = useBenchmarkActions();
  const { start, stop } = useBenchmarkRun();

  // Same contract as the chat tool: normalise the selection when the catalog
  // changes, without writing the store during render.
  useEffect(() => {
    if (!firstAvailableId) return;
    if (!model || !options.some((o) => o.id === model)) {
      setBenchmarkModel(firstAvailableId);
    }
  }, [model, options, firstAvailableId, setBenchmarkModel]);

  // Derived, never stored: the cards must not be able to drift from the results.
  const summary = useMemo(() => summarize(results), [results]);

  return (
    <div className="space-y-4 font-mono">
      <BenchmarkForm onStart={start} onStop={stop} />

      <div className="flex items-center justify-between text-xs">
        <span className="text-neutral-500">{STATUS_LABEL[status]}</span>
        <span aria-live="polite" className="text-neutral-400 tabular-nums">
          {total > 0 ? `${completed}/${total}` : '—'}
        </span>
      </div>

      <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-2 text-center">
        <MetricPill
          label="Wall clock"
          value={wallClockMs !== null ? `${(wallClockMs / 1000).toFixed(2)}s` : '—'}
        />
        <MetricPill
          label="TTFT p50"
          value={summary.ttftP50 !== null ? `${summary.ttftP50}ms` : '—'}
        />
        <MetricPill
          label="TTFT p95"
          value={summary.ttftP95 !== null ? `${summary.ttftP95}ms` : '—'}
        />
        <MetricPill
          label="Aggregate TPS"
          value={summary.aggregateTps !== null ? `${summary.aggregateTps} t/s` : '—'}
        />
        <MetricPill
          label="Error rate"
          value={summary.errorRate !== null ? `${Math.round(summary.errorRate * 100)}%` : '—'}
        />
        <MetricPill label="Tokens" value={summary.totalTokens} />
      </div>

      <BenchmarkResults results={results} />
    </div>
  );
});
