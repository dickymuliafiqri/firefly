import { useEffect, useState, type FormEvent } from 'react';

import { Badge } from '@/components/ui/Badge';
import { Field } from '@/components/ui/Controls';
import { KpiCard, KpiGrid, PageHeader } from '@/components/ui/PageHeader';
import { Pagination } from '@/components/ui/Pagination';
import { QueryGate } from '@/components/ui/QueryGate';
import {
  useRefreshSingboxMutation,
  useSingboxStatusQuery,
  useUpdateSingboxDefaultTagMutation,
  useUpdateSingboxStrategyMutation,
  useUpdateSingboxSubscriptionMutation,
} from '@/services/api';
import type { SingboxStatusDTO } from '@/services/schema';
import { useUiStore } from '@/state/store';

/** Refresh Now. The manager keeps the previous Box when a refresh fails, so a
 * failed refresh is reported without tearing down working egress. */
function RefreshSingboxButton({ live }: { live: boolean }) {
  const refresh = useRefreshSingboxMutation();
  const pushToast = useUiStore((s) => s.pushToast);

  return (
    <button
      className="btn btn-secondary"
      disabled={refresh.isPending}
      onClick={() =>
        refresh.mutate(undefined, {
          onSuccess: (next) =>
            pushToast({
              type: 'success',
              title: 'sing-box refreshed',
              message: next.node_count
                ? `${next.node_count} node${next.node_count === 1 ? '' : 's'} loaded.`
                : 'Subscription fetched, but it contained no usable nodes.',
            }),
          onError: (e) =>
            pushToast({
              type: 'error',
              title: live ? 'Refresh failed' : 'Could not load subscription',
              message: e instanceof Error ? e.message : 'Unknown',
            }),
        })
      }
    >
      {refresh.isPending ? 'Refreshing…' : 'Refresh now'}
    </button>
  );
}

