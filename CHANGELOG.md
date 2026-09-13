# Changelog

All notable changes to the Firefly project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.1.1] - 2026-09-13

### Fixed
- **Insecure HTTP Context Cryptography & Clipboard Support**:
  - Resolved `Uncaught (in promise) TypeError: can't access property "digest", crypto.subtle is undefined` when creating tenant API keys on plain HTTP remote deployments (e.g. non-localhost IP addresses) where `window.crypto.subtle` is disabled by browser security policies.
  - Implemented a zero-dependency, pure TypeScript FIPS 180-4 SHA-256 fallback engine (`computeSha256Hex`) in `frontend/src/lib/crypto.ts` that seamlessly activates whenever Web Crypto is unavailable or throws.
  - Added robust random key generation fallback using `Math.random` if `window.crypto.getRandomValues` is inaccessible.
  - Implemented cross-browser fallback clipboard utility (`copyToClipboard`) in `frontend/src/lib/utils.ts` utilizing an offscreen textarea and `document.execCommand('copy')` when `navigator.clipboard.writeText` is restricted in non-HTTPS environments, applied across `KeyGeneratorModal`, `UpstreamModal`, `RawJsonEditor`, and `StreamInspector`.

## [1.1.0] - 2026-09-13

### Added
- **Mobile & Desktop Responsive Layout**:
  - Integrated mobile hamburger navigation toggle button with `Menu` and `X` icons within `Header.tsx` without introducing external component files.
  - Added nocturnal bioluminescent dropdown menu (`bg-[#090b10]/95 backdrop-blur-xl border border-white/[0.08]`) with glowing active tab indicators (`bg-emerald-400`), soundwave lo-fi audio toggle, and session lock/sign-in controls.
  - Implemented viewport stabilization with `min-h-[100dvh]` to eliminate mobile layout shifts caused by address bar movements on iOS and Android browsers.
  - Added dynamic particle canvas resizing (`h-[360px] sm:h-[460px] lg:h-[560px]`) and adaptive history panel heights on compact viewports.
  - Enhanced drawer (`pl-0 sm:pl-10`), modal dialog (`p-4 sm:p-6`), and form inputs (`min-w-0`, `truncate`) for seamless touch and mobile screen ergonomics.
- **Backend Credential Vault & Session Authentication (`internal/auth`)**:
  - Centralized master password and session token manager (`auth.Manager`) storing salted SHA-256 hashes in `configs/auth.json`.
  - Added secure session verification endpoints: `POST /api/auth/login`, `GET /api/auth/verify`, `POST /api/auth/logout`, and `POST /api/auth/password`.
  - Added `-dashboard-password` CLI flag and `FIREFLY_DASHBOARD_PASSWORD` environment variable.
- **Persistent Analytics & Circuit Breaker Overrides (`internal/analytics`)**:
  - State persistence for manually and automatically tripped circuit breakers across gateway reboots via `GET/PUT /api/breakers`.
  - Added request history clearing via `DELETE /api/history`.

### Changed
- **Frontend Core Modernization (React 19 & Zustand v5)**:
  - Upgraded frontend framework dependencies to **React 19.3.0** (`react`, `react-dom`, `@types/react`, `@types/react-dom`, `@vitejs/plugin-react@^4.7.0`).
  - Upgraded Zustand state management to **v5.0.3** for full React 19 concurrent mode and `useSyncExternalStore` compatibility.
  - Replaced deprecated `forwardRef` wrappers with standard React 19 `ref` props in canvas and physics components.
  - Adopted React 19 `useTransition` for asynchronous modal submissions, history clearing, and configuration updates.
  - Standardized conditional UI rendering on explicit ternary operators per international Vercel React Best Practices.

### Fixed
- **React Minified Error #185**:
  - Resolved `Maximum update depth exceeded` infinite re-render loop by decoupling `useStoreActions` and `usePlaygroundActions` into referentially stable singleton action dispatchers with zero store subscription overhead.
  - Guarded `verifySession` in `settingsSlice.ts` to prevent redundant state setter cycles on duplicate session checks.

### Security
- Eliminated plain-text and hashed credential persistence in browser `localStorage`. Master dashboard passwords and session tokens are strictly verified on the Go backend plane.

## [1.0.0] - 2026-09-13

