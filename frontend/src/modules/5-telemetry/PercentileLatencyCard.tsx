import React from 'react';
import { Clock, Layers } from 'lucide-react';
import type { ModelTelemetryDTO } from '@/services/schema';
import { cn } from '@/lib/utils';

export interface PercentileLatencyCardProps {
  models?: ModelTelemetryDTO[];
}

function formatLatency(ms: number) {
  if (ms <= 0) {
    return <span className="text-neutral-600 font-mono text-[11px]">—</span>;
  }
  const rounded = Math.round(ms);
  return (
    <span
      className={cn(
        'tabular-nums font-mono text-[11px]',
        rounded >= 800
          ? 'text-amber-400 font-medium'
          : rounded >= 400
          ? 'text-neutral-200'
          : 'text-neutral-300'
      )}
    >
      {rounded}ms
    </span>
  );
}

/**
 * PercentileLatencyCard
 * Displays real P50, P90, P99 percentile latency and request distributions per configured model.
 * Eliminates dummy data in favor of live gateway histogram observations.
 *
 * Vercel React Best Practices:
 * - rerender-memo
 * - rendering-conditional-render
 */
export const PercentileLatencyCard = React.memo(function PercentileLatencyCard({
  models = [],
}: PercentileLatencyCardProps) {
  return (
    <div className="p-5 rounded-xl bg-transparent border border-white/[0.06] flex flex-col font-mono text-xs select-none">
      <div className="flex items-center justify-between pb-3 border-b border-white/[0.04]">
        <div className="flex items-center gap-2">
          <Clock className="w-4 h-4 text-neutral-400" />
          <h3 className="text-xs font-mono uppercase tracking-wider text-neutral-300 font-medium">
            Model Latency &amp; Request Breakdown
          </h3>
        </div>
        <span className="text-[10px] font-mono text-neutral-500">
          Live Prometheus Quantiles
        </span>
      </div>

      {models.length === 0 ? (
        <div className="p-8 text-center text-neutral-500 text-[11px] rounded-lg bg-transparent border border-white/[0.04] mt-3">
          No models configured in catalog.
        </div>
      ) : (
        <div className="mt-3 overflow-x-auto">
          <table className="w-full text-left font-mono text-xs">
            <thead>
              <tr className="text-neutral-500 border-b border-white/[0.04] text-[11px]">
                <th className="pb-2 font-normal">Model</th>
                <th className="pb-2 font-normal">Upstream</th>
                <th className="pb-2 font-normal text-right">P50</th>
                <th className="pb-2 font-normal text-right">P90</th>
                <th className="pb-2 font-normal text-right">P99</th>
                <th className="pb-2 font-normal text-right">Requests</th>
                <th className="pb-2 font-normal text-right">Errors</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-white/[0.04]">
              {models.map((row) => {
                const hasErrors = row.errors > 0;

                return (
                  <tr key={row.model} className="hover:bg-white/[0.015] transition-colors">
                    <td className="py-2.5 font-medium text-neutral-200 flex items-center gap-1.5">
                      <Layers className="w-3.5 h-3.5 text-neutral-500 shrink-0" />
                      <span className="truncate max-w-[130px]" title={row.model}>
                        {row.model}
                      </span>
                    </td>
                    <td className="py-2.5 text-neutral-500 text-[11px] truncate max-w-[100px]">
                      {row.upstream}
                    </td>
                    <td className="py-2.5 text-right tabular-nums">
                      {formatLatency(row.p50_ms)}
                    </td>
                    <td className="py-2.5 text-right tabular-nums">
                      {formatLatency(row.p90_ms)}
                    </td>
                    <td className="py-2.5 text-right tabular-nums">
                      {formatLatency(row.p99_ms)}
                    </td>
                    <td className="py-2.5 text-right text-neutral-300 tabular-nums text-[11px]">
                      {row.requests.toLocaleString()}
                    </td>
                    <td
                      className={cn(
                        'py-2.5 text-right tabular-nums text-[11px]',
                        hasErrors ? 'text-rose-400 font-medium' : 'text-neutral-500'
                      )}
                    >
                      {row.errors.toLocaleString()}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
});
