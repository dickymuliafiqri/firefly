import { useEffect, useRef, useState } from 'react';
import { Badge } from '@/components/ui/Badge';
import type { ResourceMonitorDTO } from '@/services/schema';

/**
 * ResourceMonitor — htop-style host & process snapshot for the Overview page.
 * Data comes from `resource_monitor` on /api/telemetry (admin only), which the
 * dashboard already polls every 2s, so the monitor adds no extra connection.
 * Sparkline history is accumulated client-side from consecutive polls.
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

/** Tiny dependency-free sparkline. Baseline at the bottom, flat when idle. */
function Sparkline({ points, max }: { points: number[]; max: number }) {
  const w = 100;
  const h = 28;
  if (points.length < 2) {
    return <svg className="resmon-spark" viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" />;
  }
  const step = w / (HISTORY - 1);
  const startIdx = HISTORY - points.length;
  const path = points
    .map((p, i) => {
      const x = (startIdx + i) * step;
      const y = h - (Math.max(0, Math.min(max, p)) / max) * (h - 2) - 1;
      return `${i === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(' ');
  return (
    <svg className="resmon-spark" viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none">
      <path d={`${path} L${w},${h} L0,${h} Z`} className="area" />
      <path d={path} className="line" />
    </svg>
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
  const netMax = Math.max(1, ...history.rx, ...history.tx);
  const cpuMax = 100;

  return (
    <div className="card">
      <div className="card-header">
        <h2>Resource monitor</h2>
        <Badge tone={stats.goroutines > 500 ? 'warn' : 'ok'}>
          {stats.goroutines} GOROUTINES
        </Badge>
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
                <span className="lbl mono">{pct >= 10 ? Math.round(pct) : ''}</span>
              </div>
            ))}
          </div>
          <Sparkline points={history.cpu} max={cpuMax} />
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
            <div className="resmon-rowhead sub">
              <span className="k">Swap</span>
              <span className="v mono">
                {fmtBytes(stats.swap_used_bytes)} / {fmtBytes(stats.swap_total_bytes)}
              </span>
            </div>
          ) : null}
          {stats.swap_total_bytes > 0 ? (
            <div className="resmon-bar">
              <i className={loadTone(swapPct)} style={{ width: `${Math.min(100, swapPct)}%` }} />
            </div>
          ) : null}
        </section>

        {/* Network */}
        <section className="resmon-section">
          <div className="resmon-rowhead">
            <span className="k">Network</span>
            <span className="v mono">
              <span className="net-arrow down">↓</span> {fmtRate(stats.net_rx_bps)}
              <span className="net-arrow up">↑</span> {fmtRate(stats.net_tx_bps)}
            </span>
          </div>
          <Sparkline points={history.rx} max={netMax} />
          <Sparkline points={history.tx} max={netMax} />
          <div className="resmon-rowhead sub">
            <span className="k">Totals</span>
            <span className="v mono">
              ↓ {fmtBytes(stats.net_rx_total_bytes)} · ↑ {fmtBytes(stats.net_tx_total_bytes)}
            </span>
          </div>
        </section>

        {/* Process */}
        <section className="resmon-section">
          <div className="resmon-rowhead">
            <span className="k">Firefly process</span>
            <span className="v mono">{stats.proc_cpu_percent.toFixed(1)}% CPU</span>
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
