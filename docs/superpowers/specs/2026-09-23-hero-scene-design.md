# Landing hero — pinned night-forest scene, scroll growth, and a soil footer

Date: 2026-09-23
Status: design approved, pending implementation plan

## Context

The landing page (`landing/`) opens with a night-forest hero: a `.forest` band of four
2560×512 ornaments (mist, far pines, near pines, floor) whose ground sits on the bottom edge
of every canvas, plus a grove and a fern growing out of that line, a moon, two hanging
boughs, a firefly trail and a firefly cluster. The band is a normal flow child of `.hero`
right after the copy — that is what puts the forest on screen before the first scroll
(`style.css:176-177`). Below it come the stats row, then the rest of the page; the footer
carries its own copy of the band.

Today the whole scene scrolls away with the hero. The request: keep the scene on screen
while the page content rises past it, grow its elements as the reader scrolls, and — when
the CTA/footer arrives — end the pin and resolve the scene into an earth-brown band that
closes the page.

Brainstorming classified this as architectural: it changes how the hero, the page content
and the footer relate, and the current parallax engine cannot express any of it (its model
is per-section progress, which freezes on a positioned element). The structure ("hutan jadi
bingkai") and the hand-off mechanic ("menyusul ke dasar") were picked in the browser
companion; the three feel dials were confirmed after the approach:

- growth 1.0 → 1.35
- front-stack silhouette ±20% of the viewport height
- full soil band in the footer

## Goals

- The scene leaves the scroll flow: it stays on screen while the page content rises past it.
- Content emerges from the bottom edge from behind the nearest treeline, then rises into the
  sky region and is fully readable there.
- Scroll-linked growth (1.0 → 1.35) with the ground line as the fixed origin, so the forest
  leans into the reader instead of drifting off.
- At the CTA the scene's palette turns to earth; at the footer the pin has released and a
  full-width soil band closes the page, with the night sky ended and lime fireflies the only
  light.
- Reuse the existing art and its generator. No new illustration language, no new runtime
  dependency, no layout change at scroll 0.

## Non-goals (this iteration)

- Section content is not restyled and not wrapped in opaque panels. Sections stay transparent
  so the pinned scene is visible behind the text — that invariant is what makes the frame
  readable.
- No pin, settle or growth below 960px. Small screens keep today's behaviour exactly.
- No scroll-jacking, no scroll snapping, no `scroll-behavior` changes: the page keeps native
  scrolling and the same document height.
- No changes to `fireflies.js`, `main.js`, the nav, the feature tabs, or any section markup.
- No new art files beyond a soil palette of the ornaments that already exist.

## Locked decisions

1. **Structure — hutan jadi bingkai.** The moon and the atmospheric forest sit behind the
   content; the nearest trees, the grass and the undergrowth sit in front of it; content
   travels between the two. The scene is pinned, the content is not.
2. **Mechanic — menyusul ke dasar.** At scroll 0 the page is unchanged. As the reader
   scrolls, the scene still rises but at 40% slower than the page; its ground line lands on
   the bottom edge of the viewport, where it stays pinned until the footer.
3. **Front stack is the real layers, not a re-cropped band.** The nearest treeline promoted
   in front of the content is `orn-pines-near` + `orn-floor` + the grove and the fern, at
   their existing scale inside the existing band box. A separate 20vh band would have been
   `object-fit: cover`-cropped (box aspect 8.9 vs canvas aspect 5.0) and would have sheared
   the tree tips off flat; promoting the real layers keeps every silhouette whole, which is
   what the approved dial asked for. The clearing's glow travels with them: it paints over
   the near trees and the grass today, and a glow sent behind the treeline would be a visible
   change at scroll 0 — the one thing the whole mechanic may not cause.
4. **Growth caps are staggered by depth.** Far layers grow least, the nearest grow most, cap
   1.35. A flat cap would have made the mist swell as fast as the front trees and flatten the
   depth.
5. **The pin releases because `<main>` ends.** `<footer>` is outside `<main>`, so the
   release is a property of the document, not of a timer or a breakpoint.
6. **Reduced motion parks everything.** The scene stays where the flow puts it; the footer is
   statically soil.

## Scene membership and z-order

Measured on the live page at 1920×1080 (2026-09-23) by the verification probe: document
4819px; `.hero` top 71; `.forest` top 573.5, height 326.4 → ground line at 899.9, i.e.
**G = 180.1px (16.7% of the viewport) above the bottom edge**; `.hero-stats` top 953.9;
`<main>` ends at 3818.8 (the release is `mainBottom − vh ≈ 2739`); footer top 3926.8,
height 892.1. The footer's top is 108px below `<main>`'s end: that gap is the footer's
own `margin-top`, and it is the reason the release cannot be read off the footer's box.

| Stack | Elements | z | Behaviour |
| :--- | :--- | :--- | :--- |
| Sky (fixed) | `.stars`, `#fireflies` | 0, 1 | untouched |
| Behind | `orn-mist-far`, `orn-pines-far`, `.orn-moon`, both `.orn-canopy`, `trail--hero`, `.ff-orns--hero` | 1 | pinned |
| Content | every `.wrap`, `.compat`, `.section`, `.hero-stats` | 2 | untouched, native scroll |
| Front | `orn-pines-near`, `orn-floor`, `.clearing-glow`, `.orn-grove`, `.orn-fern` | 3 | pinned |
| Nav | `.nav` | 10 | untouched |

Pinning applies to all of them; scaling applies only to the nine layers in the Growth table.
The trails and the firefly clusters move with the scene but never scale — a scaled flight
path would deform its dotted curve and drag the fireflies off their path. Every scene layer
keeps its existing per-layer pointer drift (`mAmp`); the old scroll amplitude (`sAmp`) is
superseded by the shared offset for scene elements.

At scroll 0 the front stack overlaps nothing: its silhouette tops reach 258px above the
ground line (y 641) and the stats row starts at 953.9. It only starts biting content once the
settle is under way.

The front stack keeps `pointer-events: none` (the existing rule at `style.css:290-293`) and
`aria-hidden`, so it never intercepts a click, never blocks text selection, and is invisible
to assistive tech.

## Choreography

Let `s` be `scrollY`, `G` the ground line's distance above the viewport bottom at scroll 0
(180px here, measured per resize), `bandDocBottom` the band's document bottom, and `M`
`<main>`'s document bottom. The whole mechanic is one clamped offset applied to every scene
element:

```
W    = 2.5 · G                    // settle window: 450px (42vh) at 1080p
D(s) = G · clamp(s / W, 0, 1)     // the extra downward displacement that lags the page
Omax = M - bandDocBottom          // the hard stop: the ground line may not pass main's end
o(s) = min(s + D(s), Omax)        // the scene's translateY
```

`o(s)` produces all three phases on its own:

**Beat 0 — scroll 0.** `o = 0`, growth 1.0. The layout is identical to today and every
scene transform is identity: band in its flow spot, stats below the grass, no front stack
over anything. The one expected delta is that the layers lose the old per-layer scroll-drift
offsets they carried at load (sub-pixel at 1080p, see Mechanics) — the art was authored to sit
on one ground line, so removing them aligns the band rather than disturbing it.

**Beat 1 — settle, `s ∈ [0, W]`.** The ground line's screen position is
`bandDocBottom + D(s)` — it travels from `vh − G` to `vh`. Its speed is `1 − G/W = 60%` of
the page's, so the content visibly outruns the forest while the forest catches down to the
base. The stats row passes through the front stack's silhouette zone between roughly
`s = 40px` and `s = 224px`: it emerges from behind the near pines, which is the effect the
request asked for.

**Beat 2 — pin and growth, `s ∈ [W, s_release]`.** `o` keeps growing 1:1 with the scroll, so
the scene does not move on screen at all: the ground line stays on the bottom edge and the
content rises through the frame. Growth ramps linearly across this whole stretch and reaches
the caps at `s_release`. Because every layer's origin is the ground line, the base never
drifts and the forest widens upward.

**Beat 3 — release and soil, `s > s_release`.** `o` hits `Omax` and stops; the page keeps
scrolling, so the scene lifts with the last of `<main>` exactly as a released sticky element
would. `s_release = M − vh` (≈2739px here, 73% of the 3739px of scroll); from there the ground
line climbs with the page — about 80px above the bottom edge by the document end — while the
footer, whose own soil band is rooted on the document bottom, owns the base alone. The scene
does not survive that far: its 30vh fade-out is over by ≈3063, well before the footer's band is
the only forest on screen.

Scroll-linked ramps layered on top:

| Ramp | Driver | Span |
| :--- | :--- | :--- |
| Soil palette | crossfade of the night stack to the soil stack | last 60vh before `s_release` |
| Sky withdrawal | moon and both canopies dim to 0 | same span as the soil palette |
| Scene fade-out | the whole scene's opacity 1 → 0 | first 30vh after `s_release` |

The hero's firefly trail and its firefly cluster are deliberately not part of the sky
withdrawal: fireflies are the light of the page and stay lit over the soil until the scene
fades out.

The fade-out exists because the released scene would otherwise hover over the footer's own
band as it arrives. With it, the forest hands the base over to the earth and is absorbed.

## Growth

`g(s) = 1 + (cap − 1) · clamp((s − W) / (s_release − W), 0, 1)`, applied per layer with
`transform-origin` at the layer's own anchor:

| Layer | Stack | Origin | Cap |
| :--- | :--- | :--- | :--- |
| `orn-mist-far` | behind | bottom center | 1.18 |
| `orn-pines-far` | behind | bottom center | 1.25 |
| `.clearing-glow` | front | bottom center | 1.30 |
| `.orn-moon` | behind | `100% 0` (its own top-right) | 1.35 |
| `.orn-canopy--l/r` | behind | top center | 1.35 |
| `orn-pines-near` | front | bottom center | 1.35 |
| `orn-floor` | front | bottom center | 1.15 |
| `.orn-grove` | front | bottom center | 1.35 |
| `.orn-fern` | front | bottom center | 1.30 |

Guard: the band layers' effective cap is `min(cap, 0.5 · vh / bandBoxHeight)`, never below 1 —
the guard caps growth, it never shrinks the forest under the reader's feet, and where the band
already occupies more than half the viewport (short, very wide windows) it holds the layers at
1.0. It engages on short and ultrawide windows where 17vw is already a large share of the height.

Consequences to expect, measured at 1080p: the front stack's silhouette rises from 24% of
the viewport at rest (the nearest whole-tree value to the approved ±20%, since the art
decides it) to ~32% at the cap; the band box from 30% to 41%.

## Soil footer

The footer's `.forest--footer` band becomes the **soil band**: same geometry (17vw tall,
ground on the bottom edge, the document ends at its base), same layer anatomy, earth
palette — the mist becomes the soil mass, the pines read as dark earth silhouettes, the
floor becomes litter. The palette is one more run of `landing/tools/gen-forest.mjs` over the
same shapes, so the generator stays the single source of truth for the art; the soil files
are also what the Beat 3 crossfade uses, so there is exactly one soil definition.

