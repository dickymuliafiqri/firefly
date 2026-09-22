import React, { useMemo } from 'react';
import { cn } from '@/lib/utils';
import type { BenchmarkRequestResult } from './benchmarkRunner';

export interface BenchmarkResultsProps {
  results: BenchmarkRequestResult[];
}

const STATUS_CLASS: Record<BenchmarkRequestResult['status'], string> = {
  ok: 'text-emerald-300 border-emerald-400/30',
  error: 'text-rose-300 border-rose-400/30',
  aborted: 'text-neutral-400 border-white/[0.08]',
};

/**
 * BenchmarkResults
 * One row per request: a two-segment latency bar (TTFT in the waterfall's cyan
 * accent, generation in the neutral tone) over the raw numbers, all on one shared
 * time scale so bars are comparable.
 *
 * Vercel React Best Practices:
 * - rerender-memo
 * - rendering-conditional-render
 */
export const BenchmarkResults = React.memo(function BenchmarkResults({
  results,
}: BenchmarkResultsProps) {
  // The slowest request sets the scale; 1 avoids a divide by zero before the first
  // result lands.
  const maxTotalMs = useMemo(
    () => results.reduce((max, r) => Math.max(max, r.totalMs), 0) || 1,
    [results]
  );

  if (results.length === 0) {
    return (
      <div className="p-6 rounded-xl bg-transparent border border-white/[0.06] text-center text-neutral-500 text-[11px] font-mono">
        No run yet. Configure the benchmark above and press Run.
      </div>
    );
  }

  return (
    <div className="p-4 rounded-xl bg-transparent border border-white/[0.06] flex flex-col font-mono text-xs space-y-3">
      <div className="flex items-center justify-between pb-2 border-b border-white/[0.04]">
        <h4 className="text-xs uppercase tracking-wider text-neutral-300 font-medium">
          Per-request latency
        </h4>
        <div className="flex items-center gap-3 text-[10px] text-neutral-500">
          <span className="flex items-center gap-1.5">
            <span className="w-2 h-2 rounded-sm bg-cyan-400/80" /> TTFT
          </span>
          <span className="flex items-center gap-1.5">
            <span className="w-2 h-2 rounded-sm bg-white/[0.35]" /> Generation
          </span>
        </div>
      </div>

      <div className="space-y-1">
        {results.map((r) => {
          const ttft = r.ttftMs ?? 0;
          const generation = Math.max(0, r.totalMs - ttft);
          const isOk = r.status === 'ok';
          return (
            <div
              key={r.index}
              className="flex items-center gap-2 text-[11px] hover:bg-white/[0.015] p-0.5 rounded"
            >
              <span className="w-8 text-neutral-500 text-right tabular-nums text-[10px]">
                #{r.index}
              </span>
              <div className="flex-1 h-2 rounded-sm bg-white/[0.04] overflow-hidden flex">
                <div
                  className={cn('h-full', isOk ? 'bg-cyan-400/80' : 'bg-rose-400/70')}
                  style={{ width: `${(ttft / maxTotalMs) * 100}%` }}
                />
                <div
                  className={cn('h-full', isOk ? 'bg-white/[0.35]' : 'bg-rose-400/40')}
                  style={{ width: `${(generation / maxTotalMs) * 100}%` }}
                />
              </div>
              <span className="w-16 text-right tabular-nums text-[10px] text-neutral-300">
                {r.totalMs}ms
              </span>
            </div>
          );
        })}
      </div>

      <div className="overflow-x-auto pt-1">
        <table className="w-full text-[11px]">
          <thead>
            <tr className="text-neutral-500 text-[10px] uppercase tracking-wider">
              <th scope="col" className="text-left font-normal py-1">
                #
              </th>
              <th scope="col" className="text-left font-normal py-1">
                Status
              </th>
              <th scope="col" className="text-right font-normal py-1">
                TTFT
              </th>
              <th scope="col" className="text-right font-normal py-1">
                Tokens
              </th>
              <th scope="col" className="text-right font-normal py-1">
                Duration
              </th>
              <th scope="col" className="text-right font-normal py-1">
                TPS
              </th>
            </tr>
          </thead>
          <tbody>
            {results.map((r) => (
              <tr
                key={r.index}
                className={cn(
                  'border-t border-white/[0.04]',
                  r.status === 'aborted' && 'opacity-60'
                )}
              >
                <td className="py-1 text-neutral-500 tabular-nums">{r.index}</td>
                <td className="py-1">
                  <span
                    className={cn(
                      'px-1.5 py-0.5 rounded text-[10px] border',
                      STATUS_CLASS[r.status]
                    )}
                  >
                    {r.status}
                  </span>
                  {r.errorMessage ? (
                    <span className="ml-2 text-neutral-500">{r.errorMessage}</span>
                  ) : null}
                </td>
                <td className="py-1 text-right tabular-nums text-neutral-300">
                  {r.ttftMs !== null ? `${r.ttftMs}ms` : '—'}
                </td>
                <td className="py-1 text-right tabular-nums text-neutral-300">{r.tokens}</td>
                <td className="py-1 text-right tabular-nums text-neutral-300">{r.totalMs}ms</td>
                <td className="py-1 text-right tabular-nums text-neutral-300">
                  {r.tps !== null ? r.tps : '—'}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
});
