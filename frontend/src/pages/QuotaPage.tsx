import { useMemo, useState } from 'react';
import {
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  ChevronUp,
  RefreshCw,
  Trash2,
} from 'lucide-react';
import { Badge } from '@/components/ui/Badge';
import { Segmented } from '@/components/ui/Controls';
import { KpiCard, KpiGrid, PageHeader } from '@/components/ui/PageHeader';
import { ProviderIcon } from '@/components/ui/ProviderIcon';
import { QueryGate } from '@/components/ui/QueryGate';
import { cn } from '@/lib/utils';
import {
  useDeleteOAuthConnectionMutation,
  useProviderQuotaQuery,
  useProviderQuotaRefreshMutation,
  useSettingsQuery,
  useTopupTenantMutation,
} from '@/services/api';
import type { ProviderQuotaDTO, UpstreamDTO } from '@/services/schema';
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

/**
 * Extract all OAuth connection IDs referenced by configured upstreams in the gateway.
 */
function extractUpstreamOAuthRefs(upstreams: UpstreamDTO[]): Set<string> {
  const refs = new Set<string>();
  for (const u of upstreams) {
    const candidates: (string | undefined | null)[] = [
      u.credential_ref,
      u.api_key,
      ...(u.api_keys ?? []),
      ...(u.credential_pool ?? []).flatMap((c) => [c.ref, c.secret, c.api_key]),
    ];
    for (const c of candidates) {
      if (!c) continue;
      const trimmed = c.trim().toLowerCase();
      if (trimmed.startsWith('oauth:')) {
        refs.add(trimmed.slice(6));
      } else {
        refs.add(trimmed);
      }
    }
  }
  return refs;
}

function isConnectionInUpstreams(quota: ProviderQuotaDTO, upstreamRefs: Set<string>): boolean {
  if (upstreamRefs.size === 0) return false;
  const connId = quota.connection_id.trim().toLowerCase();
  const email = (quota.email ?? '').trim().toLowerCase();
  return (
    upstreamRefs.has(connId) ||
    (email !== '' && upstreamRefs.has(email)) ||
    (email !== '' && upstreamRefs.has(`oauth:${email}`)) ||
    upstreamRefs.has(`oauth:${connId}`)
  );
}

interface ProviderQuotaCardProps {
  quota: ProviderQuotaDTO;
  inUpstream: boolean;
  isDeleting?: boolean;
  onDelete?: () => void;
}

