# Landing hero pinned-scene Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Pin the landing hero's night-forest scene so page content rises from behind the nearest
trees, grow the scene 1.0 → 1.35 as the reader scrolls, and hand the base over to a soil-brown
band at the footer.

**Architecture:** One clamped scroll offset — `o(s) = min(s + G·clamp(s/W), Omax)` — applied to
scene elements by `forest.js`, plus a per-layer scale composed into the same transform. The band
splits into paint groups: `--night`/`--soil` behind the content (z 1) and `--front-night`/
`--front-soil` in front of it (z 3). The pin releases because `<main>` ends before the footer.
No CSS `position: sticky` is used: CSS cannot express the settle ramp, and the JS offset keeps
layout untouched (transforms do not affect document height).

**Tech Stack:** Static HTML/CSS/vanilla JS in `landing/` (no build step, no dependencies),
`landing/tools/gen-forest.mjs` for the SVG art, headless Edge + `playwright-core` for verification.

**Spec:** `docs/superpowers/specs/2026-09-23-hero-scene-design.md`

## Global Constraints

- **Landing only.** Every edit lands in `landing/`. `frontend/` is the application and is out of
  scope — never read or write it.
- **Never touch the real gateway on :8080.** The landing is served as static files on
  **http://127.0.0.1:8094**.
- **Do not commit.** `landing/` is untracked (`git status --short -- landing` → `?? landing/`), so
  there is nothing to stage without the owner's decision. End every task with a report of changed
  files and evidence — never run `git commit`, `git add`, or `git stash`.
- **No new runtime dependencies and no build step.** The page stays static; `defer` scripts only.
- **Verification harness lives outside the repo** at
  `C:/Users/Dicky Mulia Fiqri/AppData/Local/Temp/firefly-demo/`, which has `node_modules` with
  `playwright-core` and `serve-landing.mjs`. Screenshots go to that folder's `verify/`. Run every
  probe command from that directory.
- **Reference geometry, 1920×1080, measured 2026-09-23 by the Task 1 probe** (all checks below
  compare against these unless a step overrides them): document height 4819; hero band top 573.5,
  bottom 899.9, height 326.4; `G = vh − bandDocBottom = 180.1`; hero stats top 953.9; max scroll
  3739; `W = 2.5·G = 450.3`. `<main>`'s box bottom is **3818.8**, so `s_release = mainBottom − vh`
  **≈ 2739**. The 3927 an earlier draft used is the *footer's* box top — the 108px between the two
  is the footer's own `margin-top`, and the footer is not part of `<main>`, so it never sets the
  release.
- **The baseline already overshoots horizontally.** `documentElement.scrollWidth` is 1946 against
  `clientWidth` 1920 (26px, from `.orn-grove { right: -26px }`; 16px at 390px wide). It predates
  this work, so never assert `scrollW === clientWidth`. Nor may you assert that a programmatic
  horizontal scroll leaves `scrollX` at 0: `body { overflow-x: hidden }` blocks *user* panning but
  not `window.scrollTo`, which lands on the 26px overhang. (Clipping the *viewport* is not a fix —
  measured: `body { overflow-x: clip }` changes neither `scrollWidth` nor the pan, because Blink
  does not propagate `clip` to the viewport.) The invariant is that the overhang does not **grow**:
  the `panX` helper's result must stay `<= scrollW - clientW` as measured at scroll 0 in the same
  run. Task 4 adds `.hero { overflow-x: clip }` (see its Step 3), which clips the *elements'*
  growth bloom and thereby removes the pre-existing overhang too; from then on the dynamically-read
  allowance is 0.
- **Exact constants:** `o(s) = min(s + G·clamp(s/W, 0, 1), Omax)`, `Omax = mainBottom − bandDocBottom`,
  `s_release = max(W, Omax − G)`; growth caps mist 1.18, far 1.25, glow 1.30, moon 1.35,
  canopies 1.35, near 1.35, floor 1.15, grove 1.35, fern 1.30; band-layer guard
  `max(1, min(cap, 0.5·vh / bandHeight))`; soil ramp over the last `0.6·vh` before `s_release`;
  scene fade over `0.3·vh` after it.
- **Line numbers after Task 2 are stale.** Task 2 inserts ~12 lines into the band, so every
  `index.html` / `style.css` line reference in Tasks 3–6 is pre-edit: each step quotes the exact
  text to replace — locate the block by its content, never by the number.
- **Parked states.** `prefers-reduced-motion: reduce` moves nothing (the existing early return in
  `forest.js:79-82` governs it). ≤960px runs no scene module at all: no settle, no pin, no growth,
  no crossfade.
- **No AI slop.** No per-element reveal animations, no opacity/scale fade-ins on scroll. The only
  opacity work is the soil crossfade and the scene fade-out specified above.
- **Every measurement is read per frame.** The band's document bottom and `<main>`'s end both move
  when fonts land, the feature tabs open, or the window resizes; nothing about the scene geometry
  may be cached across frames.

---

### Task 1: Probe harness and the scroll-0 baseline

Verification first: without this harness none of the later tasks can be checked, and the current
page is the baseline that Tasks 2–3 must not disturb.

**Files:**
- Create: `C:/Users/Dicky Mulia Fiqri/AppData/Local/Temp/firefly-demo/scene-probe.mjs`
- Output (not committed): `verify/scene-base-*.png`, `verify/scene-base.json`

**Interfaces:**
- Consumes: nothing.
- Produces: `node scene-probe.mjs [--scroll N] [--out PATH] [--check NAME] [--width N] [--height N] [--motion reduce]`.
  Prints one JSON object to stdout; with `--check` it also prints `PASS`/`FAIL` lines and exits 1 on
  any failure. Metrics keys every later task relies on: `scrollY`, `docH`, `vh`, `band.{top,bottom,height}`,
  `groundScreenY`, `bandDocBottom`, `mainBottomDoc`, `stats`, `footer`, `overflow.scrollW/clientW`,
  `layers.<name>.{sx,ty,op}`, `paints.{night,soil,frontNight,frontSoil}`.

- [ ] **Step 1: Confirm the static server is up**

```bash
cd "C:/Users/Dicky Mulia Fiqri/AppData/Local/Temp/firefly-demo" && \
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8094/index.html
```

Expected: `200`. If it is not, start it and re-check:

```bash
cd "C:/Users/Dicky Mulia Fiqri/AppData/Local/Temp/firefly-demo" && node serve-landing.mjs > serve.log 2>&1 &
```

- [ ] **Step 2: Write the probe**

Create `scene-probe.mjs` with exactly this content:

```js
// scene-probe.mjs — measures the landing scene and asserts the pinned choreography.
// Run from C:/Users/Dicky Mulia Fiqri/AppData/Local/Temp/firefly-demo
import { chromium } from 'playwright-core';

const EDGE = 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe';
const URL = 'http://127.0.0.1:8094/index.html';
const argv = process.argv.slice(2);
const opt = (n, d) => {
  const i = argv.indexOf('--' + n);
  if (i === -1) return d;
  const v = argv[i + 1];
  return v && !v.startsWith('--') ? v : true;
};

const OPT = {
  scroll: opt('scroll', null),
  out: opt('out', null),
  check: opt('check', null),
  width: Number(opt('width', 1920)),
  height: Number(opt('height', 1080)),
  motion: opt('motion', 'no-preference'),
};

/* Geometry notes for every metric below:
 *  - `.hero .forest` is never transformed by the engine, so its rect bottom is the
 *    band's document bottom (bandRect.bottom + scrollY).
 *  - the ground line is the bottom edge of every band layer, so the mist image's
 *    rect bottom IS the ground line's screen position.
 *  - a scene transform is translate3d(x, y, 0) [scale(k)]: matrix .f is y, .a is scaleX. */
const METRICS = `(() => {
  const r = (el) => { const b = el.getBoundingClientRect();
    return { top: +b.top.toFixed(1), bottom: +b.bottom.toFixed(1), height: +b.height.toFixed(1) }; };
  const read = (sel) => { const el = document.querySelector(sel); if (!el) return null;
    const cs = getComputedStyle(el); const t = cs.transform;
    const d = t === 'none' ? null : new DOMMatrix(t);
    return { sx: d ? +d.a.toFixed(4) : 1, ty: d ? +d.f.toFixed(2) : 0, op: +cs.opacity }; };
  const op = (sel) => { const el = document.querySelector(sel);
    return el ? +getComputedStyle(el).opacity : null; };
  const rect = (sel) => { const el = document.querySelector(sel); return el ? r(el) : null; };
  const y = window.pageYOffset;
  return {
    scrollY: Math.round(y),
    docH: document.documentElement.scrollHeight,
    vh: window.innerHeight,
    band: rect('.hero .forest'),
    groundScreenY: (() => { const el = document.querySelector('.hero .forest .fl-mist');
      return el ? +el.getBoundingClientRect().bottom.toFixed(1) : null; })(),
    bandDocBottom: (() => { const el = document.querySelector('.hero .forest');
      return el ? +(el.getBoundingClientRect().bottom + y).toFixed(1) : null; })(),
    mainBottomDoc: (() => { const el = document.querySelector('main');
      return el ? +(el.getBoundingClientRect().bottom + y).toFixed(1) : null; })(),
    stats: rect('.hero-stats'),
    footer: rect('.footer'),
    overflow: { scrollW: document.documentElement.scrollWidth,
                clientW: document.documentElement.clientWidth },
    layers: {
      /* One selector per layer, valid before the paint groups exist and after:
         document order puts each night twin first inside the hero band, so the
         soil twins need keys of their own. */
      nightMist: read('.hero .forest .fl-mist'),
      nightNear: read('.hero .forest .fl-near'),
      nightFloor: read('.hero .forest .fl-floor'),
      glow: read('.clearing-glow'),
      soilMist: read('.forest-paint--soil .fl-mist'),
      soilNear: read('.forest-paint--front-soil .fl-near'),
      moon: read('.orn-moon'),
      canopyL: read('.orn-canopy--l'),
      trail: read('.trail--hero'),
      fireflies: read('.ff-orns--hero'),
      footerNear: read('.forest--footer .fl-near')
    },
    paints: {
      night: op('.forest-paint--night'),
      soil: op('.forest-paint--soil'),
      frontNight: op('.forest-paint--front-night'),
      frontSoil: op('.forest-paint--front-soil')
    }
  };
})()`;

const CHECKS = {};

async function main() {
  const browser = await chromium.launch({ executablePath: EDGE, args: ['--headless=new'] });
  const page = await browser.newPage({
    viewport: { width: OPT.width, height: OPT.height },
    reducedMotion: OPT.motion === 'reduce' ? 'reduce' : 'no-preference',
  });
  const errors = [];
  page.on('pageerror', (e) => errors.push(String(e)));
  page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
  await page.goto(URL, { waitUntil: 'networkidle' });

  /* html { scroll-behavior: smooth } would make every scrollTo animate, so the
   * probe takes the engine's scroll instantaneously before measuring. */
  const at = async (y) => {
    await page.evaluate((v) => {
      document.documentElement.style.scrollBehavior = 'auto';
      window.scrollTo(0, v);
    }, y);
    await page.waitForTimeout(80);
    return page.evaluate(METRICS);
  };

  const settleImages = () => page.evaluate(() => Promise.all(
    Array.prototype.slice.call(document.images)
      .filter((i) => !i.complete && i.getBoundingClientRect().top < innerHeight + 200
                                    && i.getBoundingClientRect().bottom > -200)
      .map((i) => new Promise((r) => { i.onload = i.onerror = r; setTimeout(r, 1500); }))));

  let failures = 0;
  if (OPT.check) {
    const fn = CHECKS[OPT.check];
    if (!fn) { console.error('unknown check: ' + OPT.check); process.exit(2); }
    const res = await fn({ page, at, OPT });
    for (const line of res.lines) {
      if (!line.pass) failures++;
      console.log(`${line.pass ? 'PASS' : 'FAIL'}  ${line.what}${line.detail ? '  — ' + line.detail : ''}`);
    }
  } else {
    const m = await at(OPT.scroll === null ? 0 : Number(OPT.scroll));
    console.log(JSON.stringify(m, null, 1));
    if (OPT.out) { await settleImages(); await page.screenshot({ path: OPT.out }); }
  }

  console.log('console errors: ' + (errors.length ? errors.join(' | ') : 'none'));
  if (errors.length) failures++;
  await browser.close();
  process.exit(failures ? 1 : 0);
}

main();
```

