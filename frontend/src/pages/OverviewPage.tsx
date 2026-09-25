import { Badge } from '@/components/ui/Badge';
import { KpiCard, KpiGrid } from '@/components/ui/PageHeader';
import { QueryGate } from '@/components/ui/QueryGate';
import { SceneryStrip } from '@/components/shell/SceneryStrip';
import { useTelemetryQuery } from '@/services/api';

export function OverviewPage() {
  const telemetry = useTelemetryQuery();
  const s = telemetry.data?.summary;
  const logs = telemetry.data?.recent_logs ?? [];

  return (
    <div className="has-decor">
      <div className="page-col">
        <div className="overview-hero">
          <p className="eyebrow">AI gateway &middot; pure Go &middot; single binary</p>
          <h1>A thousand streams of light, one gateway.</h1>
        </div>

        <KpiGrid>
          <KpiCard label="Total requests" value={s ? s.total_requests.toLocaleString() : '—'} />
          <KpiCard label="Active streams" value={s ? String(s.active_streams) : '—'} />
          <KpiCard label="p99 latency" value={s ? String(Math.round(s.p99_latency_ms)) : '—'} unit="ms" />
          <KpiCard
            label="Error rate"
            value={s ? s.error_rate_pct.toFixed(2) : '—'}
            unit="%"
            tone={s && s.error_rate_pct > 5 ? 'danger' : s && s.error_rate_pct > 1 ? 'warn' : 'ok'}
          />
        </KpiGrid>

        <QueryGate isLoading={telemetry.isLoading} error={telemetry.error}>
          <div className="card">
            <div className="card-header">
              <h2>Live request history</h2>
              <Badge tone={s && s.active_streams > 0 ? 'ok' : 'neutral'}>
                {s && s.active_streams > 0 ? 'STREAMING' : 'IDLE'}
              </Badge>
            </div>
            <div className="card-body tight table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Time</th>
                    <th>Method &amp; path</th>
                    <th>Model</th>
                    <th>Upstream</th>
                    <th>Tenant</th>
                    <th>Status</th>
                    <th className="num">Duration</th>
                  </tr>
                </thead>
                <tbody>
                  {logs.length === 0 ? (
                    <tr>
                      <td colSpan={7} className="faint">Belum ada request pada sesi ini.</td>
                    </tr>
                  ) : (
                    logs.map((row) => (
                      <tr key={row.id}>
                        <td className="faint">{new Date(row.timestamp).toLocaleTimeString()}</td>
                        <td>
                          <span className="dim" style={{ marginRight: 8 }}>{row.method}</span>
                          {row.path}
                        </td>
                        <td>{row.model}</td>
                        <td className="dim">{row.upstream}</td>
                        <td className="dim">{row.tenant}</td>
                        <td>
                          <Badge tone={row.status < 400 ? 'ok' : 'danger'}>{row.status}</Badge>
                        </td>
                        <td className="num">{Math.round(row.durationMs)}ms</td>
                      </tr>
                    ))
                  )}
                </tbody>
              </table>
            </div>
          </div>
        </QueryGate>
      </div>

      <SceneryStrip />
    </div>
  );
}