function ProviderQuotaCard({ quota, inUpstream, isDeleting, onDelete }: ProviderQuotaCardProps) {
  const [expanded, setExpanded] = useState(false);
  const models = quota.models ?? [];
  const windows = quota.windows ?? [];

  const isExhausted =
    models.some((m) => m.exhausted || m.remaining_percent <= 0) ||
    windows.some((w) => w.remaining_percent <= 0);

  const visibleModels = expanded ? models : models.slice(0, 3);

  return (
    <div className="card p-3.5 flex flex-col justify-between h-full bg-[var(--surface-card)] border border-[var(--line)] rounded-xl hover:border-[var(--line-strong)] transition-colors">
      <div>
        {/* Card Header */}
        <div className="flex items-start justify-between gap-2 pb-2.5 border-b border-[var(--line)]">
          <div className="flex items-center gap-2 min-w-0">
            <div className="w-7 h-7 rounded-md bg-[var(--surface-raised)] border border-[var(--line)] flex items-center justify-center shrink-0">
              <ProviderIcon protocol={quota.provider} size={15} />
            </div>
            <div className="min-w-0">
              <div
                className="font-semibold text-xs text-ink truncate"
                title={quota.email || quota.connection_id}
              >
                {quota.email || quota.connection_id}
              </div>
              <div className="flex items-center gap-1.5 text-[11px] text-faint font-mono">
                <span className="uppercase">{quota.provider}</span>
                <span>&middot;</span>
                <span>
                  {quota.from_cache && quota.age_seconds > 0
                    ? `${quota.age_seconds}s ago`
                    : 'live'}
                </span>
              </div>
            </div>
          </div>

          <div className="flex flex-col items-end gap-1 shrink-0">
            <div className="flex items-center gap-1">
              <Badge tone={inUpstream ? 'info' : 'neutral'} title={inUpstream ? 'Configured in Upstream fleet' : 'Unassigned from Upstreams'}>
                {inUpstream ? 'UPSTREAM' : 'UNASSIGNED'}
              </Badge>
              <Badge tone={quota.free_tier ? 'neutral' : 'info'}>{quota.plan}</Badge>
              {quota.token_expired ? (
                <Badge tone="danger">EXPIRED</Badge>
              ) : isExhausted ? (
                <Badge tone="warn">OUT OF QUOTA</Badge>
              ) : (
                <Badge tone="ok">ACTIVE</Badge>
              )}
              {onDelete ? (
                <button
                  type="button"
                  className="btn btn-ghost text-xs p-1 text-faint hover:text-danger cursor-pointer transition-colors ml-0.5"
                  title="Disconnect and delete account from OAuth vault"
                  disabled={isDeleting}
                  onClick={onDelete}
                >
                  <Trash2 className="w-3.5 h-3.5" />
                </button>
              ) : null}
            </div>
          </div>
        </div>

        {/* Message Banner if any */}
        {quota.message ? (
          <p className="text-[11px] text-faint bg-[var(--surface-raised)] border border-[var(--line)] rounded px-2 py-1 my-2 leading-snug">
            {quota.message}
          </p>
        ) : null}

        {/* Rate Limit Windows */}
        {windows.length > 0 ? (
          <div className="pt-2 pb-1 space-y-1.5 border-b border-[var(--line)]">
            {windows.map((w) => (
              <div key={`${w.family}-${w.window}`} className="text-xs">
                <div className="flex items-center justify-between text-[11px] mb-1">
                  <span className="text-muted font-medium truncate">
                    {w.family} &middot; {w.window}
                  </span>
                  <span className="font-mono text-faint tabular-nums">
                    {pctText(w.remaining_percent)} &middot; {resetIn(w.reset_at)}
                  </span>
                </div>
                <div
                  className="progress"
                  style={{ height: 4 }}
                  title={`${pctText(w.remaining_percent)} remaining`}
                >
                  <div
                    className="fill"
                    style={{
                      width: `${Math.max(0, Math.min(100, w.remaining_percent))}%`,
                      background: TONE_BAR[quotaTone(w.remaining_percent)],
                    }}
                  />
                </div>
              </div>
            ))}
          </div>
        ) : null}

        {/* Models Allowance List */}
        {models.length > 0 ? (
          <div className="pt-2 space-y-1">
            {visibleModels.map((m) => {
              const tone = quotaTone(m.remaining_percent, m.exhausted);
              return (
                <div
                  key={m.model}
                  className="flex items-center justify-between gap-2 py-1 text-xs border-b border-[var(--line)]/40 last:border-0"
                >
                  <div className="min-w-0 flex items-center gap-1.5 flex-1">
                    <span
                      className="font-mono text-xs text-ink truncate"
                      title={m.display_name || m.model}
                    >
                      {m.model}
                    </span>
                    {m.exhausted ? (
                      <Badge tone="danger" className="text-[9px] px-1 py-0 shrink-0">
                        OUT
                      </Badge>
                    ) : null}
                  </div>

                  <div className="flex items-center gap-2 shrink-0">
                    <div className="w-14">
                      <div
                        className="progress"
                        style={{ height: 4 }}
                        title={`${pctText(m.remaining_percent)} remaining`}
                      >
                        <div
                          className="fill"
                          style={{
                            width: `${Math.max(0, Math.min(100, m.remaining_percent))}%`,
                            background: TONE_BAR[tone],
                          }}
                        />
                      </div>
                    </div>
                    <span className="font-mono text-[11px] text-ink tabular-nums w-8 text-right">
                      {pctText(m.remaining_percent)}
                    </span>
                    <span className="text-[11px] text-faint font-mono w-12 text-right">
                      {resetIn(m.reset_at)}
                    </span>
                  </div>
                </div>
              );
            })}
          </div>
        ) : windows.length === 0 ? (
          <div className="text-xs text-faint py-4 text-center">
            No quota details reported
          </div>
        ) : null}
      </div>

      {/* Show more/less toggle button */}
      {models.length > 3 ? (
        <button
          type="button"
          onClick={() => setExpanded(!expanded)}
          className="text-[11px] text-faint hover:text-ink font-medium transition-colors flex items-center justify-center gap-1 w-full pt-1.5 mt-2 border-t border-[var(--line)] cursor-pointer"
        >
          {expanded ? (
            <>
              Show fewer models <ChevronUp className="w-3 h-3" />
            </>
          ) : (
            <>
              +{models.length - 3} more models <ChevronDown className="w-3 h-3" />
            </>
          )}
        </button>
      ) : null}
    </div>
  );
}

