import { useState, type FormEvent } from 'react';

import { Badge } from '@/components/ui/Badge';
import { Field } from '@/components/ui/Controls';
import { KpiCard, KpiGrid, PageHeader } from '@/components/ui/PageHeader';
import { Pagination } from '@/components/ui/Pagination';
import { QueryGate } from '@/components/ui/QueryGate';
import {
  useCheckWarpMutation,
  useDeleteWarpLicenseMutation,
  useRotateWarpMutation,
  useSetWarpLicenseMutation,
  useSetWarpPoolSizeMutation,
  useWarpStatusQuery,
} from '@/services/api';
import type { WarpStatusDTO } from '@/services/schema';
import { useUiStore } from '@/state/store';

/** Trigger on-demand health check across all active WARP sessions. */
function CheckWarpButton({ live }: { live: boolean }) {
  const check = useCheckWarpMutation();
  const pushToast = useUiStore((s) => s.pushToast);

  return (
    <button
      className="btn btn-secondary"
      disabled={check.isPending || !live}
      onClick={() =>
        check.mutate(undefined, {
          onSuccess: (next) => {
            const sessions = next.sessions ?? [];
            const active = sessions.filter((s) => s.status === 'active').length;
            pushToast({
              type: 'success',
              title: 'WARP health check completed',
              message: sessions.length
                ? `${active} of ${sessions.length} sessions active (${next.latency_ms?.toFixed(1) ?? '—'} ms avg).`
                : 'WARP health check passed.',
            });
          },
          onError: (e) =>
            pushToast({
              type: 'error',
              title: 'Health check failed',
              message: e instanceof Error ? e.message : 'Unknown',
            }),
        })
      }
    >
      {check.isPending ? 'Checking…' : 'Check health'}
    </button>
  );
}

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

function WarpSessionStatusBadge({ status }: { status?: string }) {
  if (status === 'cooling') {
    return (
      <Badge tone="warn" title="Cooldown triggered by rate limit or errors">
        cooling
      </Badge>
    );
  }
  if (status === 'unreachable') {
    return (
      <Badge tone="danger" title="Egress probe failed">
        unreachable
      </Badge>
    );
  }
  if (status === 'active') {
    return <Badge tone="ok">active</Badge>;
  }
  return <Badge tone="neutral">{status || 'idle'}</Badge>;
}

function WarpLatencyDisplay({
  latencyMs,
  lastCheckAt,
}: {
  latencyMs?: number | null;
  lastCheckAt?: string | null;
}) {
  if (latencyMs == null || latencyMs <= 0) {
    return <span className="hint">—</span>;
  }

  let color = 'var(--text-color, currentColor)';
  if (latencyMs < 100) {
    color = 'var(--success, #10b981)';
  } else if (latencyMs < 250) {
    color = 'var(--warning, #f59e0b)';
  } else {
    color = 'var(--danger, #ef4444)';
  }

  const title = lastCheckAt
    ? `Latency: ${latencyMs.toFixed(1)} ms · Checked at ${new Date(lastCheckAt).toLocaleTimeString()}`
    : `Latency: ${latencyMs.toFixed(1)} ms`;

  return (
    <span className="mono font-semibold" style={{ color }} title={title}>
      {latencyMs.toFixed(1)} ms
    </span>
  );
}

/** Bounds used only when the server status predates the pool_size_min/max fields. */
const POOL_MIN_FALLBACK = 1;
const POOL_MAX_FALLBACK = 64;

/**
 * Pool size is the one WARP knob an operator tunes while watching the pool.
 * It is a steady-state ceiling rather than a dial: rotation adds a single slot
 * per tick, so raising it empty-fills over the following intervals instead of
 * registering a burst of sessions that would trip Cloudflare 429s.
 */
