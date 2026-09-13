import React from 'react';
import { Activity, Zap, Cpu } from 'lucide-react';
import { cn } from '@/lib/utils';

export interface TelemetryStats {
  inputTokens: number;
  outputTokens: number;
  totalTokens: number;
  totalRequests: number;
  estimatedCostUsd: number;
  activeStreams: number;
  p95LatencyMs?: number;
}

export interface StatsFooterProps {
  stats?: Partial<TelemetryStats>;
  className?: string;
}

/**
 * Format large numbers cleanly with commas
 */
function formatNumber(num: number): string {
  return new Intl.NumberFormat('en-US').format(num);
}

/**
 * Format currency to 4 decimal places
 */
function formatCurrency(amount: number): string {
  return new Intl.NumberFormat('en-US', {
    style: 'currency',
    currency: 'USD',
    minimumFractionDigits: 4,
    maximumFractionDigits: 4,
  }).format(amount);
}

export const StatsFooter = React.memo(function StatsFooter({
  stats = {},
  className,
}: StatsFooterProps) {
  const {
    inputTokens = 0,
    outputTokens = 0,
    totalTokens = 0,
    totalRequests = 0,
    estimatedCostUsd = 0,
    activeStreams = 0,
    p95LatencyMs = 124,
  } = stats;

  return (
    <footer
      className={cn(
        'relative z-20 flex flex-wrap items-center justify-between gap-4 border-t border-white/5 bg-slate-950/60 backdrop-blur-md px-6 py-2.5 font-mono text-xs text-slate-400',
        className
      )}
    >
      {/* 5 Core Telemetry Metrics */}
      <div className="flex flex-wrap items-center gap-6">
        <div className="flex items-center gap-2">
          <span className="text-[10px] uppercase tracking-wider text-slate-300 font-semibold">Tokens In:</span>
          <span className="font-semibold text-slate-200 tabular-nums">
            {formatNumber(inputTokens)}
          </span>
        </div>

        <div className="flex items-center gap-2">
          <span className="text-[10px] uppercase tracking-wider text-slate-300 font-semibold">Tokens Out:</span>
          <span className="font-semibold text-biolum-glow tabular-nums">
            {formatNumber(outputTokens)}
          </span>
        </div>

        <div className="flex items-center gap-2">
          <span className="text-[10px] uppercase tracking-wider text-slate-300 font-semibold">Total Tokens:</span>
          <span className="font-semibold text-accent-cyan tabular-nums">
            {formatNumber(totalTokens)}
          </span>
        </div>

        <div className="flex items-center gap-2">
          <span className="text-[10px] uppercase tracking-wider text-slate-300 font-semibold">Requests:</span>
          <span className="font-semibold text-accent-violet tabular-nums">
            {formatNumber(totalRequests)}
          </span>
        </div>

        <div className="flex items-center gap-2">
          <span className="text-[10px] uppercase tracking-wider text-slate-300 font-semibold">Est. Cost:</span>
          <span className="font-semibold text-biolum-aura tabular-nums">
            {formatCurrency(estimatedCostUsd)}
          </span>
        </div>
      </div>

      {/* Gateway Engine Indicators */}
      <div className="flex items-center gap-4 text-[11px]">
        <div className="flex items-center gap-1.5">
          <Zap className="h-3.5 w-3.5 text-biolum-aura" />
          <span className="text-slate-300">Streams:</span>
          <span className="font-semibold text-white tabular-nums">{activeStreams}</span>
        </div>

        <div className="flex items-center gap-1.5">
          <Activity className="h-3.5 w-3.5 text-accent-emerald" />
          <span className="text-slate-300">P95:</span>
          <span className="font-semibold text-white tabular-nums">{p95LatencyMs}ms</span>
        </div>

        <div className="flex items-center gap-1.5 text-slate-300 hidden sm:flex">
          <Cpu className="h-3.5 w-3.5 text-slate-300" />
          <span>Go Data Plane (:8080)</span>
        </div>
      </div>
    </footer>
  );
});
