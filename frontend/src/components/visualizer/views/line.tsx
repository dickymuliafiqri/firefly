import { useRef, useState, type PointerEvent as ReactPointerEvent, type ReactNode } from 'react';
import { Badge } from '@/components/ui/Badge';
import type { VisualizerView } from './index';

const ROW = 44;
const PAD = 28;
const W = 1180;

/** Left-to-right column anchors: Firefly → upstream → key → stream phases. */
const X_ROOT = 30;
const X_UPSTREAM = 320;
const X_KEY = 680;
const X_PHASE = 950;

const ROOT_W = 160;
const UPSTREAM_W = 230;
const KEY_W = 190;
const PHASE_W = 200;
const BOX_H = 26;
const ELBOW = 14;

const PHASE_LABELS: Record<string, string> = {
  reasoning: 'Thinking',
  answer: 'Writing',
  tool: 'Tool',
  usage: 'Usage',
  error: 'Error',
};

function phaseLabel(kind: string): string {
  return PHASE_LABELS[kind] ?? kind.charAt(0).toUpperCase() + kind.slice(1);
}

/**
 * Google-Maps-style pannable canvas: the SVG renders larger than its viewport
 * and pointer drags translate it. Double-click snaps back to the origin.
 */
function PanCanvas({ viewBox, label, children }: { viewBox: string; label: string; children: ReactNode }) {
  const [pan, setPan] = useState({ x: 0, y: 0 });
  const drag = useRef<{ px: number; py: number; ox: number; oy: number } | null>(null);

  const onPointerDown = (e: ReactPointerEvent<HTMLDivElement>) => {
    drag.current = { px: e.clientX, py: e.clientY, ox: pan.x, oy: pan.y };
    e.currentTarget.setPointerCapture(e.pointerId);
  };
  const onPointerMove = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (!drag.current) return;
    setPan({
      x: drag.current.ox + (e.clientX - drag.current.px),
      y: drag.current.oy + (e.clientY - drag.current.py),
    });
  };
  const endDrag = () => {
    drag.current = null;
  };

  return (
    <div
      className="viz-canvas"
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={endDrag}
      onPointerCancel={endDrag}
      onDoubleClick={() => setPan({ x: 0, y: 0 })}
    >
      <svg
        viewBox={viewBox}
        className="visualizer-svg"
        role="img"
        aria-label={label}
        style={{ transform: `translate(${pan.x}px, ${pan.y}px)` }}
      >
        {children}
      </svg>
    </div>
  );
}

/**
 * Line view: the monitored request drawn as one left-to-right flow.
 * Firefly fans out to the upstreams it considered, narrows to the credential
 * actually spent, then lands on the stream phases the response produced —
 * all of it dynamic, straight from the incoming trace data. The Firefly root
 * is always on the canvas, even before the first request arrives.
 */
