import React from 'react';
import { cn } from '@/lib/utils';

export interface MetricPillProps {
  label: string;
  value: React.ReactNode;
  className?: string;
}

/**
 * MetricPill
 * The metric card primitive both tools render: a 10px muted label over a tabular
 * value, on the shared translucent panel surface.
 */
export const MetricPill = React.memo(function MetricPill({
  label,
  value,
  className,
}: MetricPillProps) {
  return (
    <div className={cn('p-2 rounded-lg bg-transparent border border-white/[0.04]', className)}>
      <div className="text-[10px] text-neutral-500">{label}</div>
      <div className="text-neutral-200 font-medium text-sm tabular-nums mt-0.5">{value}</div>
    </div>
  );
});
