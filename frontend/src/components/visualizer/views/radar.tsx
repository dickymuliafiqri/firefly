import type { VisualizerView } from './index';

const SIZE = 300;
const C = SIZE / 2;
const MAX_R = C - 26;

/**
 * Radar view: each credential gets a spoke; every recent request is plotted by
 * time-to-first-byte (radius) and stream volume (dot size), so a slow key or an
 * upstream sagging under load shows up as a heavy arc.
 */
export const radarView: VisualizerView = {
  id: 'radar',
  label: 'Radar',
  hint: 'Keys and their first-byte latency across recent requests.',
  render: ({ traces }) => {
    const keyed = traces.filter((t) => t.key_ref);
    if (!keyed.length) return <p className="muted">Waiting for a request…</p>;
    const keys = [...new Set(keyed.map((t) => t.key_ref as string))];
    const maxTtfb = Math.max(...keyed.map((t) => t.ttfb_ms ?? 0), 1);
    const angle = (key: string) => (keys.indexOf(key) / Math.max(keys.length, 1)) * Math.PI * 2 - Math.PI / 2;
    const recent = keyed.slice(-40);
    const sweep = angle(recent[recent.length - 1].key_ref as string);

    return (
      <div className="visualizer-view">
        <svg viewBox={`0 0 ${SIZE} ${SIZE}`} className="visualizer-svg" role="img" aria-label="Latency radar">
          <circle cx={C} cy={C} r={MAX_R} className="viz-axis" />
          <circle cx={C} cy={C} r={MAX_R * 0.66} className="viz-axis" />
          <circle cx={C} cy={C} r={MAX_R * 0.33} className="viz-axis" />
          {keys.map((key) => (
            <line
              key={key}
              x1={C}
              y1={C}
              x2={C + Math.cos(angle(key)) * MAX_R}
              y2={C + Math.sin(angle(key)) * MAX_R}
              className="viz-axis"
            />
          ))}
          <line x1={C} y1={C} x2={C + Math.cos(sweep) * MAX_R} y2={C + Math.sin(sweep) * MAX_R} className="viz-sweep" />
          {recent.map((t, i) => {
            const a = angle(t.key_ref as string);
            const r = 12 + ((t.ttfb_ms ?? 0) / maxTtfb) * (MAX_R - 12);
            return (
              <circle
                key={`${t.id}-${i}`}
                cx={C + Math.cos(a) * r}
                cy={C + Math.sin(a) * r}
                r={Math.min(3 + t.deltas / 40, 9)}
                className={t.error ? 'viz-blip error' : 'viz-blip'}
              />
            );
          })}
          {keys.map((key) => (
            <text
              key={`label-${key}`}
              x={C + Math.cos(angle(key)) * (MAX_R + 14)}
              y={C + Math.sin(angle(key)) * (MAX_R + 14)}
              className="viz-sub"
              textAnchor="middle"
            >
              {key}
            </text>
          ))}
        </svg>
        <div className="visualizer-legend">
          <span>{keys.length} keys</span>
          <span>peak TTFB {maxTtfb}ms</span>
          <span>{recent.length} recent requests</span>
        </div>
      </div>
    );
  },
};
