import { useState, useMemo } from 'react';
import { Badge } from '@/components/ui/Badge';
import { KpiCard, KpiGrid, PageHeader } from '@/components/ui/PageHeader';
import { QueryGate } from '@/components/ui/QueryGate';
import { ProviderIcon } from '@/components/ui/ProviderIcon';
import { UpstreamDrawer } from '@/components/upstream/UpstreamDrawer';
import { useUiStore } from '@/state/store';
import {
  useSettingsQuery,
  useTelemetryQuery,
  useSaveSettingsSmart,
  withSettings,
} from '@/services/api';
import type { UpstreamTelemetryDTO } from '@/services/schema';
import { navigate } from '@/lib/router';
import { ChevronLeft, ChevronRight } from 'lucide-react';

function formatEndpoint(url: string): string {
  try {
    const u = new URL(url);
    const path = u.pathname === '/' ? '' : u.pathname;
    return `${u.host}${path}`;
  } catch {
    return url.replace(/^https?:\/\//, '');
  }
}

export function UpstreamsPage() {
  const telemetry = useTelemetryQuery();
  const settings = useSettingsQuery();
  const save = useSaveSettingsSmart();
  const pushToast = useUiStore((s) => s.pushToast);
  const [selected, setSelected] = useState<UpstreamTelemetryDTO | null>(null);
  const [search, setSearch] = useState('');
  const [protocolFilter, setProtocolFilter] = useState('all');
  const [statusFilter, setStatusFilter] = useState('all');
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(12);

  const ups = telemetry.data?.upstreams ?? [];
  const entries = settings.data?.upstreams ?? [];

  const keyStats = useMemo(() => {
    const map = new Map<string, { active: number; cooldown: number; revoked: number }>();
    for (const u of ups) {
      map.set(u.name, {
        active: u.slots.filter((s) => !s.is_cooldown && !s.is_revoked).length,
        cooldown: u.slots.filter((s) => s.is_cooldown).length,
        revoked: u.slots.filter((s) => s.is_revoked).length,
      });
    }
    return map;
  }, [ups]);

  const totalActive = useMemo(
    () => Array.from(keyStats.values()).reduce((acc, k) => acc + k.active, 0),
    [keyStats],
  );
  const totalCooldown = useMemo(
    () => Array.from(keyStats.values()).reduce((acc, k) => acc + k.cooldown, 0),
    [keyStats],
  );
  const totalRevoked = useMemo(
    () => Array.from(keyStats.values()).reduce((acc, k) => acc + k.revoked, 0),
    [keyStats],
  );
  const openBreakers = useMemo(
    () => ups.filter((u) => u.breaker_state === 'OPEN').length,
    [ups],
  );
  const totalIssues = totalCooldown + totalRevoked;
  const issueUnit = useMemo(() => {
    if (totalCooldown > 0 && totalRevoked > 0) return `${totalCooldown} cd · ${totalRevoked} rev`;
    if (totalCooldown > 0) return `${totalCooldown} cooldown`;
    if (totalRevoked > 0) return `${totalRevoked} revoked`;
    return undefined;
  }, [totalCooldown, totalRevoked]);

  const protocols = useMemo(() => {
    const set = new Set<string>();
    for (const u of ups) {
      if (u.protocol) set.add(u.protocol);
    }
    return Array.from(set).sort();
  }, [ups]);

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase();
    return ups.filter((u) => {
      // Protocol filter
      if (protocolFilter !== 'all' && u.protocol.toLowerCase() !== protocolFilter.toLowerCase()) {
        return false;
      }

      // Status filter
      if (statusFilter !== 'all') {
        const entry = entries.find((e) => e.name === u.name);
        const isDisabled = entry?.enabled === false;
        const stat = keyStats.get(u.name) ?? { active: 0, cooldown: 0, revoked: 0 };

        if (statusFilter === 'active' && (isDisabled || u.breaker_state === 'OPEN')) {
          return false;
        }
        if (statusFilter === 'disabled' && !isDisabled) {
          return false;
        }
        if (statusFilter === 'breaker_open' && u.breaker_state !== 'OPEN') {
          return false;
        }
        if (statusFilter === 'cooldown' && stat.cooldown === 0) {
          return false;
        }
      }

      // Search query
      if (!q) return true;
      return (
        u.name.toLowerCase().includes(q) ||
        u.protocol.toLowerCase().includes(q) ||
        u.base_url.toLowerCase().includes(q)
      );
    });
  }, [ups, entries, keyStats, search, protocolFilter, statusFilter]);

  const totalPages = Math.max(1, Math.ceil(filtered.length / pageSize));
  const safePage = Math.min(page, totalPages);
  const startIdx = filtered.length === 0 ? 0 : (safePage - 1) * pageSize + 1;
  const endIdx = Math.min(safePage * pageSize, filtered.length);
  const paged = useMemo(
    () => filtered.slice((safePage - 1) * pageSize, safePage * pageSize),
    [filtered, safePage, pageSize],
  );

  function toggleEnabled(name: string) {
    if (!settings.data) return;
    const current = settings.data.upstreams.find((u) => u.name === name);
    if (!current) return;
    const nextState = current.enabled === false;
    const updated = settings.data.upstreams.map((u) =>
      u.name === name ? { ...u, enabled: nextState } : u,
    );
    save.mutate(withSettings(settings.data, { upstreams: updated }), {
      onSuccess: (d) =>
        pushToast({
          type: 'success',
          title: nextState ? 'Upstream enabled' : 'Upstream disabled',
          message: d.local ? 'Demo mode: changes are local to browser only.' : 'Catalog synchronized.',
        }),
      onError: (e) =>
        pushToast({ type: 'error', title: 'Failed', message: e instanceof Error ? e.message : 'Unknown' }),
    });
  }

  function deleteUpstream(name: string) {
    if (!settings.data) return;
    const referencing = (settings.data.models ?? []).filter((m) => m.upstream === name);
    if (referencing.length > 0) {
      pushToast({
        type: 'error',
        title: 'Cannot delete',
        message: `This upstream is still referenced by ${referencing.length} model(s): ${referencing.map((m) => m.public_name).join(', ')}. Please update or remove those model routes first.`,
      });
      return;
    }

    if (!window.confirm(`Are you sure you want to delete upstream "${name}"?`)) return;

    const nextUpstreams = (settings.data.upstreams ?? []).filter((u) => u.name !== name);
    save.mutate(withSettings(settings.data, { upstreams: nextUpstreams }), {
      onSuccess: () => {
        pushToast({
          type: 'success',
          title: 'Upstream deleted',
          message: `Upstream '${name}' successfully deleted.`,
        });
        setSelected(null);
      },
      onError: (e) =>
        pushToast({ type: 'error', title: 'Failed', message: e instanceof Error ? e.message : 'Unknown' }),
    });
  }

  return (
    <div className="page-col">
      <PageHeader
        title="Upstreams"
        description="Provider fleet, circuit breakers, and KeyRing rotation."
        actions={
          <button className="btn btn-primary" onClick={() => navigate('upstream/new')}>
            Add Upstream
          </button>
        }
      />

      <KpiGrid>
        <KpiCard label="Fleet size" value={String(ups.length)} />
        <KpiCard label="Keys active" value={String(totalActive)} tone="ok" />
        <KpiCard
          label="Breaker open"
          value={String(openBreakers)}
          tone={openBreakers > 0 ? 'danger' : 'plain'}
        />
        <KpiCard
          label="Key issues"
          value={String(totalIssues)}
          unit={issueUnit}
          tone={totalRevoked > 0 ? 'danger' : totalCooldown > 0 ? 'warn' : 'plain'}
        />
      </KpiGrid>

      <div className="filter-bar">
        <select
          aria-label="Filter by protocol"
          value={protocolFilter}
          onChange={(e) => {
            setProtocolFilter(e.target.value);
            setPage(1);
          }}
        >
          <option value="all">All protocols</option>
          {protocols.map((p) => (
            <option key={p} value={p}>
              {p}
            </option>
          ))}
        </select>

        <select
          aria-label="Filter by status"
          value={statusFilter}
          onChange={(e) => {
            setStatusFilter(e.target.value);
            setPage(1);
          }}
        >
          <option value="all">All status</option>
          <option value="active">Active</option>
          <option value="disabled">Disabled</option>
          <option value="breaker_open">Breaker open</option>
          <option value="cooldown">In cooldown</option>
        </select>

        <span className="spacer" />

        <input
          type="search"
          placeholder="Search upstreams…"
          aria-label="Search upstreams"
          value={search}
          onChange={(e) => {
            setSearch(e.target.value);
            setPage(1);
          }}
        />

        <span className="text-xs text-faint">
          {filtered.length} {filtered.length === 1 ? 'upstream' : 'upstreams'}
        </span>
      </div>

      <QueryGate isLoading={telemetry.isLoading} error={telemetry.error}>
        <div className="upstream-grid">
          {paged.map((u) => {
            const entry = entries.find((e) => e.name === u.name);
            const stat = keyStats.get(u.name) ?? { active: 0, cooldown: 0, revoked: 0 };
            const isDisabled = entry?.enabled === false;
            const poolCount = (entry?.credential_pool ?? []).length || stat.active;

            return (
              <div
                key={u.name}
                className={`upstream-card ${isDisabled ? 'is-disabled' : ''}`}
                onClick={() => setSelected(u)}
                role="button"
                tabIndex={0}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault();
                    setSelected(u);
                  }
                }}
              >
                {/* Row 1: Protocol Icon + Name + Protocol/Egress badge on left, Status on right */}
                <div className="flex items-center justify-between gap-3">
                  <div className="flex items-center gap-2.5 min-w-0">
                    <div className="upstream-icon-badge">
                      <ProviderIcon protocol={u.protocol} name={u.name} size={20} />
                    </div>
                    <div className="flex items-center gap-2 min-w-0">
                      <span className="font-semibold text-ink text-sm truncate" title={u.name}>
                        {u.name}
                      </span>
                      <span className="px-1.5 py-0.5 rounded text-[10px] uppercase font-mono font-medium bg-[var(--surface-raised)] text-muted border border-[var(--line)] shrink-0">
                        {u.protocol}
                      </span>
                      {entry?.egress_mode === 'warp' ? (
                        <span
                          className="px-1.5 py-0.5 rounded text-[10px] font-mono font-medium bg-cyan-500/10 text-cyan-400 border border-cyan-500/20 shrink-0"
                          title="Cloudflare WARP Egress"
                        >
                          WARP
                        </span>
                      ) : null}
                      {entry?.egress_mode === 'proxy' ? (
                        <span
                          className="px-1.5 py-0.5 rounded text-[10px] font-mono font-medium bg-amber-500/10 text-amber-400 border border-amber-500/20 shrink-0"
                          title={entry.proxy_url ? `Proxy: ${entry.proxy_url}` : 'Egress Proxy'}
                        >
                          PROXY
                        </span>
                      ) : null}
                    </div>
                  </div>

                  <div className="shrink-0">
                    {isDisabled ? (
                      <Badge tone="neutral">DISABLED</Badge>
                    ) : u.breaker_state === 'OPEN' ? (
                      <Badge tone="danger">BREAKER OPEN</Badge>
                    ) : u.breaker_state === 'HALF-OPEN' ? (
                      <Badge tone="warn">HALF-OPEN</Badge>
                    ) : stat.active === 0 ? (
                      <Badge tone="warn">0 KEYS</Badge>
                    ) : (
                      <Badge tone="ok">{stat.active} {stat.active === 1 ? 'KEY' : 'KEYS'}</Badge>
                    )}
                  </div>
                </div>

                {/* Row 2: Host Endpoint */}
                <div className="upstream-endpoint-strip" title={u.base_url}>
                  <span className="endpoint-text">{formatEndpoint(u.base_url)}</span>
                </div>

                {/* Row 3: Pool refs and Total requests */}
                <div className="flex items-center justify-between gap-2 pt-2 border-t border-[var(--line)] text-xs text-faint">
                  <div className="flex items-center gap-1.5">
                    <span>Pool:</span>
                    <span className="text-ink font-mono font-medium">
                      {poolCount} {poolCount === 1 ? 'ref' : 'refs'}
                    </span>
                    {stat.cooldown > 0 ? (
                      <span className="text-warn font-mono">({stat.cooldown} cd)</span>
                    ) : null}
                    {stat.revoked > 0 ? (
                      <span className="text-danger font-mono">({stat.revoked} rev)</span>
                    ) : null}
                  </div>
                  <div className="flex items-center gap-1 shrink-0">
                    <span className="text-ink font-mono font-medium">
                      {u.total_requests.toLocaleString()}
                    </span>
                    <span>reqs</span>
                  </div>
                </div>
              </div>
            );
          })}
        </div>

        {filtered.length === 0 ? (
          <div className="card">
            <div className="card-body text-center" style={{ padding: '48px 24px' }}>
              <span className="text-faint text-sm">
                {search || protocolFilter !== 'all' || statusFilter !== 'all'
                  ? 'No upstreams match the filter or search.'
                  : 'No upstreams registered yet.'}
              </span>
            </div>
          </div>
        ) : null}

        {filtered.length > 0 ? (
          <div className="pagination-bar">
            <span className="tabular-nums">
              Showing <span className="text-ink font-medium">{startIdx}–{endIdx}</span> of{' '}
              <span className="text-ink font-medium">{filtered.length}</span> upstreams
            </span>

            <div className="pagination-controls">
              <label className="flex items-center gap-1.5 text-xs text-faint">
                Per page:
                <select
                  value={pageSize}
                  onChange={(e) => {
                    setPageSize(Number(e.target.value));
                    setPage(1);
                  }}
                  aria-label="Upstreams per page"
                >
                  <option value={12}>12</option>
                  <option value={24}>24</option>
                  <option value={48}>48</option>
                </select>
              </label>

              {totalPages > 1 ? (
                <div className="flex items-center gap-1 ml-2">
                  <button
                    type="button"
                    className="pagination-btn"
                    disabled={safePage <= 1}
                    onClick={() => setPage((p) => Math.max(1, p - 1))}
                    aria-label="Previous page"
                  >
                    <ChevronLeft className="w-4 h-4" />
                  </button>
                  <span className="text-xs text-faint px-2 tabular-nums">
                    {safePage} / {totalPages}
                  </span>
                  <button
                    type="button"
                    className="pagination-btn"
                    disabled={safePage >= totalPages}
                    onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
                    aria-label="Next page"
                  >
                    <ChevronRight className="w-4 h-4" />
                  </button>
                </div>
              ) : null}
            </div>
          </div>
        ) : null}
      </QueryGate>

      <UpstreamDrawer
        selected={selected}
        entry={entries.find((e) => e.name === selected?.name)}
        onClose={() => setSelected(null)}
        onToggleEnabled={toggleEnabled}
        isToggling={save.isPending}
        onDelete={deleteUpstream}
      />
    </div>
  );
}
