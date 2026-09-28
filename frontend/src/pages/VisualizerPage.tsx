import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTelemetryQuery } from '@/services/api';
import { Badge } from '@/components/ui/Badge';
import { Button } from '@/components/ui/Button';
import { Segmented } from '@/components/ui/Controls';
import { KpiCard, KpiGrid, PageHeader } from '@/components/ui/PageHeader';
import { VISUALIZER_VIEWS, findView } from '@/components/visualizer/views/index';
import {
  clearVisualizer,
  fetchVisualizerSnapshot,
  openVisualizerStream,
  type RoutingTrace,
  type VisualizerStats,
} from '@/services/visualizer';

const MAX_TRACES = 60;

function tone(state: string): 'ok' | 'warn' | 'danger' | 'neutral' {
  if (state === 'error') return 'danger';
  if (state === 'stream' || state === 'request') return 'warn';
  return 'ok';
}

function ms(value?: number): string {
  if (!value) return '—';
  return value >= 1000 ? `${(value / 1000).toFixed(2)}s` : `${value}ms`;
}

/**
 * Visualizer page — admin-only. Opening it attaches the first subscriber, which
 * is what turns server-side trace capture on; leaving stops it again.
 */
export function VisualizerPage() {
  const [traces, setTraces] = useState<RoutingTrace[]>([]);
  const [stats, setStats] = useState<VisualizerStats | null>(null);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [follow, setFollow] = useState(true);
  const [search, setSearch] = useState('');
  const [viewId, setViewId] = useState('line');
  const [error, setError] = useState<string | null>(null);
  const followRef = useRef(follow);
  const selectedRef = useRef(selectedId);
  selectedRef.current = selectedId;
  followRef.current = follow;

  const upsert = useCallback((trace: RoutingTrace) => {
    setTraces((prev) => {
      const idx = prev.findIndex((t) => t.id === trace.id);
      const next = idx >= 0 ? [...prev.slice(0, idx), trace, ...prev.slice(idx + 1)] : [...prev, trace];
      return next.slice(-MAX_TRACES);
    });
    if (followRef.current) setSelectedId(trace.id);
  }, []);

  useEffect(() => {
    let live = true;
    fetchVisualizerSnapshot()
      .then((snap) => {
        if (!live) return;
        const ordered = [...snap.traces].reverse();
        setTraces(ordered.slice(-MAX_TRACES));
        setStats(snap.stats);
        setSelectedId((cur) => cur ?? ordered[ordered.length - 1]?.id ?? null);
      })
      .catch((err: Error) => {
        if (live) setError(err.message);
      });

    const close = openVisualizerStream(
      (frame) => {
        if (!live) return;
        if (frame.type === 'stats') {
          setStats(frame.stats);
          return;
        }
        upsert(frame.trace);
      },
      (err: unknown) => {
        if (!live) return;
        setError(err instanceof Error ? err.message : String(err));
      },
    );
    return () => {
      live = false;
      close();
    };
  }, [upsert]);

  const clear = async () => {
    try {
      const res = await clearVisualizer();
      setStats(res.stats);
      setTraces([]);
      setSelectedId(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  const filtered = useMemo(() => {
    if (!search) return traces;
    const needle = search.toLowerCase();
    return traces.filter((t) =>
      `${t.model ?? ''} ${t.upstream ?? ''} ${t.tenant ?? ''} ${t.path ?? ''} ${t.key_ref ?? ''}`
        .toLowerCase()
        .includes(needle),
    );
  }, [traces, search]);

  const selected = useMemo(() => {
    const found = traces.find((t) => t.id === selectedId);
    return found ?? filtered[filtered.length - 1] ?? traces[traces.length - 1];
  }, [traces, filtered, selectedId]);

  const { data: telemetry } = useTelemetryQuery();
  // Every upstream in the live catalog, so the canvas always shows one node
  // per active upstream — not just the ones the selected trace touched.
  const catalogUpstreams = useMemo(
    () => telemetry?.upstreams.map((u) => u.name) ?? [],
    [telemetry],
  );

  const view = findView(viewId);

  return (
    <div className="page-col">
      <PageHeader
        title="Visualizer"
        description="Live model-routing traces: which upstream and credential won, and what the stream did. In-memory only, captured while this page is open."
        actions={
          <Button variant="ghost" onClick={clear}>
            Clear
          </Button>
        }
      />

      {error ? <p className="viz-error">{error}</p> : null}

      <KpiGrid>
        <KpiCard label="Captured" value={(stats?.captured ?? 0).toString()} unit="traces" />
        <KpiCard
          label="In flight"
          value={(stats?.inflight ?? 0).toString()}
          unit="requests"
          tone={stats?.inflight ? 'ok' : 'plain'}
        />
        <KpiCard label="Subscribers" value={(stats?.subscribers ?? 0).toString()} unit="streams" />
        <KpiCard
          label="Dropped frames"
          value={(stats?.dropped ?? 0).toString()}
          tone={stats?.dropped ? 'warn' : 'plain'}
        />
      </KpiGrid>

      <div className="filter-bar">
        <span className="spacer" />
        <label className="viz-toggle">
          <input
            type="checkbox"
            checked={follow}
            onChange={(e) => {
              const on = e.target.checked;
              setFollow(on);
              if (on) setSelectedId(traces[traces.length - 1]?.id ?? null);
            }}
          />
          Follow newest
        </label>
        <input
          type="search"
          placeholder="Search model / upstream / key…"
          aria-label="Search traces"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
      </div>

      <div className="visualizer-grid">
        <div className="card">
          <div className="card-header">
            <h2>{view.label} view</h2>
            <span className="viz-sub">{view.hint}</span>
            <span className="spacer" />
            <Segmented
              items={VISUALIZER_VIEWS.map((v) => ({ id: v.id, label: v.label }))}
              value={view.id}
              onChange={setViewId}
              ariaLabel="Visualizer view"
            />
          </div>
          <div className="card-body">{view.render({ trace: selected, traces: filtered, catalogUpstreams })}</div>
        </div>

        <div className="card">
          <div className="card-header">
            <h2>Requests</h2>
          </div>
          <div className="card-body tight table-wrap viz-scroll">
            <table>
              <thead>
                <tr>
                  <th>Time</th>
                  <th>Model</th>
                  <th>Upstream</th>
                  <th>State</th>
                  <th className="num">TTFB</th>
                  <th className="num">Total</th>
                </tr>
              </thead>
              <tbody>
                {filtered.length === 0 ? (
                  <tr>
                    <td colSpan={6} className="viz-sub">
                      No traces yet — send a request through the gateway.
                    </td>
                  </tr>
                ) : (
                  [...filtered].reverse().map((t) => (
                    <tr
                      key={t.id}
                      className={t.id === selected?.id ? 'selected' : undefined}
                      onClick={() => {
                        setFollow(false);
                        setSelectedId(t.id);
                      }}
                    >
                      <td className="viz-sub">{new Date(t.started_at).toLocaleTimeString()}</td>
                      <td>{t.model ?? '—'}</td>
                      <td>{t.upstream ?? '—'}</td>
                      <td>
                        <Badge tone={tone(t.state)}>{t.error ? `HTTP ${t.status}` : t.state}</Badge>
                      </td>
                      <td className="num">{ms(t.ttfb_ms)}</td>
                      <td className="num">{ms(t.duration_ms)}</td>
                    </tr>
                  ))
                )}
              </tbody>
            </table>
          </div>
        </div>
      </div>

      <div className="visualizer-lower">
        <div className="card">
          <div className="card-header">
            <h2>Phase timeline</h2>
          </div>
          <div className="card-body tight table-wrap viz-scroll">
            <table>
              <thead>
                <tr>
                  <th>Phase</th>
                  <th className="num">Δ</th>
                  <th>Detail</th>
                  <th className="num">Activity</th>
                </tr>
              </thead>
              <tbody>
                {(selected?.stages ?? []).map((s, i) => (
                  <tr key={`${s.name}-${i}`}>
                    <td>{s.name}</td>
                    <td className="num">{s.delta ? `${s.delta}ms` : '—'}</td>
                    <td className="viz-sub">{s.detail ?? '—'}</td>
                    <td className="num">
                      {s.name === 'stream' && selected ? `${selected.deltas} deltas / ${selected.bytes}B` : '—'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>

        <div className="card">
          <div className="card-header">
            <h2>Raw frames</h2>
          </div>
          <div className="card-body tight table-wrap viz-scroll">
            <table>
              <thead>
                <tr>
                  <th className="num">+ms</th>
                  <th>Kind</th>
                  <th>Name</th>
                  <th>Detail</th>
                </tr>
              </thead>
              <tbody>
                {(selected?.events ?? []).map((e, i) => (
                  <tr key={i}>
                    <td className="num">{(e.at - (selected?.started_at ?? e.at)).toString()}</td>
                    <td>{e.kind}</td>
                    <td>{e.name ?? '—'}</td>
                    <td className="viz-sub">{e.detail ?? '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </div>
  );
}
