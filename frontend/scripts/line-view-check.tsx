/**
 * Headless verification for the Line view topology invariants (run with
 * `npm run check:line-view`). The project ships no test runner, so this bundles
 * the view with esbuild and renders it through `react-dom/server`:
 *
 *   1. the node set is constant — root + one node per catalog upstream + the
 *      credential node + the fixed terminal column — whether or not a trace is
 *      selected, so nothing appears or disappears mid-stream;
 *   2. connectors past the upstream column exist only when the routing resolved
 *      a credential (a detected `key_ref`);
 *   3. every detected `key_ref` converges on the single fixed credential node;
 *   4. a trace only changes node *state* (`live` / `done` / `idle`);
 *   5. the Token Saver node renders `applied` only when the trace carries the
 *      server's `tokensaver` stage (a real body rewrite) and stays `idle` on a
 *      pass-through — the node never appears or disappears.
 *
 * Geometry constants mirrored here come from `views/line.tsx`: X_TS = 320 and
 * X_TS + TS_W = 470 (the Token Saver column the request leg passes through),
 * X_UP + UP_W = 880 (upstream column exit), X_KEY = 1040, PAD = 70, ROW = 120 →
 * for a 5-row canvas the midline is 370 and the terminal rows are
 * 130 / 250 / 370 / 490 / 610.
 */
