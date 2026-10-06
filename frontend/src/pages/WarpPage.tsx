import { Badge } from '@/components/ui/Badge';
import { KpiCard, KpiGrid, PageHeader } from '@/components/ui/PageHeader';
import { QueryGate } from '@/components/ui/QueryGate';
import { useRotateWarpMutation, useWarpStatusQuery } from '@/services/api';
import { useUiStore } from '@/state/store';

/**
 * Manual rotation / cold start. The button lives on the page (not Settings) so
 * an operator can roll the pool and watch slots drain without leaving here.
 */
function RotateWarpButton({ live }: { live: boolean }) {
  const rotate = useRotateWarpMutation();
  const pushToast = useUiStore((s) => s.pushToast);

  return (
    <button
      className="btn btn-secondary"
      disabled={rotate.isPending}
      onClick={() =>
        rotate.mutate(undefined, {
          onSuccess: () =>
            pushToast({
              type: 'success',
              title: 'WARP rotate',
              message: live
                ? 'New egress slot created; retired slots keep draining their pinned connections.'
                : 'WARP pool established on the first available egress.',
            }),
          onError: (e) =>
            pushToast({
              type: 'error',
              title: 'Rotate failed',
              message: e instanceof Error ? e.message : 'Unknown',
            }),
        })
      }
    >
      {rotate.isPending ? 'Working…' : live ? 'Rotate pool' : 'Start pool'}
    </button>
  );
}

function CopyIp({ ip }: { ip: string }) {
  const pushToast = useUiStore((s) => s.pushToast);
  return (
    <button
      type="button"
      className="btn btn-ghost"
      style={{ padding: '2px 8px', fontSize: '0.75rem', height: 'auto' }}
      onClick={() => {
        void navigator.clipboard.writeText(ip);
        pushToast({ type: 'success', title: 'IP copied', message: ip });
      }}
    >
      Copy
    </button>
  );
}

