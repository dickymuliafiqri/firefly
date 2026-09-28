/**
 * Headless verification for the Line view topology invariants (run with
 * `npm run check:line-view`). The project ships no test runner, so this bundles
 * the view with esbuild and renders it through `react-dom/server`:
 *
 *   1. the node set is constant: the Firefly root, one node per catalog upstream
 *      and the fixed terminal column, whether or not a trace is selected, so
 *      nothing appears or disappears mid-stream;
 *   2. every connector is permanent as well: the root fans out to every upstream
 *      row and *every* upstream row fans out to the whole phase column from the
 *      first render, so the canvas is one connected graph no matter which row
 *      wins the routing. Only a lane's state moves (bright + flowing on the path
 *      the request took, a settled trail on a phase it already left behind, dim
 *      on everything else — including the rows the request never touched);
 *   3. the canvas is exactly `Firefly -> upstream -> stream phases`: there is no
 *      credential node and no Token Saver node, so an unauthenticated trace can
 *      never imply a credential was spent (none is ever drawn) and the rewrite
 *      fact lands in the legend instead of owning a column;
 *   4. an upstream node carries the model that upstream served (the root carries
 *      the ingress call), and every sub-label is clipped to its box;
 *   5. a trace only changes node *state* (`live` / `done` / `idle`).
 *
 * Geometry mirrored from `views/line.tsx`: X_ROOT = 90 with X_ROOT + ROOT_W =
 * 290 (root exit), X_UP = 470 with X_UP + UP_W = 770 (upstream exit), X_PHASE =
 * 1010, PAD = 70, ROW = 120 -> for a 5-row canvas the midline is 370 and the
 * rows are 130 / 250 / 370 / 490 / 610. The short root leg clamps its bezier
 * handle to (470 - 290) / 2 = 90; the phase leg keeps the full 120.
 *
 * Second stanza (same bundle): the Town mapping (agent-town office lens) — one
 * staffer per catalog upstream (idle with no bubble when there is no work),
 * the newest live trace pinned to its upstream drives a working status, the
 * newest settled trace leaves a short afterglow, and bubbles carry
 * `model @ tenant` (never a credential).
 */
import { renderToStaticMarkup } from 'react-dom/server';
import { lineView } from '../src/components/visualizer/views/line';
import { contentSize, fitTransform, fitZoom } from '../src/components/visualizer/views/line';
import {
  AFTERGLOW_MS,
  clampTownZoom,
  lastTraceFor,
  liveTraceFor,
  messageOf,
  officeSizeFor,
  staffEntries,
  staffId,
  staffOf,
  statusOf,
  townView,
  officeScale,
  TOWN_ZOOM_BOOST,
  TOWN_ZOOM_MAX,
  TOWN_ZOOM_MIN,
  unstaff,
} from '../src/components/visualizer/views/town';
import type { RoutingTrace } from '../src/services/visualizer';

const now = Date.now();

function traceOf(over: Partial<RoutingTrace>): RoutingTrace {
  return {
    id: 't1',
    state: 'stream',
    started_at: now,
    stream: true,
    status: 200,
    bytes: 120,
    deltas: 7,
    method: 'POST',
    path: '/v1/chat/completions',
    model: 'gpt-4o',
    ttfb_ms: 420,
    stages: [],
    activity: { kind: 'reasoning', state: 'stream', bytes: 120, deltas: 7, at: now },
    ...over,
  };
}

const catalog = ['alpha', 'beta'];

const markup = (trace: RoutingTrace | undefined, upstreams = catalog) =>
  renderToStaticMarkup(<>{lineView.render({ trace, traces: trace ? [trace] : [], catalogUpstreams: upstreams })}</>);

