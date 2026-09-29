import { Badge } from '@/components/ui/Badge';
import { KpiCard, KpiGrid, PageHeader } from '@/components/ui/PageHeader';
import { QueryGate } from '@/components/ui/QueryGate';
import {
  useProviderQuotaQuery,
  useProviderQuotaRefreshMutation,
  useSettingsQuery,
  useTopupTenantMutation,
} from '@/services/api';
import type { ProviderQuotaDTO } from '@/services/schema';
import { useUiStore } from '@/state/store';

const STATUS_TONE: Record<string, 'ok' | 'warn' | 'danger' | 'neutral'> = {
  active: 'ok',
  suspended: 'warn',
  revoked: 'danger',
  exhausted: 'warn',
  expired: 'warn',
};

/** Tones follow DESIGN_RULES §8: the bar color is never the only signal. */
function quotaTone(pct: number, exhausted?: boolean): 'ok' | 'warn' | 'danger' {
  if (exhausted || pct <= 0) return 'danger';
  if (pct <= 20) return 'danger';
  if (pct <= 45) return 'warn';
  return 'ok';
}

const TONE_BAR: Record<'ok' | 'warn' | 'danger', string> = {
  ok: 'var(--biolum)',
  warn: 'var(--warn)',
  danger: 'var(--danger)',
};

function pctText(pct: number): string {
  return `${Math.max(0, Math.min(100, Math.round(pct)))}%`;
}

/**
 * A reset countdown ("in 4h", "in 12m") or an em dash when the provider did not
 * report one. Counted from the upstream's own reset time, never from our cache
 * age, so a stale snapshot cannot move the deadline.
 */
function resetIn(iso?: string): string {
  if (!iso) return '—';
  const at = Date.parse(iso);
  if (Number.isNaN(at)) return '—';
  const ms = at - Date.now();
  if (ms <= 0) return 'now';
  const mins = Math.round(ms / 60000);
  if (mins < 60) return `in ${Math.max(1, mins)}m`;
  const hours = Math.round(ms / 3600000);
  if (hours < 48) return `in ${hours}h`;
  return `in ${Math.round(hours / 24)}d`;
}

function QuotaBar({ pct, exhausted }: { pct: number; exhausted?: boolean }) {
  const tone = quotaTone(pct, exhausted);
  return (
    <div
      className="progress"
      role="progressbar"
      aria-valuenow={Math.round(Math.max(0, Math.min(100, pct)))}
      aria-valuemin={0}
      aria-valuemax={100}
      title={`${pctText(pct)} remaining`}
    >
      <div
        className="fill"
        style={{ width: `${Math.max(0, Math.min(100, pct))}%`, background: TONE_BAR[tone] }}
      />
    </div>
  );
}

