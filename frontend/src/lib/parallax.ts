import { useEffect, useRef } from 'react';

/**
 * Shared parallax engine — ported from ../firefly-web ParallaxScript.astro
 * and mockup/parallax.js.
 *
 * GPU efficiency rules:
 * - single global requestAnimationFrame loop, lerp 0.06
 * - translate3d only
 * - disabled entirely on prefers-reduced-motion
 * - paused when document.hidden
 * - subscriber per component (auto-removed on unmount) — zero DOM queries per frame
 */

type Hint = 'moon' | 'stars' | undefined;

interface Subscriber {
  el: HTMLElement;
  depth: number;
  hint: Hint;
}

const subs = new Set<Subscriber>();
const mouse = { x: 0, y: 0 };
const cur = { x: 0, y: 0 };
let rafId: number | null = null;

function frame() {
  rafId = null;
  cur.x += (mouse.x - cur.x) * 0.06;
  cur.y += (mouse.y - cur.y) * 0.06;

  const scroll = window.scrollY || 0;

  for (const s of subs) {
    // Skip elements that are not currently visible
    if (!s.el.getClientRects().length) continue;

    const mx = cur.x * s.depth;
    const my = cur.y * s.depth * 0.55;
    let sc = 0;

    if (s.hint === 'moon') {
      sc = -Math.min(scroll * 0.1, 60);
    } else if (s.hint === 'stars') {
      sc = -Math.min(scroll * 0.045, 40);
    }

    s.el.style.transform =
      'translate3d(' + mx.toFixed(2) + 'px,' + (my + sc).toFixed(2) + 'px,0)';
  }

  rafId = requestAnimationFrame(frame);
}

function start() {
  if (rafId === null && !document.hidden) rafId = requestAnimationFrame(frame);
}

function stop() {
  if (rafId !== null) {
    cancelAnimationFrame(rafId);
    rafId = null;
  }
}

let initialized = false;

function ensureEngine() {
  if (initialized) return;
  initialized = true;

  if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;

  window.addEventListener(
    'mousemove',
    (e) => {
      mouse.x = (e.clientX / window.innerWidth) * 2 - 1;
      mouse.y = (e.clientY / window.innerHeight) * 2 - 1;
    },
    { passive: true },
  );

  document.addEventListener('visibilitychange', () => {
    if (document.hidden) stop();
    else if (subs.size) start();
  });

  if (subs.size) start();
}

/**
 * Attach to global parallax engine.
 * Unmounting components automatically detach from the engine (per-page scenery).
 */
export function useParallax<T extends HTMLElement>(depth: number, hint: Hint = undefined) {
  const ref = useRef<T>(null);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;

    ensureEngine();

    const s: Subscriber = { el, depth, hint };
    subs.add(s);
    if (!window.matchMedia('(prefers-reduced-motion: reduce)').matches) start();

    return () => {
      subs.delete(s);
    };
  }, [depth, hint]);

  return ref;
}