// A node is any <rect> carrying the viz-node class family.
const nodeClasses = (m: string) => [...m.matchAll(/class="viz-node([^"]*)"/g)].map((x) => x[1].trim());
const nodeCount = (m: string) => nodeClasses(m).length;
const inState = (m: string, state: string) => nodeClasses(m).filter((c) => c.split(/\s+/).includes(state)).length;
// Every lane, paired with the class list that carries its state. The view always
// renders `d` before `class`, and paths carrying an `id` (the ghost paths the
// light packets ride) are skipped: they only shadow a lane's geometry.
type Leg = { x0: number; y0: number; x1: number; y1: number; cls: string };
const legs = (m: string): Leg[] =>
  [...m.matchAll(/<path d="M (\d+) (\d+) C [^"]* (\d+) (\d+)" class="([^"]*)"/g)].map((x) => ({
    x0: Number(x[1]),
    y0: Number(x[2]),
    x1: Number(x[3]),
    y1: Number(x[4]),
    cls: x[5],
  }));
const base = (ls: Leg[]) => ls.filter((l) => l.cls.startsWith('viz-base'));
const dim = (ls: Leg[]) => ls.filter((l) => l.cls.split(/\s+/).includes('dim'));
const rowsOf = (ls: Leg[], key: 'y0' | 'y1') =>
  [...new Set(ls.map((l) => l[key]))].sort((a, b) => a - b).join(',');
// Lanes leaving the root column (x=290) into the upstream column (x=470).
const rootLegs = (m: string) => base(legs(m).filter((l) => l.x0 === 290 && l.x1 === 470));
// Lanes leaving the upstream column (x=770) into the terminal column (x=1010).
const phaseLegs = (m: string) => base(legs(m).filter((l) => l.x0 === 770 && l.x1 === 1010));
const packets = (m: string) => m.split('<animateMotion').length - 1;
const count = (m: string, needle: string) => m.split(needle).length - 1;

let failures = 0;
function check(name: string, got: unknown, want: unknown) {
  const ok = got === want;
  if (!ok) failures++;
  console.log(`${ok ? 'PASS' : 'FAIL'} ${name}: got ${JSON.stringify(got)} want ${JSON.stringify(want)}`);
}

// --- empty canvas: the whole topology is already present -------------------
const empty = markup(undefined);
check('empty: node count (root + 2 upstreams + 5 terminals)', nodeCount(empty), 1 + 2 + 5);
check('empty: no token saver node', count(empty, '>Token Saver<'), 0);
check('empty: no token saver node class left behind', count(empty, 'viz-node ts'), 0);
check('empty: no credential node', count(empty, '>credential<'), 0);
check('empty: no credential wording anywhere', count(empty, 'key_ref'), 0);
check('empty: root is on the canvas', count(empty, '>Firefly<'), 1);
check('empty: root waits for a request', count(empty, '>waiting for a request...<'), 1);
check('empty: root carries no status word yet', count(empty, 'class="viz-st red"'), 0);
check('empty: one root lane per upstream row', rootLegs(empty).length, 2);
check('empty: root lanes end on the upstream rows', rowsOf(rootLegs(empty), 'y1'), '130,250');
check('empty: root lanes start dim', dim(rootLegs(empty)).length, 2);
check('empty: a phase lane per terminal kind per upstream row', phaseLegs(empty).length, 10);
check('empty: every phase fan hangs off its own upstream row', rowsOf(phaseLegs(empty), 'y0'), '130,250');
check('empty: phase lanes end on the terminal rows', rowsOf(phaseLegs(empty), 'y1'), '130,250,370,490,610');
check('empty: phase lanes start dim', dim(phaseLegs(empty)).length, 10);
check('empty: nothing flows before a request', count(empty, 'viz-flow'), 0);
check('empty: no flow packets', packets(empty), 0);
check('empty: idle nodes (5 terminals + 2 untouched upstreams)', inState(empty, 'idle'), 5 + 2);
for (const label of ['Thinking', 'Tool', 'Writing', 'Usage', 'Error']) {
  check(`empty: terminal node ${label} rendered`, count(empty, `>${label}<`), 1);
}

// --- streaming trace: the winning row carries the model and lights up ------
const live = markup(
  traceOf({
    upstream: 'alpha',
    candidates: [
      { upstream: 'alpha', state: 'chosen' },
      { upstream: 'beta', state: 'skipped', note: '429 cooldown 30s' },
    ],
    phases: ['reasoning', 'tool'],
  }),
);
check('stream: node count unchanged', nodeCount(live), 1 + 2 + 5);
check('stream: the winning root lane lights up', dim(rootLegs(live)).length, 1);
check('stream: every phase fan stays on its own row', rowsOf(phaseLegs(live), 'y0'), '130,250');
check(
  'stream: winner lights up, untouched row stays dim',
  dim(phaseLegs(live)).length,
  8, // winner: 5 - live - settled tool; untouched row: all 5 dim
);
check('stream: traversed phase lane keeps a settled trail', count(live, 'class="viz-flow info settled"'), 1);
check('stream: exactly one live phase lane', count(live, 'id="viz-p1"'), 1);
check('stream: packets ride both active legs (2 x 3)', packets(live), 6);
check('stream: the model is reported on the upstream node', count(live, '>gpt-4o<'), 1);
check('stream: the model is not on the root node', count(live, '>Firefly<'), 1);
check('stream: the root reports the ingress call', count(live, '>POST /v1/chat/completions<'), 1);
check('stream: the winning row reports its ttfb', count(live, '>ttfb 420ms<'), 1);
check('stream: the skipped row reports why', count(live, '>429 cooldown 30s<'), 1);
check('stream: the live phase reports its deltas (node + legend)', count(live, '>7 deltas<'), 2);
check('stream: exactly one live node', inState(live, 'live'), 1);
check('stream: the traversed terminal is marked done', count(live, '>done<'), 2);

// --- a finished trace: everything settles, nothing new appears -------------
const settled = markup(
  traceOf({
    state: 'done',
    upstream: 'alpha',
    candidates: [{ upstream: 'alpha', state: 'chosen' }],
    phases: ['reasoning', 'answer'],
  }),
);
check('done: node count unchanged', nodeCount(settled), 1 + 2 + 5);
check('done: no packets once the stream ended', packets(settled), 0);
check('done: no live node left', inState(settled, 'live'), 0);
check(
  'done: 4 marks from the two done nodes (sub + status) + root status + legend badge',
  count(settled, '>done<'),
  6,
);
check('done: the winning root lane keeps a settled flow', count(settled, 'class="viz-flow settled"'), 1);
check('done: traversed phase lanes keep settled trails', count(settled, 'class="viz-flow info settled"'), 2);
check('done: every phase fan stays on its own row', rowsOf(phaseLegs(settled), 'y0'), '130,250');
check('done: untouched rows and phases stay dim', dim(phaseLegs(settled)).length, 8);

// --- failed request: the root states it, the error phase is traversed ------
const failed = markup(
  traceOf({
    state: 'error',
    error: 'upstream returned 500',
    upstream: 'beta',
    candidates: [
      { upstream: 'alpha', state: 'skipped', note: 'connect timeout' },
      { upstream: 'beta', state: 'chosen' },
    ],
    phases: ['error'],
  }),
);
check('error: root shows the failure in red', count(failed, 'class="viz-st red"'), 1);
check('error: error lane lit on the failing row, fans keep their rows', dim(phaseLegs(failed)).length, 9);
check('error: phase fans stay on their own rows', rowsOf(phaseLegs(failed), 'y0'), '130,250');

// --- token saver: reported in the legend, never as a node ------------------
const saved = markup(
  traceOf({
    candidates: [{ upstream: 'alpha', state: 'chosen' }],
    stages: [{ name: 'tokensaver', at: now, detail: 'guard+compress -8.2kB' }],
    phases: ['answer'],
  }),
);
check('token-saver: no node is drawn for it', count(saved, '>Token Saver<'), 0);
check('token-saver: node count unchanged', nodeCount(saved), 1 + 2 + 5);
check('token-saver: the rewrite fact lands in the legend', count(saved, '>token saver guard+compress -8.2kB<'), 1);

// --- long model names stay inside their box --------------------------------
const longModel = markup(
  traceOf({
    model: 'claude-sonnet-4-5-20250929-thinking-high',
    candidates: [{ upstream: 'alpha', state: 'chosen' }],
    phases: ['answer'],
  }),
);
check('clip: a long model name is clipped with an ellipsis', count(longModel, '>claude-sonnet-4-5-20250929-thin...<'), 1);
check('clip: the un-clipped model name is not on the canvas', count(longModel, '>claude-sonnet-4-5-20250929-thinking-high<'), 0);

// --- larger catalog: only the upstream column grows ------------------------
const big = markup(undefined, ['u1', 'u2', 'u3', 'u4', 'u5', 'u6', 'u7', 'u8']);
check('8 upstreams: node count', nodeCount(big), 1 + 8 + 5);
check('8 upstreams: one root lane per upstream row', rootLegs(big).length, 8);
check('8 upstreams: every root lane dim before a request', dim(rootLegs(big)).length, 8);
check('8 upstreams: a fan per row per kind', phaseLegs(big).length, 8 * 5);
check('8 upstreams: every fan hangs off its own row', rowsOf(phaseLegs(big), 'y0'), '130,250,370,490,610,730,850,970');
check('8 upstreams: no model is reported before a request', count(big, '>gpt-4o<'), 0);

// --- a trace that has not resolved any routing yet -------------------------
const unrouted = markup(traceOf({ upstream: undefined, candidates: [] }));
check('unrouted: node count unchanged', nodeCount(unrouted), 1 + 2 + 5);
check('unrouted: every root lane stays dim', dim(rootLegs(unrouted)).length, 2);
check('unrouted: fans stay on their rows, none lit', rowsOf(phaseLegs(unrouted), 'y0'), '130,250');
check('unrouted: no upstream node carries a model yet', count(unrouted, '>gpt-4o<'), 0);

// --- non-chat endpoint: the same three columns, different call -------------
const embeddings = markup(
  traceOf({
    state: 'done',
    path: '/v1/embeddings',
    model: 'text-embedding-3-large',
    candidates: [{ upstream: 'alpha', state: 'chosen' }],
    phases: ['usage'],
  }),
);
check('embeddings: node count unchanged', nodeCount(embeddings), 1 + 2 + 5);
check('embeddings: root reports the endpoint', count(embeddings, '>POST /v1/embeddings<'), 1);
check('embeddings: the model rides the winning upstream row', count(embeddings, '>text-embedding-3-large<'), 1);
check('embeddings: only the winner usage lane lit, fans unchanged', dim(phaseLegs(embeddings)).length, 9);

// --- Town mapping: staff roster mirrors the catalog -----------------------
check('town: roster follows catalog order', staffOf(['beta', 'alpha'], []).join(','), 'beta,alpha');
check(
  'town: trace-only upstreams append after the catalog',
  staffOf(['beta'], [traceOf({ id: 'x', upstream: 'alpha' })]).join(','),
  'beta,alpha',
);
check('town: empty catalog and no traces means empty office', staffOf([], []).length, 0);
check('town: staff ids are namespaced', staffId('beta'), 'up:beta');
check('town: unstaff recovers the name', unstaff('up:beta'), 'beta');
check('town: unstaff rejects trace ids', unstaff('t19') === null ? 1 : 0, 1);

// --- Town mapping: live trace pins to its upstream --------------------------
const routed = [
  traceOf({ id: 'old', state: 'stream', upstream: 'beta' }),
  traceOf({ id: 'fresh', state: 'request', upstream: 'beta' }),
];
check('town: request outranks stream on the same upstream', liveTraceFor(routed, 'beta')?.id, 'fresh');
check('town: unrouted upstream has no live trace', liveTraceFor(routed, 'alpha') === null ? 1 : 0, 1);
check(
  'town: unrouted traces never pin to the roster',
  liveTraceFor([traceOf({ id: 'u', state: 'request', upstream: undefined })], 'beta') === null ? 1 : 0,
  1,
);
check(
  'town: settled trace feeds the afterglow, not the live pin',
  liveTraceFor([traceOf({ id: 's', state: 'done', upstream: 'beta' })], 'beta') === null ? 1 : 0,
  1,
);
check(
  'town: newest settled trace is the afterglow source',
  lastTraceFor(
    [
      traceOf({ id: 's1', state: 'done', upstream: 'beta' }),
      traceOf({ id: 's2', state: 'done', upstream: 'beta' }),
    ],
    'beta',
  )?.id,
  's2',
);

// --- Town mapping: entries idle, work, then glow ----------------------------
const emptyOffice = staffEntries(['beta'], [], now);
check('town: no work means idle with no bubble', emptyOffice[0]?.status, 'idle');
check('town: idle agents carry no bubble', emptyOffice[0]?.message === null ? 1 : 0, 1);
const workTrace = traceOf({
  id: 'w',
  state: 'stream',
  upstream: 'beta',
  tenant: 'acme',
  activity: { kind: 'answer', state: 'stream', bytes: 10, deltas: 7, at: now },
});
const atWork = staffEntries(['beta'], [workTrace], now);
check('town: live trace puts staff to work', atWork[0]?.status, 'typing');
check('town: work bubble names the tenant, never the upstream', atWork[0]?.message, 'gpt-4o @ acme · 7Δ');
const freshDone = traceOf({ id: 'd', state: 'done', upstream: 'beta', ended_at: now - 1000 });
check('town: settled trace leaves a success afterglow', staffEntries(['beta'], [freshDone], now)[0]?.status, 'success');
const staleDone = traceOf({ id: 'd', state: 'done', upstream: 'beta', ended_at: now - AFTERGLOW_MS - 1000 });
check('town: afterglow expires back to idle', staffEntries(['beta'], [staleDone], now)[0]?.status, 'idle');
check('town: small roster seats a small office', officeSizeFor(4), 'small');
check('town: mid roster seats a medium office', officeSizeFor(10), 'medium');
check('town: large roster seats a large office', officeSizeFor(20), 'large');

// --- Town mapping: trace state -> AgentStatus -------------------------------
check('town: typing while the answer streams', statusOf(traceOf({ state: 'stream', activity: { kind: 'answer', state: 'stream', bytes: 10, deltas: 1, at: now } })), 'typing');
check('town: thinking while reasoning streams', statusOf(traceOf({ state: 'stream', activity: { kind: 'reasoning', state: 'stream', bytes: 10, deltas: 1, at: now } })), 'thinking');
check('town: reading while tools run', statusOf(traceOf({ state: 'stream', activity: { kind: 'tool', state: 'stream', bytes: 0, deltas: 0, at: now } })), 'reading');
check('town: waiting on usage frames', statusOf(traceOf({ state: 'stream', activity: { kind: 'usage', state: 'stream', bytes: 0, deltas: 0, at: now } })), 'waiting');
check('town: thinking while the request is unrouted', statusOf(traceOf({ state: 'request', upstream: undefined, candidates: [] })), 'thinking');
check('town: settled traces rest at success', statusOf(traceOf({ state: 'done' })), 'success');
check('town: error text beats the state', statusOf(traceOf({ state: 'done', error: 'upstream returned 500' })), 'error');
check('town: error state without text is still error', statusOf(traceOf({ state: 'error' })), 'error');

// --- Town mapping: bubbles carry model @ tenant, never a credential -------
const settledTrace = traceOf({
  model: 'gpt-4o',
  upstream: 'beta',
  tenant: 'acme',
  key_ref: 'sk-gw-SECRET',
  state: 'done',
  deltas: 0,
});
check('town: bubble carries model @ tenant', messageOf(settledTrace), 'gpt-4o @ acme');
check('town: idle has no bubble', messageOf(null) === null ? 1 : 0, 1);
check(
  'town: bubble never leaks the credential ref',
  messageOf(settledTrace)?.includes('sk-gw-SECRET') ? 1 : 0,
  0,
);
check(
  'town: error text survives clipping to the bubble',
  (messageOf(traceOf({ error: `x`.repeat(200) }))?.length ?? 0) <= 80 ? 1 : 0,
  1,
);

// --- Auto-fit: content dictates the opening zoom ----------------------------
const box2 = contentSize(2);
check('fit: content box spans the full canvas width', box2.width, 1310);
check('fit: content height grows with the row count', contentSize(8).height > box2.height ? 1 : 0, 1);
check('fit: fit zoom shrinks as rows grow', fitZoom(1200, 560, 1310, contentSize(8).height) < fitZoom(1200, 560, 1310, box2.height) ? 1 : 0, 1);
check('fit: zoom clamps to the panning limits', fitZoom(40, 40, 1310, box2.height), 0.3);
const fitted = fitTransform(1200, 560, 1310, box2.height);
check('fit: the whole box lands inside the viewport', fitted.x >= 0 && fitted.y >= 0 ? 1 : 0, 1);
check('fit: fitted content stays within the viewport', fitted.x + 1310 * fitted.z <= 1200 && fitted.y + box2.height * fitted.z <= 560 ? 1 : 0, 1);
check('fit: zero viewport falls back to 100%', fitZoom(0, 560, 1310, box2.height), 1);
check('fit: office scale grows with the viewport', officeScale('small', 1200, 520) > officeScale('small', 300, 200) ? 1 : 0, 1);
check('fit: tiny viewports bottom out at scale 1', officeScale('small', 40, 40), 1);
check('fit: dpr multiplies device pixels before fitting', officeScale('small', 600, 300, 2) >= officeScale('small', 600, 300, 1) ? 1 : 0, 1);
check('town: default zoom carries the 50% boost', TOWN_ZOOM_BOOST, 1.5);
check('town: user zoom clamps to the camera limits', clampTownZoom(99), TOWN_ZOOM_MAX);
check('town: zoom below the floor clamps up', clampTownZoom(0.01), TOWN_ZOOM_MIN);
check('town: garbage zoom falls back to 100%', clampTownZoom(Number.NaN), 1);

// --- Town mapping: office always matches the roster -------------------------
check('town: registry holds both views', townView.id, 'town');
check('town: office hint names the model', townView.hint.includes('staffer') ? 1 : 0, 1);

if (failures > 0) throw new Error(`${failures} check(s) failed`);
console.log('\nALL CHECKS PASSED');