/** humanizes a nanoseconds duration (Go time.Duration JSON encoding). */
function formatDurationNs(ns?: number): string {
  if (!ns || ns <= 0) return '—';
  const seconds = Math.round(ns / 1e9);
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`;
  return `${Math.round(seconds / 3600)}h`;
}

/**
 * Subscription form. The URL is persisted before it is applied server-side, so
 * a save that fails on the network leaves the stored subscription untouched.
 */
function SingboxSubscriptionCard({ status }: { status?: SingboxStatusDTO }) {
  const save = useUpdateSingboxSubscriptionMutation();
  const pushToast = useUiStore((s) => s.pushToast);

  const [url, setUrl] = useState('');
  const [intervalS, setIntervalS] = useState('');

  // Mirror the server until the operator starts editing. The status query
  // re-polls every 5s, so copying the server value on every render would erase
  // what is being typed mid-edit.
  const [dirty, setDirty] = useState(false);
  useEffect(() => {
    if (dirty) return;
    setUrl(status?.subscription_url ?? '');
    setIntervalS(
      status?.refresh_interval != null && status.refresh_interval > 0
        ? String(Math.round(status.refresh_interval / 1e9))
        : '',
    );
  }, [status?.subscription_url, status?.refresh_interval, dirty]);

  const parsedInterval = intervalS.trim() === '' ? undefined : Number(intervalS);
  const intervalOk =
    parsedInterval === undefined ||
    (Number.isInteger(parsedInterval) && parsedInterval >= 0);

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (save.isPending || !intervalOk) return;
    save.mutate(
      {
        subscription_url: url.trim(),
        refresh_interval_s: parsedInterval,
      },
      {
        onSuccess: (next) => {
          setDirty(false);
          pushToast({
            type: 'success',
            title: 'Subscription saved',
            message: next.subscription_url
              ? `Loaded ${next.node_count} node${next.node_count === 1 ? '' : 's'}.`
              : 'Saved.',
          });
        },
        onError: (err) => {
          pushToast({
            type: 'error',
            title: 'Could not save subscription',
            message: err instanceof Error ? err.message : 'Unknown',
          });
        },
      },
    );
  }

  return (
    <div className="card">
      <div className="card-header">
        <h2>Subscription</h2>
        <span className="hint">Saved before it is applied</span>
      </div>
      <div className="card-body">
        <form onSubmit={handleSubmit}>
          <Field
            label="Subscription URL"
            htmlFor="singbox-sub-url"
            hint="An http(s) URL serving a client subscription (base64 or plain node links)."
          >
            <input
              id="singbox-sub-url"
              type="text"
              className="mono"
              value={url}
              placeholder="https://example.com/sub?token=…"
              disabled={save.isPending}
              onChange={(e) => {
                setDirty(true);
                setUrl(e.target.value);
              }}
            />
          </Field>
          <Field
            label="Refresh interval (seconds)"
            htmlFor="singbox-sub-interval"
            hint="How often to re-fetch. Empty keeps the configured default."
          >
            <input
              id="singbox-sub-interval"
              type="number"
              min={0}
              step={60}
              inputMode="numeric"
              value={intervalS}
              disabled={save.isPending}
              onChange={(e) => {
                setDirty(true);
                setIntervalS(e.target.value);
              }}
            />
          </Field>
          <div className="form-row form-row-end" style={{ marginTop: 14 }}>
            <span className="hint">
              {intervalOk
                ? url.trim() === ''
                  ? 'An empty URL unsubscribes; egress falls back to direct.'
                  : 'Saving re-fetches the subscription immediately.'
                : 'Interval must be a whole number of seconds.'}
            </span>
            <button
              type="submit"
              className="btn btn-secondary"
              disabled={save.isPending || !intervalOk}
            >
              {save.isPending ? 'Saving…' : 'Save'}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

/** Default outbound picker, populated from the parsed node list. */
function SingboxDefaultTagCard({
  status,
}: {
  status?: SingboxStatusDTO;
}) {
  const save = useUpdateSingboxDefaultTagMutation();
  const pushToast = useUiStore((s) => s.pushToast);
  const [tag, setTag] = useState('auto');
  const [dirty, setDirty] = useState(false);

  useEffect(() => {
    if (dirty) return;
    setTag(status?.default_tag || 'auto');
  }, [status?.default_tag, dirty]);

  const nodes = status?.nodes ?? [];
  const changed = tag !== (status?.default_tag || 'auto');

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (!changed || save.isPending) return;
    save.mutate(tag, {
      onSuccess: (next) => {
        setDirty(false);
        pushToast({
          type: 'success',
          title: 'Default outbound saved',
          message: `New upstreams without a tag dial ${next.default_tag || tag}.`,
        });
      },
      onError: (err) =>
        pushToast({
          type: 'error',
          title: 'Could not save default outbound',
          message: err instanceof Error ? err.message : 'Unknown',
        }),
    });
  }

  return (
    <div className="card">
      <div className="card-header">
        <h2>Default outbound</h2>
        <span className="hint">Used by upstreams without an explicit tag</span>
      </div>
      <div className="card-body">
        <form onSubmit={handleSubmit}>
          <Field
            label="Default outbound tag"
            htmlFor="singbox-default-tag"
            hint="`auto` load-balances across nodes by latency; pick a node to pin it."
          >
            <select
              id="singbox-default-tag"
              value={tag}
              disabled={save.isPending}
              onChange={(e) => {
                setDirty(true);
                setTag(e.target.value);
              }}
            >
              <option value="auto">auto (URL-test)</option>
              {nodes
                .filter((n) => n.tag !== 'auto')
                .map((n) => (
                  <option key={n.tag} value={n.tag}>
                    {n.tag} · {n.type}
                  </option>
                ))}
            </select>
          </Field>
          <div className="form-row form-row-end" style={{ marginTop: 14 }}>
            <span className="hint">
              {changed ? 'Unsaved change.' : 'Matches the active configuration.'}
            </span>
            <button
              type="submit"
              className="btn btn-secondary"
              disabled={!changed || save.isPending}
            >
              {save.isPending ? 'Saving…' : 'Save'}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

/** Node-selection strategy: urltest (fastest node) vs round_robin (rotate
 * across the healthy pool). Persisted before it is applied, like the other
 * singbox.json-backed settings. */
function SingboxStrategyCard({ status }: { status?: SingboxStatusDTO }) {
  const save = useUpdateSingboxStrategyMutation();
  const pushToast = useUiStore((s) => s.pushToast);
  const [strategy, setStrategy] = useState('urltest');

  useEffect(() => {
    setStrategy(status?.strategy || 'urltest');
  }, [status?.strategy]);

  const changed = strategy !== (status?.strategy || 'urltest');

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (!changed || save.isPending) return;
    save.mutate(strategy, {
      onSuccess: (next) =>
        pushToast({
          type: 'success',
          title: 'Strategy saved',
          message:
            next.strategy === 'round_robin'
              ? 'Dials now rotate across healthy nodes.'
              : 'Dials now follow the URL-test group (fastest node).',
        }),
      onError: (err) =>
        pushToast({
          type: 'error',
          title: 'Could not save strategy',
          message: err instanceof Error ? err.message : 'Unknown',
        }),
    });
  }

  return (
    <div className="card">
      <div className="card-header">
        <h2>Node selection</h2>
        <span className="hint">How the default dial picks a node</span>
      </div>
      <div className="card-body">
        <form onSubmit={handleSubmit}>
          <Field
            label="Strategy"
            htmlFor="singbox-strategy"
            hint="URL-test follows the fastest node; round-robin spreads load across every healthy node."
          >
            <select
              id="singbox-strategy"
              value={strategy}
              disabled={save.isPending}
              onChange={(e) => setStrategy(e.target.value)}
            >
              <option value="urltest">URL-test (fastest node)</option>
              <option value="round_robin">Round-robin (healthy pool)</option>
            </select>
          </Field>
          <div className="form-row form-row-end" style={{ marginTop: 14 }}>
            <span className="hint">
              {changed
                ? 'Unsaved change.'
                : `Active: ${status?.strategy || 'urltest'}. Failing nodes cool down and are skipped.`}
            </span>
            <button
              type="submit"
              className="btn btn-secondary"
              disabled={!changed || save.isPending}
            >
              {save.isPending ? 'Saving…' : 'Save'}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

export function SingboxPage() {
  const singbox = useSingboxStatusQuery();
  const d = singbox.data;
  const ready = Boolean(d?.ready);
  const nodes = d?.nodes ?? [];
  const [nodePage, setNodePage] = useState(1);
  const NODE_PAGE_SIZE = 20;
  const nodeTotalPages = Math.max(1, Math.ceil(nodes.length / NODE_PAGE_SIZE));
  const safeNodePage = Math.min(nodePage, nodeTotalPages);
  const pagedNodes = nodes.slice((safeNodePage - 1) * NODE_PAGE_SIZE, safeNodePage * NODE_PAGE_SIZE);

  // Three states: ready, configured-but-not-ready (a subscription loaded and
  // then failed, so dials fall back to direct), and idle (no subscription).
  const badgeTone = ready ? 'ok' : d?.last_refresh_error ? 'warn' : 'neutral';
  const badgeText = ready ? 'READY' : d?.last_refresh_error ? 'DEGRADED' : 'IDLE';

  return (
    <div className="page-col">
      <PageHeader
        title="Singbox"
        description="sing-box egress pool: subscription nodes, URL-test selection, and per-upstream outbound tags."
        actions={
          <div className="flex items-center gap-2">
            <Badge tone={badgeTone} title={d?.last_refresh_error || undefined}>
              {badgeText}
            </Badge>
            <RefreshSingboxButton live={ready} />
          </div>
        }
      />

      <KpiGrid>
        <KpiCard label="Nodes" value={d?.node_count != null ? String(d.node_count) : '—'} />
        <KpiCard label="Default outbound" value={d?.default_tag || 'auto'} />
        <KpiCard label="Strategy" value={d?.strategy === 'round_robin' ? 'round-robin' : 'urltest'} />
        <KpiCard label="Refresh interval" value={formatDurationNs(d?.refresh_interval)} />
        <KpiCard
          label="Last refresh"
          value={d?.last_refresh_at ? new Date(d.last_refresh_at).toLocaleTimeString() : '—'}
          tone={d?.last_refresh_error ? 'warn' : 'plain'}
        />
      </KpiGrid>

      <QueryGate isLoading={singbox.isLoading} error={singbox.error}>
        <div className="stack">
          <div className="card">
            <div className="card-header">
              <h2>Nodes</h2>
              <span className="hint">
                {nodes.length} node{nodes.length === 1 ? '' : 's'} from the subscription
              </span>
            </div>
            <div className="card-body tight table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Tag</th>
                    <th>Type</th>
                    <th>Server</th>
                    <th className="num">Port</th>
                    <th className="num">Fails</th>
                  </tr>
                </thead>
                <tbody>
                  {pagedNodes.length === 0 ? (
                    <tr>
                      <td colSpan={5} className="faint">
                        No nodes loaded. Configure a subscription above, then refresh.
                      </td>
                    </tr>
                  ) : (
                    pagedNodes.map((n) => (
                      <tr key={n.tag}>
                        <td className="mono">
                          {n.tag}
                          {n.tag === (d?.default_tag || 'auto') ? (
                            <Badge tone="ok">default</Badge>
                          ) : null}
                          {n.cooling ? (
                            <Badge
                              tone="warn"
                              title={
                                n.cooldown_until
                                  ? `Cooling until ${new Date(n.cooldown_until).toLocaleTimeString()}`
                                  : 'Failing; skipped by the rotation'
                              }
                            >
                              cooling
                            </Badge>
                          ) : null}
                        </td>
                        <td>{n.type}</td>
                        <td className="mono">{n.server}</td>
                        <td className="num">{n.port}</td>
                        <td className="num">
                          {n.failures ? (
                            <span className="hint">{n.failures}×</span>
                          ) : (
                            <span className="hint">—</span>
                          )}
                        </td>
                      </tr>
                    ))
                  )}
                </tbody>
              </table>
            </div>
            <div className="card-body tight" style={{ paddingTop: 0 }}>
              <Pagination
                total={nodes.length}
                page={safeNodePage}
                pageSize={NODE_PAGE_SIZE}
                totalPages={nodeTotalPages}
                onPageChange={setNodePage}
                unit="node"
              />
            </div>
          </div>

          <SingboxSubscriptionCard status={d} />
          <SingboxDefaultTagCard status={d} />
          <SingboxStrategyCard status={d} />

          <div className="card">
            <div className="card-header">
              <h2>Status</h2>
            </div>
            <div className="card-body">
              <div className="kv-list">
                <div>
                  <span className="k">Ready</span>
                  <span className="v">{ready ? 'yes' : 'no (dialling direct)'}</span>
                </div>
                <div>
                  <span className="k">Box created</span>
                  <span className="v">{d?.box_created ? 'yes' : 'no'}</span>
                </div>
                <div>
                  <span className="k">Running</span>
                  <span className="v">{d?.running ? 'yes' : 'no'}</span>
                </div>
                <div>
                  <span className="k">Nodes</span>
                  <span className="v">{d?.node_count ?? '—'}</span>
                </div>
                <div>
                  <span className="k">Default tag</span>
                  <span className="v">{d?.default_tag || 'auto'}</span>
                </div>
                <div>
                  <span className="k">Strategy</span>
                  <span className="v">{d?.strategy || 'urltest'}</span>
                </div>
                <div>
                  <span className="k">Refresh interval</span>
                  <span className="v">{formatDurationNs(d?.refresh_interval)}</span>
                </div>
                <div>
                  <span className="k">Last refresh</span>
                  <span className="v">
                    {d?.last_refresh_at ? new Date(d.last_refresh_at).toLocaleString() : '—'}
                  </span>
                </div>
              </div>
            </div>
          </div>

          {d?.last_refresh_error ? (
            <div className="card" style={{ borderLeft: '3px solid var(--danger)' }}>
              <div className="card-body">
                <div style={{ fontSize: 13, fontWeight: 600 }}>Last refresh error</div>
                <div
                  className="hint"
                  style={{
                    marginTop: 4,
                    fontSize: 12,
                    wordBreak: 'break-word',
                    fontFamily: 'var(--font-mono, monospace)',
                  }}
                >
                  {d.last_refresh_error}
                </div>
              </div>
            </div>
          ) : null}
        </div>
      </QueryGate>
    </div>
  );
}
