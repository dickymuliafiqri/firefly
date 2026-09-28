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
const W = 1310;

const X_ROOT = 90;
const ROOT_W = 200;
/**
 * Three columns, left to right: the Firefly root, one node per live catalog
 * upstream, and the fixed stream-phase column. The credential and the Token
 * Saver hop are deliberately *not* nodes here: one request spends exactly one
 * credential out of a ring that may hold a thousand keys, and the rewrite is
 * invisible in the payload unless it changed the body, so either of them would
 * end up either exploding the graph or reporting noise. What the operator needs
 * per row is the *model* the winning upstream serves, which is what the upstream
 * node carries instead.
 */
const X_UP = 470;
const UP_W = 300;
const X_PHASE = 1010;
const PHASE_W = 230;
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

/**
 * Node sub-labels are single SVG text lines: they neither wrap nor ellipsize on
 * their own, so a long model name or skip reason would silently run into the
 * neighbouring column. Clip to what the box width actually holds.
 */
const SUB_CHARS = 34;

function clip(text: string, max = SUB_CHARS): string {
  return text.length <= max ? text : `${text.slice(0, max - 3)}...`;
}

/**
 * Terminal nodes are canvas topology, not traffic. The set is fixed (it mirrors
 * the `kind` values `trace.Classify` emits on the server), so the graph never
 * grows or shrinks while a trace streams in or completes; only a node *state*
 * changes: `live` while that kind flows, `done` once the trace recorded it,
 * `idle` otherwise.
 */
const TERMINAL_KINDS = ['reasoning', 'tool', 'answer', 'usage', 'error'] as const;

/**
 * Smooth left-to-right bezier lane between two port points. `curve` is the
 * horizontal handle length; callers clamp it to half the horizontal run for
 * short legs, because a handle longer than the gap itself bends the path
 * backwards and the lane visibly doubles back on itself.
 */
