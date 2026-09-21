import { useEffect, useRef } from 'react';
import type { FireflyUpstream, Ripple, TailSpark } from './types';
import { useLatest } from '@/hooks/useLatest';

class FireflyParticle {
  upstream: FireflyUpstream;
  x: number;
  y: number;
  vx: number;
  vy: number;
  targetVx: number;
  targetVy: number;
  wanderAngle: number;
  speed: number;
  blinkPhase: number;
  blinkPeriod: number;
  glow: number;
  activeBurst: number;
  ripples: Ripple[];
  tailTrails: TailSpark[];
  hovered: boolean;

  constructor(upstream: FireflyUpstream, w: number, h: number) {
    this.upstream = upstream;
    const safeW = w > 0 ? w : 800;
    const safeH = h > 0 ? h : 320;
    this.x = 80 + Math.random() * Math.max(100, safeW - 160);
    this.y = 60 + Math.random() * Math.max(80, safeH - 120);
    this.vx = (Math.random() - 0.5) * 0.6;
    this.vy = (Math.random() - 0.5) * 0.6;
    this.targetVx = this.vx;
    this.targetVy = this.vy;
    this.wanderAngle = Math.random() * Math.PI * 2;
    this.speed = 0.45 + Math.random() * 0.35;
    this.blinkPhase = Math.random() * Math.PI * 2;
    this.blinkPeriod = 2.0 + Math.random() * 1.5;
    this.glow = 0;
    this.activeBurst = 0;
    this.ripples = [];
    this.tailTrails = [];
    this.hovered = false;
  }

  triggerBurst() {
    this.activeBurst = 1.0;
    this.glow = 1.0;
    this.ripples.push({ r: 4, alpha: 0.95, maxR: 38 });
  }

  update(w: number, h: number, dt: number, now: number) {
    const isOnline = this.upstream.connected !== false && this.upstream.breaker_state !== 'OPEN';

    // Dormant State: restful slow hovering drift without glow (OPEN/DISABLED)
    if (!isOnline) {
      this.glow = 0;
      this.activeBurst = 0;
      this.ripples = [];
      this.tailTrails = [];
      this.targetVx = 0.04 * Math.sin(now * 0.001 + this.blinkPhase);
      this.targetVy = 0.04 * Math.cos(now * 0.001 + this.blinkPhase);
      this.vx += (this.targetVx - this.vx) * 0.05;
      this.vy += (this.targetVy - this.vy) * 0.05;
      this.x += this.vx;
      this.y += this.vy;
      this.x = Math.max(25, Math.min((w || 800) - 25, this.x));
      this.y = Math.max(25, Math.min((h || 320) - 25, this.y));
      return;
    }

    // When hovered, freeze motion to allow precise inspection
    if (this.hovered) {
      this.vx = 0;
      this.vy = 0;
      this.targetVx = 0;
      this.targetVy = 0;
    } else {
      // 1. Natural Brownian wander steering
      this.wanderAngle += (Math.random() - 0.5) * 0.18;
      this.targetVx = Math.cos(this.wanderAngle) * this.speed;
      this.targetVy = Math.sin(this.wanderAngle) * this.speed + Math.sin(now * 0.003 + this.blinkPhase) * 0.2;

      // 2. Soft margin repulsion (keep gently inside celestial viewport)
      const margin = 65;
      const boundW = w || 800;
      const boundH = h || 320;
      if (this.x < margin) this.targetVx += 0.5;
      if (this.x > boundW - margin) this.targetVx -= 0.5;
      if (this.y < margin) this.targetVy += 0.5;
      if (this.y > boundH - margin) this.targetVy -= 0.5;

      // 3. Smooth velocity dampening and position integration
      this.vx += (this.targetVx - this.vx) * 0.04;
      this.vy += (this.targetVy - this.vy) * 0.04;
      this.x += this.vx;
      this.y += this.vy;

      this.x = Math.max(55, Math.min(boundW - 55, this.x));
      this.y = Math.max(40, Math.min(boundH - 40, this.y));
    }

    // 4. Bioluminescent Glow: Shines actively on connection burst or ongoing in-flight stream
    const hasActiveTraffic = (this.upstream.inflight ?? 0) > 0;

    if (this.activeBurst > 0) {
      this.activeBurst = Math.max(0, this.activeBurst - dt * 0.75);
      const pulse = (Math.sin(now * 0.016) + 1) / 2;
      this.glow = this.activeBurst * (0.6 + 0.4 * pulse);
      if (Math.random() < 0.2) {
        this.ripples.push({ r: 4, alpha: 0.85 * this.activeBurst, maxR: 34 });
      }
    } else if (hasActiveTraffic) {
      // Sustained living luminescence while connection is actively streaming
      const streamPulse = (Math.sin(now * 0.012) + 1) / 2;
      this.glow = 0.55 + 0.35 * streamPulse;
      if (Math.random() < 0.1) {
        this.ripples.push({ r: 4, alpha: 0.65, maxR: 30 });
      }
    } else {
      // Quiet resting flight: no blinking when no connection is active
      this.glow = 0;
    }

    // 5. Subtle drifting luminescence sparks
    if (this.glow > 0.25 && Math.random() < 0.25) {
      this.tailTrails.push({
        x: this.x - this.vx * 3 + (Math.random() - 0.5) * 3,
        y: this.y - this.vy * 3 + (Math.random() - 0.5) * 3,
        alpha: this.glow * 0.7,
        size: 1.0 + Math.random() * 1.5,
      });
    }
    for (let i = this.tailTrails.length - 1; i >= 0; i--) {
      const t = this.tailTrails[i];
      t.alpha -= dt * 1.2;
      t.y -= dt * 6;
      if (t.alpha <= 0) this.tailTrails.splice(i, 1);
    }

    // 6. Expanding photon ripple rings
    for (let i = this.ripples.length - 1; i >= 0; i--) {
      const r = this.ripples[i];
      r.r += dt * 36;
      r.alpha -= dt * 1.5;
      if (r.alpha <= 0 || r.r >= r.maxR) {
        this.ripples.splice(i, 1);
      }
    }
  }