export const lineView: VisualizerView = {
  id: 'line',
  label: 'Line',
  hint: 'Flow: Firefly → upstream → key → stream phases.',
  render: ({ trace }) => {
    const candidates = trace?.candidates ?? [];
    const upstreams = candidates.length
      ? candidates
      : trace?.upstream
        ? [{ upstream: trace.upstream, state: 'chosen' as const }]
        : [];
    const chosenIdx = Math.max(
      upstreams.findIndex((c) => c.state === 'chosen'),
      0,
    );
    const keyRef = upstreams[chosenIdx]?.key ?? trace?.key_ref ?? '';

    const phases = trace
      ? trace.phases?.length
        ? trace.phases
        : trace.activity?.kind && trace.activity.kind !== 'stage'
          ? [trace.activity.kind]
          : [trace.state === 'error' ? 'error' : 'done']
      : [];
    const liveKind = trace?.state === 'stream' ? trace.activity?.kind : undefined;

    const rows = Math.max(upstreams.length, phases.length, 1);
    const height = rows * ROW + PAD * 2;
    const rowCenter = (i: number) => PAD + ROW * i + ROW / 2;
    const boxTop = (i: number) => rowCenter(i) - BOX_H / 2;
    const mid = height / 2;
    const rootY = mid - BOX_H / 2;
    const keyY = boxTop(chosenIdx);
    const keyMid = keyY + BOX_H / 2;

    return (
      <div className="visualizer-view">
        <PanCanvas viewBox={`0 0 ${W} ${height}`} label="Request routing flow">
          {/* root: Firefly, with the model the client asked for — always visible */}
          <g className="viz-chosen">
            <rect x={X_ROOT} y={rootY} width={ROOT_W} height={BOX_H} rx={8} className="viz-box" />
            <text x={X_ROOT + ROOT_W / 2} y={rootY + BOX_H / 2 + 4} textAnchor="middle" className="viz-label">
              Firefly
            </text>
            <text x={X_ROOT + ROOT_W / 2} y={rootY + BOX_H + 16} textAnchor="middle" className="viz-sub">
              {trace?.model ?? trace?.path ?? (trace ? 'request' : 'waiting for a request…')}
            </text>
          </g>

          {/* upstreams considered for this request */}
          {upstreams.map((c, i) => {
            const y = boxTop(i);
            return (
              <g key={`${c.upstream}-${i}`} className={c.state === 'chosen' ? 'viz-chosen' : 'viz-skipped'}>
                <path
                  d={`M ${X_ROOT + ROOT_W} ${mid} H ${X_ROOT + ROOT_W + 16} V ${rowCenter(i)} H ${X_UPSTREAM - ELBOW}`}
                  className="viz-edge"
                />
                <rect x={X_UPSTREAM} y={y} width={UPSTREAM_W} height={BOX_H} rx={6} className="viz-box" />
                <text x={X_UPSTREAM + 10} y={y + BOX_H / 2 + 4} className="viz-label">
                  {c.upstream}
                </text>
                <text x={X_UPSTREAM + UPSTREAM_W + 8} y={y + BOX_H / 2 + 4} className="viz-sub">
                  {c.state}
                  {c.note ? ` · ${c.note}` : ''}
                </text>
              </g>
            );
          })}

          {/* credential actually spent */}
          {trace ? (
            <g className="viz-chosen">
              <path
                d={`M ${X_UPSTREAM + UPSTREAM_W} ${rowCenter(chosenIdx)} H ${X_KEY - ELBOW}`}
                className="viz-edge"
              />
              <rect x={X_KEY} y={keyY} width={KEY_W} height={BOX_H} rx={6} className="viz-box" />
              <text x={X_KEY + KEY_W / 2} y={keyY + BOX_H / 2 + 4} textAnchor="middle" className="viz-label">
                {keyRef || 'credential'}
              </text>
            </g>
          ) : null}

          {/* stream phases the response produced */}
          {phases.map((kind, i) => {
            const y = boxTop(i);
            const active = kind === liveKind;
            return (
              <g key={`${kind}-${i}`} className={active ? 'viz-live' : undefined}>
                <path
                  d={`M ${X_KEY + KEY_W} ${keyMid} H ${X_KEY + KEY_W + 16} V ${rowCenter(i)} H ${X_PHASE - ELBOW}`}
                  className="viz-edge"
                />
                <rect x={X_PHASE} y={y} width={PHASE_W} height={BOX_H} rx={6} className="viz-box" />
                <text x={X_PHASE + PHASE_W / 2} y={y + BOX_H / 2 + 4} textAnchor="middle" className="viz-label">
                  {phaseLabel(kind)}
                </text>
              </g>
            );
          })}
        </PanCanvas>
        <div className="visualizer-legend">
          {trace ? (
            <>
              <Badge tone={trace.error ? 'danger' : 'ok'}>{trace.error ? 'error' : trace.state}</Badge>
              <span>{trace.upstream ?? '—'}</span>
              <span>
                {trace.tokens_in ?? 0} in / {trace.tokens_out ?? 0} out
              </span>
              <span>TTFB {trace.ttfb_ms ?? 0}ms</span>
              <span>Total {trace.duration_ms ?? 0}ms</span>
              <span>{trace.deltas} deltas</span>
              <span>{trace.bytes}B content</span>
            </>
          ) : (
            <span className="muted">Send a request through the gateway to populate the flow.</span>
          )}
        </div>
      </div>
    );
  },
};
