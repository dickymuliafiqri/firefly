# Changelog

All notable changes to the Firefly project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.2.2] - 2026-09-15

### Fixed
- **Turso embedded-replica initialization failed under systemd with `mkdir data: permission denied`**:
  - The local replica database default (`data/firefly.db`) is a relative path, so it was resolved against the process working directory. Under a systemd unit the CWD is typically `/` (or a root-owned install dir), so `os.MkdirAll("data", …)` failed with `permission denied`, aborting Turso store initialization (observed on `GET /api/turso/providers` and `GET /api/turso/*`).
  - Relative local paths are now anchored to the writable config directory. `internal/server/turso_manager.go` gained a `resolveLocalPath` helper used by both `GetOrInitStore` and `UpdateConfig`, and `cmd/firefly/main.go` anchors a relative `-turso-local-path` to `-config-dir` on startup. Absolute paths and `FIREFLY_TURSO_LOCAL_PATH` / config overrides are respected unchanged.
  - With the default systemd layout (`-config-dir=/etc/firefly`, `ReadWritePaths=/etc/firefly`) the replica now lands at `/etc/firefly/data/firefly.db`, inside a writable location.

## [1.2.1] - 2026-09-15

### Fixed
- **Garbled SSE / non-streaming responses when the upstream compressed the body (`internal/openai`)**:
  - The OpenAI adapter now scrubs the client `Accept-Encoding` header before forwarding, so Go's transport negotiates encoding itself and transparently decompresses gzip/deflate responses instead of relaying raw compressed bytes to the client.
  - Added `decodeResponseBody` as defense-in-depth on both the streaming (`RelaySSE`) and non-streaming (`RelayBuffered`) paths for upstreams that compress unconditionally.
- **Circuit breaker mis-classification for OpenAI and Anthropic adapters**:
  - `401`/`429`/other 4xx responses are no longer reported to the upstream circuit breaker. Classification is now enforced centrally by `upstream.ProcessAttemptOutcome`, honoring the Layer 1 (key) vs Layer 2 (host) invariant for every adapter.
- **Deleting the last model / upstream / combo / tenant did not persist (Turso store)**:
  - Authoritative-delete flags (`manage_models`, `manage_upstreams`, `manage_combos`, `manage_tenants`) now let the dashboard delete the final catalog item, while a partial/stale save with an empty list still never wipes existing rows (race protection with the 5s settings poll).
- **Deleting one model deleted all models / upstream delete validation error**:
  - The Turso `SaveSettings` cascade no longer removes models for an upstream that surviving models still reference, and the frontend refuses to send an inconsistent partial catalog.

### Added
- **Upstreams without credentials**: an upstream may now be created and saved with no `api_key`/`credential_ref`/`credential_pool` (built with an empty key ring) so keys can be added later. Requests routed to it fail closed at forward time; the configuration itself is valid and hot-swappable.
- **Playground reasoning / "thinking" display (`ChatWindow.tsx`)**: assistant reasoning streamed via `delta.reasoning_content` (or `message.reasoning_content` for non-streaming) is captured and shown in a collapsible "Thinking" panel above the answer; added auto-scroll to keep the newest tokens in view.
- **Frontend delete guard**: the Upstreams delete dialog blocks removing an upstream that model routes still reference and lists the blocking routes.

### Changed
- **Shared upstream attempt engine (`internal/upstream/attempt.go`)**: extracted the duplicated per-adapter breaker/key-outcome/failover logic into `ProcessAttemptOutcome`, removing DRY violations across the five adapters and preventing classification drift.
- **Frontend settings payload helper (`core/state/store.ts`)**: introduced `buildSettingsPayload(overrides)` to build the `PUT /api/settings` body from a single fresh store snapshot; replaced 11 duplicated payload-construction sites across the Upstreams, Models, Combos, Tenants views/modals.
- **Playground streaming flush**: replaced the `requestAnimationFrame`-throttled render with a wall-clock time-throttled flush that force-renders the first token, and set `cache: 'no-store'` on the chat fetch for streaming reliability across browsers.
- **Language consistency**: translated all remaining Indonesian UI strings, comments, and documentation to English (`UpstreamModal.tsx`, `SettingsView.tsx`, `docs/loadtest.md`, `cmd/loadtest/*`, `CHANGELOG.md`).
- **Dev proxy streaming (`frontend/vite.config.ts`)**: the `/v1` dev proxy forces `Accept-Encoding: identity` and streaming-friendly response headers so SSE is not buffered when running via the Vite dev server.

### Known Issues
- **Playground live token rendering**: in some browsers the chat reply and SSE inspector may still render only after the stream completes even though the gateway relays SSE incrementally (verified via `curl -N`). The gateway and API are confirmed correct; the remaining issue is client-side rendering and is under investigation.

