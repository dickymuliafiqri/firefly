import React from 'react';
import { Cpu } from 'lucide-react';
import { cn } from '@/lib/utils';

export interface InflightSemaphoreGaugeProps {
  activeInflight?: number;
  maxSlots?: number;
  queueDepth?: number;
}

/**
 * InflightSemaphoreGauge
 * Visualizes the Go GlobalLimiter admission gate capacity (default 1,500 slots).
 * Vercel React Best Practices:
 * - rerender-memo
 * - rendering-animate-svg-wrapper
 * - rendering-conditional-render
 */
export const InflightSemaphoreGauge = React.memo(function InflightSemaphoreGauge({
  activeInflight = 0,
  maxSlots = 1500,
  queueDepth = 0,
}: InflightSemaphoreGaugeProps) {
  const ratio = Math.min(1, Math.max(0, activeInflight / maxSlots));
  const percentage = Math.round(ratio * 100);
  const isWarning = ratio >= 0.8; // > 80% warning zone
  const isCritical = ratio >= 0.95;

  // Arc calculation for semi-circle gauge (radius 75, cx 100, cy 100)
  const radius = 75;
  const strokeWidth = 8;
  const circumference = Math.PI * radius; // Half-circle
  const strokeDashoffset = circumference - ratio * circumference;

  const statusColor = isCritical
    ? 'text-rose-400'
    : isWarning
      ? 'text-amber-400'
      : 'text-neutral-200';

  const strokeColor = isCritical
    ? '#f87171'
    : isWarning
      ? '#fbbf24'
      : '#34d399';

  return (
    <div className="p-5 rounded-xl bg-transparent border border-white/[0.06] flex flex-col justify-between font-mono text-xs select-none">
      <div className="flex items-center justify-between pb-3 border-b border-white/[0.04]">
        <div className="flex items-center gap-2">
          <Cpu className="w-4 h-4 text-neutral-400" />
          <h3 className="text-xs font-mono uppercase tracking-wider text-neutral-300 font-medium">
            Global Admission Semaphore
          </h3>
        </div>

        <div className="flex items-center gap-1.5 text-[11px] tabular-nums">
          <span
            className={cn(
              'w-1.5 h-1.5 rounded-full',
              isCritical
                ? 'bg-rose-400'
                : isWarning
                ? 'bg-amber-400'
                : 'bg-emerald-400'
            )}
          />
          <span
            className={cn(
              isCritical
                ? 'text-rose-400/90'
                : isWarning
                ? 'text-amber-400/90'
                : 'text-emerald-400/90'
            )}
          >
            {isCritical ? 'Saturated' : isWarning ? 'Warning (>80%)' : 'Nominal'}
          </span>
        </div>
      </div>

      {/* SVG Semi-Circle Gauge with wrapper div */}
      <div className="relative flex flex-col items-center justify-center my-3">
        <div className="w-48 h-28 relative flex items-center justify-center">
          <svg viewBox="0 0 200 115" className="w-full h-full overflow-visible">
            {/* Background Arc */}
            <path
              d="M 25 100 A 75 75 0 0 1 175 100"
              fill="none"
              stroke="rgba(255, 255, 255, 0.06)"
              strokeWidth={strokeWidth}
              strokeLinecap="round"
            />

            {/* Warning zone indicator at 80% */}
            <path
              d="M 160.68 55.92 A 75 75 0 0 1 175 100"
              fill="none"
              stroke="rgba(244, 63, 94, 0.2)"
              strokeWidth={strokeWidth}
              strokeLinecap="round"
            />

            {/* Filled Progress Arc */}
            <path
              d="M 25 100 A 75 75 0 0 1 175 100"
              fill="none"
              stroke={strokeColor}
              strokeWidth={strokeWidth}
              strokeDasharray={circumference}
              strokeDashoffset={strokeDashoffset}
              strokeLinecap="round"
              className="transition-all duration-700 ease-out"
            />
          </svg>

          {/* Center Value */}
          <div className="absolute inset-0 flex flex-col items-center justify-end pb-1 text-center">
            <span
              className={cn(
                'font-mono text-2xl font-bold tracking-tight tabular-nums transition-colors',
                statusColor
              )}
            >
              {activeInflight.toLocaleString()}
            </span>
            <span className="text-[10px] font-mono text-neutral-500">
              / {maxSlots.toLocaleString()} in-flight
            </span>
          </div>
        </div>

        <div className="text-[11px] font-mono text-neutral-500 flex items-center gap-1.5 mt-1">
          <span>Utilization:</span>
          <span className={cn('font-medium tabular-nums', statusColor)}>
            {percentage}%
          </span>
        </div>
      </div>

      {/* Footer Metrics Breakdown */}
      <div className="grid grid-cols-3 gap-2 pt-3 border-t border-white/[0.04] text-center font-mono text-xs">
        <div className="p-2 rounded-lg bg-transparent border border-white/[0.04]">
          <div className="text-[10px] text-neutral-500">Available</div>
          <div className="text-neutral-200 font-medium tabular-nums mt-0.5">
            {Math.max(0, maxSlots - activeInflight).toLocaleString()}
          </div>
        </div>

        <div className="p-2 rounded-lg bg-transparent border border-white/[0.04]">
          <div className="text-[10px] text-neutral-500">Queue Wait</div>
          <div className="text-neutral-300 font-medium tabular-nums mt-0.5">
            {queueDepth} reqs
          </div>
        </div>

        <div className="p-2 rounded-lg bg-transparent border border-white/[0.04]">
          <div className="text-[10px] text-neutral-500">Wait Timeout</div>
          <div className="text-neutral-300 font-medium tabular-nums mt-0.5">
            1.5s
          </div>
        </div>
      </div>
    </div>
  );
});
