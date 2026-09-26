import { useMemo, useState } from 'react';
import { Badge } from '@/components/ui/Badge';
import { Segmented } from '@/components/ui/Controls';
import { PageHeader } from '@/components/ui/PageHeader';
import { QueryGate } from '@/components/ui/QueryGate';
import { useTelemetryQuery } from '@/services/api';
import type { LiveConnectionLog } from '@/services/schema';

type Bucket = 'all' | '2xx' | '4xx' | '5xx';

function inBucket(status: number, bucket: Bucket): boolean {
  if (bucket === 'all') return true;
  if (bucket === '2xx') return status >= 200 && status < 300;
  if (bucket === '4xx') return status >= 400 && status < 500;
  return status >= 500;
}

function statusTone(status: number): 'ok' | 'warn' | 'danger' {
  if (status < 300) return 'ok';
  if (status < 500) return 'warn';
  return 'danger';
}

export function ConsolePage() {
  const telemetry = useTelemetryQuery();
  const [bucket, setBucket] = useState<Bucket>('all');
  const [search, setSearch] = useState('');

  const logs = telemetry.data?.recent_logs ?? [];

  const filtered = useMemo(
    () =>
      logs.filter(
        (l: LiveConnectionLog) =>
          inBucket(l.status, bucket) &&
          (search === '' ||
            `${l.method} ${l.path} ${l.model ?? ''} ${l.upstream ?? ''} ${l.tenant ?? ''}`
              .toLowerCase()
              .includes(search.toLowerCase())),
      ),
    [logs, bucket, search],
  );

  const errCount = logs.filter((l) => l.status >= 400).length;

  return (
    <div className="page-col">
      <PageHeader
        title="Console"
        description="Live request log window from the gateway ring buffer (2s polling)."
        actions={
          <span style={{ color: errCount > 0 ? 'var(--warn)' : 'var(--ok)', fontWeight: 600, fontSize: 13 }}>
            {errCount} errors
          </span>
        }
      />

      <div className="filter-bar">
        <Segmented
          items={[
            { id: 'all', label: 'All' },
            { id: '2xx', label: '2xx' },
            { id: '4xx', label: '4xx' },
            { id: '5xx', label: '5xx' },
          ]}
          value={bucket}
          onChange={(id) => setBucket(id as Bucket)}
          ariaLabel="Filter status"
        />
        <span className="spacer" />
        <input
          type="search"
          placeholder="Search path / model / tenant…"
          aria-label="Search log"
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
                  <th>Time</th>
                  <th>Method</th>
                  <th>Path</th>
                  <th>Model</th>
                  <th>Upstream</th>
                  <th>Tenant</th>
                  <th>Status</th>
                  <th className="num">Duration</th>
                  <th className="num">Tokens</th>
                </tr>
              </thead>
              <tbody>
                {filtered.map((l) => (
                  <tr key={l.id}>
                    <td className="faint">{new Date(l.timestamp).toLocaleTimeString()}</td>
                    <td className="dim">{l.method}</td>
                    <td className="mono" style={{ fontSize: 12 }}>{l.path}</td>
                    <td>{l.model ?? '—'}</td>
                    <td className="dim">{l.upstream ?? '—'}</td>
                    <td className="dim">{l.tenant ?? '—'}</td>
                    <td>
                      <Badge tone={statusTone(l.status)} title={l.error || undefined}>{l.status}</Badge>
                    </td>
                    <td className="num">{Math.round(l.durationMs)}ms</td>
                    <td className="num">
                      {l.tokensIn !== undefined ? `${l.tokensIn}/${l.tokensOut ?? 0}` : '—'}
                      {l.stream ? <span className="sub">stream</span> : null}
                    </td>
                  </tr>
                ))}
                {filtered.length === 0 ? (
                  <tr>
                    <td colSpan={9} className="faint">No log entries match the filter.</td>
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
