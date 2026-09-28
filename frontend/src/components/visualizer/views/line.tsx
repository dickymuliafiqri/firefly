import {
  useEffect,
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from 'react';
import { Badge } from '@/components/ui/Badge';
import type { VisualizerView } from './index';

/**
 * Geometry. Columns are anchored; rows fan out vertically around the canvas
 * midline. Edge geometry mirrors the firefly-web feature illustrations:
 * cubic-bezier lanes with a dim base stroke plus an animated dashed overlay.
 */
const ROW = 120;
const PAD = 70;
const W = 1580;

const X_ROOT = 90;
const ROOT_W = 170;
const X_UP = 540;
const UP_W = 260;
const X_KEY = 1040;
const KEY_W = 210;
const X_PHASE = 1330;
const PHASE_W = 210;
const BOX_H = 56;
const CURVE = 120;

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

/** Smooth left-to-right bezier lane between two port points. */
function lane(x0: number, y0: number, x1: number, y1: number): string {
  return `M ${x0} ${y0} C ${x0 + CURVE} ${y0}, ${x1 - CURVE} ${y1}, ${x1} ${y1}`;
}

/**
 * Google-Maps-style pannable + zoomable canvas: the SVG renders larger than
 * its viewport; pointer drags pan it, the wheel (and two-finger pinch) zoom
 * toward the cursor, zoom buttons sit in the corner, and double-click snaps
 * back to the origin at 100%.
 */
const MIN_ZOOM = 0.3;
const MAX_ZOOM = 3;

type Transform = { x: number; y: number; z: number };

const clampZoom = (z: number) => Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, z));

/** Keep the viewport point m fixed while zooming from z to nz around it. */
function zoomAround(prev: Transform, mx: number, my: number, nz: number): Transform {
  return {
    x: mx - ((mx - prev.x) * nz) / prev.z,
    y: my - ((my - prev.y) * nz) / prev.z,
    z: nz,
  };
}