export function WarpPage() {
  const warp = useWarpStatusQuery();
  const d = warp.data;
  const live = Boolean(d?.enabled);
  const ips = d?.egress_ips ?? [];

  // Three states, not two: a warm-up that never reached Cloudflare is neither a
  // live pool nor a healthy idle engine, so it is reported as UNAVAILABLE with
  // the recorded reason instead of the neutral STANDBY badge.
  const badgeTone = live ? 'ok' : d?.error ? 'warn' : 'neutral';
  const badgeText = live ? 'ONLINE' : d?.error ? 'UNAVAILABLE' : 'STANDBY';

  const intervalText = d?.auto_rotate_interval_seconds
    ? `${Math.max(1, Math.round(d.auto_rotate_interval_seconds / 60))}m`
    : 'disabled';

  return (
    <div className="page-col">
      <PageHeader
        title="Warp"
        description="Cloudflare WARP egress pool — live slots, rotation, and draining connections."
        actions={
          <div className="flex items-center gap-2">
            <Badge tone={badgeTone} title={d?.error || undefined}>
              {badgeText}
            </Badge>
            <RotateWarpButton live={live} />
          </div>
        }
      />

      <KpiGrid>
        <KpiCard
          label="Active sessions"
          value={`${d?.active_sessions ?? '—'} / ${d?.pool_size ?? '—'}`}
        />
        <KpiCard label="Primary egress" value={d?.public_ip || '—'} />
        <KpiCard
          label="Active connections"
          value={d?.active_connections != null ? String(d.active_connections) : '—'}
        />
        <KpiCard
          label="Draining slots"
          value={d?.draining_sessions != null ? String(d.draining_sessions) : '—'}
          tone={d?.draining_sessions ? 'warn' : 'plain'}
        />
      </KpiGrid>

      <QueryGate isLoading={warp.isLoading} error={warp.error}>
        <div className="stack">
          <div className="card">
            <div className="card-header">
              <h2>Session pool</h2>
              <span className="hint">
                {ips.length} slot{ips.length === 1 ? '' : 's'} resolving egress
              </span>
            </div>
            <div className="card-body tight table-wrap">
              <table>
                <thead>
                  <tr>
                    <th className="num">Slot</th>
                    <th>Egress IP</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {ips.length === 0 ? (
                    <tr>
                      <td colSpan={3} className="faint">
                        No active sessions — the pool dials on first use.
                      </td>
                    </tr>
                  ) : (
                    ips.map((ip, i) => (
                      <tr key={`${ip}-${i}`}>
                        <td className="num">#{i + 1}</td>
                        <td>
                          <span
                            className="inline-flex items-center gap-2"
                            style={{ fontFamily: 'var(--font-mono, monospace)' }}
                          >
                            {ip}
                            {ip === d?.public_ip ? <Badge tone="ok">primary</Badge> : null}
                          </span>
                        </td>
                        <td className="num">
                          <CopyIp ip={ip} />
                        </td>
                      </tr>
                    ))
                  )}
                </tbody>
              </table>
            </div>
          </div>
          <div className="card">
            <div className="card-header">
              <h2>Connection &amp; rotation</h2>
            </div>
            <div className="card-body">
              <div className="kv-list">
                <div>
                  <span className="k">Pool size</span>
                  <span className="v">{d?.pool_size ?? '—'}</span>
                </div>
                <div>
                  <span className="k">Active sessions</span>
                  <span className="v">{d?.active_sessions ?? '—'}</span>
                </div>
                <div>
                  <span className="k">Primary egress</span>
                  <span className="v">
                    {d?.public_ip ? (
                      <span
                        className="inline-flex items-center gap-2"
                        style={{ justifyContent: 'flex-end' }}
                      >
                        {d.public_ip}
                        <CopyIp ip={d.public_ip} />
                      </span>
                    ) : (
                      '—'
                    )}
                  </span>
                </div>
                <div>
                  <span className="k">Internal tunnel IP</span>
                  <span className="v">{d?.internal_ip || '—'}</span>
                </div>
                <div>
                  <span className="k">Colo</span>
                  <span className="v">{d?.colo || '—'}</span>
                </div>
                <div>
                  <span className="k">Edge endpoint</span>
                  <span className="v">{d?.endpoint || '—'}</span>
                </div>
                <div>
                  <span className="k">Handshake latency</span>
                  <span className="v">{d?.latency_ms ? `${d.latency_ms.toFixed(1)} ms` : '—'}</span>
                </div>
                <div>
                  <span className="k">Active connections</span>
                  <span className="v">{d?.active_connections ?? '—'}</span>
                </div>
                <div>
                  <span className="k">Draining slots</span>
                  <span className="v">{d?.draining_sessions ?? '—'}</span>
                </div>
                <div>
                  <span className="k">Last rotated</span>
                  <span className="v">
                    {d?.last_rotated_at ? new Date(d.last_rotated_at).toLocaleString() : '—'}
                  </span>
                </div>
                <div>
                  <span className="k">Auto-rotate interval</span>
                  <span className="v">{intervalText}</span>
                </div>
                <div>
                  <span className="k">Next rotation</span>
                  <span className="v">
                    {d?.next_rotation_at ? new Date(d.next_rotation_at).toLocaleTimeString() : '—'}
                  </span>
                </div>
              </div>
            </div>
          </div>
          {d?.error ? (
            <div className="card" style={{ borderLeft: '3px solid var(--danger)' }}>
              <div className="card-body">
                <div style={{ fontSize: 13, fontWeight: 600 }}>WARP error</div>
                <div
                  className="hint"
                  style={{
                    marginTop: 4,
                    fontSize: 12,
                    wordBreak: 'break-word',
                    fontFamily: 'var(--font-mono, monospace)',
                  }}
                >
                  {d.error}
                </div>
              </div>
            </div>
          ) : null}
        </div>
      </QueryGate>
    </div>
  );
}
