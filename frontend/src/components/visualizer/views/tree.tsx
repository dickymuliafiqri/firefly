import type { VisualizerView } from './index';

const ROW = 44;
const PAD = 30;

/** Tree view: model → surviving upstream → credential, with losing branches. */
export const treeView: VisualizerView = {
  id: 'tree',
  label: 'Tree',
  hint: 'Routing branches: what won and what was skipped.',
  render: ({ trace }) => {
    if (!trace) return <p className="muted">Waiting for a request…</p>;
    const candidates = trace.candidates ?? [];
    const rows = Math.max(candidates.length, 1) + 2;
    const height = rows * ROW + PAD;
    const chosen = candidates.findIndex((c) => c.state === 'chosen');
    const keyRow = chosen >= 0 ? chosen : 0;

    return (
      <div className="visualizer-view">
        <svg viewBox={`0 0 460 ${height}`} className="visualizer-svg" role="img" aria-label="Routing branch tree">
          {/* root: the public model the client asked for */}
          <circle cx={PAD + 6} cy={PAD + 6} r={6} className="viz-node" />
          <text x={PAD + 20} y={PAD + 10} className="viz-label">
            {trace.model ?? trace.path ?? 'request'}
          </text>
          <line x1={PAD + 6} y1={PAD + 12} x2={PAD + 6} y2={PAD + ROW * rows - 10} className="viz-axis" />

          {candidates.map((c, i) => {
            const y = PAD + ROW * (i + 1);
            return (
              <g key={`${c.upstream}-${i}`} className={c.state === 'chosen' ? 'viz-chosen' : 'viz-skipped'}>
                <path d={`M ${PAD + 6} ${y} H ${PAD + 40}`} className="viz-edge" />
                <rect x={PAD + 40} y={y - 12} width={190} height={24} rx={6} className="viz-box" />
                <text x={PAD + 50} y={y + 4} className="viz-label">
                  {c.upstream}
                </text>
                <text x={PAD + 238} y={y + 4} className="viz-sub">
                  {c.state}
                  {c.note ? ` · ${c.note}` : ''}
                </text>
              </g>
            );
          })}

          {/* leaf: the credential actually spent, plus what came back */}
          <g className="viz-chosen">
            <path d={`M ${PAD + 230} ${PAD + ROW * (keyRow + 1)} H ${PAD + 270}`} className="viz-edge" />
            <rect x={PAD + 270} y={PAD + ROW * (keyRow + 1) - 12} width={140} height={24} rx={6} className="viz-box" />
            <text x={PAD + 280} y={PAD + ROW * (keyRow + 1) + 4} className="viz-label">
              {trace.key_ref || 'credential'}
            </text>
          </g>
        </svg>
        <div className="visualizer-legend">
          <span>{trace.upstream ?? '—'}</span>
          <span>{trace.protocol ?? '—'}</span>
          <span>
            {trace.tokens_in ?? 0} in / {trace.tokens_out ?? 0} out
          </span>
        </div>
      </div>
    );
  },
};
