import { Badge } from '@/components/ui/Badge';
import { KpiCard, KpiGrid } from '@/components/ui/PageHeader';
import { QueryGate } from '@/components/ui/QueryGate';
import { ResourceMonitor } from '@/components/ui/ResourceMonitor';
import { SceneryStrip } from '@/components/shell/SceneryStrip';
import { useTelemetryQuery } from '@/services/api';

/**
 * Dispatcher health at a glance. An operator needs to see that events are
 * being dropped before a missed alert becomes an incident, so the drop counter
 * is surfaced with a warning tone rather than hidden in telemetry.
 */
function NotificationsPanel({ stats }: { stats?: { queued: number; dropped: number; delivered: number; failed: number } | null }) {
  if (!stats) return null;
  const quiet = stats.delivered === 0 && stats.dropped === 0 && stats.failed === 0 && stats.queued === 0;
  return (
    <div className="card">
      <div className="card-header">
        <h2>Notifications</h2>
        <Badge tone={stats.dropped > 0 ? 'warn' : stats.failed > 0 ? 'warn' : quiet ? 'neutral' : 'ok'}>
          {stats.dropped > 0 ? 'DROPPING' : stats.failed > 0 ? 'FAILING' : quiet ? 'IDLE' : 'HEALTHY'}
        </Badge>
      </div>
      <div className="card-body tight">
        <KpiGrid>
          <KpiCard label="Delivered" value={stats.delivered.toLocaleString()} />
          <KpiCard label="Queued" value={stats.queued.toLocaleString()} />
          <KpiCard
            label="Dropped"
            value={stats.dropped.toLocaleString()}
            tone={stats.dropped > 0 ? 'warn' : 'plain'}
          />
          <KpiCard
            label="Failed"
            value={stats.failed.toLocaleString()}
            tone={stats.failed > 0 ? 'warn' : 'plain'}
          />
        </KpiGrid>
        {stats.dropped > 0 ? (
          <p className="hint" style={{ marginTop: 10, fontSize: 12, color: 'var(--faint)' }}>
            Events are being dropped because the queue is full — a channel is too slow or
            unreachable. Raise the queue size or fix the channel in Settings → Notifications.
          </p>
        ) : null}
      </div>
    </div>
  );
}

export function OverviewPage() {
  const telemetry = useTelemetryQuery();
  const s = telemetry.data?.summary;
  const logs = telemetry.data?.recent_logs ?? [];

  return (
    <div className="has-decor">
      <div className="page-col overview-stack">
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
          <ResourceMonitor stats={telemetry.data?.resource_monitor} />
          <NotificationsPanel stats={telemetry.data?.notifications} />

          <div className="card">
            <div className="card-header">
              <h2>Live request history</h2>
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
                      <td colSpan={7} className="faint">No requests recorded in this session.</td>
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