- [ ] **Step 3: Capture the baseline**

```bash
cd "C:/Users/Dicky Mulia Fiqri/AppData/Local/Temp/firefly-demo" && \
node scene-probe.mjs --scroll 0    --out verify/scene-base-0.png    > verify/scene-base.json && \
node scene-probe.mjs --scroll 1000 --out verify/scene-base-1000.png >> verify/scene-base.json && \
node scene-probe.mjs --scroll 3739 --out verify/scene-base-end.png  >> verify/scene-base.json && \
node scene-probe.mjs --scroll 0 --width 390 --height 844 --out verify/scene-base-390.png
```

Expected: three JSON blobs in `verify/scene-base.json` plus a phone screenshot. Confirm the first
blob reads `"band": {"top": 573.5, "bottom": 899.9, "height": 326.4}`, `"bandDocBottom": 899.9`,
`"groundScreenY": 900.1`, `"mainBottomDoc": 3818.8`, `"stats": {"top": 953.9`, `"docH": 4819`,
`"overflow": {"scrollW": 1946, "clientW": 1920}` and `console errors: none`. The 0.2px between
`groundScreenY` and the band bottom is sub-pixel rounding; the 26px of horizontal overshoot is the
pre-existing `.orn-grove` overhang and is unpannable (`body { overflow-x: hidden }`).

Record the scroll-0 `layers.*.ty` values in the report: the old engine gives each layer a small
per-section drift of its own, and after Task 3 they all read 0. That shift is the one permitted
delta at scroll 0 (spec, Beat 0) and the reason the screenshots are compared with an eye rather
than a diff.

- [ ] **Step 4: Report**

Report the baseline numbers, the four screenshot paths, and that `landing/` is untouched. Do not commit.

---

### Task 2: Paint groups and the front stack in front of the content

The band's layers have to sit in two stacks — behind the text and in front of it — so the content
can travel between them. This task is purely structural: nothing on screen may move.

**Files:**
- Modify: `landing/index.html:66-74` (the hero band block)
- Modify: `landing/assets/css/style.css:295` (the will-change rule), `:312-316` (`.forest`), `:333-342` (`.clearing-glow`)
- Modify: `C:/Users/Dicky Mulia Fiqri/AppData/Local/Temp/firefly-demo/scene-probe.mjs` (add check `stack`)

**Interfaces:**
- Consumes: the probe from Task 1.
- Produces: the paint-group classes `.forest-paint--night`, `.forest-paint--soil`,
  `.forest-paint--front-night`, `.forest-paint--front-soil` (all `position: absolute; inset: 0`;
  the night/soil pair `z-index: 1`, the front pair `z-index: 3`), and the attributes
  `data-scene-mouse="<px>"` and `data-scene-grow="<cap>"` on scene layers (used from Task 3 on).
  `.forest` loses its `z-index` so its children can compete in the page stacking context.

- [ ] **Step 1: Add the failing check to the probe**

Append this after the `const CHECKS = {};` line in `scene-probe.mjs`. It parks the stats row over
the band with a synthetic transform (standing in for the pinned scene that does not exist yet) and
hit-tests a point that both share.

```js
CHECKS.stack = async ({ page, at }) => {
  const lines = [];
  const base = await at(0);
  lines.push({ what: 'band geometry unchanged', pass: Math.abs(base.band.top - 573) < 1.5 && Math.abs(base.band.bottom - 899) < 1.5,
    detail: JSON.stringify(base.band) });

  const hit = await page.evaluate(() => {
    const band = document.querySelector('.hero .forest');
    const stats = document.querySelector('.hero-stats');
    stats.style.transform = 'translateY(-420px)';        // park the stats row over the band
    const br = band.getBoundingClientRect();
    const sr = stats.getBoundingClientRect();
    const overlap = Math.min(br.bottom, sr.bottom) - Math.max(br.top, sr.top);
    const x = Math.round((Math.max(br.left, sr.left) + Math.min(br.right, sr.right)) / 2);
    const y = Math.round((Math.max(br.top, sr.top) + Math.min(br.bottom, sr.bottom)) / 2);
    const front = document.querySelector('.forest-paint--front-night');
    const forced = [];
    document.querySelectorAll('.forest-paint--front-night, .forest-paint--front-night *').forEach((el) => {
      forced.push([el, el.style.pointerEvents]);
      el.style.pointerEvents = 'auto';
    });
    const el = document.elementFromPoint(x, y);
    forced.forEach(([n, v]) => { n.style.pointerEvents = v; });
    stats.style.transform = '';
    return { overlap: +overlap.toFixed(1), inFront: !!(el && front && front.contains(el)),
             tag: el ? el.tagName + '.' + (el.className || '') : null,
             bandZ: getComputedStyle(band).zIndex,
             frontZ: front ? getComputedStyle(front).zIndex : null,
             frontPE: front ? getComputedStyle(front).pointerEvents : null };
  });
  lines.push({ what: 'front stack paints above the stats row', pass: hit.overlap > 20 && hit.inFront,
    detail: 'overlap ' + hit.overlap + 'px, hit ' + hit.tag });
  lines.push({ what: 'the band creates no stacking context', pass: hit.bandZ === 'auto', detail: hit.bandZ });
  lines.push({ what: 'front stack z-index is 3', pass: hit.frontZ === '3', detail: String(hit.frontZ) });
  lines.push({ what: 'front stack keeps pointer-events: none', pass: hit.frontPE === 'none', detail: String(hit.frontPE) });
  return { lines };
};
```

- [ ] **Step 2: Run it to verify it fails**

```bash
cd "C:/Users/Dicky Mulia Fiqri/AppData/Local/Temp/firefly-demo" && node scene-probe.mjs --check stack
```

Expected: `FAIL  the band creates no stacking context` (`1`), `FAIL  front stack z-index is 3`
(`null`) and `FAIL  front stack paints above the stats row` (the hit lands on the stats text).
Exit code 1.

- [ ] **Step 3: Group the band's layers in `index.html`**

Replace the band block at `landing/index.html:66-74` with:

```html
      <!-- night-forest band: four layers rooted on one ground line, plus the
           foreground grove and fern that grow out of it. The layers are split
           into two paint groups: the mist and the far treeline stay behind the
           page content, the near trees, the grass and the undergrowth stand in
           front of it, so content rises out of the forest. Each group is also
           the crossfade unit for the soil palette. -->
      <div class="forest" aria-hidden="true">
        <div class="forest-paint forest-paint--night">
          <img class="fl fl-mist" src="assets/img/orn-mist-far.svg" alt="" width="2560" height="512" data-scene-mouse="5" data-scene-grow="1.18">
          <img class="fl fl-far" src="assets/img/orn-pines-far.svg" alt="" width="2560" height="512" data-scene-mouse="8" data-scene-grow="1.25">
        </div>
        <div class="forest-paint forest-paint--front-night">
          <img class="fl fl-near" src="assets/img/orn-pines-near.svg" alt="" width="2560" height="512" data-scene-mouse="12" data-scene-grow="1.35">
          <img class="fl fl-floor" src="assets/img/orn-floor.svg" alt="" width="2560" height="512" data-scene-mouse="15" data-scene-grow="1.15">
          <div class="clearing-glow" data-scene-grow="1.30"></div>
          <img class="orn-grove" src="assets/img/orn-grove-right.svg" alt="" width="900" height="1200" data-scene-mouse="16" data-scene-grow="1.35">
          <img class="orn-fern" src="assets/img/orn-fern-left.svg" alt="" width="620" height="470" data-scene-mouse="20" data-scene-grow="1.30">
        </div>
      </div>
```

