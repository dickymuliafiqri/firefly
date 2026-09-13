# DESIGN.md — Frontend Architecture, Design System & Extension Guidelines

> **Status**: Core System Locked 🔒 • Modules Extensible 🧩  
> **Tech Stack**: React 18+ • Vite 5+ • TypeScript 5+ • Tailwind CSS 3+ • Zustand • TanStack Query  
> **Backend Counterpart**: Firefly (Go 1.22+ High-Concurrency AI Reverse Proxy)  
> **Version**: 1.0.0 — 2026-09-12

---

## 1. Design Philosophy & Core Identity (LOCKED 🔒)

Firefly embodies a **Nocturnal Celestial & Real-Time Telemetry** aesthetic. This dashboard is intentionally crafted not as a conventional administrative CRUD interface, but as a serene, elegant, and informative living-network visualization tailored for backend engineers and AI infrastructure operators.

### 1.1. Invariable Aesthetic Rules (Non-Negotiable)
1. **Nocturnal Dual-Tone Atmosphere**:
   - The background strictly uses a 2-stop vertical gradient:
     - Top (0%): `#020617` (Slate 950 - deep pitch black)
     - Bottom (100%): `#08152a` (Deep Midnight Blue)
   - The background must never be altered to bright, high-contrast themes without explicit context.
2. **3-Layer Atmospheric Stack**:
   - **Layer 0**: Celestial Starfield (deterministic radial-gradient stars across three vertical bands: upper, mid, lower).
   - **Layer 1**: Aurora Borealis Gradient (`#10b981` emerald, `#3b82f6` blue, `#8b5cf6` purple with subtle opacity `0.015 - 0.03` and a 20s breathing cycle).
   - **Layer 2**: Subtle Vignette Edge Darkening (`radial-gradient` 80%x70% rgba(2,6,23,0.4)) focusing visual attention onto the central canvas.
3. **Bioluminescent Living Network (Firefly Swarm)**:
   - Upstream AI endpoints are visualized as living bioluminescent fireflies on an HTML5 2D canvas.
   - Connected/healthy state: fluttering wings, luminous tail glow (`#f0ffb4` / `#bef264`), particle emission, and photon halos.
   - Disconnected/circuit-open state: dormant gray silhouette (`#6b7280`), muted labels, zero tail emission.
   - Dynamic constellation lines (`drawConstellationLines`) connect adjacent active nodes with proximity-based opacity.
4. **Procedural Lo-Fi Soundscape (Web Audio API)**:
   - A lightweight, procedural 64 BPM Lo-Fi synthesizer engine (Rhodes jazz voicings Dm9-G13-Cmaj9-Am9, vinyl brown noise crackle, soft sub-bass, and gentle sparkle chimes).
   - Strictly conforms to browser autoplay policies (only activated upon explicit user interaction via toggle button).
5. **Precision Typography & Telemetry**:
   - Sans-serif: `Inter` (weights 300 to 800) with OpenType features `cv02, cv03, cv04, cv11`.
   - Monospace: `JetBrains Mono` (weights 300 to 700) for metrics, endpoints, payloads, and logs.
   - All telemetry and counter numbers must apply `.tabular-nums` to eliminate layout jitter during high-frequency updates.
   - Counter updates utilize smooth interpolating counting animations (`animateValue`).

---

## 2. Decision Matrix: Locked vs Extensible

| Frontend Aspect | Status | Policy & Constraints |
| :--- | :---: | :--- |
| **Theme & Palette Tokens** | 🔒 LOCKED | Night-sky palette, firefly bioluminescence, and base typography cannot be modified unilaterally. |
| **Canvas Firefly Core Engine** | 🔒 LOCKED | Particle physics simulation, constellation lines, and `requestAnimationFrame` loop remain central to the visual experience. |
| **Global Layout Shell** | 🔒 LOCKED | Minimalist translucent header, full-viewport canvas container, and subtle footer telemetry bar. |
| **Audio Engine Architecture** | 🔒 LOCKED | Pure procedural Web Audio API synthesis without external heavy `.mp3` dependencies. |
| **Feature Tabs & New Modules** | 🧩 EXTENSIBLE | AI agents and developers are welcome to add new feature modules via the **Module Registry**. |
| **Telemetry Metric Cards** | 🧩 EXTENSIBLE | New metrics (P99 latency, cache hit rates, token throughput/sec) can be added to the statistics grid. |
| **Drawer / Modal Inspectors** | 🧩 EXTENSIBLE | Per-upstream detail drawers, circuit breaker diagnostic inspectors, and payload viewers are extensible. |
| **Transport Layer Provider** | 🧩 EXTENSIBLE | HTTP polling for `/api/settings` and `/api/telemetry` can be augmented with WebSockets or SSE streams. |

---

## 3. Design Tokens & Styling Guide (LOCKED 🔒)