The footer also stops the night sky: it is painted with the soil ground instead of letting
the fixed star field through, the moon and boughs are absent, and the footer's own lime
fireflies (`ff-orns--footer`) plus the global swarm over the soil are the only light. The
footer keeps its content, its trail, and its copy unchanged.

## Small screens and reduced motion

**≤960px:** no settle, no pin, no growth, no palette ramp. The scene scrolls away with the
hero exactly as today (the parallax drift stays). The footer is still the soil band — it is
static art, not motion, and the page's ending should read the same on a phone.

**`prefers-reduced-motion: reduce`:** `forest.js` already returns before the layers code, so
nothing moves and the trails park. The new choreography lives behind the same gate. The scene
therefore stays in the flow, the CTA stretch stays night, and the only soil is the footer's
static band. No scroll position depends on motion.

## Mechanics for the plan

- **The driver changes from per-section progress to page scroll.** `forest.js:145` computes
  `p` from the layer's scope rect; on a pinned element that rect is static, so `p` freezes
  and the formula feeds back on itself. Scene elements move to the `o(s)` model above;
  non-scene `[data-parallax]` elements (the footer's) keep today's model.
- **One transform string.** `forest.js:148` writes a single `translate3d(...)` per frame;
  `scale()` is composed into the same string. CSS animations on a layer wrapper would be
  overwritten — they stay on children, as `bough-sway` already does.
- **Promoting the front stack out of the band's stacking context.** `.forest` is
  `position: relative; z-index: 1` (`style.css:313-314`), which makes it a stacking context,
  so its children cannot rise above `.wrap`'s z 2 from inside. Two candidate routes for the
  plan to pick between, in the browser: drop the band's `z-index` so its children compete in
  the page context (one-line change, must be verified against `.stars`/`#fireflies`/`.nav`),
  or move the front art into a parallel absolutely-positioned box kept aligned with the
  band. Choose by what the probe shows, not by taste.
- **`body { overflow-x: hidden }` (`style.css:40`) propagates to the viewport**, which is why
  the sticky nav works today. If the pin or `will-change` interacts badly with it,
  `overflow-x: clip` is the modern equivalent; verify empirically before switching.
  Note what this does and does not buy: `hidden` blocks *user* panning but not
  `window.scrollTo`, which still lands on the 26px grove overhang, so the horizontal check is
  "the overhang does not grow", never "`scrollX` is 0". Measured 2026-09-23: clipping the
  *viewport* is not a fix either — `body { overflow-x: clip }` changes neither `scrollWidth` nor
  the pan, because Blink does not propagate `clip` to the viewport.
- **Growth must be clipped at the hero.** Scaling a full-width layer by its cap blooms it ~336px
  past each side at 1920, and Blink counts that in the document's scrollable overflow: with growth
  live and no clip the document measures `scrollWidth` 2256 against a 1920 viewport and a
  programmatic scroll moves 336px sideways. `overflow-x: clip` on `.hero` — the x axis alone, which
  pairs with a `visible` y axis — removes the bloom and the pre-existing grove overhang with it
  (`scrollWidth` 1920, pan 0), while leaving the pinned scene unclipped: the hero's box scrolls
  away during the pin, so an `overflow: hidden` there would cut the scene off the screen, which is
  why the clip has to be horizontal-only. Safari < 16 ignores `clip`; there the bloom returns but
  stays invisible behind `body { overflow-x: hidden }`.
- **Measurements are read per frame, never cached** (one or two `getBoundingClientRect`
  calls, as today), because the feature tabs change the page height and `M` moves with them.
  Nothing is cached, `G` included: the band's document bottom is read per frame too, so a font
  landing or a tab opening cannot leave the pin calibrated to a stale layout.
- **Guard against a null scene.** If the band, `<main>`, or the footer is missing, the engine
  falls back to today's behaviour rather than dividing by zero.

## Risks

- **Resampling softness.** The SVG orphans are rasterised at layout size and then scaled up
  to 1.35. The art is low-contrast silhouette work, so softness should be invisible; if it
  is not, the fallback is a lower cap (the dial is a target, not a promise).
- **Reading room on short windows.** At the cap the band occupies up to half the viewport by
  design. If the browser probe shows the content squeezed at 1280×720, trim the caps rather
  than the band height.
- **Jank.** Full-width scaled transforms on four to six images, plus the existing global
  canvas. `will-change: transform` is already set; the probe should scroll the page at 60fps
  and confirm no per-frame layout reads beyond the two rects.
- **Double ground during the release.** Mitigated by the fade-out ramp; verify at max scroll
  that only the footer's band is visible.

## Verification

No test suite covers `landing/`; verification is visual and structural, using the existing
static server on port 8094 plus headless Edge/Chromium via `playwright-core`:

1. **Scroll 0 is unchanged** — layout geometry (band, stats, footer, document height) and every
   scene transform at rest must match the pre-change capture at 1920×1080, with one permitted
   delta: the sub-pixel per-layer drift the old engine applied at load and the new one does not
   (Beat 0). The probe asserts the geometry and identity transforms; the screenshot is the eye
   check on the drift.
2. **Settle** — captures at ~25% and ~60% of `W`: the ground line descending toward the
   base, the stats row emerging from behind the near pines, no content trapped under the
   front stack.
3. **Pinned mid-page** — captures at ~40% and ~75% of `<main>`: content rising between the
   stacks, growth visible, the clear reading window above the silhouettes.
4. **CTA and footer** — captures during the soil ramp, at `s_release`, and at max scroll: the
   palette change, the fade-out, and the footer's soil band alone on the base with no night
   sky.
5. **Breakpoints** — 1920×1080, 1440×900, 1280×720, 390×844: the mechanic is absent at
   ≤960px and the page is otherwise unchanged; the horizontal overhang never grows past the
   pre-existing one (a programmatic scroll attempt stays within `scrollWidth - clientWidth`,
   which is 26px at 1920 before this work, from the grove's `right: -26px` overhang — so neither
   the equality of the two widths nor a `scrollX` of 0 is the invariant), and the document height
   is unchanged by the pin.
6. **Reduced motion** — run with `--force-prefers-reduced-motion`: every capture is
   identical to a static render; trails parked.
7. **Console clean** — no errors or warnings at any scroll position, and the nav, copy chip,
   feature tabs and firefly trails still behave.
8. **Guard behaviour** — resize the window across the `G = 0` case (the band already below
   the fold) and across the 960px breakpoint while scrolled, and confirm no jump.

## Documentation to update in the same commit

- `landing/README.md` — the choreography (settle, pin, growth caps, release), the soil
  palette, and the two parked states (≤960px, reduced motion).

## Follow-ups (not this iteration)

- Mobile choreography (a cheaper pin without the growth) if the phone reading experience
  turns out to miss the frame.
- A soil variant for the hero itself, if the transition ever wants to start earlier than the
  CTA.
- Profiling on low-end hardware and on battery-saver browsers.
