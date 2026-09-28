import { useEffect, useMemo, useRef, useState } from 'react';
import { Badge } from '@/components/ui/Badge';
import type { RoutingTrace } from '@/services/visualizer';
import type { VisualizerView } from './index';
// agent-town ships typed ESM + .d.ts through package exports; the named
// `AgentTown` import works with Vite's ESM interop.
import { AgentTown } from 'agent-town';
import type { AgentStatus } from 'agent-town';

/**
 * Upstream-to-town mapping. One agent per catalog upstream (`up:<name>`), not
 * one per trace: the office is always staffed exactly like the live catalog.
 *
 * agent-town owns the acting: every status change triggers its internal
 * scheduleMovement, which walks the agent to a zone from the office table
 * (typing -> desk, idle -> break_area/whiteboard_area/desk, ...), and idle
 * agents re-roam on their own movement timer. This view only reconciles
 * gateway traces into staff statuses:
 *
 * - a live trace pinned to the upstream (request outranks stream as the
 *   fresher intent) drives a working status: typing while the answer streams,
 *   thinking while reasoning or unrouted, reading while tools run;
 * - no live trace: idle with no bubble, the worker lounges;
 * - the newest settled trace leaves a short success/error afterglow before
 *   the worker goes back to idle;
 * - unrouted traces (upstream not yet resolved, or unknown) are ignored: the
 *   office mirrors the catalog, never the routing transient.
 *
 * Secrets never enter the town: bubbles carry model @ tenant plus the live
 * delta count (key_ref stays in the tables below).
 */

export const STAFF_PREFIX = 'up:';

/** After a stream ends the worker keeps its outcome this long, then idles. */
export const AFTERGLOW_MS = 8000;

/** localStorage key for the town camera (zoom multiplier + follow toggle). */
export const TOWN_CAMERA_KEY = 'firefly.visualizer.town.camera.v1';

/** Extra headroom over the engine auto-fit: the office felt ~50% too small. */
export const TOWN_ZOOM_BOOST = 1.5;
export const TOWN_ZOOM_MIN = 0.5;
export const TOWN_ZOOM_MAX = 4;
export const TOWN_ZOOM_STEP = 1.25;

export interface TownCamera {
  zoom: number;
  follow: boolean;
}

export const DEFAULT_TOWN_CAMERA: TownCamera = { zoom: 1, follow: true };

export function clampTownZoom(z: number): number {
  if (!Number.isFinite(z)) return 1;
  return Math.min(TOWN_ZOOM_MAX, Math.max(TOWN_ZOOM_MIN, z));
}

export function readTownCamera(): TownCamera {
  try {
    const raw = localStorage.getItem(TOWN_CAMERA_KEY);
    if (!raw) return DEFAULT_TOWN_CAMERA;
    const parsed = JSON.parse(raw) as Partial<TownCamera>;
    return {
      zoom: clampTownZoom(parsed.zoom ?? 1),
      follow: parsed.follow ?? true,
    };
  } catch {
    return DEFAULT_TOWN_CAMERA;
  }
}

export function writeTownCamera(cam: TownCamera): void {
  try {
    localStorage.setItem(TOWN_CAMERA_KEY, JSON.stringify(cam));
  } catch {
    /* private mode / quota — the camera just won't survive reloads */
  }
}

export function staffId(upstream: string): string {
  return `${STAFF_PREFIX}${upstream}`;
}

export function unstaff(id: string): string | null {
  return id.startsWith(STAFF_PREFIX) ? id.slice(STAFF_PREFIX.length) : null;
}

/**
 * Newest live trace pinned to one upstream. `request` outranks `stream` — a
 * request that has not routed yet is fresher intent than an older stream on
 * the same upstream. Returns null when nobody works there.
 */
export function liveTraceFor(traces: RoutingTrace[], upstream: string): RoutingTrace | null {
  let stream: RoutingTrace | null = null;
  for (const t of traces) {
    if (t.upstream !== upstream) continue;
    if (t.state === 'request') return t;
    if (t.state === 'stream') stream = t;
  }
  return stream;
}

/**
 * Newest settled trace for one upstream, whatever its outcome. Drives the
 * afterglow once the stream ends.
 */
export function lastTraceFor(traces: RoutingTrace[], upstream: string): RoutingTrace | null {
  for (let i = traces.length - 1; i >= 0; i--) {
    if (traces[i].upstream === upstream) return traces[i];
  }
  return null;
}

/**
 * The roster: catalog order first (telemetry order), then any upstream seen
 * in traces but missing from the catalog. Empty only when both are empty.
 */
export function staffOf(catalogUpstreams: string[] | undefined, traces: RoutingTrace[]): string[] {
  const roster = [...(catalogUpstreams ?? [])];
  for (const t of traces) {
    if (t.upstream && !roster.includes(t.upstream)) roster.push(t.upstream);
  }
  return roster;
}