### 3.1. Color Tokens (Tailwind Semantic Palette)

```typescript
// tailwind.config.ts — Semantic Color Mapping
export const colors = {
  background: {
    top: '#020617',       // Slate 950
    bottom: '#08152a',    // Deep Midnight
    surface: 'rgba(15, 23, 42, 0.65)',
    card: 'rgba(30, 41, 59, 0.45)',
    overlay: 'rgba(2, 6, 23, 0.85)',
  },
  biolum: {
    glow: '#f0ffb4',      // Firefly core bioluminescent
    aura: '#bef264',      // Lime 300 outer aura
    dim: 'rgba(190, 242, 100, 0.15)',
  },
  accent: {
    emerald: '#10b981',   // Status OK / Healthy
    cyan: '#06b6d4',      // Telemetry / Tokens
    amber: '#f59e0b',     // Warning / Circuit Half-Open / Cooldown
    rose: '#f43f5e',      // Circuit Breaker Open / Error 5xx
    violet: '#8b5cf6',    // Model / Routing tags
  },
  border: {
    subtle: 'rgba(255, 255, 255, 0.06)',
    medium: 'rgba(255, 255, 255, 0.12)',
    focus: 'rgba(190, 242, 100, 0.40)',
  },
  text: {
    primary: '#f8fafc',   // Slate 50
    secondary: '#94a3b8', // Slate 400
    muted: '#475569',     // Slate 600
    code: '#38bdf8',      // Sky 400
  }
};
```

### 3.2. Glassmorphism & Depth Specs
- **Card Background**: `backdrop-blur-md bg-slate-900/40`
- **Card Border**: `1px solid rgba(255, 255, 255, 0.07)`
- **Card Shadow**: `0 8px 32px 0 rgba(0, 0, 0, 0.37)`
- **Inset Highlight**: `box-shadow: inset 0 1px 0 0 rgba(255, 255, 255, 0.08)`
- **Interactive Hover**: Border transitions to `border-white/20`, translateY `-1px`, and subtle luminous shadow.

### 3.3. Typography Rules
- Body text size: `13px` to `14px` with `leading-relaxed` for clean dashboard density.
- Large metric numbers: `20px` to `28px`, bold, `font-mono`, `tracking-tight`, and `.tabular-nums`.
- Badges & Tags: `10px` to `11px`, `uppercase tracking-wider font-semibold px-2 py-0.5 rounded-full`.

---

## 4. Frontend Technical Architecture

### 4.1. Directory Structure

```
frontend/
├── index.html                   # HTML entrypoint (fonts, metadata)
├── vite.config.ts               # Vite build configuration with proxy
├── tsconfig.json                # Strict TypeScript configuration
├── tailwind.config.ts           # Semantic design tokens
├── package.json                 # React 18, Vite 5, Zustand, TanStack Query
├── src/
│   ├── main.tsx                 # Root React DOM render & QueryClientProvider
│   ├── App.tsx                  # Global Shell (Header, Canvas Background, Module Outlet)
│   ├── core/                    # [LOCKED 🔒] Core Infrastructure
│   │   ├── canvas/              # HTML5 Firefly Engine & Constellations
│   │   │   ├── FireflyCanvas.tsx
│   │   │   ├── useFireflyPhysics.ts
│   │   │   └── types.ts
│   │   ├── audio/               # Web Audio API Synthesizer Engine
│   │   │   ├── AudioManager.ts
│   │   │   └── useAudioState.ts
│   │   ├── layout/              # Shell, Header, Navigation Tabs, Drawer
│   │   │   ├── Shell.tsx
│   │   │   ├── Header.tsx
│   │   │   └── StatsFooter.tsx
│   │   └── state/               # Zustand Global State Slices
│   │       ├── store.ts         # Combined Root Store
│   │       ├── telemetrySlice.ts# Live Token & Request Counters
│   │       ├── settingsSlice.ts # Upstream & Model Configurations
│   │       ├── playgroundSlice.ts
│   │       └── uiSlice.ts
│   ├── modules/                 # [EXTENSIBLE 🧩] Pluggable Feature Modules
│   │   ├── 1-overview/          # Module 1: Live Overview & Quick Telemetry
│   │   ├── 2-upstreams/         # Module 2: Upstreams & Massive KeyRing Management
│   │   ├── 3-models/            # Module 3: Models & Routing Target Catalog
│   │   ├── 4-tenants/           # Module 4: Tenant Credentials & RPS Gates
│   │   ├── 5-telemetry/         # Module 5: Real-Time Stream Telemetry & Metrics
│   │   └── 6-playground/        # Module 6: Interactive Prompt Playground
│   ├── components/ui/           # Reusable UI Elements (GlassCard, Badge, Button, Modal)
│   ├── services/                # API Client & Adapters
│   │   ├── api.ts               # Typed fetch / TanStack Query hooks
│   │   └── schema.ts            # Go backend DTO definitions
│   └── hooks/                   # Shared utility hooks (useKeyboardShortcuts, useReducedMotion)
```