import { renderToStaticMarkup } from 'react-dom/server';
import { lineView } from '../src/components/visualizer/views/line';
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
// Nodes showing one state token, whatever else their class list carries (the
// Token Saver node is `viz-node ts idle` / `viz-node ts applied`).
const inState = (m: string, state: string) => nodeClasses(m).filter((c) => c.split(/\s+/).includes(state)).length;
// Lane paths leaving the upstream column (x=880) toward the credential column:
// every one of them must terminate on the same fixed node (x=1040, y=midline),
// and only rows whose key_ref was detected get one.
const credLaneSources = (m: string) =>
  [...new Set([...m.matchAll(/d="M 880 (\d+) C [^"]* 1040 370"/g)].map((x) => x[1]))].sort();
const credLanes = (m: string) => credLaneSources(m).length;
// Canvas midline for a given row count, mirroring `views/line.tsx`
// (mid = padding + rows * rowHeight / 2); the row count is
// max(catalog upstreams, terminal kinds).
const midOf = (rows: number) => 70 + (rows * 120) / 2;
// The request leg: Firefly (x=260) into the Token Saver column (x=320). The
// horizontal run is short, so the handle is clamped to 30 instead of CURVE.
const requestLeg = (rows: number) => {
  const m = midOf(rows);
  return `d="M 260 ${m} C 290 ${m}, 290 ${m}, 320 ${m}"`;
};
// Fan-out out of the Token Saver node (x=470) — one lane per upstream.
const tsFan = (rows: number) => `d="M 470 ${midOf(rows)} C`;
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
check(
  'empty: node count (root + token saver + 2 upstreams + credential + 5 terminals)',
  nodeCount(empty),
  1 + 1 + 2 + 1 + 5,
);
check('empty: credential node rendered', count(empty, '>credential<'), 1);
check('empty: token saver node rendered', count(empty, '>Token Saver<'), 1);
check('empty: token saver standby sub-label', count(empty, '>standby<'), 1);
check('empty: no credential lanes', credLanes(empty), 0);
check('empty: no flow packets', packets(empty), 0);
check('empty: one request leg, not animated yet', count(empty, requestLeg(5)), 1);
check('empty: the token saver node fans out to every upstream', count(empty, tsFan(5)), 2);
check(
  'empty: idle nodes (token saver + credential + 5 terminals + 2 untouched upstreams)',
  inState(empty, 'idle'),
  1 + 1 + 5 + 2,
);
for (const label of ['Thinking', 'Tool', 'Writing', 'Usage', 'Error']) {
  check(`empty: terminal node ${label} rendered`, count(empty, `>${label}<`), 1);
}

// --- streaming trace with detected key_refs --------------------------------
const withKey = markup(
  traceOf({
    key_ref: 'sk-gw-chosen',
    upstream: 'alpha',
    candidates: [
      { upstream: 'alpha', key: 'sk-gw-chosen', state: 'chosen' },
      { upstream: 'beta', key: 'sk-gw-other', state: 'skipped', note: '429 cooldown 30s' },
    ],
    phases: ['reasoning', 'tool'],
  }),
);
check('with-key: node count unchanged', nodeCount(withKey), 1 + 1 + 2 + 1 + 5);
check('with-key: both detected key_refs converge on the one fixed node', credLanes(withKey), 2);
check('with-key: credential lane sources', credLaneSources(withKey).join(','), '130,250');
check('with-key: credential label is the chosen ref', count(withKey, '>sk-gw-chosen<'), 1);
check('with-key: animated chosen lane labelled', count(withKey, '>selected<'), 1);
check('with-key: packets ride the active path (4 legs x 3 packets)', packets(withKey), 12);
check('with-key: request leg animated base + flow + ghost', count(withKey, requestLeg(5)), 3);
check('with-key: live terminal marked', count(withKey, '>live<'), 1);
check('with-key: recorded terminal marked done (sub + status)', count(withKey, '>done<'), 2);
check('with-key: exactly one live node', inState(withKey, 'live'), 1);
check(
  'with-key: token saver idle with untouched terminals (no rewrite in this trace)',
  inState(withKey, 'idle'),
  1 + 3,
);
check('with-key: no rewrite reported', count(withKey, 'class="viz-node ts applied"'), 0);

// --- trace with no key_ref anywhere: connectors must not be drawn ----------
const noKey = markup(
  traceOf({
    upstream: 'alpha',
    candidates: [{ upstream: 'alpha', state: 'chosen' }],
    phases: ['answer'],
  }),
);
check('no-key: node count unchanged', nodeCount(noKey), 1 + 1 + 2 + 1 + 5);
check('no-key: no credential lanes', credLanes(noKey), 0);
check('no-key: no selected label', count(noKey, '>selected<'), 0);
check('no-key: no packets', packets(noKey), 0);
check('no-key: credential node still on canvas', count(noKey, '>credential<'), 1);
check('no-key: terminal nodes still on canvas', count(noKey, '>Error<'), 1);
check('no-key: token saver node still on canvas', count(noKey, '>Token Saver<'), 1);
check('no-key: request leg not gated on a credential', count(noKey, requestLeg(5)), 3);
check(
  'no-key: idle nodes (token saver + credential + 3 untouched terminals + untouched upstream)',
  inState(noKey, 'idle'),
  1 + 1 + 3 + 1,
);

// --- larger catalog: only the upstream column grows ------------------------
const big = markup(undefined, ['u1', 'u2', 'u3', 'u4', 'u5', 'u6', 'u7', 'u8']);
check('8 upstreams: node count', nodeCount(big), 1 + 1 + 8 + 1 + 5);
check('8 upstreams: token saver still a single node on the midline', count(big, '>Token Saver<'), 1);
check('8 upstreams: one fan lane per upstream', count(big, tsFan(8)), 8);

// --- token saver reported: the node states what the rewrite did ------------
const saved = markup(
  traceOf({
    key_ref: 'sk-gw-chosen',
    upstream: 'alpha',
    candidates: [{ upstream: 'alpha', key: 'sk-gw-chosen', state: 'chosen' }],
    stages: [{ name: 'tokensaver', at: now, detail: 'guard+compress -8.2kB' }],
    phases: ['answer'],
  }),
);
check('token-saver: node count unchanged', nodeCount(saved), 1 + 1 + 2 + 1 + 5);
check('token-saver: node rendered once', count(saved, '>Token Saver<'), 1);
check('token-saver: sub-label is the stage detail', count(saved, '>guard+compress -8.2kB<'), 1);
check('token-saver: node marked applied', count(saved, 'class="viz-node ts applied"'), 1);
check('token-saver: no idle token saver node left behind', count(saved, 'class="viz-node ts idle"'), 0);
check('token-saver: applied node is not counted as idle', inState(saved, 'idle'), 3 + 1);

// --- chat trace without a rewrite: pass-through stays visible --------------
const plain = markup(traceOf({ phases: ['answer'] }));
check('pass-through: node count unchanged', nodeCount(plain), 1 + 1 + 2 + 1 + 5);
check('pass-through: node reports no change', count(plain, '>no change<'), 1);
check('pass-through: node stays idle', count(plain, 'class="viz-node ts idle"'), 1);

// --- non-chat endpoint: the optimizer never runs there ---------------------
const nonChat = markup(traceOf({ path: '/v1/embeddings', phases: ['usage'] }));
check('non-chat: node count unchanged', nodeCount(nonChat), 1 + 1 + 2 + 1 + 5);
check('non-chat: node states what it applies to', count(nonChat, '>chat only<'), 1);
check('non-chat: node stays idle', count(nonChat, 'class="viz-node ts idle"'), 1);

if (failures > 0) throw new Error(`${failures} check(s) failed`);
console.log('\nALL CHECKS PASSED');
