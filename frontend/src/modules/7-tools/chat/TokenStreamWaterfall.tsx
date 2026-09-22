import React from 'react';
import { Zap } from 'lucide-react';
import { cn } from '@/lib/utils';
import type { ChunkTiming } from '@/core/state/playgroundSlice';

export type { ChunkTiming };

export interface TokenStreamWaterfallProps {
  ttftMs: number | null;
  totalDurationMs: number | null;
  tps: number | null;
  totalTokens: number;
  timings: ChunkTiming[];
  isStreaming: boolean;
}

/**
 * TokenStreamWaterfall
 * Visualizes inter-token chunk arrival timing distribution, Time-to-First-Token (TTFT),
 * and tokens-per-second (TPS) throughput metrics.
 *
 * Vercel React Best Practices:
 * - rerender-memo
 * - rendering-conditional-render
 */
export const TokenStreamWaterfall = React.memo(function TokenStreamWaterfall({
  ttftMs,
  totalDurationMs,
  tps,
  totalTokens,
  timings,
  isStreaming,
}: TokenStreamWaterfallProps) {
  // Find max inter-token delta for relative bar scaling
  // If there are multiple tokens, exclude timings[0] (which represents TTFT) so inter-token jitter isn't squished.
  let maxDelta = 50;
  const jitterTokens = timings.length > 1 ? timings.slice(1) : timings;
  for (let i = 0; i < jitterTokens.length; i++) {
    if (jitterTokens[i].deltaMs > maxDelta) {
      maxDelta = jitterTokens[i].deltaMs;
    }
  }

  return (
    <div className="p-4 rounded-xl bg-transparent border border-white/[0.06] flex flex-col font-mono text-xs space-y-3 select-none">
      <div className="flex items-center justify-between pb-2 border-b border-white/[0.04]">
        <div className="flex items-center gap-2">
          <Zap className="w-4 h-4 text-neutral-400" />
          <h4 className="text-xs uppercase tracking-wider text-neutral-300 font-medium">
            Token Stream Waterfall
          </h4>
        </div>

        <div className="flex items-center gap-1.5 text-[11px] tabular-nums">
          <span
            className={cn(
              'w-1.5 h-1.5 rounded-full',
              isStreaming
                ? 'bg-emerald-400'
                : totalTokens > 0
                ? 'bg-neutral-400'
                : 'bg-neutral-600'
            )}
          />
          <span
            className={cn(
              isStreaming
                ? 'text-emerald-400/90'
                : 'text-neutral-400'
            )}
          >
            {isStreaming ? 'Streaming' : totalTokens > 0 ? 'Complete' : 'Idle'}
          </span>
        </div>
      </div>

      {/* Live Telemetry Pill Grid */}
      <div className="grid grid-cols-2 sm:grid-cols-4 gap-2 text-center">
        <div className="p-2 rounded-lg bg-transparent border border-white/[0.04]">
          <div className="text-[10px] text-neutral-500">TTFT</div>
          <div className="text-neutral-200 font-medium text-sm tabular-nums mt-0.5">
            {ttftMs !== null ? `${ttftMs}ms` : '—'}
          </div>
        </div>

        <div className="p-2 rounded-lg bg-transparent border border-white/[0.04]">
          <div className="text-[10px] text-neutral-500">Velocity</div>
          <div className="text-neutral-200 font-medium text-sm tabular-nums mt-0.5">
            {tps !== null ? `${tps} t/s` : '—'}
          </div>
        </div>

        <div className="p-2 rounded-lg bg-transparent border border-white/[0.04]">
          <div className="text-[10px] text-neutral-500">Chunks</div>
          <div className="text-neutral-200 font-medium text-sm tabular-nums mt-0.5">
            {totalTokens}
          </div>
        </div>

        <div className="p-2 rounded-lg bg-transparent border border-white/[0.04]">
          <div className="text-[10px] text-neutral-500">Elapsed</div>
          <div className="text-neutral-200 font-medium text-sm tabular-nums mt-0.5">
            {totalDurationMs !== null ? `${(totalDurationMs / 1000).toFixed(2)}s` : '—'}
          </div>
        </div>
      </div>

      {/* Waterfall Timing Bars */}
      <div className="space-y-1.5 pt-1">
        <div className="flex items-center justify-between text-[11px] text-neutral-500">
          <span>Inter-token jitter:</span>
          <span>Max: {maxDelta}ms</span>
        </div>

        {timings.length === 0 ? (
          <div className="p-6 text-center text-neutral-500 text-[11px] rounded-lg bg-transparent border border-white/[0.04]">
            Send a prompt to observe real-time token arrival latency.
          </div>
        ) : (
          <div className="max-h-[180px] overflow-y-auto space-y-1 pr-1">
            {timings.slice(-40).map((item) => {
              const isFirstToken = item.index === 1;
              const widthPct = isFirstToken
                ? 100
                : Math.min(100, Math.max(8, Math.round((item.deltaMs / maxDelta) * 100)));
              const isHighLatency = !isFirstToken && item.deltaMs > 150;

              return (
                <div
                  key={item.index}
                  className="flex items-center gap-2 text-[11px] hover:bg-white/[0.015] p-0.5 rounded"
                >
                  <span className="w-8 text-neutral-500 text-right tabular-nums text-[10px]">
                    #{item.index}
                  </span>

                  {/* Latency bar */}
                  <div className="flex-1 h-2 rounded-sm bg-white/[0.04] overflow-hidden relative">
                    <div
                      className={cn(
                        'h-full rounded-sm transition-all duration-150',
                        isHighLatency
                          ? 'bg-amber-400/80'
                          : isFirstToken
                          ? 'bg-cyan-400/80'
                          : 'bg-white/[0.35]'
                      )}
                      style={{ width: `${widthPct}%` }}
                    />
                  </div>

                  <span
                    className={cn(
                      'w-16 text-right tabular-nums text-[10px]',
                      isHighLatency
                        ? 'text-amber-300 font-medium'
                        : isFirstToken
                        ? 'text-cyan-300 font-medium'
                        : 'text-neutral-300'
                    )}
                  >
                    {isFirstToken ? `${item.deltaMs}ms` : `${item.deltaMs}ms`}
                  </span>
                </div>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
});