export function QuotaPage() {
  const settings = useSettingsQuery();
  const topup = useTopupTenantMutation();
  const quota = useProviderQuotaQuery();
  const refreshQuota = useProviderQuotaRefreshMutation();
  const deleteOAuth = useDeleteOAuthConnectionMutation();
  const pushToast = useUiStore((s) => s.pushToast);

  // Tab State
  const [activeTab, setActiveTab] = useState<'providers' | 'tenants'>('providers');

  // Provider Scope Filter ('upstream' by default for consistency with Upstreams page)
  const [providerScopeFilter, setProviderScopeFilter] = useState<'upstream' | 'all' | 'unassigned'>('upstream');
  const [providerSearch, setProviderSearch] = useState('');
  const [providerStatusFilter, setProviderStatusFilter] = useState('all');
  const [providerPage, setProviderPage] = useState(1);
  const [providerPageSize, setProviderPageSize] = useState(6);

  // Tenant State
  const [tenantSearch, setTenantSearch] = useState('');
  const [tenantStatusFilter, setTenantStatusFilter] = useState('all');
  const [tenantPage, setTenantPage] = useState(1);
  const [tenantPageSize, setTenantPageSize] = useState(10);

  // Tenant Computations
  const tenants = settings.data?.tenants ?? [];
  const quotaTenants = tenants.filter((t) => t.max_tokens !== undefined);
  const totalRemaining = quotaTenants.reduce(
    (a, t) => a + Math.max(0, (t.max_tokens ?? 0) - (t.used_tokens ?? 0)),
    0,
  );
  const week = Date.now() / 1000 + 7 * 86400;
  const expiring = tenants.filter((t) => t.expires_at != null && t.expires_at < week).length;
  const suspended = tenants.filter((t) => String(t.status) !== 'active').length;

  // Provider Accounts from Vault
  const providerQuotas = quota.data?.providers ?? [];

  // Extract OAuth credentials referenced by configured upstreams
  const upstreamOAuthRefs = useMemo(() => {
    return extractUpstreamOAuthRefs(settings.data?.upstreams ?? []);
  }, [settings.data?.upstreams]);

  // Categorize accounts: active in upstream fleet vs unassigned
  const upstreamProviders = useMemo(() => {
    return providerQuotas.filter((p) => isConnectionInUpstreams(p, upstreamOAuthRefs));
  }, [providerQuotas, upstreamOAuthRefs]);

  const unassignedProviders = useMemo(() => {
    return providerQuotas.filter((p) => !isConnectionInUpstreams(p, upstreamOAuthRefs));
  }, [providerQuotas, upstreamOAuthRefs]);

  // Base list of providers depending on providerScopeFilter
  const baseProviders = useMemo(() => {
    if (providerScopeFilter === 'upstream') return upstreamProviders;
    if (providerScopeFilter === 'unassigned') return unassignedProviders;
    return providerQuotas;
  }, [providerScopeFilter, upstreamProviders, unassignedProviders, providerQuotas]);

  // Provider Computations based on current scope
  const exhaustedModels = baseProviders.reduce(
    (n, p) => n + (p.models ?? []).filter((m) => m.exhausted).length,
    0,
  );
  const needsReconnect = baseProviders.filter((p) => p.token_expired).length;
  const liveCount = baseProviders.filter((p) => !p.from_cache).length;

  // Filtered Providers
  const filteredProviders = useMemo(() => {
    return baseProviders.filter((p) => {
      if (providerSearch.trim()) {
        const q = providerSearch.toLowerCase().trim();
        const matchEmail = (p.email ?? '').toLowerCase().includes(q);
        const matchConn = (p.connection_id ?? '').toLowerCase().includes(q);
        const matchProv = (p.provider ?? '').toLowerCase().includes(q);
        const matchPlan = (p.plan ?? '').toLowerCase().includes(q);
        const matchModel = (p.models ?? []).some((m) => m.model.toLowerCase().includes(q));
        if (!matchEmail && !matchConn && !matchProv && !matchPlan && !matchModel) {
          return false;
        }
      }

      if (providerStatusFilter === 'expired') {
        return p.token_expired;
      }
      if (providerStatusFilter === 'exhausted') {
        const hasExhausted =
          (p.models ?? []).some((m) => m.exhausted || m.remaining_percent <= 0) ||
          (p.windows ?? []).some((w) => w.remaining_percent <= 0);
        return hasExhausted && !p.token_expired;
      }
      if (providerStatusFilter === 'ok') {
        const hasExhausted =
          (p.models ?? []).some((m) => m.exhausted || m.remaining_percent <= 0) ||
          (p.windows ?? []).some((w) => w.remaining_percent <= 0);
        return !p.token_expired && !hasExhausted;
      }

      return true;
    });
  }, [baseProviders, providerSearch, providerStatusFilter]);

  const totalProviderPages = Math.max(1, Math.ceil(filteredProviders.length / providerPageSize));
  const safeProviderPage = Math.min(providerPage, totalProviderPages);
  const pagedProviders = filteredProviders.slice(
    (safeProviderPage - 1) * providerPageSize,
    safeProviderPage * providerPageSize,
  );
  const pStartIdx = filteredProviders.length === 0 ? 0 : (safeProviderPage - 1) * providerPageSize + 1;
  const pEndIdx = Math.min(safeProviderPage * providerPageSize, filteredProviders.length);

  // Filtered Tenants
  const filteredTenants = useMemo(() => {
    return tenants.filter((t) => {
      if (tenantSearch.trim()) {
        const q = tenantSearch.toLowerCase().trim();
        if (!t.name.toLowerCase().includes(q)) return false;
      }

      const max = t.max_tokens;
      const used = t.used_tokens ?? 0;
      const remaining = max !== undefined ? max - used : undefined;
      const exhausted = remaining !== undefined && remaining <= 0;
      const isExpiring = t.expires_at != null && t.expires_at < week;
      const isActive = String(t.status) === 'active';

      if (tenantStatusFilter === 'active') {
        return isActive && !exhausted;
      }
      if (tenantStatusFilter === 'exhausted') {
        return exhausted;
      }
      if (tenantStatusFilter === 'expiring') {
        return isExpiring;
      }
      if (tenantStatusFilter === 'suspended') {
        return !isActive;
      }

      return true;
    });
  }, [tenants, tenantSearch, tenantStatusFilter, week]);

  const totalTenantPages = Math.max(1, Math.ceil(filteredTenants.length / tenantPageSize));
  const safeTenantPage = Math.min(tenantPage, totalTenantPages);
  const pagedTenants = filteredTenants.slice(
    (safeTenantPage - 1) * tenantPageSize,
    safeTenantPage * tenantPageSize,
  );
  const tStartIdx = filteredTenants.length === 0 ? 0 : (safeTenantPage - 1) * tenantPageSize + 1;
  const tEndIdx = Math.min(safeTenantPage * tenantPageSize, filteredTenants.length);

  return (
    <div className="page-col">
      <PageHeader
        title="Quota"
        description="Token quota & expiry per tenant, plus the upstream allowance of every connected OAuth account."
      />

      {/* KPI Summary — exactly 4 cards per tab view */}
      {activeTab === 'providers' ? (
        <KpiGrid>
          <KpiCard
            label={providerScopeFilter === 'upstream' ? 'Accounts in upstream' : 'Provider accounts'}
            value={String(baseProviders.length)}
          />
          <KpiCard
            label="Models out of quota"
            value={String(exhaustedModels)}
            tone={exhaustedModels > 0 ? 'warn' : 'plain'}
          />
          <KpiCard
            label="Needs reconnect"
            value={String(needsReconnect)}
            tone={needsReconnect > 0 ? 'danger' : 'plain'}
          />
          <KpiCard
            label="Live accounts"
            value={String(liveCount)}
            unit={`/ ${baseProviders.length} total`}
            tone="plain"
          />
        </KpiGrid>
      ) : (
        <KpiGrid>
          <KpiCard label="Tenants with quota" value={String(quotaTenants.length)} />
          <KpiCard
            label="Total remaining"
            value={
              totalRemaining >= 1_000_000
                ? `${(totalRemaining / 1_000_000).toFixed(1)}M`
                : totalRemaining.toLocaleString()
            }
          />
          <KpiCard
            label="Expiring ≤7d"
            value={String(expiring)}
            tone={expiring > 0 ? 'warn' : 'plain'}
          />
          <KpiCard
            label="Non-active"
            value={String(suspended)}
            tone={suspended > 0 ? 'warn' : 'plain'}
          />
        </KpiGrid>
      )}

      {/* Tab Switcher */}
      <div style={{ marginBottom: 16 }}>
        <Segmented
          items={[
            {
              id: 'providers',
              label: `Provider Accounts (${upstreamProviders.length}${
                unassignedProviders.length > 0 && providerScopeFilter !== 'upstream'
                  ? ` / ${providerQuotas.length}`
                  : ''
              })`,
            },
            { id: 'tenants', label: `Tenant Quotas (${tenants.length})` },
          ]}
          value={activeTab}
          onChange={(val) => setActiveTab(val as 'providers' | 'tenants')}
          ariaLabel="Quota Category Tabs"
        />
      </div>

      {/* Tab 1: Provider Accounts */}
      {activeTab === 'providers' ? (
        <>
          <div className="filter-bar">
            {/* Scope Selector: Upstream Fleet vs All vs Unassigned */}
            <select
              aria-label="Filter by upstream association"
              value={providerScopeFilter}
              onChange={(e) => {
                setProviderScopeFilter(e.target.value as 'upstream' | 'all' | 'unassigned');
                setProviderPage(1);
              }}
            >
              <option value="upstream">In Upstream Fleet ({upstreamProviders.length})</option>
              <option value="all">All OAuth Accounts ({providerQuotas.length})</option>
              {unassignedProviders.length > 0 ? (
                <option value="unassigned">Unassigned ({unassignedProviders.length})</option>
              ) : null}
            </select>

            <input
              type="search"
              placeholder="Search email, ID, provider…"
              aria-label="Search provider accounts"
              value={providerSearch}
              onChange={(e) => {
                setProviderSearch(e.target.value);
                setProviderPage(1);
              }}
            />

            <select
              aria-label="Filter by provider status"
              value={providerStatusFilter}
              onChange={(e) => {
                setProviderStatusFilter(e.target.value);
                setProviderPage(1);
              }}
            >
              <option value="all">All statuses</option>
              <option value="ok">Active &amp; OK</option>
              <option value="exhausted">Limit reached</option>
              <option value="expired">Token expired</option>
            </select>

            <span className="spacer" />

            <span className="text-xs text-faint">
              {filteredProviders.length} {filteredProviders.length === 1 ? 'account' : 'accounts'}
            </span>

            <button
              type="button"
              className="btn btn-secondary text-xs flex items-center gap-1.5"
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
              <RefreshCw className={cn('w-3.5 h-3.5', refreshQuota.isPending && 'animate-spin')} />
              {refreshQuota.isPending ? 'Refreshing…' : 'Refresh Quota'}
            </button>
          </div>

          {/* Unassigned Accounts Notice */}
          {providerScopeFilter === 'upstream' && unassignedProviders.length > 0 ? (
            <div className="flex flex-wrap items-center justify-between gap-3 px-3.5 py-2.5 rounded-lg bg-[var(--surface-raised)] border border-[var(--line)] text-xs text-muted mb-4">
              <div className="flex items-center gap-2">
                <span className="text-warn">ℹ</span>
                <span>
                  Showing <strong>{upstreamProviders.length}</strong> account(s) active in Upstream fleet.{' '}
                  <span className="text-faint">
                    ({unassignedProviders.length} unassigned OAuth account(s) exist in vault)
                  </span>
                </span>
              </div>
              <button
                type="button"
                className="btn btn-ghost text-xs text-ink hover:text-biolum font-medium p-0"
                onClick={() => {
                  setProviderScopeFilter('unassigned');
                  setProviderPage(1);
                }}
              >
                Manage Unassigned ({unassignedProviders.length}) &rarr;
              </button>
            </div>
          ) : null}

          <QueryGate isLoading={quota.isLoading || settings.isLoading} error={quota.error || settings.error}>
            {filteredProviders.length === 0 ? (
              <div className="card">
                <div className="card-body text-center" style={{ padding: '48px 24px' }}>
                  <p className="text-sm text-faint" style={{ margin: 0 }}>
                    {providerSearch || providerStatusFilter !== 'all'
                      ? 'No provider accounts match your search or filter.'
                      : providerScopeFilter === 'upstream' && upstreamProviders.length === 0
                        ? 'No OAuth provider accounts are currently registered in your Upstreams fleet. Connect an account in Upstreams → Keys tab to view its quota here.'
                        : providerScopeFilter === 'unassigned' && unassignedProviders.length === 0
                          ? 'No unassigned OAuth accounts found in vault.'
                          : providerQuotas.length === 0
                            ? `No connected account exposes a quota endpoint yet${
                                quota.data?.supported?.length
                                  ? ` (supported: ${quota.data.supported.join(', ')})`
                                  : ''
                              }. Connect one on the Providers page to see its allowance here.`
                            : 'No provider accounts found.'}
                  </p>
                </div>
              </div>
            ) : (
              <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
                {pagedProviders.map((p) => {
                  const inUpstream = isConnectionInUpstreams(p, upstreamOAuthRefs);
                  return (
                    <ProviderQuotaCard
                      key={p.connection_id}
                      quota={p}
                      inUpstream={inUpstream}
                      isDeleting={deleteOAuth.isPending}
                      onDelete={() => {
                        const label = p.email || p.connection_id;
                        if (
                          window.confirm(
                            `Disconnect and permanently delete OAuth account "${label}" from Firefly?\n\nThis will remove its stored token from the database vault.`
                          )
                        ) {
                          deleteOAuth.mutate(p.connection_id, {
                            onSuccess: () =>
                              pushToast({
                                type: 'success',
                                title: 'Account removed',
                                message: `${label} was removed from the OAuth vault.`,
                              }),
                            onError: (err) =>
                              pushToast({
                                type: 'error',
                                title: 'Failed to delete',
                                message: err instanceof Error ? err.message : 'Unknown error',
                              }),
                          });
                        }
                      }}
                    />
                  );
                })}
              </div>
            )}
          </QueryGate>

          {filteredProviders.length > 0 ? (
            <div className="pagination-bar">
              <span className="tabular-nums">
                Showing <span className="text-ink font-medium">{pStartIdx}–{pEndIdx}</span> of{' '}
                <span className="text-ink font-medium">{filteredProviders.length}</span> accounts
              </span>

              <div className="pagination-controls">
                <label className="flex items-center gap-1.5 text-xs text-faint">
                  Per page:
                  <select
                    value={providerPageSize}
                    onChange={(e) => {
                      setProviderPageSize(Number(e.target.value));
                      setProviderPage(1);
                    }}
                    aria-label="Accounts per page"
                  >
                    <option value={6}>6</option>
                    <option value={12}>12</option>
                    <option value={24}>24</option>
                  </select>
                </label>

                {totalProviderPages > 1 ? (
                  <div className="flex items-center gap-1 ml-2">
                    <button
                      type="button"
                      className="pagination-btn"
                      disabled={safeProviderPage <= 1}
                      onClick={() => setProviderPage((p) => Math.max(1, p - 1))}
                      aria-label="Previous page"
                    >
                      <ChevronLeft className="w-4 h-4" />
                    </button>
                    <span className="text-xs text-faint px-2 tabular-nums">
                      {safeProviderPage} / {totalProviderPages}
                    </span>
                    <button
                      type="button"
                      className="pagination-btn"
                      disabled={safeProviderPage >= totalProviderPages}
                      onClick={() => setProviderPage((p) => Math.min(totalProviderPages, p + 1))}
                      aria-label="Next page"
                    >
                      <ChevronRight className="w-4 h-4" />
                    </button>
                  </div>
                ) : null}
              </div>
            </div>
          ) : null}
        </>
      ) : (
        /* Tab 2: Tenant Quotas */
        <>
          <div className="filter-bar">
            <input
              type="search"
              placeholder="Search tenant name…"
              aria-label="Search tenants"
              value={tenantSearch}
              onChange={(e) => {
                setTenantSearch(e.target.value);
                setTenantPage(1);
              }}
            />
            <select
              aria-label="Filter by tenant status"
              value={tenantStatusFilter}
              onChange={(e) => {
                setTenantStatusFilter(e.target.value);
                setTenantPage(1);
              }}
            >
              <option value="all">All statuses</option>
              <option value="active">Active</option>
              <option value="exhausted">Exhausted</option>
              <option value="expiring">Expiring ≤7d</option>
              <option value="suspended">Suspended / Other</option>
            </select>

            <span className="spacer" />

            <span className="text-xs text-faint">
              {filteredTenants.length} {filteredTenants.length === 1 ? 'tenant' : 'tenants'}
            </span>
          </div>

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
                    {pagedTenants.map((t) => {
                      const max = t.max_tokens;
                      const used = t.used_tokens ?? 0;
                      const remaining = max !== undefined ? max - used : undefined;
                      const exhausted = remaining !== undefined && remaining <= 0;
                      return (
                        <tr key={t.name}>
                          <td className="font-medium text-ink">{t.name}</td>
                          <td>
                            <Badge
                              tone={exhausted ? 'danger' : STATUS_TONE[String(t.status)] ?? 'neutral'}
                            >
                              {exhausted ? 'EXHAUSTED' : String(t.status).toUpperCase()}
                            </Badge>
                          </td>
                          <td className="num">{max !== undefined ? max.toLocaleString() : '—'}</td>
                          <td className="num">{max !== undefined ? used.toLocaleString() : '—'}</td>
                          <td className="num">
                            {remaining !== undefined ? remaining.toLocaleString() : '—'}
                            {max !== undefined &&
                            remaining !== undefined &&
                            remaining < max * 0.1 &&
                            remaining > 0 ? (
                              <span className="sub ml-1 text-warn">low</span>
                            ) : null}
                          </td>
                          <td className="dim">
                            {t.expires_at ? new Date(t.expires_at * 1000).toLocaleString() : '—'}
                          </td>
                          <td className="num">
                            <button
                              className="btn btn-ghost text-xs"
                              disabled={topup.isPending}
                              onClick={() =>
                                topup.mutate(
                                  { tenant_name: t.name, reset_used: false },
                                  {
                                    onSuccess: (d) =>
                                      pushToast({
                                        type: 'success',
                                        title: 'Tenant top-up',
                                        message:
                                          d.message ||
                                          `Remaining ${d.remaining_tokens.toLocaleString()} tokens.`,
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
                    {filteredTenants.length === 0 ? (
                      <tr>
                        <td colSpan={7} className="faint text-center" style={{ padding: '32px 16px' }}>
                          {tenantSearch || tenantStatusFilter !== 'all'
                            ? 'No tenants match the filter or search.'
                            : 'No tenants registered yet.'}
                        </td>
                      </tr>
                    ) : null}
                  </tbody>
                </table>
              </div>
            </div>
          </QueryGate>

          {filteredTenants.length > 0 ? (
            <div className="pagination-bar">
              <span className="tabular-nums">
                Showing <span className="text-ink font-medium">{tStartIdx}–{tEndIdx}</span> of{' '}
                <span className="text-ink font-medium">{filteredTenants.length}</span> tenants
              </span>

              <div className="pagination-controls">
                <label className="flex items-center gap-1.5 text-xs text-faint">
                  Per page:
                  <select
                    value={tenantPageSize}
                    onChange={(e) => {
                      setTenantPageSize(Number(e.target.value));
                      setTenantPage(1);
                    }}
                    aria-label="Tenants per page"
                  >
                    <option value={10}>10</option>
                    <option value={25}>25</option>
                    <option value={50}>50</option>
                  </select>
                </label>

                {totalTenantPages > 1 ? (
                  <div className="flex items-center gap-1 ml-2">
                    <button
                      type="button"
                      className="pagination-btn"
                      disabled={safeTenantPage <= 1}
                      onClick={() => setTenantPage((p) => Math.max(1, p - 1))}
                      aria-label="Previous page"
                    >
                      <ChevronLeft className="w-4 h-4" />
                    </button>
                    <span className="text-xs text-faint px-2 tabular-nums">
                      {safeTenantPage} / {totalTenantPages}
                    </span>
                    <button
                      type="button"
                      className="pagination-btn"
                      disabled={safeTenantPage >= totalTenantPages}
                      onClick={() => setTenantPage((p) => Math.min(totalTenantPages, p + 1))}
                      aria-label="Next page"
                    >
                      <ChevronRight className="w-4 h-4" />
                    </button>
                  </div>
                ) : null}
              </div>
            </div>
          ) : null}
        </>
      )}
    </div>
  );
}