---

## 5. Module Extension System & AI Agent Guidelines (EXTENSIBLE 🧩)

To keep code clean and decoupled as new features are added by AI agents or developers, Firefly adheres to a **Pluggable Module Architecture**.

### 5.1. Module Interface Contract

New feature views must adhere to standard module patterns:
```typescript
export interface ModuleViewProps {
  // Clean, self-contained view communicating via Zustand and TanStack Query
}
```

### 5.2. Step-by-Step Guide for Adding New Feature Views
1. **Isolate Core Infrastructure:**
   - **NEVER MODIFY** files in `src/core/` (`canvas/`, `audio/`, `layout/Shell.tsx`) unless specifically instructed by the user.
   - Create a dedicated folder under `src/modules/<module-name>/`.
2. **Component Structure:**
   ```
   src/modules/<feature-name>/
   ├── <FeatureName>View.tsx     # Root view component
   ├── components/              # Memoized subcomponents
   └── types.ts                 # Local component types
   ```
3. **Design Compliance:**
   - Use `GlassCard` or transparent containers matching the nocturnal theme.
   - Apply semantic accent colors (`emerald` for healthy, `rose` for circuit open, `amber` for cooldown, `cyan` for telemetry).
   - Format numeric data with `font-mono` and `.tabular-nums`.

---

## 6. Frontend Engineering Best Practices

### 6.1. Canvas Lifecycle & Performance
1. **Render Loop Decoupled from React Lifecycle:**
   - The firefly `requestAnimationFrame` loop executes outside of React re-render cycles.
   - High-frequency telemetry updates must never trigger canvas re-initialization.
   - Canvas state is isolated in `useFireflyPhysics`.
2. **High-DPI Scaling:**
   - Canvas scales to `window.devicePixelRatio` for crisp rendering on Retina displays:
     ```typescript
     const dpr = Math.min(window.devicePixelRatio || 1, 2);
     canvas.width = rect.width * dpr;
     canvas.height = rect.height * dpr;
     ctx.scale(dpr, dpr);
     ```
3. **Zero Memory Leaks:**
   - Every `useEffect` binding window listeners, `requestAnimationFrame`, intervals, or `AudioContext` must return a cleanup function.
4. **Visibility State Throttling:**
   - When the browser tab is hidden (`document.hidden`), canvas animations and audio loops are paused to conserve system CPU cycles and battery life.

### 6.2. Accessibility & Keyboard Navigation (WCAG 2.1 AA)
1. **Global Keyboard Shortcuts:**
   - `1` through `7`: Rapid module switching (1: Overview, 2: Upstreams, 3: Models & Combos, 4: Tenants, 5: Telemetry, 6: Settings, 7: Playground).
   - `M`: Toggle Lo-Fi audio ambience.
   - `P`: Trigger test photon pulse broadcast across network.
   - `Space`: Pause/resume particle physics canvas simulation.
   - `Escape`: Dismiss open dialogs and modals.
2. **Focus Rings & Contrast:**
   - All interactive elements provide visible focus styling (`focus-visible:outline-none focus-visible:border-white/30`).
   - Accessible `aria-label` tags are present on icon-only buttons.
3. **Reduced Motion Support:**
   - Flying particle physics and star animations respect OS `prefers-reduced-motion` preferences.

### 6.3. Dashboard Route Authentication & Security Policy
1. **Public vs Protected Routes**:
   - **Overview Tab (`1`)**: Always accessible publicly without credentials for instant uptime and live network inspection.
   - **Configuration & Operational Tabs (`2-7`)**: Intercepted by `LoginModal` requiring the dashboard access password (default: `12345678`).
2. **State & Persistence**:
   - Authentication session is tracked in `sessionStorage` (`firefly_auth_session_v1`) to prevent token persistence across closed browser tabs.
   - Customized access password persists in browser `localStorage` (`firefly_dashboard_password_v1`) and can be reconfigured or reset to default from the Settings panel.
   - Header provides instant session locking (`Lock` button) to terminate access immediately.

---

## 7. Single-Binary Go Distribution (`go:embed`)

Firefly compiles down to a single, standalone binary. The frontend is built into `/frontend/dist/` and embedded into Go:

```go
// frontend/embed.go
package frontend

import (
    "embed"
    "io/fs"
)

//go:embed all:dist
var DistFS embed.FS

func FS() fs.FS {
    sub, err := fs.Sub(DistFS, "dist")
    if err != nil {
        return DistFS
    }
    return sub
}
```
Total compressed bundle size (HTML + JS + CSS) is kept **< 250 KB (gzip)** for optimal deployment portability.
