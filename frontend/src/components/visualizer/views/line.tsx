import { Badge } from '@/components/ui/Badge';
import type { VisualizerView } from './index';

const W = 720;
const H = 170;
const PAD = 24;

/** Line view: the request drawn as a latency line with a node per stage. */
export const lineView: VisualizerView = {
  id: 'line',
  label: 'Line',
  hint: 'Latency along the request pipeline.',
  render: ({ trace }) => {
    if (!trace) return <p className="muted">Waiting for a request…</p>;
    const end = trace.ended_at ?? Date.now();
    const span = Math.max(end - trace.started_at, 1);
    const x = (at: number) => PAD + ((at - trace.started_at) / span) * (W - PAD * 2);
    const baseline = H - 58;
    const points = trace.stages.map((s) => ({ ...s, cx: x(s.at) }));

    return (
      <div className="visualizer-view">
        <svg viewBox={`0 0 ${W} ${H}`} className="visualizer-svg" role="img" aria-label="Request latency line">
          <line x1={PAD} y1={baseline} x2={W - PAD} y2={baseline} className="viz-axis" />
          <polyline
            className="viz-line"
            points={points.map((p, i) => `${p.cx},${baseline - 18 - i * 12}`).join(' ')}
          />
          {points.map((p, i) => (
            <g key={`${p.name}-${i}`}>
              <circle cx={p.cx} cy={baseline - 18 - i * 12} r={4} className="viz-node" />
              <text x={p.cx} y={baseline - 26 - i * 12} className="viz-label">
                {p.name}
              </text>
              {p.delta ? (
                <text x={p.cx} y={baseline + 30} className="viz-sub">
                  +{p.delta}ms
                </text>
              ) : null}
            </g>
          ))}
        </svg>
        <div className="visualizer-legend">
          <Badge tone={trace.error ? 'danger' : 'ok'}>{trace.error ? 'error' : trace.state}</Badge>
          <span>TTFB {trace.ttfb_ms ?? 0}ms</span>
          <span>Total {trace.duration_ms ?? 0}ms</span>
          <span>{trace.deltas} deltas</span>
          <span>{trace.bytes}B content</span>
        </div>
      </div>
    );
  },
};
