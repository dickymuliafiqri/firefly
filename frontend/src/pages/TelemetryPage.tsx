import { Badge } from '@/components/ui/Badge';
import { KpiCard, KpiGrid, PageHeader } from '@/components/ui/PageHeader';
import { QueryGate } from '@/components/ui/QueryGate';
import { useTelemetryQuery } from '@/services/api';

export function TelemetryPage() {
  const telemetry = useTelemetryQuery();
  const s = telemetry.data?.summary;
  const models = telemetry.data?.models ?? [];

  return (
    <div className="page-col">
      <PageHeader
        title="Telemetry"
        description="Realtime throughput & latency percentiles from the gateway."
      />

      <KpiGrid>
        <KpiCard label="p50" value={s ? String(Math.round(s.p50_latency_ms)) : '—'} unit="ms" />
        <KpiCard label="p95" value={s ? String(Math.round(s.p95_latency_ms)) : '—'} unit="ms" />
        <KpiCard label="p99" value={s ? String(Math.round(s.p99_latency_ms)) : '—'} unit="ms" />
        <KpiCard
          label="Tokens total"
          value={s ? fmtTokens(s.total_tokens ?? 0) : '—'}
          tone={s && (s.total_tokens ?? 0) > 0 ? 'ok' : 'plain'}
        />
      </KpiGrid>

      <QueryGate isLoading={telemetry.isLoading} error={telemetry.error}>
        <div className="stack">
          <div className="card">
            <div className="card-header">
              <h2>Per-model aggregate</h2>
              <Badge tone="neutral">LIVE · {telemetry.data ? new Date(telemetry.data.timestamp).toLocaleTimeString() : ''}</Badge>
            </div>
            <div className="card-body tight table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Model</th>
                    <th>Upstream</th>
                    <th className="num">Requests</th>
                    <th className="num">Errors</th>
                    <th className="num">p50</th>
                    <th className="num">p90</th>
                    <th className="num">p99</th>
                  </tr>
                </thead>
                <tbody>
                  {models.map((row) => (
                    <tr key={row.model}>
                      <td>{row.model}</td>
                      <td className="dim">{row.upstream}</td>
                      <td className="num">{row.requests.toLocaleString()}</td>
                      <td className="num">
                        <span style={{ color: row.errors > row.requests * 0.05 ? 'var(--warn)' : 'var(--ok)', fontWeight: 600 }}>
                          {row.errors}
                        </span>
                      </td>
                      <td className="num">{Math.round(row.p50_ms)}ms</td>
                      <td className="num">{Math.round(row.p90_ms)}ms</td>
                      <td className="num">{Math.round(row.p99_ms)}ms</td>
                    </tr>
                  ))}
                  {models.length === 0 ? (
                    <tr>
                      <td colSpan={7} className="faint">No model telemetry yet.</td>
                    </tr>
                  ) : null}
                </tbody>
              </table>
            </div>
          </div>

          <div className="card">
            <div className="card-header">
              <h2>Global admission</h2>
            </div>
            <div className="card-body">
              <div className="kv-list">
                <div>
                  <span className="k">In-flight / capacity</span>
                  <span className="v">
                    {telemetry.data
                      ? `${telemetry.data.global_admission.inflight} / ${telemetry.data.global_admission.capacity}`
                      : '—'}
                  </span>
                </div>
                <div>
                  <span className="k">Queue depth</span>
                  <span className="v">{telemetry.data?.global_admission.queue_depth ?? '—'}</span>
                </div>
                <div>
                  <span className="k">Circuit trips</span>
                  <span className="v">{s?.circuit_trips ?? '—'}</span>
                </div>
                <div>
                  <span className="k">Estimated cost</span>
                  <span className="v">{s ? `$${s.estimated_cost_usd?.toFixed(2) ?? '—'}` : '—'}</span>
                </div>
              </div>
            </div>
          </div>
        </div>
      </QueryGate>
    </div>
  );
}

function fmtTokens(n: number | undefined): string {
  if (!n) return '0';
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M';
  if (n >= 1_000) return (n / 1_000).toFixed(0) + 'K';
  return String(n);
}