What changed, and what must not change: the four band images lost `data-parallax="…"` and gained
`data-scene-mouse` (the same value they carried as the old mouse amplitude: 5, 8, 12, 15) plus
`data-scene-grow`. The grove and fern keep their old mouse amplitudes 16 and 20. The order inside
the front group (near, floor, glow, grove, fern) is the old paint order — the glow painted over the
near trees and the grass and must keep doing so.

- [ ] **Step 4: Add the paint rules, drop the band's stacking context, re-centre the glow**

Replace `style.css:312-316`:

```css
.forest {
  position: relative;
  z-index: 1;
  height: clamp(140px, 17vw, 700px);
}
```

with:

```css
/* The band itself must NOT create a stacking context (no z-index here), or its
 * children could never rise above .wrap. */
.forest {
  position: relative;
  height: clamp(140px, 17vw, 700px);
}

.forest-paint { position: absolute; inset: 0; }
.forest-paint--night,
.forest-paint--soil { z-index: 1; }
.forest-paint--front-night,
.forest-paint--front-soil { z-index: 3; }
```

Extend the will-change rule at `style.css:295`:

```css
[data-parallax],
[data-scene],
[data-scene-grow] { will-change: transform; }
```

And change the glow's centring at `style.css:333-342` — the engine writes a single transform to
every `[data-scene-grow]`, which would wipe the `translateX(-50%)` and throw the glow half a
viewport to the right. Same box, expressed without a transform:

```css
.clearing-glow {
  position: absolute;
  left: 4vw;
  bottom: 0;
  width: 92vw;
  height: 190px;
  background: radial-gradient(50% 100% at 50% 100%, rgba(190, 242, 100, 0.07), transparent 62%);
  filter: blur(18px);
}
```

- [ ] **Step 5: Run the check to verify it passes**

```bash
cd "C:/Users/Dicky Mulia Fiqri/AppData/Local/Temp/firefly-demo" && node scene-probe.mjs --check stack
```

Expected: five `PASS` lines and `console errors: none`, exit code 0.

- [ ] **Step 6: Prove nothing moved**

```bash
cd "C:/Users/Dicky Mulia Fiqri/AppData/Local/Temp/firefly-demo" && \
node scene-probe.mjs --scroll 0 --out verify/scene-t2-0.png
```

Expected: `"band": {"top": 573.5, ..., "bottom": 899.9, "height": 326.4}`, `"groundScreenY": 900.1`,
`"mainBottomDoc": 3818.8`, `"stats": {"top": 953.9`, `"docH": 4819`, `"overflow": {"scrollW": 1946,`
`"clientW": 1920}` (the pre-existing grove overhang), console clean.
Compare `verify/scene-t2-0.png` against `verify/scene-base-0.png` by eye — the only permitted
difference is the small layer drift recorded in the baseline.

- [ ] **Step 7: Report**

Report the changed files, the check output, and the screenshot paths. Do not commit.

---

### Task 3: The scene engine — settle and pin

**Files:**
- Modify: `landing/assets/js/forest.js` (insert the scene module after line 113, call it from `frame`)
- Modify: `landing/index.html:86-103` (moon, canopies, hero trail, hero fireflies)
- Modify: `C:/Users/Dicky Mulia Fiqri/AppData/Local/Temp/firefly-demo/scene-probe.mjs` (add checks `settle`, `pin`)

**Interfaces:**
- Consumes: `data-scene`, `data-scene-mouse`, `data-scene-grow`; `.hero .forest`; `.forest-paint--*`.
- Produces: `applyScene(s, bandRect, mainRect)` in `forest.js` and the module-level
  `sceneEls`, `band`, `mainEl`, `small`, `sceneReady`. Task 4 adds `L.cap` and the scale, Task 5
  adds `paints`, `skyEls`, `lifeEls` and the ramps inside the same function.

- [ ] **Step 1: Add the two failing checks to the probe**

```js
const groundAt = (m) => m.groundScreenY;

/* body { overflow-x: hidden } propagates to the viewport, so scrollWidth can exceed
 * clientWidth (the grove's right: -26px) while the page stays unpannable. The honest
 * test is whether a horizontal scroll attempt moves anything. */
const panX = (page, y) => page.evaluate((v) => {
  document.documentElement.style.scrollBehavior = 'auto';
  window.scrollTo(60, v);
  var x = window.scrollX;
  window.scrollTo(0, v);
  return x;
}, y);

CHECKS.settle = async ({ page, at }) => {
  const lines = [];
  const a = await at(0);
  const G = Math.max(0, a.vh - a.bandDocBottom);   // the ground line's distance above the base
  const W = Math.max(1, 2.5 * G);                  // the settle window
  const half = await at(Math.round(W / 2));
  const c = await at(Math.round(W));
  lines.push({ what: 'scroll 0 ground line is where the flow puts it', pass: Math.abs(groundAt(a) - (a.bandDocBottom - a.scrollY)) < 1.5, detail: String(groundAt(a)) });
  lines.push({ what: 'scroll 0 leaves the scene untransformed', pass: Math.abs(a.layers.nightMist.ty) < 1.5, detail: String(a.layers.nightMist.ty) });
  lines.push({ what: 'half way: ground line has descended most of G', pass: G < 5 || Math.abs(groundAt(half) - (a.vh - G / 2)) < 4,
    detail: groundAt(half) + ' vs ' + (a.vh - G / 2).toFixed(1) + ' (G=' + G.toFixed(0) + ')' });
  lines.push({ what: 'end of settle: ground line on the viewport bottom', pass: G < 5 || Math.abs(groundAt(c) - c.vh) < 2, detail: groundAt(c) + ' vs ' + c.vh });
  const speed = G < 5 ? 0 : (groundAt(c) - groundAt(half)) / (W - W / 2);
  lines.push({ what: 'the lag is G/W of the scroll (content rises 2.5x faster)', pass: G < 5 || (speed > 0.25 && speed < 0.85), detail: 'dy/ds=' + speed.toFixed(3) });
  lines.push({ what: 'document height unchanged', pass: c.docH === a.docH, detail: String(c.docH) });
  return { lines };
};

CHECKS.pin = async ({ page, at }) => {
  const lines = [];
  const z = await at(0);
  const G = Math.max(0, z.vh - z.bandDocBottom);
  const W = Math.max(1, 2.5 * G);
  const stop = await page.evaluate(() => Math.round(
    document.querySelector('main').getBoundingClientRect().bottom + window.pageYOffset - window.innerHeight));
  const p1 = await at(Math.round(W + 0.3 * (stop - W)));
  const p2 = await at(Math.round(W + 0.7 * (stop - W)));
  const rel = await at(stop + 300);        // past the release: the scene climbs with the page
  const pan = await panX(page, 3739);
  const slack = z.overflow.scrollW - z.overflow.clientW;   // the pre-existing 26px grove overhang
  lines.push({ what: 'pinned: ground line holds at the viewport bottom', pass: Math.abs(groundAt(p1) - p1.vh) < 2 && Math.abs(groundAt(p2) - p2.vh) < 2,
    detail: groundAt(p1) + ' / ' + groundAt(p2) + ' in ' + p1.vh });
  lines.push({ what: 'pinned: the band does not drift with the scroll', pass: Math.abs((p2.layers.nightMist.ty - p1.layers.nightMist.ty) - (p2.scrollY - p1.scrollY)) < 3,
    detail: (p2.layers.nightMist.ty - p1.layers.nightMist.ty).toFixed(1) });
  lines.push({ what: 'released: ground line climbs with the page', pass: Math.abs(groundAt(rel) - (rel.mainBottomDoc - rel.scrollY)) < 3,
    detail: groundAt(rel) + ' vs ' + (rel.mainBottomDoc - rel.scrollY) });
  lines.push({ what: 'no new horizontal overflow at the end', pass: pan <= slack + 1,
    detail: 'scrollX ' + pan + ' vs pre-existing ' + slack });
  return { lines };
};
```

- [ ] **Step 2: Run them to verify they fail**

```bash
cd "C:/Users/Dicky Mulia Fiqri/AppData/Local/Temp/firefly-demo" && \
node scene-probe.mjs --check settle ; node scene-probe.mjs --check pin
```

Expected: `settle` fails the half-way and end-of-settle lines (today the ground line still scrolls
away with the page: at s = 452 it sits at 447, not 1080). `pin` fails both pinned lines and the
released line.

- [ ] **Step 3: Mark the remaining scene elements in `index.html`**

Replace `landing/index.html:86-103` so the moon, both canopies, the trail and the firefly cluster
belong to the scene (their `data-parallax="sAmp,mAmp"` becomes `data-scene-mouse="mAmp"`; the trail
and the firefly cluster get a plain `data-scene`):

```html
      <!-- sky: moon, hanging boughs, a firefly trail -->
      <div class="orn orn-moon" data-scene-grow="1.35" data-scene-mouse="5" aria-hidden="true"></div>
      <div class="orn orn-canopy orn-canopy--l" data-scene-grow="1.35" data-scene-mouse="12" aria-hidden="true">
        <img src="assets/img/orn-canopy-left.svg" alt="" width="620" height="380">
      </div>
      <div class="orn orn-canopy orn-canopy--r" data-scene-grow="1.35" data-scene-mouse="12" aria-hidden="true">
        <img src="assets/img/orn-canopy-right.svg" alt="" width="620" height="380">
      </div>
      <svg class="trail trail--hero" viewBox="0 0 520 340" data-trail data-scene aria-hidden="true">
        <path d="M18 300 C 120 190, 210 268, 300 150 C 350 92, 420 96, 498 44"/>
      </svg>
      <div class="ff-orns ff-orns--hero" data-scene data-scene-mouse="10" aria-hidden="true">
```

