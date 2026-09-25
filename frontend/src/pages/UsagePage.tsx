import { useMemo, useState } from 'react';
import { KpiCard, KpiGrid, PageHeader } from '@/components/ui/PageHeader';
import { QueryGate } from '@/components/ui/QueryGate';
import { useTelemetryQuery } from '@/services/api';

function fmtTokens(n: number | undefined): string {
  if (!n) return '0';
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M';
  if (n >= 1_000) return (n / 1_000).toFixed(0) + 'K';
  return String(n);
}

export function UsagePage() {
  const telemetry = useTelemetryQuery();
  const [tenantFilter, setTenantFilter] = useState('all');
  const [search, setSearch] = useState('');

  const s = telemetry.data?.summary;
  const rows = telemetry.data?.tenants_usage ?? [];
  const tenantNames = useMemo(() => Array.from(new Set(rows.map((r) => r.tenant))), [rows]);

  const filtered = rows.filter(
    (r) =>
      (tenantFilter === 'all' || r.tenant === tenantFilter) &&
      (search === '' || r.model.toLowerCase().includes(search.toLowerCase())),
  );

  return (
    <div className="page-col">
      <PageHeader
        title="Usage"
        description="Metering per tenant, model, and credential from the usage recorder."
      />

      <KpiGrid>
        <KpiCard label="Total requests" value={s ? s.total_requests.toLocaleString() : '—'} />
        <KpiCard label="Tokens in" value={s ? fmtTokens(s.input_tokens) : '—'} />
        <KpiCard label="Tokens out" value={s ? fmtTokens(s.output_tokens) : '—'} />
        <KpiCard label="Estimated cost" value={s ? `$${s.estimated_cost_usd?.toFixed(2) ?? '0.00'}` : '—'} />
      </KpiGrid>

      <div className="filter-bar">
        <select
          aria-label="Filter by tenant"
          value={tenantFilter}
          onChange={(e) => setTenantFilter(e.target.value)}
        >
          <option value="all">All tenants</option>
          {tenantNames.map((name) => (
            <option key={name} value={name}>
              {name}
            </option>
          ))}
        </select>
        <span className="spacer" />
        <input
          type="search"
          placeholder="Search model…"
          aria-label="Search model"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
      </div>

      <QueryGate isLoading={telemetry.isLoading} error={telemetry.error}>
        <div className="card">
          <div className="card-body tight table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Tenant</th>
                  <th>Model</th>
                  <th>Credential</th>
                  <th className="num">Requests</th>
                </tr>
              </thead>
              <tbody>
                {filtered.map((r) => (
                  <tr key={`${r.tenant}-${r.model}-${r.credential_ref}`}>
                    <td>{r.tenant}</td>
                    <td>{r.model}</td>
                    <td className="mono dim" style={{ fontSize: 12 }}>{r.credential_ref}</td>
                    <td className="num">{r.total_requests.toLocaleString()}</td>
                  </tr>
                ))}
                {filtered.length === 0 ? (
                  <tr>
                    <td colSpan={4} className="faint">Tidak ada baris usage yang cocok.</td>
                  </tr>
                ) : null}
              </tbody>
            </table>
          </div>
        </div>
        <p className="hint" style={{ marginTop: 10, fontSize: 12, color: 'var(--faint)' }}>
          Ledger token per tenant/model/credential tercatat di usage recorder gateway; agregat
          token in/out tampil pada kartu ringkasan.
        </p>
      </QueryGate>
    </div>
  );
}
