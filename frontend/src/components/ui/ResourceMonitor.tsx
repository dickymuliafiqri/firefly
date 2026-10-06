import { useEffect, useRef, useState } from 'react';
import { Badge } from '@/components/ui/Badge';
import type { ResourceMonitorDTO } from '@/services/schema';

/**
 * ResourceMonitor — htop-style host & process snapshot for the Overview page.
 * Data comes from `resource_monitor` on /api/telemetry (admin only), which the
 * dashboard already polls every 2s, so the monitor adds no extra connection.
 * Chart history is accumulated client-side from consecutive polls.
 */

const HISTORY = 60; // ~2 minutes of samples at the 2s polling cadence

interface History {
  cpu: number[];
  rx: number[];
  tx: number[];
}

function pushCapped(arr: number[], v: number): number[] {
  const next = arr.length >= HISTORY ? arr.slice(arr.length - HISTORY + 1) : arr.slice();
  next.push(v);
  return next;
}

function fmtBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

function fmtRate(n: number): string {
  return `${fmtBytes(n)}/s`;
}

function loadTone(pct: number): 'ok' | 'warn' | 'danger' {
  if (pct >= 85) return 'danger';
  if (pct >= 60) return 'warn';
  return 'ok';
}

type ChartColor = 'cpu' | 'rx' | 'tx';
interface ChartSeries {
  points: number[];
  color: ChartColor;
}

const CHART_W = 100;
const CHART_H = 28;

function colorVar(c: ChartColor): string {
  if (c === 'rx') return 'var(--info)';
  if (c === 'tx') return 'var(--biolum)';
  return 'var(--ok)';
}

/**
 * Left-pad a series to HISTORY samples so the newest value always sits at the
 * right edge and the chart spans the full width. Empty or single-sample series
 * become a flat line (zeros sit on the baseline) — the graph never disappears.
 */
function toWindow(points: number[]): number[] {
  if (points.length >= HISTORY) return points.slice(points.length - HISTORY);
  if (points.length === 0) return new Array<number>(HISTORY).fill(0);
  const edge = points[0];
  return [...new Array<number>(HISTORY - points.length).fill(edge), ...points];
}

/**
 * Minimalist dependency-free chart: a soft gradient area + a crisp rounded line
 * per series over a faint baseline grid. Always renders (flat when idle).
 */
function SparkChart({
  series,
  max,
  className,
}: {
  series: ChartSeries[];
  max: number;
  className?: string;
}) {
  const top = 1.5;
  const usable = CHART_H - top - 1;
  const step = CHART_W / (HISTORY - 1);
  const yFor = (v: number) => top + (1 - Math.min(max, Math.max(0, v)) / max) * usable;

  const drawn = series.map(({ points, color }) => {
    const seq = toWindow(points);
    const line = seq
      .map((p, i) => `${i === 0 ? 'M' : 'L'}${(i * step).toFixed(2)},${yFor(p).toFixed(2)}`)
      .join(' ');
    return { color, line, area: `${line} L${CHART_W},${CHART_H} L0,${CHART_H} Z` };
  });

  return (
    <svg
      className={`resmon-chart${className ? ` ${className}` : ''}`}
      viewBox={`0 0 ${CHART_W} ${CHART_H}`}
      preserveAspectRatio="none"
      role="img"
    >
      <defs>
        {drawn.map((r) => (
          <linearGradient key={r.color} id={`resmon-fill-${r.color}`} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor={colorVar(r.color)} stopOpacity="0.3" />
            <stop offset="100%" stopColor={colorVar(r.color)} stopOpacity="0" />
          </linearGradient>
        ))}
      </defs>
      <line className="grid top" x1="0" y1={top} x2={CHART_W} y2={top} />
      <line className="grid base" x1="0" y1={top + usable} x2={CHART_W} y2={top + usable} />
      {drawn.map((r) => (
        <path key={`a-${r.color}`} d={r.area} fill={`url(#resmon-fill-${r.color})`} stroke="none" />
      ))}
      {drawn.map((r) => (
        <path key={`l-${r.color}`} className={`line ${r.color}`} d={r.line} vectorEffect="non-scaling-stroke" />
      ))}
    </svg>
  );
}

function LegendChip({ kind }: { kind: 'rx' | 'tx' }) {
  return (
    <span className={`resmon-legend ${kind}`}>
      <i className="dot" />
      {kind === 'rx' ? 'RX ↓' : 'TX ↑'}
    </span>
  );
}

