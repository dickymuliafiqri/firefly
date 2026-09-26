import { Badge } from '@/components/ui/Badge';
import { KpiCard, KpiGrid, PageHeader } from '@/components/ui/PageHeader';
import { QueryGate } from '@/components/ui/QueryGate';
import { useSettingsQuery, useTopupTenantMutation } from '@/services/api';
import { useUiStore } from '@/state/store';

const STATUS_TONE: Record<string, 'ok' | 'warn' | 'danger' | 'neutral'> = {
  active: 'ok',
  suspended: 'warn',
  revoked: 'danger',
  exhausted: 'warn',
  expired: 'warn',
};

export function QuotaPage() {
  const settings = useSettingsQuery();
  const topup = useTopupTenantMutation();
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

  return (
    <div className="page-col">
      <PageHeader
        title="Quota"
        description="Token quota & expiry per tenant, from the gateway tenant ledger."
      />

      <KpiGrid>
        <KpiCard label="Tenants with quota" value={String(quotaTenants.length)} />
        <KpiCard label="Total remaining" value={totalRemaining >= 1_000_000 ? `${(totalRemaining / 1_000_000).toFixed(1)}M` : totalRemaining.toLocaleString()} />
        <KpiCard label="Expiring ≤7d" value={String(expiring)} tone={expiring > 0 ? 'warn' : 'plain'} />
        <KpiCard label="Non-active" value={String(suspended)} tone={suspended > 0 ? 'warn' : 'plain'} />
      </KpiGrid>

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
