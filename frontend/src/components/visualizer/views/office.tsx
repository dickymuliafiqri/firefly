import { Badge } from '@/components/ui/Badge';
import type { VisualizerView } from './index';

/**
 * Office view: one desk per upstream. A desk lights up while it is serving the
 * selected request, and shows the live activity of the stream feeding it.
 */
export const officeView: VisualizerView = {
  id: 'office',
  label: 'Office',
  hint: 'Which upstream is at work, and what are the others doing.',
  render: ({ trace, traces }) => {
    if (!trace) return <p className="muted">Waiting for a request…</p>;
    const desks = new Map<string, { busy: number; models: Set<string> }>();
    for (const t of traces) {
      if (!t.upstream) continue;
      const desk = desks.get(t.upstream) ?? { busy: 0, models: new Set<string>() };
      if (!t.ended_at) desk.busy += 1;
      if (t.model) desk.models.add(t.model);
      desks.set(t.upstream, desk);
    }
    if (!desks.has(trace.upstream ?? '')) {
      desks.set(trace.upstream ?? 'unknown', { busy: 0, models: new Set([trace.model ?? '—']) });
    }
    const rows = [...desks.entries()].sort((a, b) => b[1].busy - a[1].busy);

    return (
      <div className="visualizer-view">
        <div className="viz-office">
          {rows.map(([name, desk]) => {
            const active = name === trace.upstream;
            return (
              <div key={name} className={`viz-desk${active ? ' active' : ''}`}>
                <header>
                  <strong>{name}</strong>
                  <Badge tone={active ? 'info' : desk.busy > 0 ? 'warn' : 'neutral'}>
                    {active ? trace.activity.kind : desk.busy > 0 ? 'busy' : 'idle'}
                  </Badge>
                </header>
                <p className="muted">{[...desk.models].slice(0, 2).join(', ') || '—'}</p>
                <footer>
                  <span>{active ? `${trace.deltas} deltas` : `${desk.busy} in flight`}</span>
                  <span>{active ? `${Math.round(trace.bytes / 1024)} KB` : '—'}</span>
                </footer>
              </div>
            );
          })}
        </div>
      </div>
    );
  },
};
