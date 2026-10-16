import { useState, type FormEvent } from 'react';

import { Badge } from '@/components/ui/Badge';
import { Field } from '@/components/ui/Controls';
import { KpiCard, KpiGrid, PageHeader } from '@/components/ui/PageHeader';
import { QueryGate } from '@/components/ui/QueryGate';
import { useRotateWarpMutation, useSetWarpPoolSizeMutation, useWarpStatusQuery } from '@/services/api';
import type { WarpStatusDTO } from '@/services/schema';
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

/** Bounds used only when the server status predates the pool_size_min/max fields. */
const POOL_MIN_FALLBACK = 1;
const POOL_MAX_FALLBACK = 64;

/**
 * Pool size is the one WARP knob an operator tunes while watching the pool.
 * It is a steady-state ceiling rather than a dial: rotation adds a single slot
 * per tick, so raising it empty-fills over the following intervals instead of
 * registering a burst of sessions that would trip Cloudflare 429s.
 */
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
                        No active sessions. The pool dials on first use.
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
          <WarpPoolSizeCard status={d} />
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