### Added
- **Core AI Gateway Architecture**:
  - High-performance, multi-tenant reverse proxy compatible with OpenAI and Anthropic Claude wire protocols.
  - Granular single-binary distribution compiling the full React SPA into Go via `//go:embed all:dist`.
- **SSE Streaming Relay Engine (`openai.RelaySSE`)**:
  - Support for 1,000+ simultaneous Server-Sent Events (SSE) connections.
  - Client disconnect abort via request context propagation to immediately stop billing on upstream AI providers.
  - Idle watchdog (`StreamWatchdog`) to tear down zombie sockets if upstream stops transmitting tokens.
  - Immediate token flushing (`http.Flusher`) for minimal TTFT (Time-To-First-Token).
- **Two-Layer Resilience**:
  - **Layer 1 (KeyRing 429/401)**: Dynamic cooldown parsed from upstream `Retry-After` header and permanent revocation on HTTP 401 with transparent failover across remaining API keys.
  - **Layer 2 (Circuit Breaker)**: Host-level circuit breaker protecting gateway from upstream host outages and 5xx errors without penalizing client quota errors. Dynamic manual override and state persistence via `/api/breakers`.
- **Backend Persistent Analytics & Ledger Subsystem (`internal/analytics`)**:
  - Thread-safe disk persistence for cumulative tokens (input, output, total), cost estimates (USD), and request transaction logs in `configs/analytics.json`.
  - Background atomic flush every 5 seconds and graceful shutdown drain.
  - Dedicated REST endpoints: `GET/PUT /api/breakers` and `GET/DELETE /api/history` with administrative authorization check.
- **Three-Tier Admission Control**:
  - Server-wide admission semaphore (`httpx.GlobalLimiter`) capped at 1,500 in-flight requests.
  - Per-tenant token-bucket rate limiter (`golang.org/x/time/rate`) and concurrency limiter.
  - Per-credential atomic CAS in-flight concurrency ticket gate.
- **Virtual Combos (Model Load Balancing)**:
  - Group multiple upstream models into a unified public virtual model route.
  - Supports `least_inflight` pool, `round_robin` pool, and `failover` chain strategies.
- **Interactive Embedded Dashboard (React SPA)**:
  - **Overview**: Live request history, system pulse, and nocturnal lo-fi audio player.
  - **Upstreams**: Real-time circuit breaker status and multi-key management.
  - **Models & Combos**: Dual-tab management for individual models and virtual combo pools.
  - **Tenants**: Secure API key generator and rate/concurrency quota configuration.
  - **Telemetry**: Real-time Prometheus metrics, latency percentiles (P50/P90/P99), inflight semaphore gauges, and cooldown heatmaps.
  - **Settings**: Dual visual forms and raw JSON editor with line diff preview and atomic hot-swap.
  - **Dashboard Authentication**: Route protection requiring password authentication (default: `12345678`) to navigate protected tabs while keeping Overview public.
  - **Playground**: In-browser testing environment with TTFT, TPS, and SSE packet inspector.
- **Detached High-Concurrency Load Tester (`cmd/loadtest`)**:
  - Standalone binary supporting 100 and 1,000 simultaneous request bursts.
  - Workload profiles: Low (rendah), Medium (sedang), and Heavy (berat).
  - Built-in `-mock` flag for self-contained validation without live external AI API keys.
- **CLI & Packaging**:
  - Added `-version` flag to CLI to display version, commit hash, and build timestamp.
  - Environment variable overrides for all CLI flags (`FIREFLY_ADDR`, `FIREFLY_CONFIG_DIR`, etc.).
  - Universal one-line installer (`install.sh`) for automated background `systemd` daemon setup across Linux distributions.
  - GitHub Actions multi-platform cross-compilation release pipeline.

### Security
- **Backend Credential Vault & Authorization Check**: Sensitive credentials and passwords are completely removed from browser `localStorage`. Master dashboard password and dynamic session tokens are stored, hashed (salted SHA-256), and validated solely on the backend (`auth.Manager`, `configs/auth.json`). All frontend credential access and mutations require active backend authorization verification (`/api/auth/verify`, `/api/auth/password`).
- Zero-leaking secret masking in structured logs (`slog`) and client error JSON.
- Fail-closed typed-nil safety (`httpx.IsNil`) across all dependency injection boundaries.
- Adherence to architectural invariants: zero `WriteTimeout` on the data plane to protect long-lived streaming connections.