  draw(c: CanvasRenderingContext2D) {
    const isOnline = this.upstream.connected !== false && this.upstream.breaker_state !== 'OPEN';

    // Disconnected / Dormant Rendering (Gray silhouette, no glow when OPEN/DISABLED)
    if (!isOnline) {
      c.save();
      c.translate(this.x, this.y);

      // Folded dormant wings
      c.fillStyle = 'rgba(255, 255, 255, 0.16)';
      c.beginPath();
      c.ellipse(-2, -3, 6, 2, 0.2, 0, Math.PI * 2);
      c.fill();
      c.beginPath();
      c.ellipse(-2, 3, 6, 2, -0.2, 0, Math.PI * 2);
      c.fill();

      // Inactive Thorax
      c.fillStyle = '#6b7280';
      c.beginPath();
      c.ellipse(0, 0, 3.8, 2.2, 0, 0, Math.PI * 2);
      c.fill();
      c.restore();

      // Inactive Host Label
      c.save();
      c.font = '11px "JetBrains Mono", monospace';
      c.textAlign = 'center';
      c.fillStyle = '#9ca3af';
      c.fillText(`${this.upstream.name} [DISABLED]`, this.x, this.y + 20);
      c.restore();
      return;
    }

    // Draw Sparks
    for (const t of this.tailTrails) {
      c.save();
      c.fillStyle = `rgba(190, 242, 100, ${t.alpha})`;
      c.beginPath();
      c.arc(t.x, t.y, t.size, 0, Math.PI * 2);
      c.fill();
      c.restore();
    }

    // Draw Expanding Ripples
    for (const r of this.ripples) {
      c.save();
      c.strokeStyle = `rgba(217, 249, 157, ${r.alpha * 0.8})`;
      c.lineWidth = 1.5;
      c.beginPath();
      c.arc(this.x, this.y, r.r, 0, Math.PI * 2);
      c.stroke();
      c.restore();
    }

    // Draw Bioluminescent Radial Glow Halo
    if (this.glow > 0.02) {
      const glowVal = Math.min(1.0, this.glow);

      if (this.glow > 0.3) {
        const haloRadius = 25 + this.glow * 20;
        const haloGradient = c.createRadialGradient(this.x, this.y, 0, this.x, this.y, haloRadius);
        haloGradient.addColorStop(0, `rgba(240, 255, 180, ${this.glow * 0.25})`);
        haloGradient.addColorStop(0.5, `rgba(240, 255, 180, ${this.glow * 0.12})`);
        haloGradient.addColorStop(1, 'rgba(240, 255, 180, 0)');

        c.fillStyle = haloGradient;
        c.beginPath();
        c.arc(this.x, this.y, haloRadius, 0, Math.PI * 2);
        c.fill();
      }

      const glowRadius = 16 + glowVal * 34;
      const grad = c.createRadialGradient(this.x, this.y, 0, this.x, this.y, glowRadius);
      grad.addColorStop(0, `rgba(240, 255, 180, ${0.95 * glowVal})`);
      grad.addColorStop(0.25, `rgba(190, 242, 100, ${0.65 * glowVal})`);
      grad.addColorStop(0.6, `rgba(163, 230, 53, ${0.2 * glowVal})`);
      grad.addColorStop(1, 'rgba(163, 230, 53, 0)');

      c.save();
      c.fillStyle = grad;
      c.beginPath();
      c.arc(this.x, this.y, glowRadius, 0, Math.PI * 2);
      c.fill();
      c.restore();
    }

    // Draw Insect Silhouette & Fluttering Wings
    c.save();
    const angle = Math.atan2(this.vy, this.vx);
    c.translate(this.x, this.y);
    c.rotate(angle);

    // Fluttering translucent wings
    const flutter = Math.sin(Date.now() * 0.035 + this.blinkPhase);
    c.fillStyle = 'rgba(255, 255, 255, 0.45)';
    c.beginPath();
    c.ellipse(-2, -5 * Math.abs(flutter), 6.5, 2.6, Math.PI / 4, 0, Math.PI * 2);
    c.fill();
    c.beginPath();
    c.ellipse(-2, 5 * Math.abs(flutter), 6.5, 2.6, -Math.PI / 4, 0, Math.PI * 2);
    c.fill();

    // Head / Thorax
    c.fillStyle = '#0f172a';
    c.beginPath();
    c.ellipse(2, 0, 3.8, 2.3, 0, 0, Math.PI * 2);
    c.fill();

    // Lantern Tail (Light organ)
    c.beginPath();
    if (this.glow > 0.02) {
      c.fillStyle = `rgba(240, 255, 180, ${0.3 + 0.7 * this.glow})`;
      c.arc(-2.6, 0, 3.2, 0, Math.PI * 2);
    } else {
      c.fillStyle = 'rgba(255, 255, 255, 0.18)';
      c.arc(-2.6, 0, 2.4, 0, Math.PI * 2);
    }
    c.fill();

    c.restore();

    // Upstream Host Label floating softly underneath
    c.save();
    c.font = '11px "JetBrains Mono", monospace';
    c.textAlign = 'center';
    const labelAlpha = this.hovered ? 1.0 : (this.glow > 0.02 ? 0.65 + this.glow * 0.35 : 0.4);
    c.fillStyle = `rgba(248, 250, 252, ${labelAlpha})`;
    c.fillText(this.upstream.name, this.x, this.y + 20);
    c.restore();
  }
}

