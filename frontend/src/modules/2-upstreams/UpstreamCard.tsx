import React from 'react';
import type { UpstreamDTO } from '@/services/schema';
import { KeyRingSlotList } from './KeyRingSlotList';
import { Edit3, Trash2 } from 'lucide-react';
import { cn } from '@/lib/utils';

export interface UpstreamCardProps {
  upstream: UpstreamDTO;
  breakerState?: 'CLOSED' | 'HALF-OPEN' | 'OPEN';
  onToggleBreaker?: (name: string) => void;
  onEdit?: (upstream: UpstreamDTO) => void;
  onDelete?: (upstream: UpstreamDTO) => void;
}

export const UpstreamCard = React.memo(function UpstreamCard({
  upstream,
  breakerState = 'CLOSED',
  onToggleBreaker,
  onEdit,
  onDelete,
}: UpstreamCardProps) {
  const isAnthropic = upstream.protocol === 'anthropic';
  const baseUrl = upstream.base_url || (upstream.base_urls && upstream.base_urls[0]) || '';
  const timeoutSec = Math.round((upstream.timeout_ms || 30000) / 1000);
  const streamTimeoutSec = Math.round((upstream.stream_idle_timeout_ms || 120000) / 1000);

  const isEnabled = upstream.enabled !== false && breakerState === 'CLOSED';
  const isHalfOpen = breakerState === 'HALF-OPEN';
  const keyCount =
    upstream.credential_pool?.length ||
    upstream.api_keys?.length ||
    (upstream.api_key ? 1 : 0);
  const strategy = upstream.key_strategy || 'round_robin';

  return (
    <div className="group flex flex-col justify-between p-4 rounded-xl bg-transparent border border-white/[0.06] hover:border-white/[0.14] hover:bg-white/[0.015] transition-all duration-200 font-mono text-xs select-none">
      {/* Top Header Row */}
      <div className="flex items-baseline justify-between pb-3 border-b border-white/[0.04]">
        <div className="flex items-baseline gap-2.5 truncate">
          <span className="text-white font-medium text-[13px] tracking-tight">
            {upstream.name}
          </span>
          <span className="text-[11px] text-neutral-500 uppercase">
            {upstream.protocol?.toUpperCase() || (isAnthropic ? 'ANTHROPIC' : 'OPENAI')}
          </span>
        </div>

        <div className="flex items-center gap-2.5 flex-shrink-0">
          <button
            type="button"
            onClick={() => onToggleBreaker?.(upstream.name)}
            className="flex items-center gap-1.5 text-[11px] tabular-nums hover:opacity-80 transition-opacity cursor-pointer"
            title={isEnabled ? 'Click to disable' : 'Click to enable'}
          >
            <span
              className={cn(
                'w-1.5 h-1.5 rounded-full',
                isEnabled ? 'bg-emerald-400' : isHalfOpen ? 'bg-amber-400' : 'bg-neutral-600'
              )}
            />
            <span
              className={cn(
                isEnabled
                  ? 'text-emerald-400/90'
                  : isHalfOpen
                  ? 'text-amber-400/90'
                  : 'text-neutral-500'
              )}
            >
              {isEnabled ? 'ACTIVE' : isHalfOpen ? 'HALF-OPEN' : 'DISABLED'}
            </span>
          </button>

          <button
            type="button"
            onClick={() => onEdit?.(upstream)}
            className="text-neutral-400 hover:text-white transition-colors p-1 rounded hover:bg-white/[0.05] cursor-pointer"
            title="Edit Upstream"
            aria-label={`Edit ${upstream.name}`}
          >
            <Edit3 className="w-3.5 h-3.5" />
          </button>

          <button
            type="button"
            onClick={() => onDelete?.(upstream)}
            className="text-neutral-500 hover:text-rose-400 transition-colors p-1 rounded hover:bg-white/[0.05] cursor-pointer"
            title="Delete Upstream"
            aria-label={`Delete ${upstream.name}`}
          >
            <Trash2 className="w-3.5 h-3.5" />
          </button>
        </div>
      </div>

      {/* Target Endpoint Route */}
      <div className="flex items-center gap-2 py-2.5 font-mono text-xs overflow-x-auto text-neutral-400">
        <span className="text-neutral-500 text-[11px]">Endpoint:</span>
        <span className="text-neutral-200 font-medium truncate" title={baseUrl}>
          {baseUrl || 'Default Provider Endpoint'}
        </span>
        {upstream.base_urls && upstream.base_urls.length > 1 ? (
          <span className="text-neutral-500 text-[11px] flex-shrink-0">
            (+{upstream.base_urls.length - 1} failover)
          </span>
        ) : null}
      </div>

      {/* KeyRing Credential Pool Preview */}
      {upstream.credential_pool && upstream.credential_pool.length > 0 ? (
        <KeyRingSlotList
          slots={upstream.credential_pool}
          strategy={upstream.key_strategy}
        />
      ) : null}

      {/* Capabilities & Timeouts Footer */}
      <div className="flex flex-wrap items-center gap-2.5 pt-2.5 border-t border-white/[0.04] text-[11px] text-neutral-500">
        <span>
          {keyCount} {keyCount === 1 ? 'key' : 'keys'} ({strategy})
        </span>
        <span>·</span>
        <span>{timeoutSec}s ttfb</span>
        <span>·</span>
        <span>{streamTimeoutSec}s idle</span>
        {upstream.credential_max_concurrent ? (
          <span className="ml-auto tabular-nums text-[10px] text-neutral-500">
            {upstream.credential_max_concurrent} max inflight
          </span>
        ) : null}
      </div>
    </div>
  );
});