Keep the six `<i>` firefly children exactly as they are. The moon, canopies and firefly cluster
keep the pointer drift they had (5, 12, 10); the trail has no mouse amplitude, as today.

- [ ] **Step 4: Add the scene module to `forest.js`**

Insert directly after line 113 (`var vw = window.innerWidth, vh = window.innerHeight;`) — not
earlier: `applyScene` is called with `vh` inside the same frame, so the declaration must already
have run.

```js
  /* ---------------- pinned scene ----------------
   * Scene elements leave the scroll flow: one clamped offset holds them on screen
   * while the content rises past them.
   *   G      — the ground line's distance above the viewport bottom
   *   W      — settle window; the scene catches down to the base over 2.5·G of scroll
   *   Omax   — the hard stop: the ground line may not pass the end of <main>
   *   o(s)   = min(s + G·clamp(s / W, 0, 1), Omax)   settle, then pin 1:1 with the scroll
   * Every value is read from the live layout each frame: the band's document bottom
   * moves when fonts land, <main>'s end moves when the feature tabs open, and the
   * viewport height moves on resize. Nothing here is cached across frames. */

  var sceneEls = [];
  Array.prototype.forEach.call(document.querySelectorAll('[data-scene], [data-scene-grow]'), function (el) {
    sceneEls.push({
      el: el,
      mAmp: parseFloat(el.getAttribute('data-scene-mouse')) || 0
    });
  });

  var band = document.querySelector('.hero .forest');
  var mainEl = document.querySelector('main');
  var small = window.matchMedia('(max-width: 960px)');
  var sceneReady = !!(band && mainEl && sceneEls.length);

  function applyScene(s, bandRect, mainRect) {
    var on = sceneReady && !small.matches;
    var i;
    if (!on) {
      /* Parked (≤960px): the scene stays in the flow with only its pointer drift —
         the per-layer scroll offsets are gone by design (spec, Beat 0). */
      for (i = 0; i < sceneEls.length; i++) {
        var P = sceneEls[i];
        P.el.style.transform = 'translate3d(' + (mx * P.mAmp).toFixed(2) + 'px,' + (my * P.mAmp * 0.35).toFixed(2) + 'px,0)';
      }
      return;
    }
    var bandDocBottom = bandRect.bottom + s;
    var G = Math.max(0, vh - bandDocBottom);
    var W = Math.max(1, 2.5 * G);
    var Omax = Math.max(0, mainRect.bottom + s - bandDocBottom);
    var o = Math.min(s + G * clamp(s / W, 0, 1), Omax);
    for (i = 0; i < sceneEls.length; i++) {
      var L = sceneEls[i];
      var x = mx * L.mAmp;
      var y = o + my * L.mAmp * 0.35;
      L.el.style.transform = 'translate3d(' + x.toFixed(2) + 'px,' + y.toFixed(2) + 'px,0)';
    }
  }
```

`g` (the growth ramp) is unused until Task 4 — do not add it yet; the linters here are the probe
and the console.

Then wire it into the frame loop. Replace `var t = now / 1000;` (line 128) with:

```js
    var t = now / 1000;
    var s = window.pageYOffset;
```

and insert the call after the trails' `visible` loop (after line 140), before the layer-write loop
— every rect read in the frame stays batched ahead of the first write:

```js
    if (sceneReady) applyScene(s, band.getBoundingClientRect(), mainEl.getBoundingClientRect());
```

- [ ] **Step 5: Run the checks to verify they pass**

```bash
cd "C:/Users/Dicky Mulia Fiqri/AppData/Local/Temp/firefly-demo" && \
node scene-probe.mjs --check settle && node scene-probe.mjs --check pin && \
node scene-probe.mjs --scroll 0 --out verify/scene-t3-0.png && \
node scene-probe.mjs --scroll 226 --out verify/scene-t3-226.png && \
node scene-probe.mjs --scroll 1000 --out verify/scene-t3-1000.png
```

Expected: both checks all `PASS`, exit 0. Scroll-0 metrics still read band 573.5→899.9, `docH` 4819,
and now every scene transform is identity (`ty: 0`) — the small drift the baseline recorded is gone
by design. `scene-t3-226.png` shows the ground line part way down the fold with the stats row
emerging over it; `scene-t3-1000.png` shows the treeline pinned on the bottom edge with page
content over it.

- [ ] **Step 6: Report**

Report the changed files, both check outputs, and the screenshots. Do not commit.

---

### Task 4: Growth

**Files:**
- Modify: `landing/assets/js/forest.js` (add the scale to `applyScene`, add `cap` to the collection)
- Modify: `landing/assets/css/style.css` (transform origins next to the paint rules; the `.hero`
  growth clip)
- Modify: `C:/Users/Dicky Mulia Fiqri/AppData/Local/Temp/firefly-demo/scene-probe.mjs` (add checks `growth`, `guard`)

**Interfaces:**
- Consumes: `bandRect.height`, `vh`, `data-scene-grow` caps, `band.contains(el)`.
- Produces: `L.cap` per scene element; the transform string becomes
  `translate3d(x, y, 0) scale(k)` for elements with a cap.

- [ ] **Step 1: Add the failing checks**

```js
const releaseAt = (page) => page.evaluate(() => {
  const main = document.querySelector('main');
  return Math.round(main.getBoundingClientRect().bottom + window.pageYOffset - window.innerHeight);
});

CHECKS.growth = async ({ page, at }) => {
  const lines = [];
  const z = await at(0);
  const G = Math.max(0, z.vh - z.bandDocBottom);
  const W = Math.max(1, 2.5 * G);
  const start = await at(Math.round(W));   // end of the settle
  const mid = await at(1600);              // half way through <main>
  const stop = await releaseAt(page);
  const near = await at(stop);
  lines.push({ what: 'no growth at the end of the settle', pass: Math.abs(start.layers.nightMist.sx - 1) < 0.01 && Math.abs(start.layers.moon.sx - 1) < 0.01,
    detail: start.layers.nightMist.sx + ' / ' + start.layers.moon.sx });
  lines.push({ what: 'growth is part way through the middle', pass: mid.layers.nightNear.sx > 1.02 && mid.layers.nightNear.sx < 1.34,
    detail: String(mid.layers.nightNear.sx) });
  lines.push({ what: 'mist reaches 1.18 by the release', pass: Math.abs(near.layers.nightMist.sx - 1.18) < 0.02, detail: String(near.layers.nightMist.sx) });
  lines.push({ what: 'the near treeline reaches 1.35 by the release', pass: Math.abs(near.layers.nightNear.sx - 1.35) < 0.02, detail: String(near.layers.nightNear.sx) });
  lines.push({ what: 'glow reaches 1.30 by the release', pass: Math.abs(near.layers.glow.sx - 1.30) < 0.02, detail: String(near.layers.glow.sx) });
  lines.push({ what: 'floor reaches 1.15 by the release', pass: Math.abs(near.layers.nightFloor.sx - 1.15) < 0.02, detail: String(near.layers.nightFloor.sx) });
  lines.push({ what: 'moon reaches 1.35 by the release', pass: Math.abs(near.layers.moon.sx - 1.35) < 0.02, detail: String(near.layers.moon.sx) });
  lines.push({ what: 'ground line does not move while growing',
    pass: G > 5 ? Math.abs(near.groundScreenY - near.vh) < 2 : Math.abs(near.groundScreenY - near.bandDocBottom) < 2,
    detail: near.groundScreenY + ' vs ' + (G > 5 ? near.vh : near.bandDocBottom) + ' (G=' + G.toFixed(0) + ')' });
  const origins = await page.evaluate(() => {
    const o = (sel) => { const el = document.querySelector(sel);
      const t = getComputedStyle(el).transformOrigin.split(' ');
      return { x: parseFloat(t[0]), y: parseFloat(t[1]), w: el.offsetWidth, h: el.offsetHeight }; };
    return { moon: o('.orn-moon'), near: o('.hero .forest .fl-near'), canopy: o('.orn-canopy') };
  });
  lines.push({ what: 'moon grows from its own top-right corner', pass: Math.abs(origins.moon.x - origins.moon.w) < 1 && origins.moon.y < 1,
    detail: origins.moon.x + ',' + origins.moon.y + ' in ' + origins.moon.w + 'x' + origins.moon.h });
  lines.push({ what: 'band layers grow from the ground line', pass: Math.abs(origins.near.y - origins.near.h) < 1 && Math.abs(origins.near.x - origins.near.w / 2) < 1,
    detail: origins.near.x + ',' + origins.near.y + ' in ' + origins.near.w + 'x' + origins.near.h });
  lines.push({ what: 'canopies hang from their tops', pass: origins.canopy.y < 1, detail: String(origins.canopy.y) });
  return { lines };
};

CHECKS.guard = async ({ page, at }) => {
  const lines = [];
  const stop = await releaseAt(page);
  const end = await at(stop);
  const bandH = end.band.height;
  const bandCap = Math.max(1, (0.5 * end.vh) / bandH);
  const near = end.layers.nightNear.sx;
  lines.push({ what: 'growth stops at min(cap, half the viewport)', pass: Math.abs(near - Math.min(1.35, bandCap)) < 0.02,
    detail: 'sx ' + near + ' with cap ' + bandCap.toFixed(3) + ' (band ' + bandH + 'px of ' + end.vh + ')' });
  lines.push({ what: 'the guard never shrinks the forest', pass: bandH * near <= Math.max(bandH, end.vh * 0.5) + 1,
    detail: (bandH * near).toFixed(0) + 'px vs ' + Math.max(bandH, end.vh * 0.5).toFixed(0) + 'px' });
  return { lines };
};
```

The shear guard — the night and soil twins of the near treeline scaling together — lives in
Task 5's `soil` check, because the soil twin does not exist until then.

- [ ] **Step 2: Run them to verify they fail**

```bash
cd "C:/Users/Dicky Mulia Fiqri/AppData/Local/Temp/firefly-demo" && \
node scene-probe.mjs --check growth ; node scene-probe.mjs --check guard
```