export function ResourceMonitor({ stats }: { stats?: ResourceMonitorDTO | null }) {
  const [history, setHistory] = useState<History>({ cpu: [], rx: [], tx: [] });
  const lastTs = useRef<number>(0);

  useEffect(() => {
    if (!stats || stats.timestamp === lastTs.current) return;
    lastTs.current = stats.timestamp;
    setHistory((prev) => ({
      cpu: pushCapped(prev.cpu, stats.cpu_percent),
      rx: pushCapped(prev.rx, stats.net_rx_bps),
      tx: pushCapped(prev.tx, stats.net_tx_bps),
    }));
  }, [stats]);

  if (!stats) {
    return (
      <div className="card">
        <div className="card-header">
          <h2>Resource monitor</h2>
          <Badge tone="neutral">UNAVAILABLE</Badge>
        </div>
        <div className="card-body">
          <p className="faint" style={{ margin: 0, color: 'var(--faint)' }}>
            Host resource stats require an authorized dashboard session. Sign in
            to view live CPU, memory, network, and goroutine metrics.
          </p>
        </div>
      </div>
    );
  }

  const memPct = Math.min(100, Math.max(0, stats.mem_percent));
  const swapPct = stats.swap_total_bytes > 0 ? (stats.swap_used_bytes / stats.swap_total_bytes) * 100 : 0;
  const procPct = Math.min(100, Math.max(0, stats.proc_cpu_percent));
  const netMax = Math.max(1, ...history.rx, ...history.tx);
  const cpuMax = 100;

  return (
    <div className="card">
      <div className="card-header">
        <h2>Resource monitor</h2>
      </div>
      <div className="card-body resmon">
        {/* CPU */}
        <section className="resmon-section">
          <div className="resmon-rowhead">
            <span className="k">CPU</span>
            <span className="v mono">{stats.cpu_percent.toFixed(1)}%</span>
          </div>
          <div className="resmon-cores">
            {stats.cpu_per_core.map((pct, i) => (
              <div className="resmon-core" key={i} title={`cpu${i}: ${pct.toFixed(1)}%`}>
                <div className="resmon-bar">
                  <i className={loadTone(pct)} style={{ width: `${Math.min(100, Math.max(0, pct))}%` }} />
                </div>
                <span className="lbl mono">{Math.round(pct)}</span>
              </div>
            ))}
          </div>
          <SparkChart series={[{ points: history.cpu, color: 'cpu' }]} max={cpuMax} />
        </section>

        {/* Memory */}
        <section className="resmon-section">
          <div className="resmon-rowhead">
            <span className="k">Memory</span>
            <span className="v mono">
              {fmtBytes(stats.mem_used_bytes)} / {fmtBytes(stats.mem_total_bytes)} · {memPct.toFixed(1)}%
            </span>
          </div>
          <div className="resmon-bar tall">
            <i className={loadTone(memPct)} style={{ width: `${memPct}%` }} />
          </div>
          {stats.swap_total_bytes > 0 ? (
            <>
              <div className="resmon-rowhead sub">
                <span className="k">Swap</span>
                <span className="v mono">
                  {fmtBytes(stats.swap_used_bytes)} / {fmtBytes(stats.swap_total_bytes)}
                </span>
              </div>
              <div className="resmon-bar">
                <i className={loadTone(swapPct)} style={{ width: `${Math.min(100, swapPct)}%` }} />
              </div>
            </>
          ) : null}
        </section>

        {/* Network */}
        <section className="resmon-section">
          <div className="resmon-rowhead">
            <span className="k">Network</span>
            <span className="v mono">
              {fmtRate(stats.net_rx_bps)} · {fmtRate(stats.net_tx_bps)}
            </span>
          </div>
          <div className="resmon-legend-row">
            <LegendChip kind="rx" />
            <LegendChip kind="tx" />
            <span className="v mono totals">
              ↓ {fmtBytes(stats.net_rx_total_bytes)} · ↑ {fmtBytes(stats.net_tx_total_bytes)}
            </span>
          </div>
          <SparkChart
            className="dual"
            series={[
              { points: history.rx, color: 'rx' },
              { points: history.tx, color: 'tx' },
            ]}
            max={netMax}
          />
        </section>

        {/* Process */}
        <section className="resmon-section">
          <div className="resmon-rowhead">
            <span className="k">Firefly process</span>
            <span className="v mono">{stats.proc_cpu_percent.toFixed(1)}% CPU</span>
          </div>
          <div className="resmon-bar">
            <i className={loadTone(procPct)} style={{ width: `${procPct}%` }} />
          </div>
          <div className="resmon-kv">
            <div>
              <span className="k">Goroutines</span>
              <span className="v mono">{stats.goroutines.toLocaleString()}</span>
            </div>
            <div>
              <span className="k">Go heap</span>
              <span className="v mono">{fmtBytes(stats.go_heap_bytes)}</span>
            </div>
            <div>
              <span className="k">Go sys</span>
              <span className="v mono">{fmtBytes(stats.go_sys_bytes)}</span>
            </div>
            <div>
              <span className="k">RSS</span>
              <span className="v mono">{fmtBytes(stats.proc_rss_bytes)}</span>
            </div>
          </div>
        </section>
      </div>
    </div>
  );
}