function PanCanvas({ viewBox, label, children }: { viewBox: string; label: string; children: ReactNode }) {
  const [t, setT] = useState<Transform>({ x: 0, y: 0, z: 1 });
  const tRef = useRef(t);
  tRef.current = t;
  const canvasRef = useRef<HTMLDivElement>(null);
  const drag = useRef<{ px: number; py: number; ox: number; oy: number } | null>(null);
  const pointers = useRef(new Map<number, { x: number; y: number }>());
  const pinch = useRef<{ dist: number; mx: number; my: number; ox: number; oy: number; z: number } | null>(null);

  useEffect(() => {
    const el = canvasRef.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      const prev = tRef.current;
      const nz = clampZoom(prev.z * Math.exp(-e.deltaY * 0.0015));
      if (nz === prev.z) return;
      const rect = el.getBoundingClientRect();
      setT(zoomAround(prev, e.clientX - rect.left, e.clientY - rect.top, nz));
    };
    el.addEventListener('wheel', onWheel, { passive: false });
    return () => el.removeEventListener('wheel', onWheel);
  }, []);

  const zoomBy = (factor: number) => {
    const el = canvasRef.current;
    const prev = tRef.current;
    const nz = clampZoom(prev.z * factor);
    if (!el || nz === prev.z) return;
    const rect = el.getBoundingClientRect();
    setT(zoomAround(prev, rect.width / 2, rect.height / 2, nz));
  };

  const reset = () => setT({ x: 0, y: 0, z: 1 });

  const onPointerDown = (e: ReactPointerEvent<HTMLDivElement>) => {
    pointers.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
    e.currentTarget.setPointerCapture(e.pointerId);
    if (pointers.current.size === 2) {
      drag.current = null;
      const [a, b] = [...pointers.current.values()];
      pinch.current = {
        dist: Math.hypot(a.x - b.x, a.y - b.y),
        mx: (a.x + b.x) / 2,
        my: (a.y + b.y) / 2,
        ox: tRef.current.x,
        oy: tRef.current.y,
        z: tRef.current.z,
      };
      return;
    }
    if (pointers.current.size === 1) {
      drag.current = { px: e.clientX, py: e.clientY, ox: tRef.current.x, oy: tRef.current.y };
    }
  };

  const onPointerMove = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (!pointers.current.has(e.pointerId)) return;
    pointers.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
    const el = canvasRef.current;
    if (!el) return;
    if (pointers.current.size === 2 && pinch.current) {
      const [a, b] = [...pointers.current.values()];
      const dist = Math.hypot(a.x - b.x, a.y - b.y);
      const nz = clampZoom((pinch.current.z * dist) / pinch.current.dist);
      const rect = el.getBoundingClientRect();
      const nmx = (a.x + b.x) / 2 - rect.left;
      const nmy = (a.y + b.y) / 2 - rect.top;
      const imx = pinch.current.mx - rect.left;
      const imy = pinch.current.my - rect.top;
      setT({
        x: nmx - ((imx - pinch.current.ox) * nz) / pinch.current.z,
        y: nmy - ((imy - pinch.current.oy) * nz) / pinch.current.z,
        z: nz,
      });
      return;
    }
    if (pointers.current.size === 1 && drag.current) {
      setT({
        ...tRef.current,
        x: drag.current.ox + (e.clientX - drag.current.px),
        y: drag.current.oy + (e.clientY - drag.current.py),
      });
    }
  };

  const endPointer = (e: ReactPointerEvent<HTMLDivElement>) => {
    pointers.current.delete(e.pointerId);
    if (pointers.current.size < 2) pinch.current = null;
    if (pointers.current.size === 1) {
      const [p] = [...pointers.current.values()];
      drag.current = { px: p.x, py: p.y, ox: tRef.current.x, oy: tRef.current.y };
    } else {
      drag.current = null;
    }
  };

  return (
    <div
      ref={canvasRef}
      className="viz-canvas"
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={endPointer}
      onPointerCancel={endPointer}
      onDoubleClick={reset}
    >
      <svg
        viewBox={viewBox}
        className="visualizer-svg"
        role="img"
        aria-label={label}
        style={{ transform: `translate(${t.x}px, ${t.y}px) scale(${t.z})`, transformOrigin: '0 0' }}
      >
        {children}
      </svg>
      <div className="viz-zoom" onPointerDown={(e) => e.stopPropagation()}>
        <div className="zoom-level">{Math.round(t.z * 100)}%</div>
        <button type="button" title="Zoom in" onClick={() => zoomBy(1.25)}>
          +
        </button>
        <button type="button" title="Zoom out" onClick={() => zoomBy(0.8)}>
          −
        </button>
        <button type="button" title="Reset view" onClick={reset}>
          ⟲
        </button>
      </div>
    </div>
  );
}

/**
 * One reference row of this view. `idle` marks an upstream that exists in the
 * live catalog but was not touched by the selected trace — those rows are
 * permanent, exactly like the root node.
 */
type LaneNode = {
  upstream: string;
  key?: string;
  note?: string;
  state: 'chosen' | 'skipped' | 'idle';
};

/** Staggered light packets riding a ghost path (SMIL animateMotion). */
function Packets({ pathId, dur, tone = '' }: { pathId: string; dur: number; tone?: string }) {
  const begins = [0, dur / 4.4, dur / 2.2];
  return (
    <>
      {begins.map((b, i) => (
        <circle key={i} r={i % 2 ? 2.4 : 3} className={`viz-pkt${i % 2 ? ' dim' : ''}${tone ? ` ${tone}` : ''}`}>
          <animateMotion dur={`${dur}s`} begin={`${b}s`} repeatCount="indefinite">
            <mpath href={`#${pathId}`} />
          </animateMotion>
        </circle>
      ))}
    </>
  );
}