## [1.2.0] - 2026-09-15

### Added
- **Third-Party AI OAuth Integration (`internal/oauth`)**:
  - Centralized OAuth 2.0 authorization code and device code manager (`oauth.Manager`) with encrypted atomic persistence (`configs/oauth.json`) and automated token refresh lifecycles.
  - Native provider adapters: Google Antigravity Cloud Code (`internal/antigravity`), Cline (`internal/cline`), and CodeBuddy China & International (`internal/codebuddy`).
  - Multi-account upstream keyrings: support connecting 2+ accounts per provider on a single upstream host with automatic `least_inflight` / `round_robin` load balancing and 429 quota failover.
  - Dynamic token resolution: runtime references (`oauth:<id>`) resolve and refresh bearer tokens on every outbound request without requiring server restarts or environment variables.
  - Interactive dashboard OAuth connection dialog and account management in `UpstreamModal.tsx`, `UpstreamCard.tsx`, and `AccountRingSlotList.tsx`.
  - OAuth REST API endpoints: `/api/oauth/providers`, `/api/oauth/authorize`, `/api/oauth/callback`, `/api/oauth/poll`, `/api/oauth/connections`, and `/api/oauth/connections/{id}`.
- **Native Let's Encrypt Auto-TLS (`internal/server/autotls.go`, `internal/config/tls.go`)**:
  - Embedded ACME HTTP-01 challenge support for zero-config HTTPS certificate provisioning and automated renewal via `golang.org/x/crypto/acme/autocert`.
  - Secure certificate cache directory (`configs/certificates/`, mode `0700`) and operational configuration in `configs/tls.json`.
  - Automatic HTTP-to-HTTPS redirect on port 80 while serving the data and admin plane on port 443 without interrupting the standard `-addr` listener.
- **Active Model Route Connectivity Verification (`internal/server/upstream_check.go`)**:
  - Enhanced `/api/upstreams/check` with optional `model` parameter executing minimal inference probes (`max_tokens: 1`) across OpenAI, Anthropic, Cline, Codebuddy, and Antigravity protocols.
  - Granular error classification powered by `gjson` (HTTP 200 active, 401/403 auth rejected, 404 model not found, 429 quota exhausted, 400 invalid parameters, 5xx server error).
  - Added **Model Verification** panel and "Test Connection" button in `ModelModal.tsx` with responsive feedback badges (Emerald success / Rose rejection with upstream error message and latency).

### Changed
- **Configuration Builder Protocol & Token Resolution (`internal/config/builder.go`)**:
  - Extended configuration builder to support `cline`, `antigravity`, `codebuddy-cn`, and `codebuddy-intl` protocols.
  - Enabled dynamic `oauth:...` credential references in keyrings without requiring host OS environment variable lookup.
- **Frontend Upstream & Models Layout**:
  - Redesigned Upstreams view and modal to accommodate multi-account keyrings, OAuth status badges, and interactive authorization flows.
  - Strictly enforced Vercel React Best Practices across newly introduced components (`rerender-no-inline-components`, `rendering-conditional-render`, `rerender-functional-setstate`, `js-early-exit`).

### Fixed
- Fixed Cline OAuth protocol validation and default base URL normalization (`https://api.cline.bot/api/v1`).
- Fixed upstream model catalog discovery when authenticating via dynamic OAuth tokens.

## [1.1.2] - 2026-09-14

### Changed
- **Air Hot-Reload Volatile File Exclusions (`.air.toml`)**:
  - Configured Air live-reloading to exclude volatile telemetry, analytics, and dynamic state files (`configs/analytics.json`, `configs/telemetry.json`, `configs/auth.json`, `build-errors.log`).
  - Added comprehensive exclusion regex patterns in `exclude_regex` (`.*(telemetry|analytics).*`, `.*auth\.json$`, `.*\.tmp$`, `.*\.temp$`, `.*\.log$`, `.*\.swp$`, `.*\.swo$`, `.*~.*$`, `.*\.DS_Store$`) to eliminate infinite rebuild loops and spurious server restarts during request forwarding and token accounting.
  - Added `.gemini`, `.idea`, and `.vscode` to `exclude_dir` to suppress IDE workspace trigger events.

### Documentation
- **Architecture & Layout Modernization**:
  - Updated `CONTRIBUTING.md`, `AGENTS.md`, and `llms.txt` with package mappings for `internal/analytics` and `internal/auth`.
  - Documented React 19 frontend layout and Air live-reloading configuration in `CONTRIBUTING.md`.

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
  - Workload profiles: Low, Medium, and Heavy.
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