/**
 * Draw subtle constellation lines between nearby fireflies
 */
function drawConstellationLines(ctx: CanvasRenderingContext2D, fireflies: FireflyParticle[]) {
  const maxDistance = 180;
  const lineOpacity = 0.08;

  for (let i = 0; i < fireflies.length; i++) {
    for (let j = i + 1; j < fireflies.length; j++) {
      const f1 = fireflies[i];
      const f2 = fireflies[j];
      const isOnline1 = f1.upstream.connected !== false && f1.upstream.breaker_state !== 'OPEN';
      const isOnline2 = f2.upstream.connected !== false && f2.upstream.breaker_state !== 'OPEN';
      if (!isOnline1 || !isOnline2) continue;

      const dist = Math.hypot(f2.x - f1.x, f2.y - f1.y);

      if (dist < maxDistance) {
        const opacity = lineOpacity * (1 - dist / maxDistance);
        ctx.strokeStyle = `rgba(248, 250, 252, ${opacity})`;
        ctx.lineWidth = 0.5;
        ctx.beginPath();
        ctx.moveTo(f1.x, f1.y);
        ctx.lineTo(f2.x, f2.y);
        ctx.stroke();
      }
    }
  }
}

// Upstream names and base URLs are operator-supplied strings, so they are written as
// text nodes: a name like `<img src=x onerror=...>` would otherwise execute in the
// dashboard origin, where the admin session token lives.
function renderTooltip(el: HTMLElement, name: string, details: string) {
  const nameEl = document.createElement('span');
  nameEl.className = 'font-bold';
  nameEl.textContent = name;

  const detailsEl = document.createElement('span');
  detailsEl.className = 'ml-1 text-[10px] opacity-80';
  detailsEl.textContent = details;

  el.replaceChildren(nameEl, detailsEl);
}