function lane(x0: number, y0: number, x1: number, y1: number, curve = CURVE): string {
  return `M ${x0} ${y0} C ${x0 + curve} ${y0}, ${x1 - curve} ${y1}, ${x1} ${y1}`;
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
 * live catalog but was not touched by the selected trace: those rows are
 * permanent, exactly like the root node. The credential a candidate resolved is
 * not carried here, because credentials are not nodes on this canvas.
 */
type LaneNode = {
  upstream: string;
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
 * lanes with floating status labels for skipped branches.
 *
 * The topology is permanent by design: the Firefly root, one node per live
 * catalog upstream and one terminal node per stream phase sit on the canvas
 * before the first request arrives and stay there afterwards, so nothing appears
 * or disappears mid-stream and the operator never loses their place (rows are
 * anchored). A request only changes node *state*.
 *
 * Connectors are permanent too: the root fans out to every upstream row and the
 * winning row fans out to the whole phase column from the first render, so the
 * canvas reads as one connected graph. The fan past the upstream column hangs
 * off the row this request routed to (the phases belong to that provider); while
 * nothing has resolved yet it hangs off the column midline, so the idle canvas
 * is already complete. Only the state moves: bright + flowing on the path the
 * request took, a settled trail on a phase it already left behind, dim on
 * everything else.
 *
 * Each node states one fact: the root shows the ingress call, an upstream node
 * shows the model it served (or why it was skipped), and a terminal node shows
 * its phase state.
 */
export const lineView: VisualizerView = {
  id: 'line',
  label: 'Line',
  hint: 'Flow: Firefly -> upstream -> stream phases.',
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

    const phases = trace
      ? trace.phases?.length
        ? trace.phases
        : trace.activity?.kind && trace.activity.kind !== 'stage'
          ? [trace.activity.kind]
          : [trace.state === 'error' ? 'error' : 'done']
      : [];
    const recorded = new Set(phases);
    const streaming = trace?.state === 'stream';
    const liveKind = streaming ? trace?.activity?.kind : undefined;

    // Rows are reserved for the fixed terminal column too, so the canvas never
    // resizes when a trace arrives or a phase lights up.
    const rows = Math.max(upstreams.length, TERMINAL_KINDS.length, 1);
    const height = rows * ROW + PAD * 2;
    const rowCenter = (i: number) => PAD + ROW * i + ROW / 2;
    const boxTop = (i: number) => rowCenter(i) - BOX_H / 2;
    const mid = height / 2;
    const rootY = mid - BOX_H / 2;
    const rootRight = X_ROOT + ROOT_W;
    const upRight = X_UP + UP_W;

    const settled = trace ? !streaming : false;

    // Root sub-label: the ingress call. The *model* belongs to the upstream row
    // that served it, not here.
    const ingress = trace ? `${trace.method ?? 'POST'} ${trace.path ?? ''}`.trim() : '';
    // The phase the stream is inside right now, if any. `activity.kind` can also
    // be a non-phase marker (`stage`), and then no terminal lane is live and the
    // packet path must not be referenced at all.
    const liveTerminal = TERMINAL_KINDS.find((k) => k === liveKind);
    // The phases belong to the upstream that won the routing, so the fan past the
    // upstream column hangs off that row. Before a trace has resolved a winner
    // there is no such row and the fan hangs off the column midline, which keeps
    // the idle canvas complete and symmetric; either way the node set is
    // untouched and only lane geometry follows the winner.
    const fanY = trace ? rowCenter(chosenIdx) : mid;
    // Short legs need a shorter handle: a curve longer than the run itself bends
    // the lane backwards and it visibly doubles back on itself.
    const rootCurve = Math.min(CURVE, (X_UP - rootRight) / 2);
    const phaseCurve = Math.min(CURVE, (X_PHASE - upRight) / 2);
    // The pre-forward rewrite is still reported, it just does not own a node any
    // more: the server emits `trace.StageTokenSaver` only when a pass really
    // changed the body, so the stage is the "this request was compressed" fact.
    const tsStage = trace?.stages?.find((s) => s.name === 'tokensaver');

    return (
      <div className="visualizer-view">
        <PanCanvas viewBox={`0 0 ${W} ${height}`} label="Request routing flow">
          {/* ---------- lanes: Firefly -> upstream rows (permanent fan-out) ----------
              The root fans out to the whole upstream column from the first render,
              so the canvas is one connected graph before any traffic arrives. Only
              the row the request routed to lights up (flowing dashes + packet). */}
          {upstreams.map((c, i) => {
            const chosen = c.state === 'chosen';
            const d = lane(rootRight, mid, X_UP, rowCenter(i), rootCurve);
            return (
              <g key={`lane-up-${i}`}>
                <path d={d} className={`viz-base${chosen ? '' : ' dim'}`} />
                {chosen && <path d={d} className={`viz-flow${settled ? ' settled' : ''}`} />}
                {chosen && !settled && <path id="viz-p0" d={d} className="viz-ghost" />}
              </g>
            );
          })}

          {/* ---------- lanes: winning upstream -> stream phases (permanent) ----------
              All five lanes exist from the first render. Only the state moves: the
              phase the stream is inside right now is bright and flowing, a phase it
              already left behind keeps a settled trail, the rest stay dim. */}
          {TERMINAL_KINDS.map((kind, i) => {
            const live = kind === liveKind;
            const done = !live && recorded.has(kind);
            const d = lane(upRight, fanY, X_PHASE, rowCenter(i), phaseCurve);
            return (
              <g key={`lane-ph-${kind}`}>
                <path d={d} className={`viz-base${live || done ? '' : ' dim'}`} />
                {live && <path d={d} className={`viz-flow info${settled ? ' settled' : ''}`} />}
                {done && <path d={d} className="viz-flow info settled" />}
                {live && !settled && <path id="viz-p1" d={d} className="viz-ghost" />}
              </g>
            );
          })}

          {/* ---------- packets on the active path ---------- */}
          {streaming && (
            <g>
              <Packets pathId="viz-p0" dur={2.2} />
              {liveTerminal && <Packets pathId="viz-p1" dur={1.1} tone="cyan" />}
            </g>
          )}

          {/* ---------- root node: Firefly (always visible) ---------- */}
          <g>
            <rect x={X_ROOT} y={rootY} width={ROOT_W} height={BOX_H} rx={12} className="viz-node ff" />
            <text x={X_ROOT + ROOT_W / 2} y={rootY + 25} textAnchor="middle" className="viz-title">
              Firefly
            </text>
            <text x={X_ROOT + ROOT_W / 2} y={rootY + 43} textAnchor="middle" className="viz-sub">
              {trace ? clip(ingress) : 'waiting for a request...'}
            </text>
            {trace && (
              <text
                x={X_ROOT + ROOT_W - 20}
                y={rootY + 25}
                textAnchor="end"
                className={`viz-st ${trace.error ? 'red' : 'dim'}`}
              >
                {trace.error ? 'error' : trace.state}
              </text>
            )}
          </g>

          {/* ---------- upstream nodes: name plus the model it is serving ---------- */}
          {upstreams.map((c, i) => {
            const chosen = c.state === 'chosen';
            const sub = chosen
              ? clip(trace?.model ?? 'model')
              : c.state === 'skipped'
                ? clip(c.note ?? 'skipped')
                : 'idle';
            return (
              <g key={`${c.upstream}-${i}`}>
                <rect x={X_UP} y={boxTop(i)} width={UP_W} height={BOX_H} rx={10} className={`viz-node${chosen ? ' chosen' : c.state === 'idle' ? ' idle' : ' gone'}`} />
                <text x={X_UP + 24} y={boxTop(i) + 25} className="viz-title">
                  {c.upstream}
                </text>
                <text x={X_UP + 24} y={boxTop(i) + 43} className="viz-sub">
                  {sub}
                </text>
                <text
                  x={X_UP + UP_W - 20}
                  y={boxTop(i) + 25}
                  textAnchor="end"
                  className={`viz-st ${chosen ? 'grn' : 'dim'}`}
                >
                  {chosen ? `ttfb ${trace?.ttfb_ms ?? 0}ms` : c.state}
                </text>
              </g>
            );
          })}

          {/* ---------- terminal nodes: fixed set, always visible ---------- */}
          {TERMINAL_KINDS.map((kind, i) => {
            const live = kind === liveKind;
            const done = !live && recorded.has(kind);
            return (
              <g key={`terminal-${kind}`}>
                <rect x={X_PHASE} y={boxTop(i)} width={PHASE_W} height={BOX_H} rx={10} className={`viz-node${live ? ' live' : done ? '' : ' idle'}`} />
                <text x={X_PHASE + 24} y={boxTop(i) + 25} className="viz-title">
                  {phaseLabel(kind)}
                </text>
                <text x={X_PHASE + 24} y={boxTop(i) + 43} className="viz-sub">
                  {live ? `${trace?.deltas ?? 0} deltas` : done ? 'done' : 'idle'}
                </text>
                <text
                  x={X_PHASE + PHASE_W - 20}
                  y={boxTop(i) + 25}
                  textAnchor="end"
                  className={`viz-st ${live ? 'cyan' : done ? 'grn' : 'dim'}`}
                >
                  {live ? 'live' : done ? 'done' : 'idle'}
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
              {tsStage && <span>{`token saver ${tsStage.detail ?? 'rewrote body'}`}</span>}
            </>
          ) : (
            <span className="muted">Send a request through the gateway to populate the flow.</span>
          )}
        </div>
      </div>
    );
  },
};
