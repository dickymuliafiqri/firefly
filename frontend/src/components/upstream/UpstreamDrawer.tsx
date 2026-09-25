import { useState, useEffect } from 'react';
import { Badge } from '@/components/ui/Badge';
import { Drawer } from '@/components/ui/Drawer';
import { ProviderIcon } from '@/components/ui/ProviderIcon';
import type { UpstreamTelemetryDTO, UpstreamDTO } from '@/services/schema';
import { checkUpstreamHealth } from '@/services/api';
import { navigate } from '@/lib/router';
import {
  ExternalLink,
  Power,
  ChevronLeft,
  ChevronRight,
  Activity,
  Trash2,
} from 'lucide-react';

export function breakerTone(breaker: string): 'ok' | 'warn' | 'danger' {
  const v = (breaker || '').toUpperCase();
  return v === 'CLOSED' ? 'ok' : v === 'HALF-OPEN' ? 'warn' : 'danger';
}

export interface UpstreamDrawerProps {
  selected: UpstreamTelemetryDTO | null;
  entry?: UpstreamDTO;
  onClose: () => void;
  onToggleEnabled?: (name: string) => void;
  isToggling?: boolean;
  onDelete?: (name: string) => void;
}

export function UpstreamDrawer({
  selected,
  entry,
  onClose,
  onToggleEnabled,
  isToggling = false,
  onDelete,
}: UpstreamDrawerProps) {
  const [keyPage, setKeyPage] = useState(1);
  const keyPageSize = 8;

  const [pingResult, setPingResult] = useState<{
    healthy: boolean;
    statusCode: number;
    latencyMs: number;
    message?: string;
  } | null>(null);
  const [isPinging, setIsPinging] = useState(false);

  useEffect(() => {
    setKeyPage(1);
    setPingResult(null);
  }, [selected?.name]);

  async function handlePing() {
    if (!selected) return;
    setIsPinging(true);
    try {
      const res = await checkUpstreamHealth({
        name: selected.name,
        protocol: selected.protocol,
        base_url: selected.base_url,
        egress_mode: entry?.egress_mode,
        proxy_url: entry?.proxy_url,
      });
      setPingResult({
        healthy: res.healthy,
        statusCode: res.status_code,
        latencyMs: res.latency_ms,
        message: res.message,
      });
    } catch (err) {
      setPingResult({
        healthy: false,
        statusCode: 0,
        latencyMs: 0,
        message: err instanceof Error ? err.message : 'Ping failed',
      });
    } finally {
      setIsPinging(false);
    }
  }

  if (!selected) return null;

  const isEnabled = entry?.enabled !== false;
  const activeCount = selected.slots.filter((s) => !s.is_cooldown && !s.is_revoked).length;
  const cooldownCount = selected.slots.filter((s) => s.is_cooldown).length;
  const revokedCount = selected.slots.filter((s) => s.is_revoked).length;

  const totalKeyPages = Math.max(1, Math.ceil(selected.slots.length / keyPageSize));
  const safeKeyPage = Math.min(keyPage, totalKeyPages);
  const pagedSlots = selected.slots.slice((safeKeyPage - 1) * keyPageSize, safeKeyPage * keyPageSize);
  const keyStartIdx = selected.slots.length === 0 ? 0 : (safeKeyPage - 1) * keyPageSize + 1;
  const keyEndIdx = Math.min(safeKeyPage * keyPageSize, selected.slots.length);

  return (
    <Drawer open={Boolean(selected)} onClose={onClose} title={selected.name}>
      {/* Top Provider Profile Banner */}
      <div className="p-4 rounded-xl bg-[var(--surface-raised)] border border-[var(--line)]">
        <div className="flex items-center justify-between gap-4">
          <div className="flex items-center gap-3.5 min-w-0">
            <div className="w-11 h-11 rounded-xl flex items-center justify-center bg-[var(--surface-card)] border border-[var(--line)] shadow-sm shrink-0">
              <ProviderIcon protocol={selected.protocol} name={selected.name} size={26} />
            </div>
            <div className="min-w-0">
              <div className="font-semibold text-ink text-base truncate" title={selected.name}>
                {selected.name}
              </div>
              <div className="flex items-center gap-2 mt-1">
                <span className="inline-flex items-center gap-2 px-2 py-0.5 rounded text-xs font-mono font-medium bg-[var(--surface-input)] text-muted border border-[var(--line)]">
                  <ProviderIcon protocol={selected.protocol} name={selected.name} size={13} />
                  <span className="capitalize">{selected.protocol}</span>
                </span>
                {isEnabled ? (
                  <Badge tone="ok" className="text-[10px] px-2 py-0.5">
                    ENABLED
                  </Badge>
                ) : (
                  <Badge tone="neutral" className="text-[10px] px-2 py-0.5">
                    DISABLED
                  </Badge>
                )}
              </div>
            </div>
          </div>

          <div className="flex items-center gap-2 shrink-0">
            <button
              type="button"
              className="btn btn-secondary text-xs"
              disabled={isPinging}
              onClick={() => void handlePing()}
              title="Ping upstream host"
            >
              <Activity
                className={`w-3.5 h-3.5 ${isPinging ? 'animate-spin' : ''}`}
                style={{ color: 'var(--biolum)' }}
              />
              {isPinging ? 'Pinging…' : 'Ping'}
            </button>
            <button
              type="button"
              className="btn btn-secondary text-xs"
              onClick={() => {
                onClose();
                navigate('upstream/edit/' + encodeURIComponent(selected.name));
              }}
            >
              <ExternalLink className="w-3.5 h-3.5" />
              Edit
            </button>
            {entry && onToggleEnabled ? (
              <button
                type="button"
                className="btn btn-ghost text-xs"
                disabled={isToggling}
                onClick={() => onToggleEnabled(selected.name)}
              >
                <Power className="w-3.5 h-3.5" />
                {isEnabled ? 'Disable' : 'Enable'}
              </button>
            ) : null}
          </div>
        </div>
      </div>

      {pingResult && (
        <div
          className="p-3 rounded-lg flex items-center justify-between text-xs"
          style={{
            background: pingResult.healthy ? 'var(--surface-raised)' : 'rgba(239, 68, 68, 0.1)',
            border: `1px solid ${pingResult.healthy ? 'var(--line)' : 'rgba(239, 68, 68, 0.3)'}`,
          }}
        >
          <div className="flex items-center gap-2">
            <Badge tone={pingResult.healthy ? 'ok' : 'danger'}>
              {pingResult.healthy ? `ONLINE (${pingResult.statusCode})` : `OFFLINE (${pingResult.statusCode || 'ERR'})`}
            </Badge>
            <span className="text-muted truncate max-w-[220px]">
              {pingResult.message || (pingResult.healthy ? 'Host reachable' : 'Probe failed')}
            </span>
          </div>
          {pingResult.latencyMs > 0 && (
            <span className="mono font-semibold shrink-0" style={{ color: 'var(--ok)' }}>
              {pingResult.latencyMs}ms
            </span>
          )}
        </div>
      )}

      <div>
        <h3>Host telemetry</h3>
        <div className="kv-list">
          <div>
            <span className="k">Base URL</span>
            <span className="v mono text-xs break-all">{selected.base_url}</span>
          </div>
          <div>
            <span className="k">Circuit breaker</span>
            <span className="v">
              <Badge tone={breakerTone(selected.breaker_state)}>{selected.breaker_state}</Badge>
            </span>
          </div>
          <div>
            <span className="k">Total requests</span>
            <span className="v mono">{selected.total_requests.toLocaleString()}</span>
          </div>
          {entry?.egress_mode ? (
            <div>
              <span className="k">Egress mode</span>
              <span className="v mono">{entry.egress_mode}</span>
            </div>
          ) : null}
          {entry?.credential_pool ? (
            <div>
              <span className="k">Credential pool</span>
              <span className="v mono">{entry.credential_pool.length} refs</span>
            </div>
          ) : null}
        </div>
      </div>

      <div className="mt-4">
        <div className="flex items-center justify-between mb-2">
          <h3 className="m-0">KeyRing slots</h3>
          <span className="text-[11px] text-faint">
            {activeCount} active &middot; {cooldownCount} cooldown &middot; {revokedCount} revoked
          </span>
        </div>
        <div className="table-wrap">
          <table className="w-full">
            <thead>
              <tr>
                <th style={{ width: '42%' }}>Ref</th>
                <th className="num" style={{ width: '16%' }}>Inflight</th>
                <th className="num" style={{ width: '18%' }}>Cooldown</th>
                <th style={{ width: '24%', textAlign: 'right' }}>Status</th>
              </tr>
            </thead>
            <tbody>
              {pagedSlots.map((s) => (
                <tr key={s.ref}>
                  <td className="mono text-xs truncate max-w-[190px]" title={s.ref}>
                    {s.ref}
                  </td>
                  <td className="num mono">{s.inflight}</td>
                  <td className="num mono">{s.is_cooldown ? `${s.cooldown_remaining_sec}s` : '—'}</td>
                  <td className="text-right">
                    <div className="flex justify-end">
                      {s.is_revoked ? (
                        <Badge tone="danger">REVOKED</Badge>
                      ) : s.is_cooldown ? (
                        <Badge tone="warn">COOLDOWN</Badge>
                      ) : (
                        <Badge tone="ok">HEALTHY</Badge>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
              {selected.slots.length === 0 ? (
                <tr>
                  <td colSpan={4} className="faint">Belum ada slot terdaftar.</td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>

        {selected.slots.length > keyPageSize ? (
          <div className="flex items-center justify-between pt-3 border-t border-[var(--line)] text-xs text-faint mt-2">
            <span className="tabular-nums">
              Showing <span className="text-ink font-medium">{keyStartIdx}–{keyEndIdx}</span> of{' '}
              <span className="text-ink font-medium">{selected.slots.length}</span> keys
            </span>
            <div className="flex items-center gap-1.5">
              <button
                type="button"
                className="pagination-btn"
                disabled={safeKeyPage <= 1}
                onClick={() => setKeyPage((p) => Math.max(1, p - 1))}
                aria-label="Previous key page"
              >
                <ChevronLeft className="w-3.5 h-3.5" />
              </button>
              <span className="text-xs text-faint px-1.5 tabular-nums">
                {safeKeyPage} / {totalKeyPages}
              </span>
              <button
                type="button"
                className="pagination-btn"
                disabled={safeKeyPage >= totalKeyPages}
                onClick={() => setKeyPage((p) => Math.min(totalKeyPages, p + 1))}
                aria-label="Next key page"
              >
                <ChevronRight className="w-3.5 h-3.5" />
              </button>
            </div>
          </div>
        ) : null}

        {onDelete ? (
          <div className="pt-4 mt-4 border-t border-[var(--line)] flex justify-between items-center">
            <span className="text-xs text-faint">Hapus upstream dan konfigurasi terkait</span>
            <button
              type="button"
              className="btn btn-ghost text-xs"
              style={{ color: 'var(--danger)' }}
              onClick={() => onDelete(selected.name)}
            >
              <Trash2 className="w-3.5 h-3.5" />
              Delete Upstream
            </button>
          </div>
        ) : null}
      </div>
    </Drawer>
  );
}