export function statusOf(t: RoutingTrace): AgentStatus {
  if (t.error || t.state === 'error') return 'error';
  if (t.state === 'stream') {
    switch (t.activity?.kind) {
      case 'reasoning':
        return 'thinking';
      case 'tool':
        return 'reading';
      case 'usage':
        return 'waiting';
      default:
        return 'typing';
    }
  }
  if (t.state === 'request') return 'thinking';
  return 'success';
}

export function messageOf(t: RoutingTrace | null): string | null {
  if (!t) return null;
  if (t.error) return t.error.length > 80 ? `${t.error.slice(0, 77)}...` : t.error;
  const model = t.model ?? 'model';
  const who = t.tenant ?? 'gateway';
  const live = t.state === 'stream' || t.state === 'request' ? ` · ${t.deltas}Δ` : '';
  return `${model} @ ${who}${live}`;
}

interface StaffEntry {
  upstream: string;
  id: string;
  status: AgentStatus;
  message: string | null;
}

/**
 * One entry per roster member: the live trace drives work, else the newest
 * settled trace leaves an afterglow, else the member idles with no bubble.
 */
export function staffEntries(
  catalogUpstreams: string[] | undefined,
  traces: RoutingTrace[],
  now: number,
): StaffEntry[] {
  return staffOf(catalogUpstreams, traces).map((upstream) => {
    const id = staffId(upstream);
    const live = liveTraceFor(traces, upstream);
    if (live) return { upstream, id, status: statusOf(live), message: messageOf(live) };
    const last = lastTraceFor(traces, upstream);
    if (last && last.ended_at && now - last.ended_at < AFTERGLOW_MS) {
      return { upstream, id, status: statusOf(last), message: messageOf(last) };
    }
    return { upstream, id, status: 'idle' as AgentStatus, message: null };
  });
}

/** Office grid in tiles per size, mirrored from the agent-town bundle. */
export const OFFICE_TILES = {
  small: { w: 20, h: 13 },
  medium: { w: 26, h: 16 },
  large: { w: 34, h: 20 },
} as const;

/**
 * agent-town seats 6 / 16 / 30 per office size; pick the smallest office that
 * fits the roster so a handful of upstreams does not rattle in a hall.
 */
/**
 * Pixel scale that fits the whole office grid into the viewport: the engine
 * canvas is device pixels (CSS size x devicePixelRatio), one tile is 16px,
 * so scale = min(w / tiles.w / 16, h / tiles.h / 16) with breathing room.
 * Pure, so the headless check can assert it. Matches the engine auto-fit
 * (floor(x * 0.9), min 1) for any viewport.
 */
export function officeScale(size: keyof typeof OFFICE_TILES, viewportW: number, viewportH: number, dpr = 1): number {
  const tiles = OFFICE_TILES[size];
  const w = viewportW * dpr;
  const h = viewportH * dpr;
  if (w <= 0 || h <= 0) return 1;
  return Math.max(1, Math.floor(Math.min(w / (tiles.w * 16), h / (tiles.h * 16)) * 0.9));
}

export function officeSizeFor(staff: number): 'small' | 'medium' | 'large' {
  if (staff > 16) return 'large';
  if (staff > 6) return 'medium';
  return 'small';
}

/**
 * Camera + staff reconciliation over the engine canvas. The engine owns the
 * pixel rendering and its own auto-fit; the camera is a CSS zoom layered on
 * top (transform on the wrapper), so user zoom never fights the engine's
 * internal scale and survives re-renders.
 */
