import React from 'react';
import { Flame, Key, Activity } from 'lucide-react';
import type { UpstreamTelemetryDTO } from '@/services/schema';
import { cn } from '@/lib/utils';

export interface CooldownHeatmapProps {
  upstreams?: UpstreamTelemetryDTO[];
}

/**
 * CooldownHeatmap
 * Visualizes 429 Too Many Requests strike frequencies and cooldown states per API Key Slot in the KeyRing.
 * Displays real-time data from Firefly gateway telemetry without dummy data.
 *
 * Vercel React Best Practices:
 * - rerender-memo
 * - rendering-conditional-render
 */
export const CooldownHeatmap = React.memo(function CooldownHeatmap({
  upstreams = [],
}: CooldownHeatmapProps) {
  // Flatten slots across all upstreams
  const allSlots = React.useMemo(() => {
    const list: Array<{
      upstreamName: string;
      breakerState: string;
      ref: string;
      inflight: number;
      isCooldown: boolean;
      cooldownRemainingSec: number;
      isRevoked: boolean;
      totalCooldownEvents: number;
      requestsTotal: number;
    }> = [];

    for (const u of upstreams) {
      if (u.slots && u.slots.length > 0) {
        for (const s of u.slots) {
          list.push({
            upstreamName: u.name,
            breakerState: u.breaker_state,
            ref: s.ref,
            inflight: s.inflight,
            isCooldown: s.is_cooldown,
            cooldownRemainingSec: s.cooldown_remaining_sec,
            isRevoked: s.is_revoked,
            totalCooldownEvents: s.total_cooldown_events,
            requestsTotal: s.requests_total,
          });
        }
      } else {
        // Upstream without credential pool slots
        list.push({
          upstreamName: u.name,
          breakerState: u.breaker_state,
          ref: 'DEFAULT_SLOT',
          inflight: 0,
          isCooldown: false,
          cooldownRemainingSec: 0,
          isRevoked: false,
          totalCooldownEvents: 0,
          requestsTotal: u.total_requests,
        });
      }
    }

    return list;
  }, [upstreams]);

  return (
    <div className="p-5 rounded-xl bg-transparent border border-white/[0.06] flex flex-col font-mono text-xs select-none">
      <div className="flex flex-wrap items-center justify-between gap-2 pb-3 border-b border-white/[0.04]">
        <div className="flex items-center gap-2">
          <Flame className="w-4 h-4 text-neutral-400" />
          <h3 className="text-xs font-mono uppercase tracking-wider text-neutral-300 font-medium">
            Upstream KeyRing 429 Cooldown Status
          </h3>
        </div>
        <div className="flex items-center gap-3 text-[10px] font-mono text-neutral-500">
          <span className="flex items-center gap-1.5">
            <span className="w-1.5 h-1.5 rounded-full bg-emerald-400" />
            Healthy
          </span>
          <span className="flex items-center gap-1.5">
            <span className="w-1.5 h-1.5 rounded-full bg-amber-400" />
            Cooldown
          </span>
          <span className="flex items-center gap-1.5">
            <span className="w-1.5 h-1.5 rounded-full bg-rose-400" />
            Revoked
          </span>
        </div>
      </div>

      <p className="text-[11px] font-mono text-neutral-500 mt-2">
        Real-time per-slot quota health and lazy cooldown timers triggered by upstream{' '}
        <code className="text-neutral-400">Retry-After</code> headers.
      </p>

      {allSlots.length === 0 ? (
        <div className="p-8 text-center text-neutral-500 text-[11px] rounded-lg bg-transparent border border-white/[0.04] mt-3">
          No upstreams or credential slots configured.
        </div>
      ) : (
        /* Heatmap Grid */
        <div className="grid grid-cols-1 sm:grid-cols-2 md:grid-cols-3 gap-2.5 mt-3">
          {allSlots.map((slot) => {
            const isBreakerOpen = slot.breakerState === 'OPEN';

            return (
              <div
                key={`${slot.upstreamName}-${slot.ref}`}
                className={cn(
                  'p-3 rounded-xl border bg-transparent hover:border-white/[0.12] transition-colors flex flex-col justify-between font-mono text-xs',
                  slot.isRevoked
                    ? 'border-rose-500/30'
                    : slot.isCooldown
                    ? 'border-amber-500/30'
                    : isBreakerOpen
                    ? 'border-rose-500/20'
                    : 'border-white/[0.06]'
                )}
              >
                <div className="flex items-start justify-between gap-1">
                  <div className="truncate">
                    <div className="text-[10px] text-neutral-500 truncate flex items-center gap-1">
                      <span>{slot.upstreamName}</span>
                      <span className="text-[9px] text-neutral-600">({slot.breakerState})</span>
                    </div>
                    <div className="font-medium text-neutral-200 flex items-center gap-1 mt-0.5">
                      <Key className="w-3 h-3 text-neutral-500 shrink-0" />
                      <span className="truncate">{slot.ref}</span>
                    </div>
                  </div>

                  <div className="flex items-center gap-1.5 text-[10px] tabular-nums flex-shrink-0">
                    <span
                      className={cn(
                        'w-1.5 h-1.5 rounded-full',
                        slot.isRevoked
                          ? 'bg-rose-400'
                          : slot.isCooldown
                          ? 'bg-amber-400 animate-pulse'
                          : 'bg-emerald-400'
                      )}
                    />
                    <span
                      className={cn(
                        slot.isRevoked
                          ? 'text-rose-400/90'
                          : slot.isCooldown
                          ? 'text-amber-400/90 font-medium'
                          : 'text-neutral-400'
                      )}
                    >
                      {slot.isRevoked
                        ? 'Revoked'
                        : slot.isCooldown
                        ? `${slot.cooldownRemainingSec}s`
                        : 'Active'}
                    </span>
                  </div>
                </div>

                <div className="mt-2.5 pt-2 border-t border-white/[0.04] grid grid-cols-2 gap-2 text-[10px] text-neutral-500">
                  <div className="flex items-center gap-1">
                    <Activity className="w-3 h-3 text-neutral-600" />
                    <span>In-flight: {slot.inflight}</span>
                  </div>
                  <div className="text-right">
                    <span>429s: {slot.totalCooldownEvents}</span>
                  </div>
                </div>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
});