/**
 * Line view: the monitored request drawn as one left-to-right flow in the
 * firefly-web illustration style — smooth bezier lanes, a dashed "data flow"
 * overlay and travelling light packets confined to the active path, dim base
 * lanes with floating status labels for skipped branches. The Firefly root and
 * one node per live catalog upstream are always on the canvas, even before the
 * first request arrives.
 */
export const lineView: VisualizerView = {
  id: 'line',
  label: 'Line',
  hint: 'Flow: Firefly → upstream → key → stream phases.',
  render: ({ trace, catalogUpstreams = [] }) => {
    const candidates = trace?.candidates ?? [];
    const routed: LaneNode[] = candidates.length
      ? candidates
      : trace?.upstream
        ? [{ upstream: trace.upstream, state: 'chosen' }]
        : [];
    const routedNames = new Set(routed.map((c) => c.upstream));
    const upstreams: LaneNode[] = [
      ...routed,
      ...catalogUpstreams
        .filter((name) => !routedNames.has(name))
        .map((name) => ({ upstream: name, state: 'idle' as const })),
    ];
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
    const streaming = trace?.state === 'stream';
    const liveKind = streaming ? trace?.activity?.kind : undefined;
    const liveIdx = liveKind ? phases.indexOf(liveKind) : -1;

    const rows = Math.max(upstreams.length, phases.length, 1);
    const height = rows * ROW + PAD * 2;
    const rowCenter = (i: number) => PAD + ROW * i + ROW / 2;
    const boxTop = (i: number) => rowCenter(i) - BOX_H / 2;
    const mid = height / 2;
    const rootY = mid - BOX_H / 2;
    const keyY = boxTop(chosenIdx);
    const keyMid = keyY + BOX_H / 2;
    const rootRight = X_ROOT + ROOT_W;
    const upRight = X_UP + UP_W;
    const keyRight = X_KEY + KEY_W;

    const settled = trace ? !streaming : false;

    return (
      <div className="visualizer-view">
        <PanCanvas viewBox={`0 0 ${W} ${height}`} label="Request routing flow">
          {/* ---------- lanes: Firefly -> upstreams ---------- */}
          {upstreams.map((c, i) => {
            const d = lane(rootRight, mid, X_UP, rowCenter(i));
            const chosen = c.state === 'chosen';
            return (
              <g key={`lane-up-${i}`}>
                <path d={d} className={`viz-base${chosen ? '' : ' dim'}`} />
                {chosen && <path d={d} className={`viz-flow${settled ? ' settled' : ''}`} />}
                {chosen && !settled && <path id="viz-p1" d={d} className="viz-ghost" />}
                {!chosen && c.state !== 'idle' && (
                  <text
                    className={`viz-st ${c.state === 'skipped' ? 'red' : 'dim'}`}
                    x={(rootRight + X_UP) / 2}
                    y={rowCenter(i) + (i % 2 ? 26 : -14)}
                    textAnchor="middle"
                  >
                    {c.note || c.state}
                  </text>
                )}
              </g>
            );
          })}

          {/* ---------- lane: chosen upstream -> key ---------- */}
          {trace && (
            <g>
              <path d={lane(upRight, rowCenter(chosenIdx), X_KEY, keyMid)} className="viz-base" />
              <path
                d={lane(upRight, rowCenter(chosenIdx), X_KEY, keyMid)}
                className={`viz-flow f2${settled ? ' settled' : ''}`}
              />
              {!settled && <path id="viz-p2" d={lane(upRight, rowCenter(chosenIdx), X_KEY, keyMid)} className="viz-ghost" />}
              <text className="viz-st grn" x={(upRight + X_KEY) / 2} y={keyMid - 14} textAnchor="middle">
                selected
              </text>
            </g>
          )}

          {/* ---------- lanes: key -> phases ---------- */}
          {phases.map((_kind, i) => {
            const d = lane(keyRight, keyMid, X_PHASE, rowCenter(i));
            const live = i === liveIdx;
            return (
              <g key={`lane-ph-${i}`}>
                <path d={d} className={`viz-base${live ? '' : ' dim'}`} />
                {live && <path d={d} className={`viz-flow info${settled ? ' settled' : ''}`} />}
                {live && !settled && <path id="viz-p3" d={d} className="viz-ghost" />}
              </g>
            );
          })}

          {/* ---------- packets on the active path ---------- */}
          {streaming && (
            <g>
              <Packets pathId="viz-p1" dur={2.2} />
              <Packets pathId="viz-p2" dur={1.4} />
              <Packets pathId="viz-p3" dur={1.1} tone="cyan" />
            </g>
          )}

          {/* ---------- root node: Firefly (always visible) ---------- */}
          <g>
            <rect x={X_ROOT} y={rootY} width={ROOT_W} height={BOX_H} rx={12} className="viz-node ff" />
            <text x={X_ROOT + ROOT_W / 2} y={rootY + 24} textAnchor="middle" className="viz-title">
              Firefly
            </text>
            <text x={X_ROOT + ROOT_W / 2} y={rootY + 43} textAnchor="middle" className="viz-sub">
              {trace
                ? `${trace.model ?? trace.path ?? 'request'} · ${trace.error ? 'error' : trace.state}`
                : 'waiting for a request…'}
            </text>
          </g>

          {/* ---------- upstream nodes ---------- */}
          {upstreams.map((c, i) => {
            const chosen = c.state === 'chosen';
            return (
              <g key={`${c.upstream}-${i}`}>
                <rect x={X_UP} y={boxTop(i)} width={UP_W} height={BOX_H} rx={10} className={`viz-node${chosen ? ' chosen' : c.state === 'idle' ? ' idle' : ' gone'}`} />
                <text x={X_UP + 24} y={boxTop(i) + 25} className="viz-title">
                  {c.upstream}
                </text>
                <text x={X_UP + 24} y={boxTop(i) + 43} className="viz-sub">
                  {c.note ?? ''}
                </text>
                <text
                  x={X_UP + UP_W - 20}
                  y={boxTop(i) + 25}
                  textAnchor="end"
                  className={`viz-st ${chosen ? 'grn' : 'dim'}`}
                >
                  {c.state}
                </text>
              </g>
            );
          })}

          {/* ---------- key node ---------- */}
          {trace && (
            <g>
              <rect x={X_KEY} y={keyY} width={KEY_W} height={BOX_H} rx={10} className="viz-node chosen" />
              <text x={X_KEY + KEY_W / 2} y={keyY + 25} textAnchor="middle" className="viz-title">
                {keyRef || 'credential'}
              </text>
              <text x={X_KEY + KEY_W / 2} y={keyY + 43} textAnchor="middle" className="viz-sub">
                ttfb {trace.ttfb_ms ?? 0}ms
              </text>
            </g>
          )}

          {/* ---------- phase nodes ---------- */}
          {phases.map((kind, i) => {
            const live = i === liveIdx;
            return (
              <g key={`${kind}-${i}`}>
                <rect x={X_PHASE} y={boxTop(i)} width={PHASE_W} height={BOX_H} rx={10} className={`viz-node${live ? ' live' : ''}`} />
                <text x={X_PHASE + 24} y={boxTop(i) + 25} className="viz-title">
                  {phaseLabel(kind)}
                </text>
                <text x={X_PHASE + 24} y={boxTop(i) + 43} className="viz-sub">
                  {live ? `${trace?.deltas ?? 0} deltas` : 'done'}
                </text>
                <text
                  x={X_PHASE + PHASE_W - 20}
                  y={boxTop(i) + 25}
                  textAnchor="end"
                  className={`viz-st ${live ? 'cyan' : 'dim'}`}
                >
                  {live ? 'live' : 'done'}
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