function TownCanvas({ roster, traces }: { roster: string[]; traces: RoutingTrace[] }) {
  const hostRef = useRef<HTMLDivElement>(null);
  const townRef = useRef<AgentTown | null>(null);
  const [size, setSize] = useState(() => officeSizeFor(roster.length));
  // Re-evaluated when the afterglow timer fires so workers return to idle.
  const [now, setNow] = useState(() => Date.now());
  const [camera, setCamera] = useState<TownCamera>(() => readTownCamera());
  // Trace-driven statuses, unless follow is off — then the office freezes on
  // the last snapshot and the crew keeps roaming on their own.
  const frozen = useRef<StaffEntry[] | null>(null);
  const liveEntries = useMemo(() => staffEntries(roster, traces, now), [roster, traces, now]);
  const entries = camera.follow ? liveEntries : (frozen.current ?? liveEntries);

  useEffect(() => {
    writeTownCamera(camera);
  }, [camera]);

  const zoomBy = (factor: number) =>
    setCamera((c) => ({ ...c, zoom: clampTownZoom(c.zoom * factor) }));
  const toggleFollow = () => {
    if (camera.follow) frozen.current = liveEntries;
    else frozen.current = null;
    setCamera((c) => ({ ...c, follow: !c.follow }));
  };

  useEffect(() => {
    setSize(officeSizeFor(roster.length));
  }, [roster.length]);

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    const hostW = host.clientWidth || 1;
    const hostH = host.clientHeight || 1;
    const town = new AgentTown({
      container: host,
      // Fit the whole office grid into the viewport: the engine auto-fits to
      // a fixed 24x16 tile window, so override its scale for the real grid.
      scale: officeScale(officeSizeFor(roster.length), hostW, hostH),
      theme: 'hybrid',
      officeSize: officeSizeFor(roster.length),
      environment: 'office',
      autoSize: false,
    });
    townRef.current = town;
    return () => {
      town.destroy();
      townRef.current = null;
    };
    // Mount once: the town is a live engine, React must not rebuild it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Resize is engine-side (rebuilds the office); guarded because shrinking
  // below the seated count throws inside the library.
  useEffect(() => {
    const town = townRef.current;
    if (!town) return;
    try {
      town.setOfficeSize(size);
    } catch {
      /* keep the current office when the size switch has no room */
    }
  }, [size]);

  // Reconcile staff: add newcomers, update statuses, remove anyone off the
  // roster (plus any stray non-staff id, e.g. from the old per-trace model).
  // Status changes trigger the engine's own scheduleMovement, which is what
  // walks a worker to a desk when work arrives and lets them roam when idle.
  useEffect(() => {
    const town = townRef.current;
    if (!town) return;
    const wanted = new Map(entries.map((e) => [e.id, e]));
    for (const existing of town.getAgents()) {
      if (!wanted.has(existing.id)) town.removeAgent(existing.id);
    }
    for (const e of entries) {
      const current = town.getAgent(e.id);
      if (!current) {
        town.addAgent({
          id: e.id,
          name: e.upstream,
          status: e.status,
          message: e.message ?? undefined,
          role: 'upstream',
          team: 'gateway',
        });
      } else {
        town.updateAgent(e.id, { status: e.status, message: e.message, name: e.upstream });
      }
    }
  }, [entries]);

  // Afterglow expiry: once a settled outcome stops driving an entry, wake up
  // so the worker drops back to idle instead of freezing on success/error.
  useEffect(() => {
    if (!entries.some((e) => e.status === 'success' || e.status === 'error')) return;
    const timer = setTimeout(() => setNow(Date.now()), AFTERGLOW_MS + 250);
    return () => clearTimeout(timer);
  }, [entries]);

  return (
    <div className="viz-town-wrap">
      <div
        ref={hostRef}
        className="viz-town"
        role="img"
        aria-label="Upstreams as pixel-art office staff"
        style={{
          transform: `scale(${TOWN_ZOOM_BOOST * camera.zoom})`,
          transformOrigin: 'center center',
        }}
      />
      <div className="viz-zoom" role="group" aria-label="Office camera">
        <button type="button" onClick={() => zoomBy(TOWN_ZOOM_STEP)} aria-label="Zoom office in">
          +
        </button>
        <span className="zoom-level">{Math.round(TOWN_ZOOM_BOOST * camera.zoom * 100)}%</span>
        <button type="button" onClick={() => zoomBy(1 / TOWN_ZOOM_STEP)} aria-label="Zoom office out">
          −
        </button>
        <button
          type="button"
          onClick={() => setCamera((c) => ({ ...c, zoom: 1 }))}
          aria-label="Reset office zoom"
        >
          ⟳
        </button>
        <button
          type="button"
          onClick={toggleFollow}
          aria-pressed={camera.follow}
          aria-label={camera.follow ? 'Pause trace follow' : 'Resume trace follow'}
          title={camera.follow ? 'Pause: freeze statuses, keep roaming' : 'Resume: follow live traces'}
        >
          {camera.follow ? '❚❚' : '▶'}
        </button>
      </div>
    </div>
  );
}

export const townView: VisualizerView = {
  id: 'town',
  label: 'Town',
  hint: 'Office: one staffer per upstream.',
  render: ({ trace, traces, catalogUpstreams }) => {
    const roster = staffOf(catalogUpstreams, traces);
    const working = roster.filter((u) => liveTraceFor(traces, u) !== null).length;
    return (
      <div className="visualizer-view">
        <TownCanvas roster={roster} traces={traces} />
        <div className="visualizer-legend">
          {trace ? (
            <>
              <Badge tone={trace.error ? 'danger' : 'ok'}>{trace.error ? 'error' : trace.state}</Badge>
              <span>{trace.upstream ?? 'routing'}</span>
              <span>
                {trace.tokens_in ?? 0} in / {trace.tokens_out ?? 0} out
              </span>
              <span>{working} working / {roster.length} staff</span>
            </>
          ) : roster.length > 0 ? (
            <>
              <span>{roster.length} staff on duty</span>
              <span className="muted">Send a request through the gateway to put them to work.</span>
            </>
          ) : (
            <span className="muted">Send a request through the gateway to populate the town.</span>
          )}
        </div>
      </div>
    );
  },
};