function WarpLicenseCard({ status }: { status?: WarpStatusDTO }) {
  const setLicense = useSetWarpLicenseMutation();
  const deleteLicense = useDeleteWarpLicenseMutation();
  const pushToast = useUiStore((s) => s.pushToast);

  const [licenseKey, setLicenseKey] = useState('');
  const configured = Boolean(status?.license_configured);

  function handleSave(e: FormEvent) {
    e.preventDefault();
    const key = licenseKey.trim();
    if (!key || setLicense.isPending) return;

    setLicense.mutate(key, {
      onSuccess: () => {
        setLicenseKey('');
        pushToast({
          type: 'success',
          title: 'WARP+ license saved',
          message: 'License key attached. Future registrations will use this license.',
        });
      },
      onError: (err) => {
        pushToast({
          type: 'error',
          title: 'Could not save license',
          message: err instanceof Error ? err.message : 'Unknown error',
        });
      },
    });
  }

  function handleRemove() {
    if (deleteLicense.isPending) return;
    deleteLicense.mutate(undefined, {
      onSuccess: () => {
        pushToast({
          type: 'success',
          title: 'WARP+ license removed',
          message: 'Reverted to free tier device registration.',
        });
      },
      onError: (err) => {
        pushToast({
          type: 'error',
          title: 'Could not remove license',
          message: err instanceof Error ? err.message : 'Unknown error',
        });
      },
    });
  }

  return (
    <div className="card">
      <div className="card-header">
        <div className="flex items-center gap-2">
          <h2>WARP+ License</h2>
          {configured ? (
            <Badge tone="ok">configured</Badge>
          ) : (
            <Badge tone="neutral">free tier</Badge>
          )}
        </div>
        <span className="hint">
          Attach a Cloudflare WARP+ or Zero Trust license key for higher quota and priority routing
        </span>
      </div>
      <div className="card-body">
        <form onSubmit={handleSave}>
          <Field
            label="License key"
            htmlFor="warp-license-key"
            hint={
              configured
                ? 'A license key is currently active. Enter a new key to replace it, or remove it to revert to free tier.'
                : 'Enter your 26-character WARP+ or Cloudflare Zero Trust license key.'
            }
          >
            <input
              id="warp-license-key"
              type="password"
              placeholder={configured ? '••••••••••••••••••••••••' : 'e.g. xxxxxxxx-xxxxxxxx-xxxxxxxx'}
              value={licenseKey}
              disabled={setLicense.isPending || deleteLicense.isPending}
              onChange={(e) => setLicenseKey(e.target.value)}
              autoComplete="off"
              spellCheck={false}
            />
          </Field>
          <div className="form-row form-row-end" style={{ marginTop: 14 }}>
            {configured ? (
              <button
                type="button"
                className="btn btn-ghost"
                disabled={deleteLicense.isPending || setLicense.isPending}
                onClick={handleRemove}
              >
                {deleteLicense.isPending ? 'Removing…' : 'Remove license'}
              </button>
            ) : null}
            <button
              type="submit"
              className="btn btn-secondary"
              disabled={!licenseKey.trim() || setLicense.isPending || deleteLicense.isPending}
            >
              {setLicense.isPending ? 'Saving…' : 'Save license'}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

function WarpPoolSizeCard({ status }: { status?: WarpStatusDTO }) {
  const save = useSetWarpPoolSizeMutation();
  const pushToast = useUiStore((s) => s.pushToast);

  const min = status?.pool_size_min ?? POOL_MIN_FALLBACK;
  const max = status?.pool_size_max ?? POOL_MAX_FALLBACK;
  const current = status?.pool_size && status.pool_size > 0 ? status.pool_size : null;

  // null means "mirror the server". The status query re-polls every 5s, so an
  // effect that copied the server value into state would erase what is being
  // typed mid-edit; the draft takes over on the first keystroke and is released
  // again once the save lands.
  const [draft, setDraft] = useState<string | null>(null);
  const shown = draft ?? (current != null ? String(current) : '');

  const parsed = Number(shown);
  const wellFormed = shown.trim() !== '' && Number.isInteger(parsed);
  const inRange = wellFormed && parsed >= min && parsed <= max;
  const dirty = inRange && parsed !== current;

  const hint =
    shown.trim() === ''
      ? `Enter a value between ${min} and ${max}.`
      : !wellFormed
        ? 'Enter a whole number.'
        : !inRange
          ? `Must be between ${min} and ${max}.`
          : `Currently active: ${status?.active_sessions ?? '—'} of ${current ?? '—'} slots.`;

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (!dirty || save.isPending) return;
    save.mutate(parsed, {
      onSuccess: (next) => {
        setDraft(null);
        pushToast({
          type: 'success',
          title: 'WARP pool size saved',
          message: `Ceiling set to ${next.pool_size ?? parsed}. Slots are added one per rotation.`,
        });
      },
      onError: (err) => {
        pushToast({
          type: 'error',
          title: 'Could not save pool size',
          message: err instanceof Error ? err.message : 'Unknown',
        });
      },
    });
  }

  return (
    <div className="card">
      <div className="card-header">
        <h2>Pool settings</h2>
        <span className="hint">
          One slot is added per rotation, so a larger pool fills gradually
        </span>
      </div>
      <div className="card-body">
        <form onSubmit={handleSubmit}>
          <Field
            label="Max pool size"
            htmlFor="warp-pool-size"
            hint={`Concurrent WARP slots to keep active (${min}–${max}).`}
          >
            <input
              id="warp-pool-size"
              type="number"
              min={min}
              max={max}
              step={1}
              inputMode="numeric"
              value={shown}
              disabled={save.isPending}
              onChange={(e) => setDraft(e.target.value)}
            />
          </Field>
          <div className="form-row form-row-end" style={{ marginTop: 14 }}>
            <span className="hint">{hint}</span>
            <button type="submit" className="btn btn-secondary" disabled={!dirty || save.isPending}>
              {save.isPending ? 'Saving…' : 'Save'}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

export function WarpPage() {
  const warp = useWarpStatusQuery();
  const d = warp.data;
  const live = Boolean(d?.enabled);
  const ips = d?.egress_ips ?? [];
  const sessions = d?.sessions ?? [];
  const totalItems = sessions.length > 0 ? sessions.length : ips.length;

  // The pool can hold up to 64 slots; paginate the list so a full pool stays
  // scannable instead of turning the card into a scroll wall.
  const [poolPage, setPoolPage] = useState(1);
  const POOL_PAGE_SIZE = 20;
  const poolTotalPages = Math.max(1, Math.ceil(totalItems / POOL_PAGE_SIZE));
  const safePoolPage = Math.min(poolPage, poolTotalPages);
  const pagedSessions = sessions.slice((safePoolPage - 1) * POOL_PAGE_SIZE, safePoolPage * POOL_PAGE_SIZE);
  const pagedIps = ips.slice((safePoolPage - 1) * POOL_PAGE_SIZE, safePoolPage * POOL_PAGE_SIZE);

  // Three states, not two: a warm-up that never reached Cloudflare is neither a
  // live pool nor a healthy idle engine, so it is reported as UNAVAILABLE with
  // the recorded reason instead of the neutral STANDBY badge.
  const badgeTone = live ? 'ok' : d?.error ? 'warn' : 'neutral';
  const badgeText = live ? 'ONLINE' : d?.error ? 'UNAVAILABLE' : 'STANDBY';

  const intervalText = d?.auto_rotate_interval_seconds
    ? d.auto_rotate_interval_seconds < 60
      ? `${d.auto_rotate_interval_seconds}s`
      : `${Math.max(1, Math.round(d.auto_rotate_interval_seconds / 60))}m`
    : 'disabled';

  return (
    <div className="page-col">
      <PageHeader
        title="Warp"
        description="Cloudflare WARP egress pool: live slots, rotation, and draining connections."
        actions={
          <div className="flex items-center gap-2">
            <Badge tone={badgeTone} title={d?.error || undefined}>
              {badgeText}
            </Badge>
            <CheckWarpButton live={live} />
            <RotateWarpButton live={live} />
          </div>
        }
      />

      <KpiGrid>
        <KpiCard
          label="Active sessions"
          value={`${d?.active_sessions ?? '—'} / ${d?.pool_size ?? '—'}`}
        />
        <KpiCard
          label="Handshake latency"
          value={d?.latency_ms ? `${d.latency_ms.toFixed(1)} ms` : '—'}
          tone={d?.latency_ms && d.latency_ms > 300 ? 'warn' : 'plain'}
        />
        <KpiCard label="Primary egress" value={d?.public_ip || '—'} />
        <KpiCard
          label="Active connections"
          value={d?.active_connections != null ? String(d.active_connections) : '—'}
        />
      </KpiGrid>

      <QueryGate isLoading={warp.isLoading} error={warp.error}>
        <div className="stack">
          <div className="card">
            <div className="card-header">
              <h2>Session pool</h2>
              <span className="hint">
                {totalItems} slot{totalItems === 1 ? '' : 's'} in egress pool
              </span>
            </div>
            <div className="card-body tight table-wrap">
              <table>
                <thead>
                  <tr>
                    <th className="num">Slot</th>
                    <th>Egress IP</th>
                    <th>Colo</th>
                    <th>Status</th>
                    <th className="num">Response Time</th>
                    <th className="num">Conns</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {totalItems === 0 ? (
                    <tr>
                      <td colSpan={7} className="faint">
                        No active sessions. The pool dials on first use.
                      </td>
                    </tr>
                  ) : sessions.length > 0 ? (
                    pagedSessions.map((s) => (
                      <tr key={`sess-${s.id}`}>
                        <td className="num">#{s.id}</td>
                        <td>
                          <span
                            className="inline-flex items-center gap-2"
                            style={{ fontFamily: 'var(--font-mono, monospace)' }}
                          >
                            {s.public_ip || '—'}
                            {s.primary || s.public_ip === d?.public_ip ? <Badge tone="ok">primary</Badge> : null}
                          </span>
                        </td>
                        <td>
                          <span className="mono">{s.colo || '—'}</span>
                        </td>
                        <td>
                          <WarpSessionStatusBadge status={s.status} />
                        </td>
                        <td className="num">
                          <WarpLatencyDisplay latencyMs={s.latency_ms} lastCheckAt={s.last_check_at} />
                        </td>
                        <td className="num">
                          <span className="hint">{s.active_conns}</span>
                        </td>
                        <td className="num">
                          {s.public_ip ? <CopyIp ip={s.public_ip} /> : null}
                        </td>
                      </tr>
                    ))
                  ) : (
                    pagedIps.map((ip, i) => (
                      <tr key={`${ip}-${safePoolPage}`}>
                        <td className="num">#{(safePoolPage - 1) * POOL_PAGE_SIZE + i + 1}</td>
                        <td>
                          <span
                            className="inline-flex items-center gap-2"
                            style={{ fontFamily: 'var(--font-mono, monospace)' }}
                          >
                            {ip}
                            {ip === d?.public_ip ? <Badge tone="ok">primary</Badge> : null}
                          </span>
                        </td>
                        <td>
                          <span className="mono">{d?.colo || '—'}</span>
                        </td>
                        <td>
                          <Badge tone="ok">active</Badge>
                        </td>
                        <td className="num">
                          <WarpLatencyDisplay latencyMs={d?.latency_ms} />
                        </td>
                        <td className="num">
                          <span className="hint">—</span>
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
            <div className="card-body tight" style={{ paddingTop: 0 }}>
              <Pagination
                total={totalItems}
                page={safePoolPage}
                pageSize={POOL_PAGE_SIZE}
                totalPages={poolTotalPages}
                onPageChange={setPoolPage}
                unit="slot"
              />
            </div>
          </div>
          <WarpPoolSizeCard status={d} />
          <WarpLicenseCard status={d} />
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