Expected: `growth` fails the middle and every cap line (all `sx` still read 1) plus the two origin
lines (`50% 50%`); `guard` fails its cap line (`sx` 1 against a cap of 1.656 at 1080p).

- [ ] **Step 3: Add the transform origins in `style.css`**

Immediately after the `.forest-paint--front-soil { z-index: 3; }` rule:

```css
/* Growth anchors: the ground line is the fixed point for everything rooted in it;
 * the moon grows away from its own top-right corner and the boughs hang from theirs. */
[data-scene-grow] { transform-origin: bottom center; }
.orn-moon[data-scene-grow] { transform-origin: 100% 0; }
.orn-canopy[data-scene-grow] { transform-origin: top center; }
```

Then the growth clip, as its own rule next to `.hero`'s other declarations (or immediately after
the block above — `.hero` is not in the paint group, so put it where a reader looking at `.hero`
will find it, with this comment):

```css
/* Growth must not widen the page. Scaling a full-width layer 1.35x blooms 336px past each
 * side, and Blink counts that in the document's scrollable overflow: measured 2026-09-23,
 * the document reaches scrollWidth 2256 against a 1920 viewport and a programmatic scroll
 * moves 336px sideways. `overflow-x: clip` on the x axis alone removes it and leaves the
 * y axis visible, so nothing the reader can see is cut and the pinned scene is not clipped
 * by the hero's box the way `overflow: hidden` would clip it (the hero box scrolls away
 * while the scene is pinned). With the rule: scrollWidth 1920, pan attempt 0px, pin still
 * holds at s_release, nav still sticky. Safari < 16 ignores `clip`; there the bloom returns
 * but stays invisible behind `body { overflow-x: hidden }`. */
.hero { overflow-x: clip; }
```

This also removes the pre-existing 26px grove overhang (`.orn-grove { right: -26px }` is inside
the hero), so `scrollWidth` becomes exactly `clientWidth` and the checks' dynamically-read `slack`
becomes 0 — `pan <= slack + 1` stays meaningful and now guards against any new bloom.

- [ ] **Step 4: Compose the scale in `forest.js`**

Add the cap to the collection loop:

```js
    sceneEls.push({
      el: el,
      mAmp: parseFloat(el.getAttribute('data-scene-mouse')) || 0,
      cap: parseFloat(el.getAttribute('data-scene-grow')) || 0
    });
```

and replace the write loop inside `applyScene` with a version that computes the ramp once per
frame:

```js
    var o = Math.min(s + G * clamp(s / W, 0, 1), Omax);
    var release = Math.max(W, Omax - G);
    var g = clamp((s - W) / Math.max(1, release - W), 0, 1);
    /* The band may never cover more than half the viewport, which binds on short
       and ultrawide windows where 17vw is already a large share of the height.
       The guard caps growth; it never shrinks the forest under the reader. */
    var bandCap = Math.max(1, (0.5 * vh) / (bandRect.height || 1));
    for (i = 0; i < sceneEls.length; i++) {
      var L = sceneEls[i];
      var x = mx * L.mAmp;
      var y = o + my * L.mAmp * 0.35;
      var t = 'translate3d(' + x.toFixed(2) + 'px,' + y.toFixed(2) + 'px,0)';
      if (L.cap) {
        var cap = band.contains(L.el) ? Math.min(L.cap, bandCap) : L.cap;
        t += ' scale(' + (1 + (cap - 1) * g).toFixed(4) + ')';
      }
      L.el.style.transform = t;
    }
```

The moon and the canopies live in `.hero`, not in the band, so `band.contains` is false for them:
they take their full cap and never eat into the band's footprint.

- [ ] **Step 5: Run the checks to verify they pass**

```bash
cd "C:/Users/Dicky Mulia Fiqri/AppData/Local/Temp/firefly-demo" && \
node scene-probe.mjs --check growth && node scene-probe.mjs --check guard && \
node scene-probe.mjs --check settle && node scene-probe.mjs --check pin && \
node scene-probe.mjs --width 1920 --height 800 --check guard && \
node scene-probe.mjs --scroll 1600 --out verify/scene-t4-1600.png && \
node scene-probe.mjs --scroll 2739 --out verify/scene-t4-2739.png
```

Expected: all four checks `PASS` at 1920×1080; `guard` also passes at 1920×800, where the cap
engages (`bandCap` 1.227, so `near` reads 1.227 instead of 1.35). In `scene-t4-1600.png` the forest
is visibly larger than in `scene-t3-1000.png` with its base still on the bottom edge; 2739 is the
reference viewport's release (`mainBottom − vh`), where growth has topped out and the ground line
is still exactly on the bottom edge. `pin`'s horizontal line must now read
`scrollX 0 vs pre-existing 0`: the clip removed the grove's 26px overhang, so any non-zero reading
means the clip is missing or something else is blooming past the hero's box.

- [ ] **Step 6: Report**

Report changed files, check output, screenshots. Do not commit.

---

### Task 5: Soil palette, crossfade, and the footer band

**Files:**
- Modify: `landing/tools/gen-forest.mjs` (palette map + two write passes)
- Create: `landing/assets/img/orn-mist-far-soil.svg`, `orn-pines-far-soil.svg`, `orn-pines-near-soil.svg`,
  `orn-floor-soil.svg`, `orn-grove-right-soil.svg`, `orn-fern-left-soil.svg` (generated)
- Modify: `landing/index.html` (soil paint groups; footer band sources)
- Modify: `landing/assets/css/style.css` (soil rest opacity, footer background)
- Modify: `landing/assets/js/forest.js` (soil + fade ramps, parked reset)
- Modify: `C:/Users/Dicky Mulia Fiqri/AppData/Local/Temp/firefly-demo/scene-probe.mjs` (add check `soil`)

**Interfaces:**
- Consumes: every attribute and class from Tasks 2–4.
- Produces: `data-scene-dims="soil"` on the moon and both canopies; the `.forest-paint--soil` /
  `.forest-paint--front-soil` opacity ramps; the `-soil.svg` art naming.

- [ ] **Step 1: Add the failing check**

```js
CHECKS.soil = async ({ page, at }) => {
  const lines = [];
  const z = await at(0);
  const stop = await releaseAt(page);              // 2739 at the reference viewport
  const maxScroll = z.docH - z.vh;
  const goTo = (x) => at(Math.max(0, Math.min(Math.round(x), maxScroll)));
  const before = await goTo(stop - 1.4 * z.vh);    // the ramp starts at stop - 0.6vh
  const mid = await goTo(stop - 0.3 * z.vh);       // half way through the ramp
  const rel = await goTo(stop);                    // the release
  const after = await goTo(stop + 0.9 * z.vh);     // past the 0.3vh fade
  lines.push({ what: 'night palette still full before the ramp', pass: before.paints.night > 0.95 && before.paints.soil < 0.05,
    detail: before.paints.night + ' / ' + before.paints.soil });
  lines.push({ what: 'crossfade is half way through the ramp', pass: mid.paints.soil > 0.2 && mid.paints.soil < 0.8,
    detail: String(mid.paints.soil) });
  lines.push({ what: 'night stack is gone by the release', pass: rel.paints.night < 0.05, detail: String(rel.paints.night) });
  lines.push({ what: 'soil stack is fully in at the release', pass: rel.paints.soil > 0.95, detail: String(rel.paints.soil) });
  lines.push({ what: 'moon has withdrawn by the release', pass: rel.layers.moon.op < 0.05, detail: String(rel.layers.moon.op) });
  lines.push({ what: 'night and soil twins scale together', pass: Math.abs(rel.layers.nightNear.sx - rel.layers.soilNear.sx) < 0.02,
    detail: rel.layers.nightNear.sx + ' / ' + rel.layers.soilNear.sx });
  lines.push({ what: 'bright fireflies stay lit over the soil', pass: rel.layers.trail.op > 0.95 && rel.layers.fireflies.op > 0.95,
    detail: rel.layers.trail.op + ' / ' + rel.layers.fireflies.op });
  lines.push({ what: 'scene has faded out after the release', pass: after.paints.soil < 0.05 && after.layers.trail.op < 0.05 && after.layers.fireflies.op < 0.05,
    detail: after.paints.soil + ' / ' + after.layers.trail.op });
  /* Ruling 21: the behind group alone is not the crossfade. Without this line a front soil
     group running the *night* ramp (opaque at rest) passes every other line here. */
  lines.push({ what: 'front soil group runs the same ramp', pass: before.paints.frontSoil < 0.05 && rel.paints.frontSoil > 0.95 && after.paints.frontSoil < 0.05,
    detail: before.paints.frontSoil + ' / ' + rel.paints.frontSoil + ' / ' + after.paints.frontSoil });
  const footerSrc = await page.evaluate(() => Array.prototype.map.call(
    document.querySelectorAll('.forest--footer img.fl'), (el) => el.getAttribute('src').split('/').pop()));
  lines.push({ what: 'footer band renders the soil art', pass: footerSrc.length === 4 && footerSrc.every((s) => /-soil\.svg$/.test(s)),
    detail: footerSrc.join(' ') });
  return { lines };
};
```

