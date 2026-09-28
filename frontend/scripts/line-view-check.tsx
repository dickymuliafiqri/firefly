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
 *   4. a trace only changes node *state* (`live` / `done` / `idle`).
 *
 * Geometry constants mirrored here come from `views/line.tsx`: X_UP + UP_W = 800
 * (upstream column exit), X_KEY = 1040, PAD = 70, ROW = 120 → for a 5-row canvas
 * the midline is 370 and the terminal rows are 130 / 250 / 370 / 490 / 610.
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
const nodeCount = (m: string) => (m.match(/class="viz-node/g) ?? []).length;
// Lane paths leaving the upstream column (x=800) toward the credential column:
// every one of them must terminate on the same fixed node (x=1040, y=midline),
// and only rows whose key_ref was detected get one.
const credLaneSources = (m: string) =>
  [...new Set([...m.matchAll(/d="M 800 (\d+) C [^"]* 1040 370"/g)].map((x) => x[1]))].sort();
const credLanes = (m: string) => credLaneSources(m).length;
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
check('empty: node count (root + 2 upstreams + credential + 5 terminals)', nodeCount(empty), 1 + 2 + 1 + 5);
check('empty: credential node rendered', count(empty, '>credential<'), 1);
check('empty: no credential lanes', credLanes(empty), 0);
check('empty: no flow packets', packets(empty), 0);
check('empty: idle nodes (credential + 5 terminals + 2 untouched upstreams)', count(empty, 'class="viz-node idle"'), 8);
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
check('with-key: node count unchanged', nodeCount(withKey), 1 + 2 + 1 + 5);
check('with-key: both detected key_refs converge on the one fixed node', credLanes(withKey), 2);
check('with-key: credential lane sources', credLaneSources(withKey).join(','), '130,250');
check('with-key: credential label is the chosen ref', count(withKey, '>sk-gw-chosen<'), 1);
check('with-key: animated chosen lane labelled', count(withKey, '>selected<'), 1);
check('with-key: packets ride the active path (3 paths x 3 packets)', packets(withKey), 9);
check('with-key: live terminal marked', count(withKey, '>live<'), 1);
check('with-key: recorded terminal marked done (sub + status)', count(withKey, '>done<'), 2);
check('with-key: exactly one live node', count(withKey, 'class="viz-node live"'), 1);
check('with-key: untouched terminals idle (tool/usage/error)', count(withKey, 'class="viz-node idle"'), 3);

// --- trace with no key_ref anywhere: connectors must not be drawn ----------
const noKey = markup(
  traceOf({
    upstream: 'alpha',
    candidates: [{ upstream: 'alpha', state: 'chosen' }],
    phases: ['answer'],
  }),
);
check('no-key: node count unchanged', nodeCount(noKey), 1 + 2 + 1 + 5);
check('no-key: no credential lanes', credLanes(noKey), 0);
check('no-key: no selected label', count(noKey, '>selected<'), 0);
check('no-key: no packets', packets(noKey), 0);
check('no-key: credential node still on canvas', count(noKey, '>credential<'), 1);
check('no-key: terminal nodes still on canvas', count(noKey, '>Error<'), 1);
check(
  'no-key: idle nodes (credential + 3 untouched terminals + untouched upstream)',
  count(noKey, 'class="viz-node idle"'),
  5,
);

// --- larger catalog: only the upstream column grows ------------------------
const big = markup(undefined, ['u1', 'u2', 'u3', 'u4', 'u5', 'u6', 'u7', 'u8']);
check('8 upstreams: node count', nodeCount(big), 1 + 8 + 1 + 5);

if (failures > 0) throw new Error(`${failures} check(s) failed`);
console.log('\nALL CHECKS PASSED');
