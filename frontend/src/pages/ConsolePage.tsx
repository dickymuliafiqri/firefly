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
            `${l.method} ${l.path} ${l.model ?? ''} ${l.resolvedModel ?? ''} ${l.upstream ?? ''} ${l.tenant ?? ''}`
              .toLowerCase()
              .includes(search.toLowerCase())),
      ),
    [logs, bucket, search],
  );

  const errCount = logs.filter((l) => l.status >= 400).length;

  // A combo failover leaves more than one console row for one client request:
  // every discarded member attempt is recorded, then the member that actually
  // answered. The rows share a base id (the retry appends "#<attempt>"), so the
  // highest attempt of a base is the one the client received and every earlier
  // one is dimmed as a discarded step rather than counted as a separate request.
  const deliveredAttempt = useMemo(() => {
    const byBase = new Map<string, number>();
    for (const l of logs) {
      const base = l.id.split('#')[0];
      const attempt = l.attempt ?? 1;
      if ((byBase.get(base) ?? 0) < attempt) byBase.set(base, attempt);
    }
    return byBase;
  }, [logs]);

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
                  <th>Attempt</th>
                  <th>Tenant</th>
                  <th>Status</th>
                  <th className="num">Duration</th>
                  <th className="num">Tokens</th>
                </tr>
              </thead>
              <tbody>
                {filtered.map((l) => {
                  const attempt = l.attempt ?? 1;
                  const base = l.id.split('#')[0];
                  const delivered = attempt === (deliveredAttempt.get(base) ?? 1);
                  return (
                    <tr key={l.id} style={delivered ? undefined : { opacity: 0.55 }}>
                      <td className="faint">{new Date(l.timestamp).toLocaleTimeString()}</td>
                      <td className="dim">{l.method}</td>
                      <td className="mono" style={{ fontSize: 12 }}>{l.path}</td>
                      <td>
                        {l.resolvedModel && l.resolvedModel !== l.model ? (
                          <>
                            <span>{l.model}</span>
                            <span className="dim" style={{ marginLeft: 6, fontSize: '0.85em' }}>
                              → {l.resolvedModel}
                            </span>
                          </>
                        ) : (
                          l.model ?? '—'
                        )}
                      </td>
                      <td className="dim">{l.upstream ?? '—'}</td>
                      <td className="dim">
                        {attempt > 1 ? (
                          <Badge
                            tone={delivered ? 'ok' : 'warn'}
                            title={
                              delivered
                                ? l.fallbackFrom
                                  ? `served after ${l.fallbackFrom} failed`
                                  : 'served on the next combo member'
                                : 'discarded combo member attempt'
                            }
                          >
                            {delivered
                              ? l.fallbackFrom
                                ? `after ${l.fallbackFrom}`
                                : `#${attempt}`
                              : `#${attempt} failed`}
                          </Badge>
                        ) : (
                          '—'
                        )}
                      </td>
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
                  );
                })}
                {filtered.length === 0 ? (
                  <tr>
                    <td colSpan={10} className="faint">No log entries match the filter.</td>
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