Every scroll point is derived from the release, not written down: the release moves with the
viewport (the band is `17vw` tall and `<main>`'s end moves with the window), and a literal such as
2847 would land 108px into the fade, where `night` reads 0.667 and the firefly line legitimately
fails.

- [ ] **Step 2: Run it to verify it fails**

```bash
cd "C:/Users/Dicky Mulia Fiqri/AppData/Local/Temp/firefly-demo" && node scene-probe.mjs --check soil
```

Expected: the night/soil opacity lines fail (`paints.soil` is `null` — the soil groups do not exist
yet), the moon line fails (opacity 1), and the footer line fails (night art). Exit code 1. The run
then aborts on `rel.layers.soilNear.sx` (a null dereference — the element does not exist yet), which
is fine: a check whose subject is missing has no lines to print past that point. The dereference
stays verbatim rather than gaining a null guard, because once the soil groups are in the DOM the
null state cannot recur.

- [ ] **Step 3: Parameterise the palette in `gen-forest.mjs`**

Replace the `const C = { … }` block (lines 221-233) with a palette map plus the working copy the
scene builders already close over:

```js
const PALETTES = {
  night: {
    mist: '#0d2118', far: '#0a1c16', near: '#071410', floor: '#040c09', shrub: '#061109',
    /* near-foreground silhouettes: dark enough to read over the pine band,
       light enough to read over the sky ink */
    fore: '#05100c', trunk: '#030a08',
    /* boughs hanging over the sky catch a little ambient light */
    moonlit: '#0a1a14',
    glow: '#d8f79a', shroom: '#102a1c', frond2: '#040b08', frond3: '#030a07',
  },
  /* earth: the same shapes, rooted in soil. Warm enough to read as ground,
   * still dark enough for a night page, neutral to the lime fireflies. */
  soil: {
    mist: '#3a2517', far: '#2e1c10', near: '#241509', floor: '#1a0f06', shrub: '#1f1208',
    fore: '#180e06', trunk: '#0c0603', moonlit: '#3a2517',
    glow: '#d8f79a', shroom: '#3f2a18', frond2: '#150c05', frond3: '#100803',
  },
};
const C = Object.assign({}, PALETTES.night);
/* the scenes the soil pass writes: the six that appear in the hero band */
const SOIL_SCENES = ['orn-mist-far.svg', 'orn-pines-far.svg', 'orn-pines-near.svg',
                     'orn-floor.svg', 'orn-grove-right.svg', 'orn-fern-left.svg'];
```

Then move the colours the soil pass must vary out of the scene bodies and into the palette:

- line 145, inside `mushrooms()`: `fill="#d8f79a"` → `fill="${C.glow}"`;
- line 285, the floor's caps: `fill="#102a1c"` → `fill="${C.shroom}"`;
- lines 357-358, the fern's second and third frond sets: `fill="#040b08"` → `fill="${C.frond2}"`
  and `fill="#030a07"` → `fill="${C.frond3}"`.

Finally replace the write loop (lines 365-374) with two passes:

```js
mkdirSync(outDir, { recursive: true });
let total = 0;
for (const pass of ['night', 'soil']) {
  Object.assign(C, PALETTES[pass]);
  const names = pass === 'night' ? Object.keys(scenes) : SOIL_SCENES;
  for (const name of names) {
    const out = pass === 'soil' ? name.replace(/\.svg$/, '-soil.svg') : name;
    const file = resolve(outDir, out);
    writeFileSync(file, scenes[name](), 'utf8');
    const kb = statSync(file).size / 1024;
    total += kb;
    console.log(`${out.padEnd(28)} ${kb.toFixed(1)} KB`);
  }
}
console.log(`${'total'.padEnd(28)} ${total.toFixed(1)} KB`);
```

- [ ] **Step 4: Regenerate the art and confirm the night files are unchanged**

```bash
cd "C:/Users/Dicky Mulia Fiqri/go/src/github.com/dickymuliafiqri/firefly/landing" && \
md5sum assets/img/orn-*.svg > /tmp/orn-before.txt && \
node tools/gen-forest.mjs && \
md5sum assets/img/orn-*.svg > /tmp/orn-after.txt && \
diff <(grep -v -- "-soil" /tmp/orn-before.txt) <(grep -v -- "-soil" /tmp/orn-after.txt) && \
echo "night art byte-identical" && ls assets/img/*-soil.svg
```

Expected: `night art byte-identical` and the six `-soil.svg` files listed. (`outDir` is already
`landing/assets/img/` — line 15 of the script.)

- [ ] **Step 5: Add the soil twins to `index.html` and re-skin the footer band**

Inside `.forest`, after the `--front-night` group, add two more groups. Order matters: within a
z-level the later group paints above, so each soil group sits above its night twin; the front pair
is always above the behind pair by z-index.

```html
        <div class="forest-paint forest-paint--soil">
          <img class="fl fl-mist" src="assets/img/orn-mist-far-soil.svg" alt="" width="2560" height="512" data-scene-mouse="5" data-scene-grow="1.18">
          <img class="fl fl-far" src="assets/img/orn-pines-far-soil.svg" alt="" width="2560" height="512" data-scene-mouse="8" data-scene-grow="1.25">
        </div>
        <div class="forest-paint forest-paint--front-soil">
          <img class="fl fl-near" src="assets/img/orn-pines-near-soil.svg" alt="" width="2560" height="512" data-scene-mouse="12" data-scene-grow="1.35">
          <img class="fl fl-floor" src="assets/img/orn-floor-soil.svg" alt="" width="2560" height="512" data-scene-mouse="15" data-scene-grow="1.15">
          <img class="orn-grove" src="assets/img/orn-grove-right-soil.svg" alt="" width="900" height="1200" data-scene-mouse="16" data-scene-grow="1.35">
          <img class="orn-fern" src="assets/img/orn-fern-left-soil.svg" alt="" width="620" height="470" data-scene-mouse="20" data-scene-grow="1.30">
        </div>
```

No `loading="lazy"` on these: they are the hero's own art and must be painted the moment the
crossfade reaches them. The twins carry the same `data-scene-mouse` / `data-scene-grow` as their
night counterparts so both palettes stay pixel-aligned; they carry no `data-scene` so they are not
part of the fade-out set (the paint groups handle that). The `.clearing-glow` deliberately has no
soil twin: it is the night's own ground light and fades out with the night group.

Mark the sky furniture: add `data-scene-dims="soil"` to the moon and both canopy wrappers
(three lines, the attributes join the ones Task 3 added).

Swap the footer band's four sources (lines 546-549) to the soil art, leaving its `data-parallax`
drift and `loading="lazy"` untouched. Task 2 cost the footer band its own stacking context (the
shared `.forest` rule), so its layers now paint below the footer's own `.trail--footer` and
`.ff-orns--footer` (z 1) instead of above them. At the reference viewport the two do not overlap
(the band's top is 565.7px into the 892px footer; the trail and the dots live in the top ~400px),
so nothing visible changed — but this task rewrites that band, so keep the containment: the footer
is itself a stacking context (`.footer { z-index: 2 }`) and its `.wrap` text (z 2) must stay above
the band, which it does.

```html
      <img class="fl fl-mist" src="assets/img/orn-mist-far-soil.svg" alt="" width="2560" height="512" data-parallax="22,5" loading="lazy">
      <img class="fl fl-far" src="assets/img/orn-pines-far-soil.svg" alt="" width="2560" height="512" data-parallax="34,8" loading="lazy">
      <img class="fl fl-near" src="assets/img/orn-pines-near-soil.svg" alt="" width="2560" height="512" data-parallax="48,12" loading="lazy">
      <img class="fl fl-floor" src="assets/img/orn-floor-soil.svg" alt="" width="2560" height="512" data-parallax="62,15" loading="lazy">
```

- [ ] **Step 6: Style the soil and the footer in `style.css`**

Next to the paint rules, add the rest opacity for the soil groups:

```css
.forest-paint--soil,
.forest-paint--front-soil { opacity: 0; }
```

and re-skin the footer (`style.css:1469-1475`) — the night sky stops here:

```css
.footer {
  position: relative;
  z-index: 2;
  margin-top: clamp(72px, 10vh, 120px);
  border-top: 1px solid rgba(120, 84, 55, 0.22);
  background: linear-gradient(180deg, rgba(18, 11, 7, 0.55), #1a0f06);
}
```

Finally, in the same file's `prefers-reduced-motion` block near the end (the `[data-parallax] {
will-change: auto; }` line), extend that selector to the attributes Task 2 introduced:

```css
  [data-parallax],
  [data-scene],
  [data-scene-grow] { will-change: auto; }
```

Under reduced motion `forest.js` returns before the scene module, so nothing writes a transform to
those elements and the compositor hint is dead weight — the reset Task 2 left behind only covered
the retired `data-parallax`. This is the last task that opens this file.

- [ ] **Step 7: Add the ramps in `forest.js`**

After the `sceneEls` collection, add the three sets the ramps drive:

```js
  var paints = [];
  Array.prototype.forEach.call(document.querySelectorAll(
    '.forest-paint--night, .forest-paint--soil, .forest-paint--front-night, .forest-paint--front-soil'
  ), function (el) {
    /* Ruling 20: test the bare 'soil', not '--soil'. `forest-paint--front-soil`'s double
       dash sits before `front`, so the group name ends `-soil` with a single dash and the
       '--soil' test reads false — which drove the front group with the *night* ramp
       (fully opaque at rest, inverted crossfade, invisible at the release). */
    paints.push({ el: el, soil: el.className.indexOf('soil') !== -1 });
  });
  var skyEls = Array.prototype.slice.call(document.querySelectorAll('[data-scene-dims="soil"]'));
  var lifeEls = Array.prototype.slice.call(document.querySelectorAll('[data-scene]'));
```

In `applyScene`, extend the parked reset (a window resize across the 960px gate must not leave the
soil palette painted on a band that is no longer pinned) …

```js
    if (!on) {
      /* Parked (≤960px): the pointer drift is all the scene keeps, and no soil
         ramp may survive a resize across the gate. */
      for (i = 0; i < sceneEls.length; i++) {
        var P = sceneEls[i];
        P.el.style.transform = 'translate3d(' + (mx * P.mAmp).toFixed(2) + 'px,' + (my * P.mAmp * 0.35).toFixed(2) + 'px,0)';
        P.el.style.opacity = '';
      }
      for (i = 0; i < paints.length; i++) paints[i].el.style.opacity = '';
      return;
    }
```

… and append the ramps at the end of the `on` branch, after the transform loop:

```js
    /* Beat 3: the palette turns to earth over the last 60vh of <main>, the sky
       withdraws with it, and the whole scene is absorbed over the first 30vh of
       the release so it cannot double up with the footer's own soil band. */
    var soil = clamp((s - (release - 0.6 * vh)) / (0.6 * vh), 0, 1);
    var fade = 1 - clamp((s - release) / (0.3 * vh), 0, 1);
    for (i = 0; i < paints.length; i++) {
      paints[i].el.style.opacity = ((paints[i].soil ? soil : 1 - soil) * fade).toFixed(3);
    }
    for (i = 0; i < skyEls.length; i++) skyEls[i].style.opacity = ((1 - soil) * fade).toFixed(3);
    for (i = 0; i < lifeEls.length; i++) lifeEls[i].style.opacity = fade.toFixed(3);
```

- [ ] **Step 8: Run the checks to verify they pass**

```bash
cd "C:/Users/Dicky Mulia Fiqri/AppData/Local/Temp/firefly-demo" && \
for c in soil settle pin growth guard; do echo "== $c"; node scene-probe.mjs --check $c; done && \
node scene-probe.mjs --scroll 2465 --out verify/scene-t5-2465.png && \
node scene-probe.mjs --scroll 2739 --out verify/scene-t5-2739.png && \
node scene-probe.mjs --scroll 3063 --out verify/scene-t5-3063.png && \
node scene-probe.mjs --scroll 3739 --out verify/scene-t5-end.png
```

Expected: every check `PASS`. The literals above are the reference viewport's ramp arithmetic
(`release − 0.3·vh`, the release, the fade's end, the document end); the checks themselves derive
their points live. In `scene-t5-2465.png` the ground is half way to earth while the treeline still
reads night; `scene-t5-2739.png` is fully earth with the fireflies still lit; in
`scene-t5-3063.png` no hovering band; `scene-t5-end.png` shows the footer's soil band alone on the
base with the footer copy over earth, not sky.

- [ ] **Step 9: Report**

Report changed and generated files, all check outputs, and the screenshots. Do not commit.

---

### Task 6: Parked states and edge geometry

**Files:**
- Modify: `landing/assets/js/forest.js` (the parked-reset invariant comment; otherwise verify-only
  unless a check fails), and `landing/assets/css/style.css` verify-only
- Modify: `C:/Users/Dicky Mulia Fiqri/AppData/Local/Temp/firefly-demo/scene-probe.mjs` (add checks
  `parked`, `flip`, `base`; add the `G = 0` line to `settle`; add the ramp-length lines to `soil`)

**Interfaces:**
- Consumes: everything above.
- Produces: nothing new — this task proves the gates hold.

- [ ] **Step 1: Add the checks**

```js
CHECKS.parked = async ({ page, at }) => {
  const lines = [];
  const a = await at(0);
  const b = await at(1600);
  const c = await at(3739);
  const all = [a, b, c];
  lines.push({ what: 'no scene transform at any scroll', pass: all.every((m) => Math.abs(m.layers.nightMist.ty) < 1.5 && Math.abs(m.layers.nightNear.sx - 1) < 0.01),
    detail: all.map((m) => m.layers.nightMist.ty).join(' / ') });
  lines.push({ what: 'soil stack stays hidden', pass: all.every((m) => m.paints.soil < 0.05),
    detail: all.map((m) => m.paints.soil).join(' / ') });
  const pans = [];
  for (const y of [0, 1600, 3739]) pans.push(await panX(page, y));
  const slack = a.overflow.scrollW - a.overflow.clientW;   // 0 since Task 4 clipped the grove's overhang
  lines.push({ what: 'no new horizontal overflow at any scroll', pass: pans.every((x) => x <= slack + 1),
    detail: pans.join(' / ') + ' vs pre-existing ' + slack });
  return { lines };
};

/* Only meaningful at a wide viewport with motion allowed: run it at the default size.
 * The crossing point is the fade's half-way mark (`stop + 0.15·vh`, fade 0.5): there the `on`
 * branch writes night paints 0, soil paints 0.5, sky 0 and life 0.5 (forest.js:196-202), each
 * distinct from the rest state `resets` asserts (1 / 0 / 1 / 1) — so any set the parked branch
 * forgets to clear fails a clause. A crossing placed anywhere else can go vacuous: at scroll
 * 1600 — this check's first draft — the ramp has not started (soil 0, night 1, moon 1) and a
 * reset that never fired passes everything; at the ramp's mid-point (`stop − 0.3·vh`, soil
 * 0.5, fade 1) the two life clauses read 1 either way, since the fade has not begun. */
CHECKS.flip = async ({ page, at, OPT }) => {
  const lines = [];
  const stop = await releaseAt(page);
  const mid = Math.round(stop + 0.15 * OPT.height);   // soil 1, fade 0.5
  const resets = (m) => m.paints.night > 0.95 && m.paints.soil < 0.05 && m.paints.frontSoil < 0.05 &&
    m.layers.moon.op > 0.95 && m.layers.trail.op > 0.95 && m.layers.fireflies.op > 0.95;
  const wide = await at(mid);
  const pinned = Math.abs(wide.layers.nightMist.ty) > 40 && Math.abs(wide.layers.nightNear.sx - 1) > 0.05;
  await page.setViewportSize({ width: 900, height: 800 });
  const parked = await at(mid);
  await page.setViewportSize({ width: OPT.width, height: OPT.height });
  const back = await at(mid);
  lines.push({ what: 'crossing the breakpoint parks and resets the scene',
    pass: pinned && Math.abs(parked.layers.nightMist.ty) < 1.5 && Math.abs(parked.layers.nightNear.sx - 1) < 0.01 && resets(parked),
    detail: 'ty ' + wide.layers.nightMist.ty + ' → ' + parked.layers.nightMist.ty + ', soil ' + parked.paints.soil + ' / front ' + parked.paints.frontSoil });
  lines.push({ what: 'crossing back resumes the scene',
    pass: Math.abs(back.layers.nightMist.ty - wide.layers.nightMist.ty) < 2 && Math.abs(back.layers.nightNear.sx - wide.layers.nightNear.sx) < 0.01
      && Math.abs(back.paints.soil - wide.paints.soil) < 0.02,
    detail: 'ty ' + back.layers.nightMist.ty + ' vs ' + wide.layers.nightMist.ty + ', soil ' + back.paints.soil + ' vs ' + wide.paints.soil });
  return { lines };
};

CHECKS.base = async ({ page, at }) => {
  const lines = [];
  const m = await at(0);
  const off = await page.evaluate(() => {
    const out = [];
    Array.prototype.forEach.call(document.querySelectorAll('[data-scene], [data-scene-grow]'), (el) => {
      const t = getComputedStyle(el).transform;
      if (t === 'none') return;
      const d = new DOMMatrix(t);
      if (Math.abs(d.e) > 0.5 || Math.abs(d.f) > 0.5) out.push(el.className + ' ' + d.e.toFixed(1) + ',' + d.f.toFixed(1));
    });
    return out;
  });
  const n = await page.evaluate(() => document.querySelectorAll('[data-scene], [data-scene-grow]').length);
  lines.push({ what: 'every scene element is untransformed at scroll 0', pass: m.scrollY === 0 && n > 10 && !off.length,
    detail: n + ' elements, ' + (off.length ? off.join(' | ') : 'all identity') });
  return { lines };
};
```

One amendment rides along from the Task 3 review; two more were folded in by the Task 5 review
(Ruling 22), which found the same shape of blind spot that Ruling 21 closed in `soil` itself — a
check asserting a value that is already true at the point it samples:

- **`settle`'s degenerate branch gets an assertion instead of a skip.** Every settle line reads
  `pass: G < 5 || …` because at a viewport whose band is taller than the fold (2560×600) there is
  nothing to settle. That skip is the only coverage of the `G = 0` case today, and a skip is not
  evidence. Add one line that pins the behaviour down. With `G ≈ 0`, `o(s) = min(s, Omax)`: the pin
  engages from the first pixel, so the ground line's **screen** position holds while the page
  scrolls under it. Measured 2026-09-23 at 2560×600: `groundScreenY` 903.3 → 903.3 across
  `s = 0 → 40 → 400`, with `mist.ty` reading 0 → 40 → 400 — the transform cancels the scroll
  exactly, which is what "rides the scroll 1:1" means for a screen coordinate. It is *not* the
  ground line travelling 40px up the screen; that would be `o = 0`, i.e. no pin at all, and it
  would take the whole Beat-3 crossfade off-screen at this aspect. Insert it as `settle`'s last
  line before `return { lines };`:

  ```js
  const d = await at(40);
  lines.push({ what: 'G = 0: the scene pins from the first pixel', pass: G >= 2 || Math.abs(groundAt(a) - groundAt(d)) < 2,
    detail: 'ground line ' + groundAt(a) + ' → ' + groundAt(d) });
  ```

  The line's own gate is `G >= 2`, not the `G >= 5` its neighbours use. The ground line's screen
  position moves down by exactly `min(G, 16)` px over the first 40 of scroll: the ramp completes
  at `s = W = 2.5·G` when `G ≤ 16`, contributing `G`, and saturates beyond that at
  `40·(1 + 1/2.5) − 40 = 16`. Measured 2026-09-23: `diff` 3.0 / 4.4 at 1280×722 / 1280×724 (both
  `= G`), 16 at 1100×900 and 1152×780 (both saturated). `|diff| < 2` therefore only says
  something where `G < 2` — the tolerance's own break-even — while a `G >= 5` gate also admits
  `G ∈ [2, 5)`, where a *correct* page fails: reproduced at 1280×722 and 1280×724.

- **`base` asserts the scroll-0 identity on every scene element, not just the mist plate.**
  `settle` proves the load-bearing "scroll 0 is unchanged" promise by reading one layer's `ty`;
  the other eleven elements are only implied by the shared `o`. One stale transform on any of them
  would slip through. `base` runs at the default 1920×1080 viewport, where the scene is live.

- **`soil` gets the ramp and fade lengths.** `soil` presently asserts the crossfade's *values* at
  four sampled scrolls, but nothing pins the two constants that decide where those samples land:
  the `mid` band (`0.2 < soil < 0.8`) admits any ramp length from ≈0.37·vh to ≈1.5·vh, and the
  fade's length is bounded only by `after` requiring completion by `stop + 0.9·vh`. Change the
  0.6 to a 0.4 and every line still passes. Append to `CHECKS.soil`, after the `front soil group
  runs the same ramp` line:

  ```js
  /* Ruling 22: the two constants the samples above merely assume. At −0.7·vh the ramp has not
     started; at −0.5·vh it is one sixth in (0.1/0.6); at +0.15·vh the 0.3·vh fade is half done. */
  const r1 = await goTo(stop - 0.7 * z.vh), r2 = await goTo(stop - 0.5 * z.vh), f1 = await goTo(stop + 0.15 * z.vh);
  lines.push({ what: 'the ramp starts 0.6vh before the release', pass: r1.paints.soil < 0.05 && Math.abs(r2.paints.soil - 1 / 6) < 0.05,
    detail: r1.paints.soil + ' at −0.7vh / ' + r2.paints.soil + ' at −0.5vh' });
  lines.push({ what: 'the fade runs 0.3vh past the release', pass: f1.paints.soil > 0.35 && f1.paints.soil < 0.65,
    detail: f1.paints.soil + ' at +0.15vh' });
  ```

- **The parked reset's completeness becomes an assertion and a stated invariant.**
  `flip`'s "soil stack stays hidden" line was vacuously true at its old crossing point: at scroll
  1600 the ramp reads `soil 0, night 1, moon 1` (measured 2026-09-23), so a parked branch that
  forgot to clear its inline opacities left nothing for the check to see. `flip` now crosses at
  the fade's half-way mark (`stop + 0.15·vh`, soil 1, fade 0.5) and asserts the whole rest
  state — `paints.night > 0.95`, `paints.soil < 0.05`, `paints.frontSoil < 0.05`, and `moon` /
  `trail` / `fireflies` back at 1 — which only holds if the parked branch resets every set the
  `on` branch writes. That crossing is not arbitrary: it is the point where every one of those
  six readings is non-default (`0 / 0.5 / 0.5 / 0 / 0.5 / 0.5`), so each clause can fail.
  `sceneEls` and `paints` are cleared explicitly; `skyEls` and `lifeEls` come along only because
  each is a filtered subset of `sceneEls` (`[data-scene-dims="soil"]` elements all carry
  `data-scene-grow`, and `[data-scene]` is half of `sceneEls`' selector), which is a coincidence
  no reader can see from the reset site 90 lines away. State it there — replace the parked
  branch's comment in `forest.js`:

  ```js
      /* Parked (≤960px): the pointer drift is all the scene keeps, and no soil ramp may
         survive a resize across the gate. Every opacity cleared here must cover every set the
         `on` branch writes: sceneEls (transform + opacity), paints, and skyEls / lifeEls — the
         last two only because each is a filtered subset of sceneEls. A future dims-only element
         would not be, and would strand its inline opacity across the gate. */
  ```

- [ ] **Step 2: Run the parked states and the tightened `soil` check**

```bash
cd "C:/Users/Dicky Mulia Fiqri/AppData/Local/Temp/firefly-demo" && \
node scene-probe.mjs --check base && \
node scene-probe.mjs --check soil && \
node scene-probe.mjs --check parked --width 900 --height 800 && \
node scene-probe.mjs --check parked --motion reduce && \
node scene-probe.mjs --check parked --motion reduce --width 900 --height 800 && \
node scene-probe.mjs --check flip
```

Expected: all six all-`PASS` on the first try. The parked runs pass because the ≤960px gate, the
reduced-motion early return and the identity transforms at scroll 0 already cover them; `soil`
passes with its two new lines at the shipped 0.6·vh / 0.3·vh constants; `flip` passes because the
parked branch already clears both sets it knows about.

If a line fails, the fix belongs in the guard, not the check — and which guard depends on the
line. `parked` and `base` failures: the `on` flag in `applyScene`. A `flip` reset failure: the
parked branch is missing a set the `on` branch writes — add it there. A `soil` ramp/fade failure:
the constants in `forest.js` and the spec disagree; stop and report rather than bending either.

- [ ] **Step 3: Run the edge geometry cases**

```bash
cd "C:/Users/Dicky Mulia Fiqri/AppData/Local/Temp/firefly-demo" && \
node scene-probe.mjs --width 1440 --height 900 --check settle && \
node scene-probe.mjs --width 1440 --height 900 --check pin && \
node scene-probe.mjs --width 1280 --height 720 --check growth && \
node scene-probe.mjs --width 2560 --height 600 --check settle && \
node scene-probe.mjs --width 2560 --height 600 --scroll 0  --out verify/scene-t6-short0.png && \
node scene-probe.mjs --width 2560 --height 600 --scroll 40 --out verify/scene-t6-short40.png
```

Expected: the 1440×900 settle and pin checks pass with the same formulas (`G` differs from 180
because the band is 17vw of 1440, and the 1280×720 growth check still reads the 1080p caps because
its guard is inert there: `0.5·720 / 218 = 1.65`). On 2560×600 the band is 435px of the 600px
viewport, so the ground line already sits at or below the fold: `G` clamps to 0, the scene pins
from the first pixel of scroll, and the half-viewport guard resolves to 1.0 — no growth. The
`--check settle` run at that size is where the `G = 0` line earns its keep: it must read the
ground line's **screen** position holding (903.3 → 903.3 across `s = 0 → 40`, measured
2026-09-23 at 2560×600) while `mist.ty` rides the scroll 1:1 — the pin engaging from the first
pixel, *not* the line travelling up the screen. Compare `scene-t6-short0.png` and
`scene-t6-short40.png` as well: the base must not jump between them.

One thing every one of these checks shares: they measure with the pointer at rest (`mx = 0`), so
the grove's pointer drift does not widen the document. That drift is pre-existing — the grove
carried the same 16px amplitude through `data-parallax` before this work — so a pointer parked at
the right edge still widens the scrollable width by up to that amplitude, pannable only
programmatically and never visible. Not a regression, not covered, not worth a check: note it in
the report so the next reader is not misled by "no new horizontal overflow" into thinking it
holds under pointer movement.

- [ ] **Step 4: Check the breakpoint flip from fresh loads**

```bash
cd "C:/Users/Dicky Mulia Fiqri/AppData/Local/Temp/firefly-demo" && \
node scene-probe.mjs --scroll 1600 --width 1100 --out verify/scene-t6-1100.png && \
node scene-probe.mjs --scroll 1600 --width 900  --out verify/scene-t6-900.png
```

Expected: at 1100px the scene is pinned (band base on the bottom edge); at 900px the scene has
scrolled with the page and its transforms are identity. No console errors in either.

- [ ] **Step 5: Report**

Report the parked runs, the flip run, the edge captures, and any fix made. Do not commit.

---

### Task 7: Documentation and the full sweep

**Files:**
- Modify: `landing/README.md`

**Interfaces:**
- Consumes: the shipped behaviour.
- Produces: the recorded choreography for future readers.

- [ ] **Step 1: Document the choreography in `landing/README.md`**

Add a section next to the existing ornament/parallax notes (keep the file's voice — plain
sentences, no bullet salad) covering, with the real numbers:

- the pinned scene: what pins, what the content does, and that the pin is one clamped offset
  (`o(s) = min(s + G·clamp(s/W), Omax)`, `W = 2.5·G`, `Omax` = the end of `<main>`) applied as a
  transform rather than CSS sticky, so the document height never changes;
- the front stack: which layers stand in front of the content and why (`z-index: 3` paint groups
  carrying the real near layers, so the tree tips stay whole; the band itself must not create a
  stacking context);
- the growth caps and the half-viewport guard (which caps growth and never shrinks the forest);
- the soil hand-off: the palette crossfade over the last 60vh of `<main>`, the sky withdrawal, the
  30vh fade, and the footer's own soil band;
- the two parked states: ≤960px (no settle/pin/growth/crossfade) and
  `prefers-reduced-motion: reduce` (nothing moves);
- that the art is regenerated with `node tools/gen-forest.mjs`, which now writes the night set and
  the `-soil` set in one run.

- [ ] **Step 2: Run the full sweep**

```bash
cd "C:/Users/Dicky Mulia Fiqri/AppData/Local/Temp/firefly-demo" && \
for c in stack settle pin growth guard soil parked flip; do echo "== $c"; node scene-probe.mjs --check $c || echo "CHECK FAILED: $c"; done && \
for wh in "1920 1080" "1440 900" "1280 720" "390 844"; do set -- $wh; \
  node scene-probe.mjs --width $1 --height $2 --scroll 0 --out verify/sweep-$1x$2-top.png; \
  node scene-probe.mjs --width $1 --height $2 --scroll 1600 --out verify/sweep-$1x$2-mid.png; \
  node scene-probe.mjs --width $1 --height $2 --scroll 3739 --out verify/sweep-$1x$2-end.png; done
```

Expected: all eight checks `PASS` at 1920×1080, twelve screenshots written, `console errors: none`
in every run, `docH` identical to the baseline (4819) at 1920×1080, and no run's horizontal
overhang past the baseline (the sweeps print `overflow.scrollW` 1946/1920 like the baseline — that
26px grove overhang is pre-existing, and `parked`'s `panX` lines are what prove it never grows).

- [ ] **Step 3: Eyeball the twelve captures against the spec's beats**

Walk the captures in order and confirm: scroll 0 unchanged; content emerging from behind the near
trees with the base pinned; the forest visibly larger mid-page with the base still on the bottom
edge; the CTA turning earth; the footer's soil band alone on the base with lime fireflies; the
phone width showing no pin and the soil footer. Report anything that reads wrong — do not adjust
numbers without saying so.

- [ ] **Step 4: Report**

Summarise the shipped behaviour, the changed/created files, and the evidence paths. Do not commit;
the owner decides how `landing/` lands.