function ProviderQuotaCard({ quota }: { quota: ProviderQuotaDTO }) {
  const models = quota.models ?? [];
  const windows = quota.windows ?? [];
  return (
    <div className="card">
      <div className="card-body">
        <div style={{ display: 'flex', justifyContent: 'space-between', gap: 12, flexWrap: 'wrap' }}>
          <div>
            <strong>{quota.email || quota.connection_id}</strong>{' '}
            <Badge tone={quota.free_tier ? 'neutral' : 'info'}>{quota.plan}</Badge>{' '}
            {quota.token_expired ? <Badge tone="danger">TOKEN EXPIRED</Badge> : null}
          </div>
          <span className="mono faint">
            {quota.provider}
            {quota.from_cache && quota.age_seconds > 0 ? ` · cached ${quota.age_seconds}s ago` : ' · live'}
          </span>
        </div>

        {quota.message ? (
          <p className="hint" style={{ marginTop: 8 }}>
            {quota.message}
          </p>
        ) : null}

        {windows.length > 0 ? (
          <div style={{ marginTop: 12, display: 'grid', gap: 8 }}>
            {windows.map((w) => (
              <div key={`${w.family}-${w.window}`}>
                <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 4 }}>
                  <span className="hint">
                    {w.family} &middot; {w.window}
                  </span>
                  <span className="mono faint">
                    {pctText(w.remaining_percent)} &middot; resets {resetIn(w.reset_at)}
                  </span>
                </div>
                <QuotaBar pct={w.remaining_percent} />
              </div>
            ))}
          </div>
        ) : null}

        {models.length > 0 ? (
          <div className="table-wrap" style={{ marginTop: 12 }}>
            <table>
              <thead>
                <tr>
                  <th>Model</th>
                  <th style={{ width: '38%' }}>Remaining</th>
                  <th className="num">Left</th>
                  <th>Resets</th>
                </tr>
              </thead>
              <tbody>
                {models.map((m) => (
                  <tr key={m.model}>
                    <td className="mono">{m.model}</td>
                    <td>
                      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                        <div style={{ flex: 1 }}>
                          <QuotaBar pct={m.remaining_percent} exhausted={m.exhausted} />
                        </div>
                        <span className="mono">{pctText(m.remaining_percent)}</span>
                      </div>
                    </td>
                    <td className="num">
                      {m.exhausted ? <Badge tone="danger">EXHAUSTED</Badge> : <span className="faint">—</span>}
                    </td>
                    <td className="dim">{resetIn(m.reset_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : null}
      </div>
    </div>
  );
}

export function QuotaPage() {
  const settings = useSettingsQuery();
  const topup = useTopupTenantMutation();
  const quota = useProviderQuotaQuery();
  const refreshQuota = useProviderQuotaRefreshMutation();
  const pushToast = useUiStore((s) => s.pushToast);

  const tenants = settings.data?.tenants ?? [];
  const quotaTenants = tenants.filter((t) => t.max_tokens !== undefined);
  const totalRemaining = quotaTenants.reduce(
    (a, t) => a + Math.max(0, (t.max_tokens ?? 0) - (t.used_tokens ?? 0)),
    0,
  );
  const week = Date.now() / 1000 + 7 * 86400;
  const expiring = tenants.filter((t) => t.expires_at != null && t.expires_at < week).length;
  const suspended = tenants.filter((t) => String(t.status) !== 'active').length;

  // Provider-side counters: how many accounts are out on at least one model, and
  // how many need a reconnect. An empty list is normal (nothing connected), so it
  // is never dressed up as a quota of zero.
  const providerQuotas = quota.data?.providers ?? [];
  const exhaustedModels = providerQuotas.reduce(
    (n, p) => n + (p.models ?? []).filter((m) => m.exhausted).length,
    0,
  );
  const needsReconnect = providerQuotas.filter((p) => p.token_expired).length;

  return (
    <div className="page-col">
      <PageHeader
        title="Quota"
        description="Token quota & expiry per tenant, plus the upstream allowance of every connected OAuth account."
      />

      <KpiGrid>
        <KpiCard label="Tenants with quota" value={String(quotaTenants.length)} />
        <KpiCard label="Total remaining" value={totalRemaining >= 1_000_000 ? `${(totalRemaining / 1_000_000).toFixed(1)}M` : totalRemaining.toLocaleString()} />
        <KpiCard label="Expiring ≤7d" value={String(expiring)} tone={expiring > 0 ? 'warn' : 'plain'} />
        <KpiCard label="Non-active" value={String(suspended)} tone={suspended > 0 ? 'warn' : 'plain'} />
        <KpiCard label="Provider accounts" value={String(providerQuotas.length)} />
        <KpiCard label="Models out of quota" value={String(exhaustedModels)} tone={exhaustedModels > 0 ? 'warn' : 'plain'} />
        <KpiCard label="Needs reconnect" value={String(needsReconnect)} tone={needsReconnect > 0 ? 'warn' : 'plain'} />
      </KpiGrid>

      <div className="card">
        <div className="card-body">
          <div
            style={{
              display: 'flex',
              justifyContent: 'space-between',
              alignItems: 'center',
              gap: 12,
              flexWrap: 'wrap',
            }}
          >
            <div>
              <h3 style={{ margin: 0 }}>Provider quota</h3>
              <p className="hint" style={{ margin: '4px 0 0' }}>
                What each provider says it has left. Read-only: refreshing never spends the quota it
                reports. A model at 0% is skipped by routing until its reset.
              </p>
            </div>
            <button
              className="btn btn-ghost"
              disabled={refreshQuota.isPending}
              onClick={() =>
                refreshQuota.mutate(undefined, {
                  onSuccess: () =>
                    pushToast({
                      type: 'success',
                      title: 'Provider quota refreshed',
                      message: 'Fetched the current allowance from every connected provider.',
                    }),
                  onError: (e) =>
                    pushToast({
                      type: 'error',
                      title: 'Quota refresh failed',
                      message: e instanceof Error ? e.message : 'Unknown error',
                    }),
                })
              }
            >
              {refreshQuota.isPending ? 'Refreshing…' : 'Refresh'}
            </button>
          </div>
        </div>
      </div>

      <QueryGate isLoading={quota.isLoading} error={quota.error}>
        {providerQuotas.length === 0 ? (
          <div className="card">
            <div className="card-body">
              <p className="hint" style={{ margin: 0 }}>
                No connected account exposes a quota endpoint
                {quota.data?.supported?.length ? ` yet (supported: ${quota.data.supported.join(', ')})` : ''}.
                Connect one on the Providers page to see its allowance here.
              </p>
            </div>
          </div>
        ) : (
          providerQuotas.map((p) => <ProviderQuotaCard key={p.connection_id} quota={p} />)
        )}
      </QueryGate>

      <QueryGate isLoading={settings.isLoading} error={settings.error}>
        <div className="card">
          <div className="card-body tight table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Tenant</th>
                  <th>Status</th>
                  <th className="num">Max tokens</th>
                  <th className="num">Used</th>
                  <th className="num">Remaining</th>
                  <th>Expires</th>
                  <th className="num">Actions</th>
                </tr>
              </thead>
              <tbody>
                {tenants.map((t) => {
                  const max = t.max_tokens;
                  const used = t.used_tokens ?? 0;
                  const remaining = max !== undefined ? max - used : undefined;
                  const exhausted = remaining !== undefined && remaining <= 0;
                  return (
                    <tr key={t.name}>
                      <td>{t.name}</td>
                      <td>
                        <Badge tone={exhausted ? 'danger' : STATUS_TONE[String(t.status)] ?? 'neutral'}>
                          {exhausted ? 'EXHAUSTED' : String(t.status).toUpperCase()}
                        </Badge>
                      </td>
                      <td className="num">{max !== undefined ? max.toLocaleString() : '—'}</td>
                      <td className="num">{max !== undefined ? used.toLocaleString() : '—'}</td>
                      <td className="num">
                        {remaining !== undefined ? remaining.toLocaleString() : '—'}
                        {max !== undefined && remaining !== undefined && remaining < max * 0.1 && remaining > 0 ? (
                          <span className="sub">low</span>
                        ) : null}
                      </td>
                      <td className="dim">
                        {t.expires_at ? new Date(t.expires_at * 1000).toLocaleString() : '—'}
                      </td>
                      <td className="num">
                        <button
                          className="btn btn-ghost"
                          disabled={topup.isPending}
                          onClick={() =>
                            topup.mutate(
                              { tenant_name: t.name, reset_used: false },
                              {
                                onSuccess: (d) =>
                                  pushToast({
                                    type: 'success',
                                    title: 'Tenant top-up',
                                    message: d.message || `Remaining ${d.remaining_tokens.toLocaleString()} tokens.`,
                                  }),
                                onError: (e) =>
                                  pushToast({
                                    type: 'error',
                                    title: 'Top-up failed',
                                    message: e instanceof Error ? e.message : 'Unknown error',
                                  }),
                              },
                            )
                          }
                        >
                          Topup
                        </button>
                      </td>
                    </tr>
                  );
                })}
                {tenants.length === 0 ? (
                  <tr>
                    <td colSpan={7} className="faint">No tenants yet.</td>
                  </tr>
                ) : null}
              </tbody>
            </table>
          </div>
        </div>
      </QueryGate>
    </div>
  );
}
