import React, { useState, useCallback } from 'react';
import type { UpstreamTelemetryDTO } from '@/services/schema';

export interface CooldownHeatmapProps {
  upstreams?: UpstreamTelemetryDTO[];
}

interface SlotRow {
  ref: string;
  status: string;
  inflight: number;
  cooldownEvents: number;
  requestsTotal: number;
}

interface UpstreamGroup {
  name: string;
  slots: SlotRow[];
  unhealthy: number; // cooldown or revoked keys
}

/**
 * CooldownHeatmap
 * Compact, per-upstream expandable table of KeyRing quota health
 * (429 cooldown / revoked / active). Real data from Firefly telemetry; no dummy values.
 */
export const CooldownHeatmap = React.memo(function CooldownHeatmap({
  upstreams = [],
}: CooldownHeatmapProps) {
  const groups = React.useMemo<UpstreamGroup[]>(() => {
    return upstreams.map((u) => {
      const rawSlots = u.slots && u.slots.length > 0 ? u.slots : null;
      let unhealthy = 0;
      const slots: SlotRow[] = rawSlots
        ? rawSlots.map((s) => {
            if (s.is_revoked || s.is_cooldown) unhealthy += 1;
            return {
              ref: s.ref,
              status: s.is_revoked
                ? 'revoked'
                : s.is_cooldown
                ? `cooldown ${s.cooldown_remaining_sec}s`
                : 'active',
              inflight: s.inflight,
              cooldownEvents: s.total_cooldown_events,
              requestsTotal: s.requests_total,
            };
          })
        : [
            {
              ref: 'DEFAULT_SLOT',
              status: 'active',
              inflight: 0,
              cooldownEvents: 0,
              requestsTotal: u.total_requests,
            },
          ];
      return { name: u.name, slots, unhealthy };
    });
  }, [upstreams]);

  const totalKeys = React.useMemo(
    () => groups.reduce((n, g) => n + g.slots.length, 0),
    [groups]
  );

  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const toggle = useCallback((name: string) => {
    setExpanded((prev) => ({ ...prev, [name]: !prev[name] }));
  }, []);

  return (
    <div className="p-4 rounded-xl bg-transparent border border-white/[0.06] font-mono text-xs">
      <div className="flex items-center justify-between pb-2 border-b border-white/[0.06]">
        <h3 className="text-[11px] uppercase tracking-wider text-neutral-300 font-medium">
          Upstream KeyRing Status
        </h3>
        <span className="text-[10px] text-neutral-500">
          {groups.length} upstreams · {totalKeys} keys
        </span>
      </div>

      {groups.length === 0 ? (
        <div className="py-4 text-neutral-500 text-[11px]">
          No upstreams or credential slots configured.
        </div>
      ) : (
        <div className="mt-2 max-h-[420px] overflow-y-auto custom-scrollbar flex flex-col gap-1">
          {groups.map((g) => {
            const isOpen = !!expanded[g.name];
            return (
              <div key={g.name}>
                <button
                  type="button"
                  onClick={() => toggle(g.name)}
                  className="w-full flex items-center justify-between py-1.5 px-2 rounded bg-white/[0.02] hover:bg-white/[0.04] text-left"
                >
                  <span className="flex items-center gap-2 text-neutral-200 truncate">
                    <span className="text-neutral-500 w-3 inline-block">{isOpen ? '▾' : '▸'}</span>
                    <span className="truncate">{g.name}</span>
                  </span>
                  <span className="flex items-center gap-3 text-[10px] text-neutral-500 flex-shrink-0">
                    {g.unhealthy > 0 ? (
                      <span className="text-amber-400/90">{g.unhealthy} cooldown/revoked</span>
                    ) : null}
                    <span>{g.slots.length} keys</span>
                  </span>
                </button>

                {isOpen ? (
                  <table className="w-full text-left text-[11px] tabular-nums mt-1 mb-1">
                    <thead>
                      <tr className="text-neutral-500">
                        <th className="font-normal py-1 pl-5 pr-3">Key</th>
                        <th className="font-normal py-1 pr-3">Status</th>
                        <th className="font-normal py-1 pr-3 text-right">In-flight</th>
                        <th className="font-normal py-1 pr-3 text-right">429s</th>
                        <th className="font-normal py-1 text-right">Requests</th>
                      </tr>
                    </thead>
                    <tbody>
                      {g.slots.map((slot, idx) => (
                        <tr
                          key={slot.ref || `slot-${idx}`}
                          className="border-t border-white/[0.04] text-neutral-300"
                        >
                          <td className="py-1 pl-5 pr-3 truncate">
                            {slot.ref || `key #${idx + 1}`}
                          </td>
                          <td className="py-1 pr-3 text-neutral-400">{slot.status}</td>
                          <td className="py-1 pr-3 text-right">{slot.inflight}</td>
                          <td className="py-1 pr-3 text-right">{slot.cooldownEvents}</td>
                          <td className="py-1 text-right">{slot.requestsTotal}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                ) : null}
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
});
