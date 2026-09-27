#!/usr/bin/env node
/* Deterministic generator for the landing page night-forest ornaments.
 *
 *   node landing/tools/gen-forest.mjs
 *
 * Writes flat silhouette SVGs into landing/assets/img/. Seeded RNG, so the
 * same command always reproduces the same forest. Edit the SCENES table at
 * the bottom to retune a band, then run again.
 */
import { writeFileSync, mkdirSync, statSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const outDir = resolve(here, '../assets/img');

/* mulberry32 — small, seeded, good enough for trees */
function rngFrom(seed) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const f = (n) => Math.round(n * 10) / 10;

/* one spruce, as a closed silhouette path */
function pine(cx, baseY, h, w, tiers, rnd, lean = 0) {
  const left = [];
  const right = [];
  const step = h / tiers;
  for (let i = 1; i <= tiers; i++) {
    const t = i / tiers;
    const y = baseY - h + step * i;
    const halfW = Math.max(2, w * Math.pow(t, 1.12) * (0.88 + rnd() * 0.24));
    const droop = h * (0.025 + rnd() * 0.02);
    const x = cx + lean * t;
    left.push([x - halfW, y + droop]);
    right.push([x + halfW, y + droop]);
    if (i < tiers) {
      const notch = halfW * (0.5 + rnd() * 0.16);
      const up = h * (0.028 + rnd() * 0.03);
      left.push([x - notch, y - up]);
      right.push([x + notch, y - up]);
    }
  }
  const baseHalf = w * (0.72 + rnd() * 0.2);
  left.push([cx + lean - baseHalf, baseY + 6]);
  right.push([cx + lean + baseHalf, baseY + 6]);

  let d = `M${f(cx + lean)} ${f(baseY - h)}`;
  for (const p of left) d += `L${f(p[0])} ${f(p[1])}`;
  for (let i = right.length - 1; i >= 0; i--) d += `L${f(right[i][0])} ${f(right[i][1])}`;
  return d + 'Z';
}

/* a band of spruces across a width */
function pineBand(opts) {
  const { width, height, seed, count, hMin, hMax, wMin, wMax, tiersMin, tiersMax, lean = 0.05 } = opts;
  const rnd = rngFrom(seed);
  const gap = (width + 80) / count;
  let d = '';
  for (let i = 0; i < count; i++) {
    const h = hMin + rnd() * (hMax - hMin);
    const w = (wMin + rnd() * (wMax - wMin)) * (0.78 + (0.44 * h) / hMax);
    const tiers = tiersMin + Math.floor(rnd() * (tiersMax - tiersMin + 1));
    const cx = -40 + i * gap + rnd() * gap * 0.6;
    const leanPx = (rnd() - 0.5) * 2 * lean * h;
    d += pine(cx, height + 8, h, w, tiers, rnd, leanPx) + ' ';
  }
  return d.trim();
}

/* smooth hill ridge; hills rise above baseY, valleys touch it */
function ridge(width, baseY, amp, seed, waves = 4, sink = 40) {
  const rnd = rngFrom(seed);
  const comps = [];
  for (let i = 0; i < waves; i++) {
    comps.push({
      wl: width / (1.2 + rnd() * 2.8),
      ph: rnd() * Math.PI * 2,
      a: (amp * (0.35 + rnd() * 0.65)) / waves * 1.9,
    });
  }
  let d = `M0 ${f(baseY + 40)}`;
  for (let x = 0; x <= width; x += 16) {
    let y = 0;
    for (const c of comps) y += c.a * Math.sin((x / c.wl) * Math.PI * 2 + c.ph);
    d += `L${f(x)} ${f(baseY - Math.abs(y))}`;
  }
  return d + `L${width} ${f(baseY + sink)}L0 ${f(baseY + sink)}Z`;
}

/* rounded shrubs sitting on a baseline */
function bushes(width, baseY, seed, opts) {
  const { count, rMin, rMax, sink = 60 } = opts;
  const rnd = rngFrom(seed);
  const gap = (width + 60) / count;
  let d = '';
  for (let i = 0; i < count; i++) {
    const r = rMin + rnd() * (rMax - rMin);
    const cx = -30 + i * gap + rnd() * gap * 0.5;
    const cy = baseY + r * (0.1 + rnd() * 0.25);
    d += `M${f(cx - r)} ${f(baseY + sink)}L${f(cx - r)} ${f(cy)}A${f(r)} ${f(r * 0.94)} 0 0 1 ${f(cx + r)} ${f(cy)}L${f(cx + r)} ${f(baseY + sink)}Z `;
  }
  return d.trim();
}

/* grass blades fanning out of tuft points */
function grass(width, baseY, seed, opts) {
  const { spots, bladesMin, bladesMax, hMin, hMax, jitter = 16 } = opts;
  const rnd = rngFrom(seed);
  let d = '';
  for (let s = 0; s < spots; s++) {
    const bx = rnd() * width;
    const by = baseY + rnd() * jitter;
    const n = bladesMin + Math.floor(rnd() * (bladesMax - bladesMin + 1));
    for (let i = 0; i < n; i++) {
      const h = hMin + rnd() * (hMax - hMin);
      const spread = (rnd() - 0.5) * h * 1.6;
      d += `M${f(bx)} ${f(by)}q${f(spread * 0.45)} ${f(-h * 0.72)} ${f(spread)} ${f(-h)} `;
    }
  }
  return d.trim();
}

/* three little lanterns on the forest floor */
function mushrooms(x, y, seed) {
  const rnd = rngFrom(seed);
  let caps = '';
  let stems = '';
  let glow = '';
  let cursor = x;
  for (let i = 0; i < 3; i++) {
    const r = 6.5 + rnd() * 5;
    const hgt = 10 + rnd() * 16;
    const cx = cursor + r;
    const capY = y - hgt;
    const stemW = 1.5 + rnd() * 0.9;
    stems += `M${f(cx - stemW)} ${f(y)}L${f(cx - stemW * 0.78)} ${f(capY)}L${f(cx + stemW * 0.78)} ${f(capY)}L${f(cx + stemW)} ${f(y)}Z `;
    caps += `M${f(cx - r)} ${f(capY + 1)}A${f(r)} ${f(r * 0.82)} 0 0 1 ${f(cx + r)} ${f(capY + 1)}Z `;
    glow += `<circle cx="${f(cx)}" cy="${f(capY - r * 0.3)}" r="${f(r * 0.24)}" fill="${C.glow}" opacity=".2"/>`;
    cursor = cx + r + 3 + rnd() * 5;
  }
  return { caps: caps.trim(), stems: stems.trim(), glow };
}

/* one hanging pine bough: a curved spine with needle strokes */
function bough(seed, opts) {
  const { x0, y0, len, drop, needles = 9, flip = false } = opts;
  const rnd = rngFrom(seed);
  const dir = flip ? -1 : 1;
  const steps = Math.round(len / needles);
  const spine = [];
  for (let i = 0; i <= steps; i++) {
    const t = i / steps;
    // quadratic: gentle downward droop that steepens at the tip
    const x = x0 + dir * len * t;
    const y = y0 + drop * (t * t * 0.75 + t * 0.25);
    spine.push([x, y]);
  }
  let spines = `M${f(spine[0][0])} ${f(spine[0][1])}`;
  for (let i = 1; i < spine.length; i++) spines += `L${f(spine[i][0])} ${f(spine[i][1])}`;
  let s = `<path d="${spines}" stroke="C_SPINE" stroke-width="2.4" fill="none" stroke-linecap="round"/>`;
  s += `<path d="${spines}" stroke="C_SPINE" stroke-width="4.2" fill="none" stroke-linecap="round" opacity=".55"/>`;

  let needlesD = '';
  for (let i = 1; i < spine.length; i++) {
    const [x, y] = spine[i];
    const [px, py] = spine[i - 1];
    const ang = Math.atan2(y - py, x - px);
    const n = 2 + (rnd() < 0.4 ? 1 : 0);
    for (let k = 0; k < n; k++) {
      const spread = (0.55 + rnd() * 0.75) * (Math.PI / 2.6);
      const side = k % 2 === 0 ? 1 : -1;
      const a = ang + side * spread * (0.75 + rnd() * 0.5);
      const lenN = 14 + rnd() * 26;
      needlesD += `M${f(x)} ${f(y)}L${f(x + Math.cos(a) * lenN)} ${f(y + Math.sin(a) * lenN)} `;
    }
  }
  s += `<path d="${needlesD.trim()}" stroke="C_NEEDLE" stroke-width="1.5" fill="none" stroke-linecap="round" opacity=".9"/>`;
  return s;
}

/* tapering filled frond for the undergrowth */
function frond(x0, y0, angleDeg, len, w0, seed) {
  const rnd = rngFrom(seed);
  const a = (angleDeg * Math.PI) / 180;
  const bend = (rnd() - 0.5) * 0.5;
  const pts = [];
  for (let i = 0; i <= 18; i++) {
    const t = i / 18;
    const ang = a + bend * t;
    const r = len * t;
    const cx = x0 + Math.cos(ang) * r;
    const cy = y0 + Math.sin(ang) * r;
    pts.push([cx, cy, w0 * Math.pow(1 - t, 1.35)]);
  }
  const dl = [];
  const dr = [];
  for (const [cx, cy, w] of pts) {
    dl.push([cx, cy - w]);
    dr.push([cx, cy + w]);
  }
  let d = `M${f(dl[0][0])} ${f(dl[0][1])}`;
  for (const p of dl) d += `L${f(p[0])} ${f(p[1])}`;
  for (let i = dr.length - 1; i >= 0; i--) d += `L${f(dr[i][0])} ${f(dr[i][1])}`;
  return d + 'Z';
}

function svg(w, h, body, bg = 'transparent') {
  const bgRect = bg === 'transparent' ? '' : `<rect width="${w}" height="${h}" fill="${bg}"/>`;
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${w} ${h}" width="${w}" height="${h}">${bgRect}${body}</svg>\n`;
}

/* ---------------------------------------------------------------- scenes */

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

/* Band geometry. Every layer of a band is the same canvas with the ground on
 * its bottom edge, so all layers are cropped and scaled identically by CSS and
 * the treeline can never drift away from the grass. Add a scene at another
 * size only if it stands alone (canopy, grove, fern). */
const BW = 2560;
const BH = 512;
const GY = BH;

const scenes = {};

/* hero + footer band: four stacked layers, all rooted on GY */
scenes['orn-mist-far.svg'] = () =>
  svg(BW, BH, `<path d="${ridge(BW, GY - 182, 74, 11, 3, 182)}" fill="${C.mist}" opacity=".45"/>`);

scenes['orn-pines-far.svg'] = () =>
  svg(
    BW,
    BH,
    `<path d="${pineBand({ width: BW, height: GY, seed: 21, count: 60, hMin: 107, hMax: 299, wMin: 30, wMax: 78, tiersMin: 5, tiersMax: 8 })}" fill="${C.far}"/>`
  );

/* The near band is the tallest silhouette in the set, and the band's height is
 * driven by the viewport width (see .forest in style.css) — so the height of the
 * tallest spruce is literally the minimum band height, as a fraction of the
 * canvas width. Tuned down from the original 432px so that a 17vw band clears
 * the tips at every desktop width instead of shearing them off flat. */
scenes['orn-pines-near.svg'] = () =>
  svg(
    BW,
    BH,
    `<path d="${pineBand({ width: BW, height: GY, seed: 34, count: 41, hMin: 153, hMax: 354, wMin: 41, wMax: 96, tiersMin: 5, tiersMax: 8, lean: 0.035 })}" fill="${C.near}"/>`
  );

scenes['orn-floor.svg'] = () => {
  const backGround = ridge(BW, GY - 32, 24, 55, 2, 40);
  const frontGround = ridge(BW, GY - 12, 20, 56, 3, 24);
  const bushD = bushes(BW, GY, 52, { count: 30, rMin: 18, rMax: 39, sink: 12 });
  const grassLit = grass(BW, GY - 34, 51, { spots: 70, bladesMin: 3, bladesMax: 6, hMin: 18, hMax: 46, jitter: 14 });
  const grassDark = grass(BW, GY, 53, { spots: 54, bladesMin: 4, bladesMax: 8, hMin: 16, hMax: 43, jitter: 9 });
  const shroomL = mushrooms(560, GY, 54);
  const shroomR = mushrooms(1660, GY, 57);
  return svg(
    BW,
    BH,
    `<path d="${backGround}" fill="${C.near}"/>` +
      `<path d="${grassLit}" stroke="${C.far}" stroke-width="2.4" fill="none" stroke-linecap="round"/>` +
      `<path d="${frontGround}" fill="${C.floor}"/>` +
      `<path d="${bushD}" fill="${C.shrub}"/>` +
      `<path d="${grassDark}" stroke="${C.trunk}" stroke-width="3.4" fill="none" stroke-linecap="round"/>` +
      `<path d="${shroomL.stems} ${shroomR.stems}" fill="${C.trunk}"/>` +
      `<path d="${shroomL.caps} ${shroomR.caps}" fill="${C.shroom}"/>${shroomL.glow}${shroomR.glow}`
  );
};

/* hanging boughs over the hero sky */
function canopy(flip) {
  const parts = [
    bough(61, { x0: flip ? 600 : 20, y0: -14, len: 330, drop: 150, needles: 9, flip }),
    bough(62, { x0: flip ? 630 : -10, y0: 34, len: 420, drop: 205, needles: 10, flip }),
    bough(63, { x0: flip ? 560 : 60, y0: -20, len: 260, drop: 96, needles: 8, flip }),
    bough(64, { x0: flip ? 640 : -20, y0: 96, len: 300, drop: 175, needles: 9, flip }),
  ].join('');
  return svg(620, 380, parts.replaceAll('C_SPINE', C.moonlit).replaceAll('C_NEEDLE', C.moonlit));
}

scenes['orn-canopy-left.svg'] = () => canopy(false);
scenes['orn-canopy-right.svg'] = () => canopy(true);

/* near grove rising from the bottom-right of the hero; own canvas, grounded on
 * its bottom edge so it can share the band's ground line */
scenes['orn-grove-right.svg'] = () => {
  const rnd = rngFrom(71);
  const G = 1200;
  const pineA = pine(350, G, 980, 176, 10, rnd, -30);
  const pineB = pine(660, G, 830, 142, 9, rnd, 21);
  const bushD = bushes(900, G, 72, { count: 6, rMin: 30, rMax: 72, sink: 16 });
  const grassNear = grass(900, G - 12, 73, { spots: 30, bladesMin: 4, bladesMax: 7, hMin: 24, hMax: 68, jitter: 14 });
  const trunks =
    'M274 1200 C 288 900, 306 660, 350 390 L372 392 C 344 660, 332 900, 334 1200 Z ' +
    'M604 1200 C 614 950, 632 760, 668 560 L690 566 C 656 772, 646 960, 651 1200 Z';
  const boughL = bough(74, { x0: 350, y0: 470, len: 245, drop: 140, needles: 8, flip: false });
  const boughR = bough(75, { x0: 668, y0: 660, len: 200, drop: 120, needles: 8, flip: false });
  return svg(
    900,
    G,
    `<path d="${pineA} ${pineB}" fill="${C.fore}"/>` +
      `<path d="${trunks}" fill="${C.trunk}"/>` +
      boughL.replaceAll('C_SPINE', C.trunk).replaceAll('C_NEEDLE', C.trunk) +
      boughR.replaceAll('C_SPINE', C.trunk).replaceAll('C_NEEDLE', C.trunk) +
      `<path d="${bushD}" fill="${C.fore}"/>` +
      `<path d="${grassNear}" stroke="${C.trunk}" stroke-width="2.6" fill="none" stroke-linecap="round"/>`
  );
};

/* ferns and shrubs at the left edge of the hero; same deal, grounded on the
 * bottom edge of its own canvas */
scenes['orn-fern-left.svg'] = () => {
  const rnd = rngFrom(81);
  let fronds = '';
  for (let i = 0; i < 9; i++) {
    const ang = -102 + i * 15 + (rnd() - 0.5) * 10;
    const len = 196 + rnd() * 240;
    const w0 = 12 + rnd() * 12;
    fronds += frond(114, 452, ang, len, w0, 90 + i) + ' ';
  }
  let fronds2 = '';
  for (let i = 0; i < 6; i++) {
    const ang = -86 + i * 19 + (rnd() - 0.5) * 8;
    const len = 107 + rnd() * 168;
    fronds2 += frond(300, 462, ang, len, 9 + rnd() * 8, 120 + i) + ' ';
  }
  let fronds3 = '';
  for (let i = 0; i < 5; i++) {
    const ang = -96 + i * 21 + (rnd() - 0.5) * 10;
    const len = 134 + rnd() * 190;
    fronds3 += frond(525, 464, ang, len, 10 + rnd() * 9, 140 + i) + ' ';
  }
  const tufts = grass(620, 458, 82, { spots: 30, bladesMin: 4, bladesMax: 8, hMin: 24, hMax: 72, jitter: 12 });
  return svg(
    620,
    470,
    `<path d="${fronds.trim()}" fill="${C.fore}"/>` +
      `<path d="${fronds2.trim()}" fill="${C.frond2}"/>` +
      `<path d="${fronds3.trim()}" fill="${C.frond3}"/>` +
      `<path d="${tufts}" stroke="${C.trunk}" stroke-width="2.5" fill="none" stroke-linecap="round"/>`
  );
};

/* ---------------------------------------------------------------- write */

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