interface UseFireflyPhysicsOptions {
  canvasRef: React.RefObject<HTMLCanvasElement | null>;
  containerRef: React.RefObject<HTMLDivElement | null>;
  tooltipRef: React.RefObject<HTMLDivElement | null>;
  upstreams?: FireflyUpstream[];
  onToggleUpstream?: (name: string) => void;
  isPaused?: boolean;
  reducedMotion?: boolean;
  canToggle?: boolean;
}

/**
 * Custom hook running the Firefly 2D Canvas physics completely outside of React's render tree.
 * Follows Vercel React Best Practices:
 * - rerender-use-ref-transient-values: Coordinates, velocities, animation frame in useRef
 * - advanced-use-latest: Stable callbacks and upstreams references
 */
export function useFireflyPhysics({
  canvasRef,
  containerRef,
  tooltipRef,
  upstreams = [],
  onToggleUpstream,
  isPaused = false,
  reducedMotion = false,
  canToggle = true,
}: UseFireflyPhysicsOptions) {
  const firefliesRef = useRef<FireflyParticle[]>([]);
  const animFrameIdRef = useRef<number>(0);
  const lastTimeRef = useRef<number>(performance.now());
  const mousePosRef = useRef<{ x: number | null; y: number | null }>({ x: null, y: null });
  const hoveredFireflyRef = useRef<FireflyParticle | null>(null);

  const latestUpstreams = useLatest(upstreams);
  const latestOnToggle = useLatest(onToggleUpstream);
  const latestIsPaused = useLatest(isPaused);
  const latestReducedMotion = useLatest(reducedMotion);
  const latestCanToggle = useLatest(canToggle);

  // Sync fireflies array strictly with real upstreams (zero dummy fallbacks)
  const syncFireflies = (w: number, h: number, list?: FireflyUpstream[]) => {
    const currentList = list || latestUpstreams.current || [];

    const currentMap = new Map<string, FireflyParticle>();
    for (const f of firefliesRef.current) {
      currentMap.set(f.upstream.name, f);
    }

    const updated: FireflyParticle[] = [];
    for (const u of currentList) {
      if (currentMap.has(u.name)) {
        const existing = currentMap.get(u.name)!;
        existing.upstream = u;
        updated.push(existing);
      } else {
        updated.push(new FireflyParticle(u, w, h));
      }
    }
    firefliesRef.current = updated;
  };

  // Trigger visual photon burst on specific upstream node
  const triggerBurst = (upstreamName: string) => {
    if (!upstreamName) return;
    const target = upstreamName.trim().toLowerCase();
    for (const f of firefliesRef.current) {
      if (f.upstream.name.toLowerCase() === target) {
        f.triggerBurst();
        return;
      }
    }
    // Partial substring fallback
    for (const f of firefliesRef.current) {
      if (
        f.upstream.name.toLowerCase().includes(target) ||
        target.includes(f.upstream.name.toLowerCase())
      ) {
        f.triggerBurst();
        return;
      }
    }
  };

  useEffect(() => {
    const canvas = canvasRef.current;
    const container = containerRef.current;
    const tooltip = tooltipRef.current;
    if (!canvas || !container) return;

    const ctx = canvas.getContext('2d');
    if (!ctx) return;

    let width = 0;
    let height = 0;

    const handleResize = () => {
      const rect = container.getBoundingClientRect();
      const w = Math.floor(rect.width || container.clientWidth || 800);
      const h = Math.floor(rect.height || container.clientHeight || 320);
      if (w <= 0 || h <= 0) return;

      const dpr = Math.min(window.devicePixelRatio || 1, 2); // Cap at 2x for performance
      width = w;
      height = h;

      canvas.width = Math.round(w * dpr);
      canvas.height = Math.round(h * dpr);
      ctx.setTransform(1, 0, 0, 1, 0, 0);
      ctx.scale(dpr, dpr);

      syncFireflies(width, height);
    };

    handleResize();
    window.addEventListener('resize', handleResize);

    // Mouse interactions
    const handleMouseMove = (e: MouseEvent) => {
      const rect = container.getBoundingClientRect();
      const mouseX = e.clientX - rect.left;
      const mouseY = e.clientY - rect.top;
      mousePosRef.current = { x: mouseX, y: mouseY };

      let hit: FireflyParticle | null = null;
      for (const f of firefliesRef.current) {
        const d = Math.hypot(f.x - mouseX, f.y - mouseY);
        if (d < 35) {
          hit = f;
          break;
        }
      }

      for (const f of firefliesRef.current) {
        f.hovered = f === hit;
      }

      if (hit && tooltip) {
        hoveredFireflyRef.current = hit;
        container.style.cursor = 'pointer';
        const isOnline = hit.upstream.connected !== false && hit.upstream.breaker_state !== 'OPEN';
        const isHalfOpen = hit.upstream.breaker_state === 'HALF-OPEN';
        const ep = hit.upstream.base_url || (hit.upstream.base_urls && hit.upstream.base_urls[0]) || '';
        const canMutate = latestCanToggle.current ?? true;
        const actionHint = canMutate
          ? (isOnline ? 'Click to disable' : 'Click to activate')
          : 'Login required to toggle';
        const details = isOnline
          ? `${(hit.upstream.protocol || 'OPENAI').toUpperCase()} · ${ep} · ${hit.upstream.latency_ms || 180}ms (Status: ACTIVE · ${actionHint})`
          : isHalfOpen
          ? `${(hit.upstream.protocol || 'OPENAI').toUpperCase()} · ${ep} · Status: HALF-OPEN (${actionHint})`
          : `Status: DISABLED (Circuit Open) · ${actionHint}`;

        renderTooltip(tooltip, hit.upstream.name, details);
        tooltip.style.left = `${hit.x}px`;
        tooltip.style.top = `${hit.y}px`;
        tooltip.classList.remove('opacity-0');
      } else {
        hoveredFireflyRef.current = null;
        container.style.cursor = 'default';
        if (tooltip) {
          tooltip.classList.add('opacity-0');
        }
      }
    };

    const handleMouseLeave = () => {
      mousePosRef.current = { x: null, y: null };
      for (const f of firefliesRef.current) {
        f.hovered = false;
      }
      hoveredFireflyRef.current = null;
      container.style.cursor = 'default';
      if (tooltip) {
        tooltip.classList.add('opacity-0');
      }
    };

    const handleClick = (e: MouseEvent) => {
      const rect = container.getBoundingClientRect();
      const clickX = e.clientX - rect.left;
      const clickY = e.clientY - rect.top;

      let hit: FireflyParticle | null = hoveredFireflyRef.current;
      if (!hit) {
        for (const f of firefliesRef.current) {
          const d = Math.hypot(f.x - clickX, f.y - clickY);
          if (d < 35) {
            hit = f;
            break;
          }
        }
      }

      if (hit) {
        hit.triggerBurst();

        const canMutate = latestCanToggle.current ?? true;
        if (canMutate) {
          // Instant optimistic toggle on the particle only if authorized
          const currentBreaker = hit.upstream.breaker_state || 'CLOSED';
          const nextBreaker: 'OPEN' | 'CLOSED' = currentBreaker === 'OPEN' ? 'CLOSED' : 'OPEN';
          hit.upstream.breaker_state = nextBreaker;
          hit.upstream.connected = nextBreaker === 'CLOSED';

          // Immediately update tooltip to match new state
          if (tooltip) {
            const isOnline = nextBreaker === 'CLOSED';
            const ep = hit.upstream.base_url || (hit.upstream.base_urls && hit.upstream.base_urls[0]) || '';
            const details = isOnline
              ? `${(hit.upstream.protocol || 'OPENAI').toUpperCase()} · ${ep} · ${hit.upstream.latency_ms || 180}ms (Status: ACTIVE · Click to disable)`
              : `Status: DISABLED (Circuit Open) · Click to activate`;

            renderTooltip(tooltip, hit.upstream.name, details);
          }
        }

        if (latestOnToggle.current) {
          latestOnToggle.current(hit.upstream.name);
        }
      }
    };

    container.addEventListener('mousemove', handleMouseMove);
    container.addEventListener('mouseleave', handleMouseLeave);
    container.addEventListener('click', handleClick);

    // Animation Render Loop
    // A tab that loads in the background never paints, so the initial `document.hidden`
    // is true and the loop must be woken up when the tab finally becomes visible.
    let isHidden = document.hidden;
    const handleVisibilityChange = () => {
      isHidden = document.hidden;
      lastTimeRef.current = performance.now();
    };
    document.addEventListener('visibilitychange', handleVisibilityChange);

    // Global pulse and pause event listeners
    const handleGlobalPulse = (e: Event) => {
      const custom = e as CustomEvent<{ upstream?: string }>;
      const targetUpstream = custom?.detail?.upstream;
      if (targetUpstream) {
        triggerBurst(targetUpstream);
      } else {
        for (const f of firefliesRef.current) {
          f.triggerBurst();
        }
      }
    };
    let localPaused = isPaused;
    const handleTogglePause = () => {
      localPaused = !localPaused;
      latestIsPaused.current = localPaused;
    };
    window.addEventListener('firefly:pulse', handleGlobalPulse);
    window.addEventListener('firefly:toggle-pause', handleTogglePause);

    lastTimeRef.current = performance.now();

    const render = (now: number) => {
      const isPausedState = latestIsPaused.current;
      const isReducedMotionState = latestReducedMotion.current;

      const rawDt = Math.min(0.05, (now - lastTimeRef.current) / 1000);
      const dt = isPausedState ? 0 : rawDt * (isReducedMotionState ? 0.25 : 1.0);
      lastTimeRef.current = now;

      if (!isHidden && width > 0 && height > 0) {
        ctx.clearRect(0, 0, width, height);

        if (firefliesRef.current.length > 0) {
          // Draw constellation lines
          if (firefliesRef.current.length > 1) {
            drawConstellationLines(ctx, firefliesRef.current);
          }

          // Update and draw each particle
          for (const f of firefliesRef.current) {
            if (!isPausedState) {
              f.update(width, height, dt, now);
            }
            f.draw(ctx);
          }
        }
      }

      animFrameIdRef.current = requestAnimationFrame(render);
    };

    animFrameIdRef.current = requestAnimationFrame(render);

    return () => {
      window.removeEventListener('resize', handleResize);
      window.removeEventListener('firefly:pulse', handleGlobalPulse);
      window.removeEventListener('firefly:toggle-pause', handleTogglePause);
      container.removeEventListener('mousemove', handleMouseMove);
      container.removeEventListener('mouseleave', handleMouseLeave);
      container.removeEventListener('click', handleClick);
      document.removeEventListener('visibilitychange', handleVisibilityChange);
      cancelAnimationFrame(animFrameIdRef.current);
    };
  }, []);

  // Update existing particles when upstreams prop changes
  useEffect(() => {
    if (canvasRef.current && containerRef.current) {
      const rect = containerRef.current.getBoundingClientRect();
      syncFireflies(rect.width || 800, rect.height || 320, upstreams);
    }
  }, [upstreams]);

  return {
    triggerBurst,
  };
}
