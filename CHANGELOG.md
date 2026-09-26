# Changelog

All notable changes to the Firefly project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.20.2] - 2026-09-26

### Fixed

- **WARP Engine Reported as `DISABLED` on a Cold Start (`cmd/firefly/main.go`, `internal/transport/warp/manager.go`, `frontend/src/pages/SettingsPage.tsx`)**: The tunnel was only dialled lazily — by the first request through a WARP-egress upstream, or by the first auto-rotation tick a full `-warp-rotate-interval` (5m by default) after boot. A fresh deployment (e.g. a new container with an empty `/etc/firefly` volume) therefore answered `enabled: false` to `GET /api/warp/status` for minutes, the Settings → WARP Engine card showed a perfectly healthy engine as `DISABLED`, and the "Rotate now" button was disabled while `enabled == false`, so the operator could not even force the first session from the UI.
  - `warp.Manager.WarmUp(ctx)` establishes (or restores from `warp_identity.json`) the tunnel during startup under the existing singleflight, so the session is published before the first request instead of on it. `cmd/firefly/main.go` calls it from a background goroutine **after** both rotation observers are registered: it never blocks startup, and it is skipped when automatic rotation is off (`-warp-rotate-interval=0`, no autonomous WARP work).
  - A warm-up failure is never fatal: the tunnel stays lazy, the next request or rotation retries, and the reason is recorded so `Status().Error` explains the badge. Shutdown racing the warm-up is a no-op rather than an error.
  - Frontend: the WARP card now surfaces `error` from the status payload (badge `UNAVAILABLE` with the reason in the title instead of a neutral `DISABLED`, plus a monospace error panel mirroring the Tunnel card), and the action button reads `Start tunnel` / `Rotate now` and stays clickable while no session exists — the endpoint establishes one.
  - `-warp-rotate-interval` is now applied to the manager even when it is `0`. Previously the flag was only forwarded inside the `> 0` branch, so the constructor default (5m) survived and `GET /api/warp/status` kept advertising `auto_rotate_interval_seconds: 300` with a future `next_rotation_at` while no scheduler was running — the dashboard showed a live 5-minute schedule for a rotation that would never happen.
  - Tests (`internal/transport/warp/manager_test.go`): `TestManager_WarmUpEstablishesTunnelOnColdStart` (publishes exactly one session, idempotent on a second call), `TestManager_WarmUpReportsFailureWithoutPublishing`, `TestManager_WarmUpIsNoopAfterClose`, and `TestManager_StatusReportsDisabledScheduleWhenIntervalIsZero`.

## [1.20.1] - 2026-09-26

### Changed

- **Frontend Copy & Comment Standardization to English (`frontend/src/**`)**: Removed all remaining Indonesian (Bahasa Indonesia) text from the React dashboard — user-facing labels, toast notifications, empty states, form placeholders/hints, table headers, dialog copy, keyboard metadata strings, source-code comments, JSDoc blocks, CSS section comments, and inline SVG documentation.
  - Pages: `App`, `BenchmarkPage`, `ChatPage`, `ConsolePage`, `LoginPage`, `ModelsPage`, `OverviewPage`, `ProvidersPage`, `QuotaPage`, `SettingsPage`, `TelemetryPage`, `TenantsPage`, `UpstreamEditorPage`, `UpstreamsPage`, `UsagePage`.
  - Shell/UI components: `AppShell`, `Sidebar`, `Topbar`, `SkyBackground`, `SceneryStrip`, `Badge`, `Controls`, `Drawer`, `QueryGate`, `ToastHost`, and the `upstream/*` tabs (`GeneralTab`, `KeysTab`, `ModelsTab`, `ResilienceTab`, `UpstreamDrawer`).
  - Libraries/services/styles/assets: `lib/router.ts`, `lib/parallax.ts`, `lib/session.ts`, `state/auth.ts`, `registry.tsx`, `services/api.ts`, `styles/global.css`, `vite.config.ts`, `assets/forest.svg`.
  - Documentation accuracy fix: the `services/api.ts` module header and `useSaveSettingsSmart` JSDoc no longer reference the removed `@/data/mock` module; mocks are described as the `MOCK PAYLOADS` block defined inside `api.ts` itself.
  - Purely textual change: no behavioral, routing, DTO-contract, or API changes — identifiers, endpoint paths, and payload shapes are untouched (`npx tsc --noEmit` and `vite build` both pass).

## [1.20.0] - 2026-09-26

### Added

- **Native Cloudflare Tunnel Ingress Engine (`internal/transport/tunnel/`, `cmd/firefly/main.go`, `internal/server/`)**: Built-in support for exposing Firefly remotely through Cloudflare Tunnel without external process managers.
  - Lifecycle strictly tied to Firefly's root context: child process is spawned in background and gracefully terminated on SIGINT/SIGTERM.
  - Supports zero-config ephemeral quick tunnels (`-tunnel quick` or `FIREFLY_TUNNEL=quick`) via `trycloudflare.com`.
  - Supports persistent production named tunnels (`-tunnel named -tunnel-token <TOKEN>` or `FIREFLY_TUNNEL_TOKEN`).
  - Added `GET /api/tunnel/status` endpoint reporting live state, mode, and auto-extracted public URL.
  - Added `TunnelCard` component to frontend `SettingsPage` with real-time status badge (`ONLINE`, `STARTING`, `DISABLED`), copyable public ingress URL, and CLI activation instructions.
  - Integrated into `frontend/src/services/api.ts` with `fetchTunnelStatus` and `useTunnelStatusQuery`.
  - Dynamic runtime lifecycle orchestration: added `Start(mode, token)` and `Stop()` methods to `tunnel.Manager` with `POST /api/tunnel/toggle` endpoint allowing dynamic on-the-fly tunnel starting and stopping without server restart.
  - **Automated `cloudflared` Binary Downloader & Path Resolver (`internal/transport/tunnel/binary.go`, `lifecycle.go`)**:
    - Automatic binary resolution order: checks system `PATH` first, then custom `-tunnel-bin-dir` (or `$FIREFLY_TUNNEL_BIN_DIR`), falling back to user home directory (`~/.firefly/bin/cloudflared`) and local `./data/bin`.
    - Automated zero-config downloader: if `cloudflared` is absent on the host, automatically downloads official releases directly from Cloudflare's GitHub releases (`https://github.com/cloudflare/cloudflared/releases/latest/download/...`) without hitting GitHub API rate limits.
    - Full multi-platform support across Windows (`.exe`), Linux (raw ELF executable with `0755` permissions), and macOS Darwin (extracts binary from official `.tgz` archive via standard library `compress/gzip` and `archive/tar`).
    - Atomically writes to temporary files before replacing, guards against concurrent downloads via single-flight mutex locks, and cleans up on cancellation.
    - UI feedback in `SettingsPage`: displays live `DOWNLOADING` badge and progress indicator when fetching the binary on the first toggle.

  - **Modular Binary Management & Downloader Engine (`internal/binx/`)**:
    - Extracted generic binary discovery, path resolution, atomic downloading, and archive extraction into a shared leaf package (`internal/binx`).
    - Decoupled platform-specific executable naming (`ExecutableName`), fallback directory resolution (`DefaultDir`), multi-stage lookup (`Find`), archive unpacking (`ExtractTarGz`), and atomic staged downloads (`Download`) for reuse across any Firefly module (Cloudflare tunnel, sing-box, harvesters, etc.).
    - Refactored `internal/transport/tunnel/binary.go` to delegate all binary discovery and download mechanics to `binx`, eliminating DRY violations and maintaining strict separation of concerns.

  - **API Error Parsing & HTTP Status Clarity (`frontend/src/services/api.ts`, `internal/server/tunnel_handlers.go`)**: Fixed string error payload parsing in frontend `request()` function to display informative backend errors in toasts instead of generic `Request failed: Bad Request`. Updated server tunnel start failure status code to `500 Internal Server Error` (server host dependency missing) with an in-card installation guide for `cloudflared`.



## [1.19.0] - 2026-09-25

### Added

- **Frontend OAuth Connect Dialog (`frontend/src/components/upstream/OAuthConnectDialog.tsx`, `KeysTab.tsx`)**: Integrated the interactive OAuth onboarding UI into Firefly's active React 19 frontend for supported protocols (`antigravity`, `cline`, `codebuddy_cn`, `codebuddy_intl`).
  - Implements a phase-based state machine (`idle`, `starting`, `waiting`, `success`, `error`) with automatic flow initialization on open.
  - Automatically pops up the provider authorization window and runs a background polling loop (2-second interval) via `pollOAuthStatus`.
  - Fast-path completion via `window.addEventListener('message')` listening for `oauth_complete` broadcast from backend's callback page.
  - Displays connected account metadata (email, Google Cloud companion project ID, and formatted key reference).
  - Automatically injects the new credential into the Upstream's credential pool as `ref: "oauth:<connection_id>"` and `secret: "oauth:<connection_id>"`, matching Firefly's server-side dynamic OAuth vault lookup.
  - Adds conditional "Connect Account" banner with `Link2` icon directly above the manual key input on `KeysTab` when an OAuth-capable protocol is selected.


## [1.18.0] - 2026-09-25

### Fixed

- **Frontend Tailwind utilities not generated (`frontend/src/styles/global.css`)**: Added missing `@tailwind base/components/utilities` directives so Tailwind utility classes (`flex`, `gap-*`, `items-center`, `justify-between`, etc.) are compiled into the CSS bundle. Buttons in `UpstreamDrawer` and `upstream-card` now correctly respect gap spacing.
- **Unstyled form inputs on GeneralTab (`frontend/src/components/upstream/GeneralTab.tsx`, `frontend/src/styles/global.css`)**: Input CSS selectors required explicit `type` attributes but many inputs had none. Added `input:not([type])` selector coverage, `input:disabled` styling, explicit `type` attributes on all inputs, and `mono` class on URL/code fields to match the design system used across other pages.

### Changed

- **Consolidated design documentation**: Merged `frontend-new/DESIGN_RULES.md` into `DESIGN.md` as the single source of truth for the solid design system. Removed the `frontend-new/` scaffolding directory.
- **Updated tailwind.config.ts doc reference** to point to `DESIGN.md` instead of the removed `frontend-new/DESIGN_RULES.md`.
- **Removed audit screenshots**: Deleted obsolete PNG files (`drawer-audit.png`, `upstream-drawer.png`, `upstream-editor-general.png`, `upstream-editor-resilience.png`, `upstream-page.png`) from the repository root.

## [1.17.1] - 2026-09-24

## [1.17.0] - 2026-09-24

### Added

- **Periodic WARP Auto-Rotation Scheduler (`internal/transport/warp/manager.go`, `warp.go`, `cmd/firefly/main.go`)**: Background ticker (`StartAutoRotation` / `autoRotationLoop`, default 5m via `DefaultAutoRotateInterval`) rotates the Cloudflare WARP WireGuard identity and egress IP on a steady timer without dropping active streams. Configurable via `-warp-rotate-interval` / `FIREFLY_WARP_ROTATE_INTERVAL` (`0` disables). Status surface (`GET /api/warp/status`) now reports `auto_rotate_interval_seconds` and `next_rotation_at`, surfaced in Settings → WARP Engine card.
- **Per-Status Key Error Rules (`internal/domain/domain.go`, `internal/config/dto.go`, `builder.go`, `internal/transport/upstream/policy.go`, `internal/storage/turso/`)**: `key_error_rules` binds one HTTP status to its own threshold-counted action (`deactivate` / `delete` / `cooldown` + `cooldown_duration_s`), e.g. `429 → cooldown 5m` and `403 → delete` on threshold 1. A status-matched rule takes precedence over the global `key_error_threshold` / `key_error_action`; unmatched statuses fall back to the legacy path. Validated at build time, persisted in the new `upstreams.key_error_rules` column (with migration), carried through settings sanitization, and editable in the Upstream modal.
- **Health Probe Key-Error Routing (`internal/transport/upstream/health.go`, `cmd/firefly/main.go`)**: Background probe outcomes are routed through the shared `HandleKeyOutcome` policy with a `ports.KeyActionNotifier` sink, so consecutive credential failures (429/401/402/403) advance `ConsecutiveErrors` toward the upstream threshold and persist the action, while success, transport drops, and host 5xx reset the counter per the Layer 1 invariant. Free/public OpenCode slots are never counted.
- **Qoder Free-Tier Model Aliasing (`internal/adapter/qoder/adapter.go`)**: Legacy and client-side spellings (`qmodel_latest`, `qoder-latest`, `qwen-3.8-flash`, `qwen3.8-flash`, `qwen-flash`, `qwen`) normalize to the live free-tier `qfmodel` key before the catalog lookup.
- **Upstream Credential Deduplication (`internal/storage/turso/store.go`)**: `LoadSettings` and `LoadCatalogSnapshot` collapse pool entries sharing the same resolved secret, so canonical provider-bound refs (`<provider>-key-<id>`) win over historical duplicates (e.g. `upstream-key-<id>`) and the ring matches the true distinct credential count.
- **Regression Tests**: `TestManager_AutoRotationPeriodicallyRotates`, `TestHandleKeyOutcome_PerStatusRules_403DeleteImmediately`, `TestHandleKeyOutcome_PerStatusRules_429CustomCooldown`, `TestHandleKeyOutcome_PerStatusRules_FallbackToLegacyWhenUnmatched`, `TestHealthChecker_ThresholdActionOnProbeFailure`, `TestHealthChecker_ProbeSuccessResetsCounter`, `TestForward_FreeTierAliasToQFModel`, and `TestStore_DeduplicateUpstreamCredentialCopies`.

### Changed

- **429-Triggered WARP Rotation Removed (`internal/transport/upstream/attempt.go`, `internal/domain/domain.go`, `frontend/`)**: The `WarpRotator` global, the per-upstream `warp_auto_rotate_on_429` toggle, and the `RotateAsync` throttle path are gone. Time-based rotation replaces the old 429 trigger, which proved unreliable behind envelopes/proxies where the 429 never reached the rotation path. `WarpAutoRotateOn429` remains as a deprecated, ignored DTO field (and the DB column is left for compat) so old configs still decode.
- **403 Now Fails Over (`internal/transport/upstream/policy.go`)**: A 403 without a matching rule rotates to the next key in the ring instead of pinning the request to the dead key, mirroring 401/429 behavior.

### Fixed

- **Qoder Catalog Errors No Longer Masked as 400 (`internal/adapter/qoder/models.go`)**: Non-200 catalog responses return a typed `ErrCatalogHTTP` carrying the status code and a 1 KiB body snippet, so genuine credential errors (401/403/429) flow into Layer 1 failover instead of surfacing as a terminal `400`.
- **Key Threshold Fields Reach the Dashboard (`internal/server/settings.go`)**: The public settings projection now carries `key_error_threshold`, `key_error_action`, `key_cooldown_duration_ms`, and `key_error_rules` so the Upstream modal edits the values the gateway actually enforces.

## [1.16.0] - 2026-09-23

### Added

- **Tools Page With a Tool Sidebar (`frontend/src/modules/7-tools/tools.tsx`, `ToolSidebar.tsx`, `ToolsView.tsx`, `frontend/src/modules/registry.tsx`, `frontend/src/core/state/toolsSlice.ts`)**: The seventh module is Tools now, and it hosts several tools behind a sidebar instead of being one screen. The layout is a registry rather than a hand-written list — `TOOL_REGISTRY` carries each tool's title, description, `order`, icon, lazy component, and preload handler, and the sidebar, the Suspense outlet, and the hover/focus chunk preloads all derive from it, mirroring `MODULE_REGISTRY` so a new tool is one entry with no parallel edit to forget. The sidebar items are real `<button>`s, so keyboard activation, focus rings, and `aria-current` come from the platform rather than from ARIA annotations on divs. The chat tool keeps its behaviour as a pure relocation into `7-tools/chat/`, and the tab, its hotkey, and the settings copy follow the registry to "Tools".
- **Benchmark Tool (`frontend/src/modules/7-tools/benchmark/`)**: A second tool fires N concurrent streaming chat completions through the gateway against one model and prompt, to answer how an upstream behaves under load without a shell. `min(concurrency, requests)` workers pull indices from a shared cursor and every finished request is appended as it lands, so a run is legible while it is still going; TTFT, token count, and end-to-end TPS use the same definitions as the chat waterfall, and `summarize` reports ok/error/aborted counts, an error rate that excludes user-initiated stops, aggregate TPS, and p50/p95 TTFT. Results pair latency bars with a per-request table. The run lives at module scope rather than in React state, so switching tools mid-run — which unmounts the tool — neither loses the run nor strands the Stop button, and it never touches `playgroundIsGenerating`, so chat and benchmark cannot interfere with each other.
- **Shared Tool Primitives (`frontend/src/modules/7-tools/shared/useModelOptions.ts`, `MetricPill.tsx`)**: The model list (grouped by upstream, carrying the default selection) and the small labelled metric card the waterfall used to own are shared by both tools instead of living inside one of them.
- **Regression Tests**: `TestKeyRing_ClearCooldowns`, `TestKeyRing_ClearCooldownsNilSafe`, `TestKeyRing_ResetConsecutiveErrors`, `TestKeyRing_ResetConsecutiveErrorsNilSafe`, `TestReleaseUpstreamKeyPenalties`, `TestReleaseUpstreamKeyPenaltiesDisarmsThreshold`, `TestManager_AutoRotationObserverFiresOnSuccess`, and `TestManager_AutoRotationObserverSkipsFailedRotation`.

### Changed

- **Collapsible Chat Telemetry (`frontend/src/modules/7-tools/chat/ChatTool.tsx`, `TokenStreamWaterfall.tsx`, `frontend/src/core/state/toolsSlice.ts`)**: The waterfall and the raw-packet inspector collapse behind a toolbar toggle instead of permanently occupying a column, so the transcript takes the full width when the panels are not being read. The toggle carries `aria-expanded`/`aria-controls` over a stable panel id, and the collapse transition honours `prefers-reduced-motion`.

### Fixed

- **429 Penalties Outliving the Rotation That Rescued Them (`internal/domain/domain.go`, `internal/transport/upstream/warp_penalties.go`, `internal/transport/warp/manager.go`, `cmd/firefly/main.go`)**: An automatic WARP rotation is triggered by a 429 that was charged to the key slot, but the free-tier quota behind that 429 belongs to the egress IP the rotation just replaced — so the slot idled for up to the cooldown cap while a healthy identity was already live (measured 302.5s of a 300s cap in the e2e sandbox, i.e. the rotation bought nothing until the penalty expired), and an IP-bound 429 storm could slowly arm the `KeyErrorThreshold` deactivate/delete action against a healthy key. The rotation observer now reports the upstream it rescued and `ReleaseUpstreamKeyPenalties` clears both penalties from the live snapshot: `KeyRing.ClearCooldowns` and `KeyRing.ResetConsecutiveErrors`. Counting is honest — nothing zeroes a cooldown field once it lapses, so an already-expired deadline is released but not reported as a rescued key, otherwise the logged `keys_cleared` would overstate what a rotation achieved. Revocation is deliberately untouched: a 401 condemns the credential, not the IP, and resetting the counter cannot undo a threshold action that already fired.
- **Benchmark Requests Holding Their Socket (`frontend/src/modules/7-tools/benchmark/benchmarkRunner.ts`)**: The stream reader was never cancelled, so every finished request left its connection for the garbage collector to reclaim — a run of N concurrent requests could then have its later requests throttled by the browser's per-host connection cap, skewing the very latencies the tool exists to measure. The read loop now releases the reader in a `finally` on every exit path — error, abort, and `[DONE]` alike — without draining the frames that follow `[DONE]`.
- **Order-Dependent OAuth Refresh Test (`internal/security/oauth/refresher_test.go`)**: The refresh sweep reads its work list from `Store.List`, which iterates a map, so which due connection gets served first is not deterministic. The cancellation test hard-coded `conn-1` and timed out whenever `conn-2` was served first (measured 4/20 then 1/40 runs with only that test selected) — this is what turned CI red on `73b19dd`. The mock now reports the connection it actually served and the assertion follows it, and it holds the provider until the sweep's cancellation has landed, so a regression of the detached-flight invariant fails on every run instead of by luck. Test-only; no production behaviour changed, and the two docs that had recorded this as an environment/timing flake were corrected in the same commits.

## [1.15.0] - 2026-09-22

### Added

- **Key Error Policy Tab (`frontend/src/modules/2-upstreams/UpstreamModal.tsx`)**: The Consecutive Key Error Resilience Policy moved out of the Load Balancing tab, where it read as part of the balancing story even though it governs per-key lifecycle actions (deactivate/delete) that are independent of host routing and key strategy. It now has a dedicated "Key Error Policy" tab between Load Balancing and Timeouts & Sockets, carrying an Active/Disabled chip in the tab strip so the state is legible without opening the tab; the policy markup moved verbatim and only its wrapper changed. The sixth tab also exposed a latent layout bug — the strip overflowed its 749px by about 51px and the buttons' default `flex-shrink: 1` compressed the labels onto two lines instead of scrolling — so the tab buttons now carry `shrink-0 whitespace-nowrap` and the strip scrolls only on narrow viewports.
- **Regression Tests**: `TestNextSyncInterval`, `TestSyncerConfigDefaults`, `TestUsageFlusher_MeteringPushQuietWindow`, and `TestStampVersionLineEndings`.

### Changed

- **Adaptive Turso Sync Pacing (`internal/storage/turso/syncer.go`, `flusher.go`, `internal/server/turso_manager.go`, `internal/config/turso.go`)**: The syncer pulled every 15s forever — 5,760 pulls/day from an idle gateway — and the usage flusher pushed metering writes every 30s (up to 2,880/day under traffic) on top of the per-mutation pushes the store already does. The pull is now adaptive: a base interval (`DefaultSyncInterval`, 60s) doubled per idle cycle up to `DefaultSyncMaxInterval` (5m). A cycle that observed a catalog change resets to base so a burst still propagates promptly, and a failed cycle keeps the current cadence instead of stretching the gap. Metering pushes are throttled to one per 5m; structural writes (revocations, deactivations, deletes) still push immediately, because a stale cloud row would resurrect a dead credential on the next pull. Deferring the metering pushes is safe because tursogo's `Pull` rebases unpushed local writes on top of remote changes rather than discarding them. Idle pull load drops from 5,760 to about 288/day. Both intervals resolve flag > env > `turso.json` > default — pacing stored in `turso.json` used to be ignored whenever credentials arrived from the environment, the sync manager hard-coded 15s regardless of configuration, and the dashboard's Turso form built a fresh DTO on save that silently wiped the operator's pacing.

### Removed

- **Harvester Machine Endpoint (`internal/server/harvester_api.go`, `service_auth.go`, `internal/server/openapi/openapi-harvester.yaml`)**: `POST /api/harvester/sync`, its dedicated service token (`-harvester-token` / `FIREFLY_HARVESTER_TOKEN`), and its OpenAPI spec are gone. The external harvester writes through the operator surface instead, authenticated with the admin token: `POST /api/providers/{id}/keys` reaches the same store path with the same three-intent `expires_at` and the same write-only `account_metadata`, and `PATCH /api/keys/{id}` covers status changes and secret rotation. One surface means one validation pass, one guard, and one spec; the price is a per-provider batch and mass deactivation as N `PATCH` calls, since there is no `deactivate_keys` list any more. The store engine behind the deleted endpoint (`SyncProviderKeys`) is gone with it — the batch path is `UpsertProviderKeyRecords`.

### Fixed

- **OpenAPI Version Stamp on a CRLF Checkout (`internal/server/openapi.go`)**: `infoVersionRe` could not match a line ending in `\r`, so on a checkout with `core.autocrlf=true` the pattern matched nothing and `SetBuildVersion` silently left the embedded `0.0.0-dev` placeholder in the served document — a release built from such a checkout served an unstamped `info.version` while an LF build served a stamped one. The pattern now tolerates an optional `\r` and the splice puts a consumed `\r` back, so the version is rewritten without rewriting the document's line endings.
- **Fake Overview Latency, Unbounded Motion, and Low-Contrast Labels (`frontend/src/modules/1-overview/OverviewView.tsx`, `frontend/src/index.css`, `frontend/src/core/layout/Shell.tsx`, `frontend/src/core/canvas/*`)**: The overview no longer prints a hard-coded 184ms latency figure as if it were measured. Every decorative animation (aurora drift, stat glow, breathing live dot, lofi bars, shooting stars) honours `prefers-reduced-motion`, while spinners and short hover/focus transitions stay because they communicate state. The idle upstream labels on the canvas clear the contrast floor.
- **Windows-Only Test Noise (`internal/config/files_test.go`, `internal/server/settings_test.go`, `internal/server/tenants_admin_test.go`)**: A `skipUnlessPosixModes` helper now skips the permission-bit assertions on Windows, where `os.Chmod` cannot represent POSIX modes and every file reports `0666`, so a Windows working copy no longer fails tests that assert an unrepresentable platform behaviour.
- **Rejected Key Batches Reporting Rolled-Back Counters (`internal/storage/turso/provider_store.go`)**: `UpsertProviderKeyRecords` returned the counters and ids it had accumulated when a later entry failed the batch, even though the transaction rolled every write back — a caller logging the result on error described rows that never existed. A failed batch now reports the empty result, and the doc comment states that contract.

## [1.14.0] - 2026-09-22

### Added

- **Registry-Driven Navigation (`frontend/src/modules/registry.tsx`, `frontend/src/modules/types.ts`, `frontend/src/core/layout/Header.tsx`, `frontend/src/hooks/useKeyboardShortcuts.ts`)**: The header tabs, the digit hotkeys, and the routed module all derive from `MODULE_REGISTRY` now: `TabId` is `keyof typeof MODULE_REGISTRY`, `NAV_TABS` projects `title` + `order`, and the hotkey map is built from the same ordering. Three hand-maintained lists that had to be edited in lockstep — the tab union, the nav labels, and the `1`–`8` key map — are gone, so a new module is one registry entry with no parallel edit to forget.
- **Project Links (`frontend/src/core/constants.ts`, `frontend/src/core/layout/Header.tsx`, `Shell.tsx`)**: The repository and a Trakteer support page are reachable from the footer at every width, from icon buttons in the header from `xl` upward, and from the mobile navigation menu. Both URLs live in `core/constants.ts`, and while `SUPPORT_URL` is empty the support entry points are not rendered at all rather than linking nowhere.
- **Credentials Table Pagination (`frontend/src/modules/8-providers/ProviderKeyTable.tsx`, `ProvidersView.tsx`)**: A credential pool can hold thousands of rows — the harvester syncs one per account — and the table grew with the pool, dragging the page with it. It now pages inside a fixed-height, independently scrollable viewport with a pinned header. Paging is derived and clamped rather than stored, so a shrinking pool cannot strand the operator on a page that no longer exists; switching provider or page size resets to the first page, and the footer reports the visible range.
- **Regression Tests**: `TestHandleKeyOutcome_TypedNilNotifierIsSkipped`, `TestWatcherPollDoesNotReloadWithoutChanges`, `TestPollFingerprintTracksConfigFiles`, `TestWatcherPollReloadsOnMetadataTouch`, `TestTelemetry_GlobalAdmissionFallbackUsesAdmissionGauge`, the `TestFileProviderStore_*` group (listing, masking, refused mutations, masked-hint rejection, unknown ids), `TestProviderStoreProvenance`, `TestStablePositiveID`, `TestProvidersAdmin_TursoStoreTakesPrecedence`, `TestBuildAndStoreCarriesTokenSaver`, and `TestLoadCatalogSnapshot_CarriesTokenSaver`; `TestMain` in `internal/server` and `internal/storage/turso` isolates the native library cache so the two test binaries can no longer race on one extraction path.

### Fixed

- **Key Error Policy Panicking on a Typed-Nil Notifier (`internal/transport/upstream/policy.go`)**: The guards in `HandleKeyOutcome`/`applyKeyAction` used a bare `!= nil`, which a typed-nil usage flusher passes — file-config mode injects exactly that — so the first `NotifyKeyAction` dereferenced a nil receiver: a threshold `429` reached the client as a recovered `500`, the request log line was skipped, and the history row stuck at status `0`. All three guards now use the package-local `isNil` (the check `httpx.IsNil` performs; `httpx` cannot be imported there without an import cycle).
- **Watcher Rebuilding an Idle Catalog Every Poll Tick (`internal/watch/watch.go`, `internal/config/source.go`)**: The poll tick signaled a reload unconditionally, so an idle gateway rebuilt its whole catalog — every `KeyRing` included — at least once per interval, wiping cooldowns and any key the error threshold had deactivated. The tick now signals only when the `name|size|mtime` fingerprint moves, and an fsnotify hit refreshes that baseline so one change never reloads twice. `config.ConfigFileNames()` became the single source of truth shared by the fsnotify filter and the fingerprint.
- **Telemetry Reporting Admission Occupancy From the Wrong Gauge (`cmd/firefly/main.go`, `internal/server/telemetry.go`)**: `RouterDeps.GlobalLimiter` was never set, so the handler fell back to the total HTTP in-flight gauge — which also counts the admin and telemetry endpoints that bypass admission, the long-lived events stream included — and reported it as admission occupancy. The limiter is now built once in the entrypoint and shared by the middleware chain and the handler, and `active_streams` is the sum of per-credential in-flight counters with no fallback.
- **Provider Surface Unavailable in File-Config Mode (`internal/server/providers_file.go`, `providers_admin.go`, `frontend/src/modules/8-providers/*`)**: `/api/providers*` required Turso storage and answered `503` without it, so the dashboard's provider catalog was dead in file-config mode. A read-only store now projects the live snapshot (one provider per credential-carrying upstream, one key per ring slot) with ids hashed from the natural keys so dashboard URLs stay stable across reloads; the list response carries `storage`/`read_only`, every mutation answers `501` naming `upstreams.json`, and the dashboard renders that mode as a read-only catalog with no edit controls.
- **Token Saver Missing From Every Rebuilt Snapshot (`internal/registry/registry.go`, `internal/storage/turso/store.go`)**: `domain.WithTokenSaver` was never passed by `registry.BuildAndStore`, which runs at startup and on every watcher reload, and `turso.LoadCatalogSnapshot` never put the settings blob's TokenSaver back into the `FileSet` either — so tool-output compression, brevity injection, and context pruning silently stopped applying after a reload and never applied in a database-backed deployment.
- **Test Binaries Racing on One Native-Library Cache (`internal/storage/turso/libcache.go`)**: `go test ./...` runs the two packages that open the `turso` driver directly as parallel test binaries, and neither goes through `NewClient` — the one place that repoints the loader — so both extracted the embedded shared object to the same path, and whichever hashed it while the other was still copying panicked with `unable to load turso library`. `turso.IsolateNativeLibraryCache()` gives every test binary its own cache directory under the OS temp dir; an explicit `TURSO_GO_CACHE_DIR` is left alone.

### Changed

- **Desktop Navigation Starts at `lg`, and the Tab Strip Scrolls (`frontend/src/core/layout/Header.tsx`)**: Between 768px and 1024px the header overflowed the viewport — a page-level horizontal scrollbar with the pills wrapped onto two lines — because the full-size navigation needs about 1140px. Desktop navigation now begins at `lg` with a burger below it, runs compact at `lg` (12px tabs, 16px gaps, short music label) and full at `xl` (14px, 24px gaps), the tab strip scrolls horizontally with its scrollbar hidden, and the brand and pills neither shrink nor wrap. Measured after the change: no page overflow at 640/768/900/1024/1280/1440, single-line pills throughout, and ~19px of spare header width at 1024 (icon links start at `xl`, where the pair still leaves ~28px at 1280).

## [1.13.0] - 2026-09-22

### Added

- **Native Provider & Key Ownership (`internal/storage/turso/schema.go`)**: Firefly now creates the `providers` and `api_keys` tables itself, so a fresh deploy bootstraps its catalog with no external harvester and no manual DDL. The migration adopts tables a harvester already created, is idempotent, preserves existing rows and `id` values, and leaves foreign harvester tables untouched.
- **Harvester Machine Endpoint (`internal/server/harvester_api.go`, `internal/server/service_auth.go`)**: `POST /api/harvester/sync` applies a batch of providers and keys in one transaction. Authentication is a dedicated service token (`-harvester-token` / `FIREFLY_HARVESTER_TOKEN`) compared in constant time and kept separate from the admin token, so an automation credential never carries dashboard access; with no token configured the endpoint fails closed with `503`. A replay is idempotent — rows upsert by natural key, every `id` stays stable, no duplicate rows appear, and the response carries hints (never secrets). A batch is all-or-nothing, a key whose `provider_id` is not in the same batch is rejected, and an unrecognised status is stored inert rather than refused.
- **Operator Provider API (`internal/server/providers_admin.go`)**: `GET|POST /api/providers`, `GET|PUT|DELETE /api/providers/{id}`, `GET|POST /api/providers/{id}/keys`, `PATCH|DELETE /api/keys/{id}` — the catalog is fully manageable from the dashboard without database access. Reads return a masked `api_key_hint` only and never `account_metadata`, which stays write-only. `PATCH /api/keys/{id}` carrying an `api_key` rotates the secret in place while preserving the row `id`, so credential refs (`<upstream>-key-<id>`) and their `upstream_credentials` mirrors stay bound to the same key; routability patches mirror onto those bound rows. Deleting a provider that an upstream still references is refused.
- **Providers Dashboard (`frontend/src/modules/8-providers/`)**: A new tab (order 8, hotkey `8`) lists providers beside a per-provider key table — create/edit/delete providers, batch key upsert, patch status/routability/expiry, and rotate a secret. The browser only ever holds hints, the modals refuse a masked value as a credential, and expiry inputs normalise legacy second-scale `expires_at` values so they no longer read as permanently expired.
- **Bind Provider Keys (`frontend/src/modules/2-upstreams/UpstreamModal.tsx`)**: Replaces the "Import from Database" flow — the server pools a provider's credentials into the upstream's credential list, so a raw secret never has to reach the browser to be bound.
- **Harvester OpenAPI Spec (`internal/server/openapi/openapi-harvester.yaml`)**: The machine endpoint gets its own spec, mounted with the same build-version stamping as the others and covered by drift tests asserting that it documents every guarded route, exposes no secret-bearing field, and offers no delete.
- **Regression Tests**: `TestMigrateSchema_AdoptsHarvesterPoolTables`, `TestMigrateSchema_IdempotentAndPreservesRows`, `TestMigrateSchema_LeavesForeignHarvesterTablesUntouched`; the harvester endpoint matrix (`TestHarvesterSync_AuthMatrix`, `TestHarvesterSync_ReplayIsIdempotent`, `TestHarvesterSync_RejectsInvalidBatches`, `TestHarvesterSync_RejectsKeyProviderMismatch`, `TestHarvesterSync_StoresUnknownStatusAsInert`, `TestHarvesterSync_AppliesBatchAndReloadsCatalog`); the operator surface (`TestProvidersAdmin_ProviderCRUDRoundTrip`, `TestProvidersAdmin_KeyLifecycle`, `TestProvidersAdmin_RotateSecretKeepsIdentityAndMasksHint`, `TestProvidersAdmin_NeverLeaksSecretsOrMetadata`, `TestProvidersAdmin_DeleteProviderRefusedWhileReferenced`, `TestProvidersAdmin_BatchExpiryIntentSurvivesTheWire`, `TestProvidersAdmin_PatchMirrorsRoutabilityOntoBoundCredentials`, `TestLegacyProviderKeysSurfaceReturnsHintsOnly`); the sync-loop guard in `internal/storage/turso/syncer_test.go` (a failed catalog load keeps the previous snapshot serving); and `TestEnsureLocalDirIsOwnerOnly`, `TestWriteSecretFileIsOwnerOnly`, `TestEnsureConfigFilesPermissions` for the on-disk permissions.

### Security

- **World-Readable Credentials on Disk (`internal/config/files.go`, `internal/server/catalog_files.go`, `internal/storage/turso/client.go`)**: `configs/upstreams.json` (raw provider keys) and `configs/tenants.json` (gateway keys) were created and re-saved at `0644`, readable by every local account, and the local libSQL replica directory was `0755`. Credential files are now written `0600` through one shared writer, pre-existing files are tightened on startup and on every save — both the settings save and the tenant CRUD save, which had already drifted apart — and the database directory is `0700`. `os.WriteFile` applies its mode only when it creates the file, which is why pre-existing files need an explicit `Chmod`.
- **Raw Secrets Returned by Legacy Read Surfaces (`internal/server/settings.go`)**: `/api/turso/providers/{id}/keys` and `/api/turso/keys` returned the stored `api_key` verbatim to the dashboard. Both now return `api_key_hint` only, which is all the UI ever needed.
- **Masked Hints Accepted as Credentials (`internal/server/providers_admin.go`, `frontend/src/lib/secret.ts`)**: A value shaped like a hint (`xxx...yyy`, `[REDACTED]`) is now refused on every operator write surface — batch upsert and `PATCH` alike. A surface that only ever displays hints will eventually echo one back, and persisting it used to mint a credential that fails upstream on every use.

### Changed

- **`expires_at` Carries Three Intents (`internal/storage/turso/provider_store.go`)**: All three write surfaces (machine batch, operator batch, operator patch) now distinguish absent (keep the stored value), explicit `null` (clear), and a number (set, in milliseconds); a non-positive or second-scale value is rejected instead of silently stored. Previously a writer that simply omitted the field could erase an expiry it did not own.
- **Usage Metering Never Advances the Reload Signal (`internal/storage/turso/provider_store.go`)**: Structural changes (key added, removed, revoked, deactivated) bump `catalog_revisions`, while `total_requests` / `last_used_at` writes leave `updated_at` alone. Metering that advanced the signal would make every node rebuild its catalog on each sync interval under active traffic.
- **Provider/Key Store Code Relocated (`internal/storage/turso/provider_store.go`, `store.go`)**: The provider and key persistence methods move out of `store.go` (roughly 100 lines lighter) into the file that owns that surface. Behavior is unchanged; the sync transaction also pushes the committed write to the cloud replica and reports `pushed` in its response.
- **One Catalog Writer (`internal/server/catalog_files.go`)**: The settings save and the tenant CRUD save each carried a copy of the five-file catalog write loop. Both now call a single writer, so the credential permissions cannot diverge again.
- **Frontend Helpers Deduplicated (`frontend/src/lib/{datetime,format,secret}.ts`)**: Epoch normalisation, date-input conversion, number/currency formatting, and secret masking were re-implemented per module and the masking shape differed from the server's. They are shared now, and `maskSecret` matches the server byte for byte and is idempotent, so an already-masked value (settings DTO) and a raw one (KeyRing snapshot) both render correctly.

## [1.12.2] - 2026-09-21

### Security

- **Canvas Tooltip Stored XSS (`frontend/src/core/canvas/useFireflyPhysics.ts`)**: The overview tooltip interpolated the operator-supplied upstream name and base URL into `innerHTML`, so an upstream named `<img src=x onerror=...>` executed script in the dashboard origin, where the admin session token lives. The tooltip is now assembled from text nodes, so the same markup renders as inert text.

### Fixed

- **Render Loop Frozen After a Background Load (`frontend/src/core/canvas/useFireflyPhysics.ts`)**: The animation loop sampled `document.hidden` once, so a tab opened in the background started paused and never resumed. A `visibilitychange` listener now clears the flag and resets the frame clock, so the loop resumes without a time jump.
- **Global Space Shortcut Swallowing Button Activation (`frontend/src/hooks/useKeyboardShortcuts.ts`)**: The shortcut handler called `preventDefault()` on every Space keypress, which broke activation of every `<button>`, `<summary>`, and link in the dashboard. It now steps aside for elements that activate on Space natively, including `role="button"`.
- **Playground Stream Outliving Its Component (`frontend/src/modules/7-playground/ChatWindow.tsx`)**: Unmount cancelled an animation frame that a previous pass had already removed while leaving the `AbortController` untouched, so the reader kept draining a response nobody rendered and the shared "generating" flag stayed set across tabs. The controller is now aborted on unmount.
- **Stream Completion Decided by the Request Flag (`frontend/src/modules/7-playground/ChatWindow.tsx`)**: Non-streaming mode was detected from the request's `stream` flag, so an upstream answering a streaming request with a buffered JSON body was parsed as SSE. Detection now uses the response `Content-Type`. Reading stops at the `[DONE]` frame instead of waiting for the socket to close, a mid-stream `error` frame surfaces as a failed request rather than a silently truncated reply, and a user-cancelled stream keeps its partial reply and metrics.
- **Empty Assistant Turns Rejected by Providers (`frontend/src/modules/7-playground/ChatWindow.tsx`)**: A stream aborted before its first token left an empty assistant message in the history, which providers reject as an empty content block on the next turn. Empty assistant turns are filtered out of the outgoing request.
- **Quadratic Timing Updates on Long Streams (`frontend/src/modules/7-playground/ChatWindow.tsx`)**: Live metrics copied the entire chunk-timing array on every read batch, so cost grew with the square of the reply length. Updates are throttled to ~10 fps with one forced flush at the end.
- **Telemetry Gauge Blanked by a Zero Capacity (`frontend/src/modules/5-telemetry/InflightSemaphoreGauge.tsx`)**: A zero or absent `maxSlots` divided by zero, turning the ratio into `NaN` and blanking the arc and its label.
- **Cooldown Heatmap Duplicate Keys and Blank Rows (`frontend/src/modules/5-telemetry/CooldownHeatmap.tsx`)**: The slot ref served as both React key and row label; refs that are empty (or repeat) produced duplicate keys and unlabelled rows. Empty refs now fall back to a positional label.
- **Mislabeled Error Rate Card (`frontend/src/modules/5-telemetry/TelemetryView.tsx`, `frontend/src/modules/5-telemetry/PercentileLatencyCard.tsx`)**: The card counts 4xx and 5xx but was labelled "5xx Circuit Trips"; the usage table keyed its rows with an array index alongside tenant and model, and the latency card's per-upstream quantiles — which make every model on an upstream report identical P50/P90/P99 — are now stated in a footnote.
- **Tenant Top-Up Contract Mismatch (`frontend/src/modules/4-tenants/TopupModal.tsx`, `frontend/src/services/schema.ts`)**: The modal sent a `key_hash` the server does not accept and the response DTO described a nested `tenant` the server never returns, so the store update on success silently never ran. Request and response now mirror `server.TenantTopupResponse`.
- **Credential Refs Aliasing Foreign `api_keys` Rows (`frontend/src/modules/2-upstreams/UpstreamModal.tsx`)**: Refs minted for keys without a database row ended in `-key-<position>`, and the backend derives the DB key id from exactly that suffix — so a positional number aliased a real row of another provider, and usage metering plus the key lifecycle actions (deactivate, delete) targeted it. Local refs now carry a `-local-` infix and a monotonic sequence, so the id resolves to `0` until the credential is re-anchored.
- **Duplicate Credential Rows Accumulating in `upstream_credentials` (`internal/storage/turso/store.go`)**: Every save that imported a pool minted a fresh ref family, so rows left by the previous save never collided on `UNIQUE(upstream_id, ref)` and accumulated without bound — `LoadCatalogSnapshot` then re-read all of them, turning one upstream into thousands of duplicate slots. A save that carries a pool is now authoritative for that upstream's active credentials and deletes the orphans; rows whose secret is still replicating and deactivated tombstones are preserved, and a save without a pool never prunes.
- **Masked Placeholders Persisted as Credentials (`internal/server/settings.go`)**: `restoreMaskedSecrets` resolves a masked pool entry by ref, then by mask equality, then by position; when all three missed, the mask itself was written back as the credential and minted a slot that fails upstream with 401 on every use. Entries still masked after every attempt are now dropped, with `api_keys` filtered in lockstep, and a live-secret check keeps a real token containing `...` from being mistaken for a placeholder.
- **Dashboard Header Reporting a Stale Version (`frontend/src/core/constants.ts`)**: The v1.12.1 release commit carried only the changelog entry, so the frontend `APP_VERSION` was never bumped and the header still displayed v1.12.0.

### Changed

- **Dead Frontend Code Removed (`frontend/src/lib/crypto.ts`, `frontend/src/services/api.ts`, `frontend/src/services/schema.ts`)**: The zero-dependency SHA-256 engine (Web Crypto path plus its pure-TypeScript FIPS 180-4 fallback), which no longer had a caller — tenant key hashing happens server-side; the unused breakers and history fetchers, the parallel prefetch nothing called, and a `TelemetrySnapshot` type matching no response.
- **Existing Duplicate Credentials Are Not Cleaned Retroactively**: The prune applies to saves made from this version onward. Orphan rows already in the database are removed by the next save that carries a pool, so operators upgrading with a bloated upstream should open it once, import from the database, and save.

### Added

- **Regression Tests**: `TestStore_SaveSettings_PrunesOrphanCredentials` (stale rows removed while unreplicated and deactivated rows survive, sibling upstream untouched, empty-pool save is a no-op); `TestRestoreMaskedSecretsDropsUnresolvablePoolEntries` covering resolvable masks, orphan drops with lockstep `api_keys` filtering, secret-less OAuth entries, live secrets that merely look masked, and the misaligned-payload guard.

## [1.12.1] - 2026-09-21

### Fixed

- **Mid-Rune Tail Cut Still Emitting Invalid UTF-8 (`internal/tokensaver/rtk.go`)**: `safeTail` was handed a pre-sliced tail (`s[len(s)-tailLen:]`), which defeated the rune-boundary alignment it performs: the cut had already landed inside a multi-byte rune, so the truncated output was invalid UTF-8 and corrupted downstream JSON bodies. The whole string is now passed in and the alignment happens on the final tail. Regression test covers 2/3/4-byte rune pads at several caps.
- **WARP Shutdown Racing Session Publication (`internal/transport/warp/manager.go`)**: A drain registered after `Close` had begun waiting on `drainWG` is WaitGroup misuse ("WaitGroup is reused before previous Wait has returned") and panics the process instead of shutting down. Publication and shutdown are serialized by `publishMu`: `installSession` publishes and registers its drain under the lock, and `Close` holds the lock while it marks the manager closed and swaps the active session out, then waits for drains that can no longer be registered behind it.
- **Closed Manager Handing Back a Session It Just Closed (`internal/transport/warp/manager.go`)**: `installSession` returned the session it had just closed, so a publish that lost to shutdown was reported to the caller as a successful rotation. It now returns `nil`; `ensureSession` and `rotate` translate that into `errClosed`, and a dial that races shutdown fails fast instead of registering a device only to discard it.
- **OAuth Refresh Canceled by a Caller That Walked Away (`internal/security/oauth/manager.go`)**: The refresh flight inherited the initiating caller's context, so a client disconnecting mid-refresh canceled work every other waiter was blocked on — the token was never rotated and the next request re-entered the same refresh. The flight body now runs on `context.WithoutCancel` with its own two-minute timeout, while callers still stop waiting on their own cancellation.

### Changed

- **Shared Helpers Extracted (`internal/textx`, `internal/singleflightx`)**: Rune-safe `Head`/`Tail` and whitespace-collapsing `Excerpt` replace the copies carried by the token saver and the protocol adapters (six call sites, three different caps, each now a named constant). `singleflightx.Group.Do` wraps `DoChan` with the "honor the caller's context, never cancel the shared flight" semantics that OAuth refresh and WARP session establishment had each hand-rolled.
- **Token Saver Defaults Centralized (`internal/domain/tokensaver.go`)**: `DefaultMaxToolOutputChars` and `DefaultContextThreshold` were written twice — once in the domain config constructor, once as a `tokensaver` constant — and could drift apart silently. They now have a single definition.
- **WARP Egress Predicate Unified (`internal/transport/warp/proxy.go`, `internal/transport/upstream/client.go`)**: The `"warp"` egress mode was compared with slightly different `EqualFold`/`TrimSpace` spellings in two packages; `warp.IsWarpEgress` is now the only check. The unused `Pool.SetWarpManager` setter is removed.

### Added

- **Regression Tests**: `TestSmartTruncateKeepsUTF8Tail`; WARP `TestManager_ConcurrentPublishAndClose`, `TestManager_InstallAfterCloseReturnsNil`, and `TestManager_EnsureSessionFailsFastWhenClosed`; `TestManager_RefreshTokenSurvivesCallerCancel`; plus unit suites for the new `textx` and `singleflightx` packages.

## [1.12.0] - 2026-09-21

### Fixed

- **WARP Cold-Start Thundering Herd and Orphaned Tunnels (`internal/transport/warp/manager.go`)**:
  - Session establishment (`ensureSession`) moved inside the single-flight group. Previously the first N concurrent requests each performed a full Cloudflare device registration and built their own `device.Device`, and every loser of the publish step was never closed — leaking tunnels and burning registration quota.
  - Publication is now a CAS swap (`installSession`): a session that lost the race is closed immediately instead of being orphaned, and the identity/telemetry of a rotation are persisted only when the built session is the one now active.
  - The saved-identity restore path is coalesced on its own flight key, so a restart under load performs one registration and one probe rather than one per caller.
  - Rotations run on a manager-owned context instead of a caller's, so a client cancelling its request can no longer abort a rotation that other requests are waiting on; a bailing caller leaves the shared flight running (`DoChan` + `select` on `ctx.Done()`).
- **WARP Drain Severing Live Streams (`internal/transport/warp/manager.go`)**: Replaced the blind post-rotation sleep (which closed the previous tunnel after a fixed grace period regardless of what was still flowing) with a per-session connection refcount. A superseded session now closes when its last connection finishes, when the grace window expires, or when the manager shuts down — whichever comes first — so long-lived SSE generations survive a rotation.
- **Missing Rotation Throttle on the 429 Path (`internal/transport/warp/manager.go`)**: Every upstream `429` could trigger a fresh device registration. `RotateAsync` is now gated by a CAS-claimed minimum-interval window (`DefaultMinRotateInterval`, 60s) so a burst of rate-limit responses yields one rotation, while the operator-forced `Rotate` entry point stays unthrottled.
- **WireGuard Device Logs Bypassing Secret Redaction (`internal/transport/warp/manager.go`)**: The `device.Logger` wrote straight to stdout, escaping the `RedactHandler` and violating the zero-leaking-secret invariant. Device chatter is now routed through `slog` (`Verbosef` at debug, `Errorf` at warn).
- **Internal Tunnel Address Published as the Egress IP (`internal/transport/warp`)**: `public_ip` was filled with the WARP-assigned `172.16/12` address whenever the edge probe had not run, misreporting the egress identity to the dashboard and to anything keying off it. The two addresses are now distinct fields, and the public one stays empty until the Cloudflare edge actually reports it.
- **Edge Trace Probe Hardening (`internal/transport/warp/manager.go`)**: Removed `InsecureSkipVerify` from the probe that returns egress identity, added an HTTP status check, bounded the response read to 8 KiB, and closed idle connections on the throwaway transport so repeated probes no longer leak sockets.
- **Silently Discarded WARP+ License Errors (`internal/transport/warp/client.go`)**: The license-attach result was dropped on the floor. Failures are now recorded on the registration response and logged as a warning; the account URL is derived from the registration endpoint rather than hardcoded, so a non-default registration target is honored end to end.
- **Non-Atomic WARP Identity Write (`internal/transport/warp/manager.go`)**: The identity file is now written via `CreateTemp` in the destination directory, `Chmod 0600`, `Sync`, `Close`, then `Rename`, with the directory created `0700`; every step is logged on failure instead of being swallowed. Loading validates the private-key length and peer list before trusting a cached identity.
- **Zero Rotation Time Serialized as Year 1 (`internal/transport/warp/warp.go`)**: `omitempty` never fires on a `time.Time` struct, so `last_rotated_at` published `0001-01-01` before the first rotation; switched to `omitzero`.
- **Warm Keep-Alive Sockets Surviving a Rotation (`internal/transport/upstream/client.go`, `cmd/firefly/main.go`)**: A rotation installed a new egress address but pooled transports kept dialing from the old one, so the new IP was never actually used. The pool now records which upstreams egress through the tunnel and `CloseIdleWarpConnections` drops only their idle sockets, wired to the manager via `SetRotationObserver`.
- **Manager Shutdown Reaping (`internal/transport/warp/manager.go`)**: Added an idempotent `Close` that cancels background work, closes the active session, and waits for drain goroutines, so tunnels and goroutines no longer outlive shutdown.
- **Unit Suite Reaching the Public Cloudflare API (`internal/transport/warp/warp_test.go`)**: A coalescing test ignored its own `httptest` server and performed a live registration while asserting nothing. It is deleted and its behavior is now covered by injected-endpoint tests; the remaining live test is explicitly labeled and quiet.
- **RTK Progress-Line Dedup Losing Distinct Output (`internal/tokensaver/rtk.go`)**: The progress regex was used as the sole dedup classifier, so consecutive lines that merely _looked_ like progress (`Downloading a.wav` then `Downloading b.wav`) were discarded. A new `progressSkeleton` classifier collapses only runs sharing the same skeleton, always keeps the final line, preserves the terminator, and skips the rewrite when it would not save bytes.
- **`SmartTruncate` Exceeding Its Own Cap (`internal/tokensaver/rtk.go`)**: The truncation marker was appended after the limit was applied, so output could surpass `DefaultMaxToolOutputChars` and compression could enlarge a payload. Marker bytes are now reserved up front, making the cap a hard guarantee.
- **Mid-Rune Truncation Emitting Invalid UTF-8 (`internal/tokensaver/rtk.go`)**: Head and tail cuts ignored rune boundaries, producing malformed UTF-8 that corrupted downstream JSON bodies. Both helpers are now rune-boundary safe.
- **Git Diff Compaction Destroying Patch Semantics (`internal/tokensaver/rtk.go`)**: Omitted hunks left no context. The compactor now retains two context lines on each side with an explicit omission marker naming the line count.
- **Headroom Growing Messages and Corrupting Non-String Content (`internal/tokensaver/headroom.go`)**: Pruning could return a payload larger than the original and attempted to rewrite `content` values that are not strings. Both are now guarded, and stray newlines from pruned joins are gone.
- **Persona Injection Stringifying Structured System Content (`internal/tokensaver/persona.go`)**: Array-valued `content` (content blocks) was flattened to a string, destroying its structure. Personas are now appended as a separate system message, and a directive-presence guard prevents double injection.
- **Compression Pipeline Ordering (`internal/tokensaver/tokensaver.go`)**: The stages now run RTK → Headroom → Personas, so tool output is structurally compressed before the middle-context prune decides what to drop, instead of being truncated to a stub first.

### Changed

- **WARP Status Contract (`internal/transport/warp/warp.go`, `frontend/src/services/schema.ts`, `frontend/src/modules/6-settings/WarpEngineCard.tsx`)**: `active_sessions` is renamed to `active_connections` and now counts live tunneled connections; `internal_ip` and `draining_sessions` are added, and `public_ip`/`last_rotated_at` are omitted rather than filled with placeholders until they are real. The dashboard renders the tunnel address in its own cell and surfaces draining sessions.
- **`warp.RegisterDevice` Signature**: the registration endpoint is now a required parameter instead of an implicit default, keeping every test off the public API.

### Added

- **WARP Session Lifecycle Tests (`internal/transport/warp/manager_test.go`)**: Fifteen tests covering cold-start coalescing, saved-identity restore, caller cancellation not aborting a shared rotation, the async throttle (exactly one winner per window), drain-waits-for-connections, `Close` reaping, public/internal address separation, unreachable-identity discard, atomic identity round-trip, and license application failures asserting that neither the license key nor the bearer token reaches the error string.
- **Token Saver Regression Tests (`internal/tokensaver/tokensaver_test.go`)**: Coverage for skeleton-aware dedup, the truncation cap, rune-boundary cuts, diff context retention, prune-never-grows, and structured system content.

## [1.11.0] - 2026-09-21

### Added

- **Tenant Management CRUD API (`internal/server/tenants_admin.go`, `internal/server/router.go`)**:
  - Full CRUD REST endpoints under `/api/tenants` (`GET /api/tenants`, `POST /api/tenants`, `GET /api/tenants/{name}`, `PUT /api/tenants/{name}`, `DELETE /api/tenants/{name}`) for programmatic tenant lifecycle management.
  - Reuses the validate-then-persist pipeline: synchronizes mutations across Turso database tables (`tenants`) and local config files (`tenants.json`), followed by atomic catalog snapshot hot-swapping without gateway downtime.
  - Automatic `sk-gw-` key generation and SHA-256 hash derivation on create when credentials are not pre-supplied.
  - Safe update semantics: preserves live metered token consumption (`used_tokens`) and handles credential rotation without leaking keys or wiping quotas.
- **Admin OpenAPI Specification & Interactive Documentation (`internal/server/openapi`, `internal/server`)**:
  - Added canonical OpenAPI 3.1 specification for administrative surfaces in `internal/server/openapi/openapi-admin.yaml`.
  - Exposed via `GET /api/openapi-admin.yaml` and interactive Scalar reference at `GET /api/docs/admin`.
  - Dynamic build version stamping (`server.SetBuildVersion`) injects ldflags binary version into both public and admin OpenAPI documents at startup.
  - Anti-drift tests (`openapi_admin_test.go`) enforcing route parity, reference resolution, and read-only invariants.
- **Turso In-Memory Store Constructor & Database Verification (`internal/storage/turso`)**:
  - Added `turso.NewStoreWithDB` constructor to facilitate in-memory database testing and embedded engine integrations.
  - Added `TestStore_OCC_Tenant` validating OCC versioning, conflict detection, and table operations on the `tenants` table.
  - Added `TestTenantsAdmin_CRUDRoundTrip_TursoBacked` verifying full end-to-end CRUD against Turso/SQLite database backing.

### Fixed

- **Tenant DTO Serialization of Zero Used Tokens (`internal/config/dto.go`)**:
  - Removed `omitempty` from `TenantDTO.UsedTokens` (`json:"used_tokens"`), preventing serialization omission when `used_tokens == 0` and ensuring full contract compliance with OpenAPI schemas and API test harnesses.
- **Live In-Memory Usage Preservation on Mutation (`internal/server/tenants_admin.go`)**:
  - Added `overlayLiveUsedTokens` to overlay in-memory atomic CAS usage counters from the active snapshot onto loaded configuration prior to saving, preventing reset of active metered usage by stale database/disk data.
- **Default Tenant Status (`internal/server/tenants_admin.go`)**:
  - Defaulted tenant status to `"active"` on creation when unspecified in request payload.
- **Frontend Version Bump**: Updated frontend package and application constant identifiers to `v1.11.0`.

## [1.10.0] - 2026-09-21

### Added

- **Qoder IDE Upstream Adapter (`internal/adapter/qoder`, `internal/domain`, `internal/config`, `cmd/firefly`)**:
  - New `qoder` protocol adapter that translates OpenAI-compatible chat/completions into Qoder's bespoke transport on `api3.qoder.sh` (device tokens) / `api2.qoder.sh` (job tokens), and translates the `{statusCodeValue, body}` SSE envelope back into OpenAI SSE.
  - Implements the full credential lifecycle: Personal Access Tokens (`pt-`) are exchanged at runtime for short-lived job tokens (`jt-`) via `jobToken/exchange`, the `userId` is resolved from `/userinfo`, and both are cached (single-flight) for reuse.
  - COSY request signing (RSA-PKCS1v15 + AES-128-CBC + MD5 over the signed payload, plus the 17 `Cosy-*` fingerprint headers) and the WAF-bypass body encoder are ported to Go with byte-for-byte parity tests against the reference implementation, and verified end-to-end against the live Qoder API.
  - Live per-account model catalog is fetched from `/model/list` (COSY-signed) and cached with a 1h TTL; a missing model config is a hard error to avoid silent model downgrade.
  - Billing/quota blocks (upstream codes 112 / 10605 / `pricingUrl`) are classified as Layer 1 credential errors (429 cooldown/failover) and never trip the upstream circuit breaker, consistent with the gateway's 2-layer resilience model.
  - Registered in the adapter registry and validated in the config builder (aliases `qoder` / `qodercli` / `qoder-cli`), with a default base URL of `https://api3.qoder.sh` auto-provisioned for dashboard-created upstreams.
- **Public API Reference via OpenAPI 3.1 (`internal/server`)**:
  - Added an embedded OpenAPI 3.1 specification documenting the public `/v1/*` surface (`/v1/models`, `/v1/models/{id}`, `/v1/usage`, `/v1/chat/completions`, `/v1/completions`, `/v1/embeddings`, `/v1/compress`) plus `/healthz`.
  - Served at `GET /api/openapi.yaml` (raw spec) and `GET /api/docs` (interactive reference), so developers can explore endpoints, schemas, and auth without reading the source.
  - Anti-drift tests validate the spec structurally (version, security scheme, per-operation responses, resolvable `$ref`s) and cross-check the documented paths against the routes actually registered in the router, in both directions — a route added or removed without a matching spec change fails CI. No new external dependencies were introduced.
- **Qoder Model Discovery on the Upstream Page (`internal/server/upstream_check.go`, `frontend`)**:
  - `POST /api/upstreams/models` and `POST /api/upstreams/check` now have a dedicated `qoder` branch that discovers models live (PAT exchange + COSY-signed `/model/list`) instead of falling through to the generic OpenAI `/models` probe (which returned HTTP 404 against Qoder).
  - The dashboard now offers **Qoder IDE** as a selectable upstream protocol, defaulting its base URL to `https://api3.qoder.sh`; previously a Qoder upstream defaulted to the `openai` protocol and failed model discovery.

### Changed

- **Shared Identifier Generators (`internal/idgen`)**:
  - Extracted the duplicated UUID/short-id/seeded-UUID helpers that had been copy-pasted across the `grok`, `qoder`, and `antigravity` adapters into a single dependency-free leaf package (`UUIDv4`, `Short`, `UUIDFromSeed`, `UUIDFromSeedVersion`, `Hash16`).
  - Migrated all three adapters to it. Antigravity's non-standard version nibble (`0x50`) is preserved via `UUIDFromSeedVersion` rather than silently normalized.
- **Keyring Deduplication at the Key Level, Not the Ref Level (`internal/config`)**:
  - An upstream's credential pool is now deduplicated by resolved secret rather than by ref/name. The same credential imported under different refs (e.g. `dahl-1` and `upstream-dahl-1`) collapses into a single key slot, with the first occurrence retaining its ref and any `rps` / `max_concurrent` overrides.
  - This replaces the previous behavior of failing the entire snapshot build on a duplicate ref; overlapping-import duplicates no longer inflate the ring or break configuration loading.

### Fixed

- **Frontend Version Bump**: Updated frontend package and application constant identifiers to `v1.10.0`.

## [1.9.3] - 2026-09-20

### Fixed

- **Credential Pool Duplication on Manual "Import from Database" (`frontend/src/modules/2-upstreams/UpstreamModal.tsx`)**:
  - Fixed a bug where editing an Upstream, opening the Credentials tab, and clicking **Import from Database** repeatedly kept _appending_ the provider's database keys to the form's credential pool. Because the merge was append-only and generated timestamp/index-based refs, saving and re-importing accumulated duplicate keys indefinitely (e.g. a 1,000-key provider became 2,000, 3,000, … on each import + save cycle).
  - `handleImportKeysFromDatabase` now **reconciles** the credential pool against the database set instead of appending, mirroring the background Turso syncer's rebuild-from-DB behavior:
    - keys present in the database but missing locally are **added**;
    - keys present locally but no longer in the database are **removed**;
    - keys present in both are **kept**, preserving the operator's local `rps` / `max_concurrent` / status overrides.
  - Key identity is anchored to the database `api_keys.id` via the canonical `"<upstream>-key-<id>"` ref (the same scheme the backend syncer uses), with the trimmed secret as a fallback. This makes repeated imports **idempotent** and also collapses any pre-existing duplicate pools (from the old behavior) down to the exact database set on the next import. After import + save, the app's persisted `credential_pool` and the database always hold the same set of keys.
  - The confirmation toast now reports the reconciliation result (`X added, Y removed, Z kept`) instead of a plain "imported" count.
  - Note: the periodic **background Turso sync** (`internal/storage/turso`) was already reconciling correctly — it rebuilds the in-memory catalog snapshot fresh from the database on every load and never appended — so no change was required there. This fix targets the manual, frontend-initiated import that writes to the persisted upstream configuration.

- **Frontend Version Bump**: Updated frontend package and application constant identifiers to `v1.9.3`.

## [1.9.2] - 2026-09-20

### Fixed

- **Healthy Keys Deleted by Client Cancellations (HTTP 499) Counting Toward the Error Threshold (`internal/transport/upstream`)**:
  - Resolved a severe issue where large key rings shrank dramatically over time (e.g. dahl provider dropping from ~1,000 active keys to ~100) because perfectly healthy credentials were being deactivated/deleted by the per-key error-threshold policy.
  - Root cause: coding-agent clients disconnect mid-request constantly (aborted streams retried on the next connection), producing HTTP 499 / `context.Canceled` outcomes. The consecutive-error counter (`KeySlot.ConsecutiveErrors`) was only reset by a clean `< 400` response, so cancellations and transport/5xx outcomes never cleared it. Occasional genuine `429`s then accumulated across many requests — never reset by the frequent successful/cancelled traffic in between — until the counter crossed `KeyErrorThreshold` and the key was deactivated or deleted.
  - Simplified the policy in `HandleKeyOutcome` to a single rule: the counter is incremented **only** for genuine credential errors (`429`, `401`, `402`, `403`); **every other outcome** — success, client cancel (`499`/`context.Canceled`), transport drops (status `0`), and host `5xx` — now **resets** the counter to `0`. This makes it impossible for transient, interleaved rate limits to accumulate to the delete/deactivate threshold on a live key.
  - Hardened breaker classification in `ProcessAttemptOutcome`: a client cancellation is now recognized via `errors.Is(err, context.Canceled)` regardless of the accompanying status (`0`, `200`, or a synthetic `5xx` an adapter stamped on a failed non-stream aggregation), so a client disconnect never trips the upstream circuit breaker (Layer 2) and stops cleanly without retrying, rotating keys, or emitting error metrics.
  - Added regression tests: `TestHandleKeyOutcome_NonKeyErrorsResetCounter`, `TestHandleKeyOutcome_TransientRateLimitsBetweenSuccessNeverAccumulate`, `TestProcessAttemptOutcome_ClientCancelNeverPenalizesKeyOrBreaker`, and `TestProcessAttemptOutcome_WrappedClientCancel`.

- **Frontend Version Bump**: Updated frontend package and application constant identifiers to `v1.9.2`.

## [1.9.1] - 2026-09-20

### Added

- **Configurable Initial Dashboard Password via `INITIAL_PASSWORD` (`internal/security/auth`, `cmd/firefly`)**:
  - The dashboard master password can now be seeded from an `INITIAL_PASSWORD` environment variable. When no persisted `auth.json` exists and no `-dashboard-password` flag is provided, `auth.NewManager` resolves the initial password in the order `INITIAL_PASSWORD` → `FIREFLY_DASHBOARD_PASSWORD` → the built-in default (`12345678`).
  - As with `FIREFLY_DASHBOARD_PASSWORD`, this only sets the _initial_ credential: once credentials are persisted or changed through the dashboard, the stored hash takes precedence on subsequent starts. An explicit `-dashboard-password` flag still overrides all environment sources.
  - Updated the `-dashboard-password` flag help text to document the new `$INITIAL_PASSWORD` source.

## [1.9.0] - 2026-09-19

### Added

- **Configurable Output-Token Default & Floor for the OpenAI Adapter (`internal/adapter/openai`, `cmd/firefly`)**:
  - Reasoning-capable models served over the OpenAI-compatible wire format (e.g. DeepSeek V4, GLM 5.3 Flash) count their thinking tokens against the same completion budget as the visible answer. When a client (Cline, Hermes, etc.) sends a small `max_tokens` — or omits it entirely — reasoning can consume the whole budget, so the upstream returns `finish_reason: "length"` before any visible content, and the client reports "no visible answer" on every continuation attempt. The large (1M) context window is irrelevant here because the limit that is exhausted is the per-response output budget, not the combined input+output context.
  - Added two adapter options that let the gateway enforce a sane output budget without relying on client configuration:
    - `DefaultMaxTokens`: injected as `max_tokens` when the request carries none of `max_tokens` / `max_completion_tokens` / `max_output_tokens`.
    - `MinMaxTokens`: a floor that raises a client-supplied value when it is below the threshold.
  - The policy modifies **only the output-token field the client actually used** (falling back to `max_tokens` when injecting from scratch), so it never introduces a conflicting or upstream-rejected field. When both options are `0` the adapter stays fully transparent, preserving the prior pass-through behavior and honoring the "everything else is untouched" invariant.
  - Exposed via flags `--openai-default-max-tokens` and `--openai-min-max-tokens`, with environment fallbacks `FIREFLY_OPENAI_DEFAULT_MAX_TOKENS` and `FIREFLY_OPENAI_MIN_MAX_TOKENS` (both default to `0` / disabled).
  - Added regression tests: `TestMaxTokensDefaultInjectedWhenAbsent`, `TestMaxTokensFloorRaisesSmallValue`, `TestMaxTokensLargeValueUntouched`, `TestMaxTokensFloorAppliesToClientField`, and `TestMaxTokensPolicyDisabledIsTransparent`.

- **Frontend Version Bump**: Updated frontend package and application constant identifiers to `v1.9.0`.

## [1.8.2] - 2026-09-19

### Fixed

- **KeyRing Load Balancing Reset on Snapshot Hot-Swap (`internal/domain`)**:
  - Resolved a severe load-balancing skew where, with large key rings (e.g. 1,000+ keys), only the first few keys received traffic and the remainder stayed at 0 requests despite being active and valid.
  - Root cause: every catalog hot-swap rebuilt each `KeyRing` via `NewKeyRing`, which created a fresh per-ring `cursor` starting at 0. Combined with frequent reloads (see the flusher fix below), round-robin and least-inflight selection perpetually restarted at `Slots[0]`, concentrating traffic on the top keys.
  - Introduced a process-wide `globalRotation` counter. `NewKeyRing` now seeds its starting cursor from this counter, and `Combo.NextCursor` lazily seeds from it as well, so rotation progress **survives** snapshot rebuilds instead of resetting. Distribution is now uniform across the entire ring even under low, bursty, or concurrent load.
  - The `inflight == 0` early-`break` in `selectLeastInflight` was verified to be a correct optimization (0 is the absolute minimum inflight); fairness is guaranteed by the rotating start offset, not by the scan's stopping point.

- **Reload Storm Triggered by Usage Metering (`internal/storage/turso`)**:
  - The Turso syncer's `GetKeysState` uses `MAX(updated_at)` on `api_keys` as a structural-change signal. The usage flusher was bumping `updated_at` on every metering write (`total_requests` / `last_used_at`), so any active traffic forced a full catalog reload on every sync interval (~15s), which rebuilt every `KeyRing` (see above).
  - Pure usage metering no longer touches `updated_at`. Revocations and deactivations still bump it, because those are genuine structural changes the syncer must observe.

- **Database Credentials with Empty/Unreplicated Secrets (`internal/config`, `internal/storage/turso`)**:
  - Fixed fresh instances connected to an existing Turso database showing configured upstreams that failed with `Invalid API Key` until keys were manually re-fetched.
  - `LoadCatalogSnapshot` now skips `upstream_credentials` rows whose secret has not yet replicated into the local replica (empty secret and no joined `api_keys` row), mirroring the existing filter on the harvester-provider path. This prevents partially-replicated rows from producing unusable key slots.
  - `config.translateUpstream` no longer silently reinterprets a database-sourced credential `ref` (e.g. `openai-cred-3`) as an environment variable name when the secret is empty. Such refs now fail with a clear `empty secret` error instead of a misleading `ENV var not set` error that collapsed the entire snapshot build into zero-config mode. Environment-variable resolution is now gated behind POSIX-style naming (`envVarNameRe`), preserving the file-config ENV var feature.
  - Added regression tests: `TestBuildRejectsEmptySecretDBStyleRef`, `TestBuildAcceptsDBStyleRefWithSecret`, and `TestLoadCatalogSnapshot_SkipsEmptySecretCredential`.

- **Frontend Version Bump**: Updated frontend package and application constant identifiers to `v1.8.2`.

## [1.8.1] - 2026-09-18

### Fixed

- **Turso Database Schema Migration Order (`internal/storage/turso`)**:
  - Resolved startup fatal error `Parse error: Error: invalid expression in CREATE INDEX: api_key` on existing Turso databases.
  - Relocated `CREATE INDEX IF NOT EXISTS idx_tenants_api_key ON tenants(api_key)` from the initial `schemaDDL` script to execute after `ALTER TABLE tenants ADD COLUMN api_key` migration has completed.
  - Added regression test `TestMigrateSchema_ExistingOldTenantsTable` verifying seamless startup on pre-v1.8.0 schemas.

- **Dual Epoch Expiration Resolution (`internal/domain`, `internal/server`, `frontend/src/modules/4-tenants`)**:
  - Resolved false-positive `Your API key has expired. Please renew your plan.` error when tenant keys were provisioned with standard Unix epoch timestamps in seconds (10 digits).
  - Enhanced `Tenant.IsExpired` in `internal/domain` to automatically normalize epoch timestamps `< 100_000_000_000` (seconds) to milliseconds before evaluating against `nowMs`.
  - Updated `handleTenantTopup` in `internal/server` to detect and preserve the original timestamp unit (seconds vs milliseconds) upon validity extensions.
  - Hardened frontend components (`TenantTable`, `TopupModal`) to reliably format and compute expiration state across both seconds and milliseconds.

- **Plaintext Tenant Key Exposure & Unredacted Copying (`internal/server`, `frontend/src/modules/4-tenants`)**:
  - Eliminated unwanted `maskSecret` redaction in `GET /api/settings` for tenant API keys, allowing operators in the authenticated admin dashboard to view, toggle, and copy the full credentials (`sk-gw-...`) instead of redacted placeholders (`sk-...xxxx`).
  - Added defensive unmasking restoration in `handleUpdateSettings` (`PUT /api/settings`) to prevent snapshot corruption if clients submit masked placeholders.
  - Rebuilt frontend production bundle with updated `v1.8.1` constants.

## [1.8.0] - 2026-09-18

### Added

- **Commercial Tenant Keys & Quota Management (`internal/domain`, `internal/storage/turso`, `internal/server`, `internal/transport/httpx`)**:
  - **Plaintext Tenant API Keys**: Eliminated SHA-256 hashing barriers by saving and resolving keys in plaintext (`sk-gw-...`) directly via `snap.TenantByKey` with zero-allocation $O(1)$ memory lookup on the inference hot path, while maintaining backward compatibility with existing hashed keys.
  - **Token Quota Enforcement (Prompt + Completion)**: Introduced configurable token quota limits (`max_tokens`) or unlimited (`0`), automatically tracking usage and returning HTTP 429 (`insufficient_quota`) once exhausted.
  - **Validity & Expiration Dates (`expires_at`)**: Supported epoch timestamp expiration with pre-flight admission checks rejecting expired credentials with HTTP 401 (`key_expired`).
  - **Lifecycle Statuses**: Added `exhausted` and `expired` statuses alongside existing `active` and `suspended` statuses.
  - **Lock-Free Atomic In-Memory Accounting**: Post-request token usage updates occur exclusively in memory via atomic CAS counters (`tenant.UsedTokens.Add(totTokens)`), completely eliminating synchronous database writes and preventing `SQLITE_BUSY` database locking under high concurrency (1,000+ streams).
  - **Asynchronous Batch Flusher**: Periodically flushes accumulated tenant token consumption to Turso/SQLite in background batches.
  - **Client Quota Inspection Endpoint (`GET /v1/usage`)**: Dedicated OpenAI-compatible endpoint allowing tenants to inspect their token usage, remaining balance, and expiration date using `Authorization: Bearer <tenant_key>`.
  - **Admin & Payment Gateway Top-Up API (`POST /api/tenants/topup`)**: Protected endpoint enabling automated token additions (`add_tokens`) and validity extensions (`extend_days`), fully compatible with webhook integrations (e.g., Stripe, Lemon Squeezy, Midtrans).
  - **Database Migration**: Added automated `ALTER TABLE` schema migration in Turso/libSQL for `api_key`, `max_tokens`, `used_tokens`, and `expires_at`, with index `idx_tenants_api_key`.

- **Commercial Tenant Dashboard Management (`frontend/src/modules/4-tenants`)**:
  - **Commercial Key Generator Modal**: Simplified key provisioning with preset token quota choices (1M, 5M, 10M, 50M, Unlimited) and validity duration presets (7d, 30d, 90d, 1y, No Expiry) with instant clipboard copying.
  - **Interactive Tenant Table**: Displays plain API keys with toggleable mask/reveal (`Eye`/`EyeOff`), one-click copying, token usage progress bars with percentage indicators, expiration dates with expired warnings, and dynamic status badges (`ACTIVE`, `EXPIRED`, `EXHAUSTED`, `SUSPENDED`).
  - **Tenant Top-Up Modal**: Dedicated modal to instantly refill token balances and extend key expiration directly from the web interface.
  - **Frontend Version Bump**: Updated frontend package and application constant identifiers to `v1.8.0`.

## [1.7.2] - 2026-09-18

### Changed & Refactored

- **Internal Package Reorganization & Clean Architecture Migration**:
  - Restructured 27 flat packages under `internal/` into cohesive, domain-driven subsystem directories:
    - `internal/adapter/`: Multi-provider AI protocol adapters (`openai`, `anthropic`, `opencode`, `grok`, `cline`, `codebuddy`, `antigravity`).
    - `internal/transport/`: Ingress & egress networking (`httpx` middleware, `upstream` client pool & circuit breaker, `warp` WireGuard egress).
    - `internal/security/`: Credential vault and OAuth subsystem (`auth`, `oauth` and providers).
    - `internal/observability/`: System telemetry and metering (`logging`, `metrics`, `usage`).
    - `internal/storage/`: Database & disk persistence (`turso`, `analytics`).
  - Preserved pure domain and core orchestrator packages at top-level `internal/`: `domain`, `ports`, `config`, `registry`, `limits`, `tokensaver`, `server`, `reqid`, `watch`, and `integration`.
  - Maintained 100% Git commit lineage and history across all relocated files via `git mv`.
  - Updated all import declarations, internal references, and architectural documentation (`AGENTS.md`, `README.md`, `CONTRIBUTING.md`, `llms.txt`, `BUSINESS.md`).

- **Frontend Version Bump**:
  - Updated frontend package and application constant identifiers to `v1.7.2`.

## [1.7.1] - 2026-09-18

### Fixed & Improved

- **OpenCode Free Tier Active Health Checking & Circuit Breaker Stability (`internal/upstream/health.go`, `internal/upstream/health_opencode.go`)**:
  - Implemented specialized OpenCode health check prober with automated probe payload selection (`OpenCodeOfficialResponsesProbePayload` for `/responses` models like `muse-*` and `OpenCodeOfficialTitleProbePayload` for chat models).
  - Added host reachability fallback probing (`probeHostFallback`) on model-level probe timeouts or failures to verify the host endpoint before reporting failure to the Circuit Breaker.
  - Correctly classified HTTP 4xx (401/403/404/429) as Layer 1 credential/quota issues, ensuring public/free-tier keys are never incorrectly marked as revoked or trip the Circuit Breaker.
  - Added keyless Free tier auto-provisioning in `internal/config/builder.go` to automatically assign an `opencode-free-public` key slot (`public`) for OpenCode upstreams when no credentials are provided.

- **OpenCode Multi-Turn Conversation & Responses API Alignment (`internal/opencode/transform.go`)**:
  - Resolved `[invalid_request_error] content type input_text is not valid on assistant messages` by converting assistant turn content to `output_text` across both standard message lists and raw Responses input objects.
  - Supported assistant messages containing both conversational text and tool invocations simultaneously.

- **OpenCode Free Tier Tool-Calling & Client Compatibility (`internal/opencode/adapter.go`, `internal/opencode/transform.go`, `internal/opencode/free_prompt.txt`)**:
  - Stripped environment-specific Darwin paths (`/private/tmp/opencode-x64`) and tool declarations from `free_prompt.txt` that caused Muse Spark to hallucinate file paths and invoke unavailable local tools (`read`, `glob`, `bash`).
  - Preserved client system instructions (e.g. from Cline) without prepending hardcoded instructions.
  - Replaced all-tool injection with minimal non-invasive verification placeholders for `bash` and `read` (`[SYSTEM VERIFICATION ONLY - DO NOT CALL]`), satisfying provider console validation while prioritizing client-provided tools.
  - Implemented standard OpenAI Chat Completions streaming event mapping for Responses tool invocations (`response.output_item.added`, `response.function_call_arguments.delta`, `response.output_item.done`) with 0-based sequential tool indexing and `finish_reason: "tool_calls"`, achieving full compatibility with Cline and `@ai-sdk/openai`.

- **Architectural Cleanup & DRY Refactoring (`internal/upstream`, `internal/opencode`)**:
  - Unified OpenCode base62 timestamp ID generation (`GenerateOpenCodeTimestampID`), session IDs (`GenerateOpenCodeSessionID`), request IDs (`GenerateOpenCodeRequestID`), user agent (`OpenCodeUserAgent`), and probe payload templates in `internal/upstream/health_opencode.go`.
  - Replaced duplicate implementations in `internal/opencode/session.go`, `internal/opencode/free_template.go`, and `internal/opencode/transform.go` with single-source delegations.
  - Pruned unused startup allocations and dead code (`parsedFreeResponsesTools`, `OfficialResponsesTools()`, `deriveCanonicalID`).

- **Frontend Version Bump**:
  - Updated frontend package and application constant identifiers to `v1.7.1`.

## [1.7.0] - 2026-09-18

### Added

- **Embedded Cloudflare WARP & Userspace WireGuard Egress Engine (`internal/warp`, `cmd/firefly`, `internal/server`, `internal/upstream`, `frontend`)**:
  - **Zero-Privilege Userspace Tunnel**: Native userspace WireGuard implementation via `golang.zx2c4.com/wireguard` and `tun/netstack` (gVisor TCP/IP), requiring zero root/sudo permissions, host network TUN/TAP kernel device privileges, or external daemon dependencies.
  - **Automated Cloudflare Device Registration**: Generates Curve25519 WireGuard keypairs on-the-fly and registers peers with Cloudflare Edge (`api.cloudflareclient.com/v0a3304/reg`) with optional WARP+ license key integration (`FIREFLY_WARP_LICENSE` / `WARP_LICENSE_KEY`).
  - **Identity Persistence & Non-Disruptive Session Drain**: Persists tunnel identity to disk (`warp_identity.json`) for instant restart recovery. Concurrent rotations are coalesced with `singleflight.Group`, and previous WireGuard devices are gracefully drained in the background to prevent severed in-flight SSE streams.
  - **Automated Upstream 429 IP Rotation (`warp_auto_rotate_on_429`)**: Upstreams configured with WARP egress can automatically trigger asynchronous background WireGuard IP rotations upon receiving HTTP 429 Too Many Requests, instantly renewing outbound IP reputation without blocking the caller.
  - **Centralized Outbound Transport Routing (`warp.ConfigureTransportEgress`)**: Unified egress pipeline supporting `direct` host routing, `warp` userspace WireGuard tunneling, and custom `proxy` endpoints (SOCKS5/SOCKS5h/HTTP/HTTPS).
  - **WARP Management Card (`WarpEngineCard.tsx`)**: Real-time status monitoring in Settings view displaying public IPv4, Cloudflare edge colo datacenter (e.g. `HKG`, `SIN`), handshake latency, active sessions count, and on-demand manual rotation.
  - **Per-Upstream Egress Controls (`UpstreamModal.tsx`, `UpstreamCard.tsx`)**: Configurable egress mode dropdown (`Direct Host Network`, `Cloudflare WARP`, `Proxy`), proxy URL input, and auto-rotate toggle with persistence to local JSON snapshots and Turso database.

- **OpenCode Free Tier Session & Responses API Engine (`internal/opencode`, `internal/server`)**:
  - **Canonical OpenCode ID Specification**: Implemented exact timestamp-encoded 26-character identifiers matching the official OpenCode client: descending IDs for sessions (`ses_<26-chars>`) and ascending IDs for messages (`msg_<26-chars>`), strictly conforming to provider console regex validation.
  - **Deterministic Session Mapping**: Non-canonical inbound session headers are deterministically mapped via SHA-256 to canonical 30-character IDs, completely eliminating upstream 403 `FreeTierError` while preserving tenant session isolation.
  - **Official Client Session Emulation**: Embedded official prompt preamble (`free_prompt.txt`) and tool declarations (`free_tools.json`) into request bodies on OpenCode Free tier, fulfilling provider console authorization requirements.
  - **Full OpenAI Responses API Compatibility (`muse-spark-1.3-contributor-free`, `muse-*`, `grok-*`)**:
    - Extracted system/developer instructions to top-level `instructions` parameter.
    - Clamped `max_output_tokens >= 16` to prevent Responses API validation errors.
    - Added comprehensive event parsing for `response.incomplete` and `response.output_item.done` (function call tool invocations), relaying streaming tool calls and finish reasons (`tool_calls`, `stop`) faithfully to OpenAI-compatible clients.
    - Implemented streaming SSE to non-streaming response aggregator (`aggregateChatSSE`) for free tier inference.
  - **Egress-Aware Active Health Checking & Model Probing**:
    - Automated probe payload dispatching: standard Chat Completions for chat models, and Responses API format for `muse-*` models.
    - Health checks and zero-token model catalog discovery execute through the upstream's configured egress mode (WARP, proxy, or direct).

### Changed & Improved

- **Code Style & Architecture Refactoring (`golang-code-style`)**:
  - Centralized egress transport configuration in `warp.ConfigureTransportEgress`, eliminating duplicated transport setup across `upstream.Pool` and `server.RouterDeps`.
  - Replaced Wall-clock `time.Now()` inside `deriveCanonicalID` with pure cryptographic seed derivation for 100% deterministic hash output across concurrent runs.
  - Standardized header assignment using default-then-override patterns in `internal/opencode/adapter.go`.
  - Refactored function signatures to maintain parameter count $\le 4$ with dedicated configuration structs (`warp.EgressConfig`).
- **Frontend Version Bump**:
  - Updated frontend package and constant identifiers to `v1.7.0`.

## [1.6.0] - 2026-09-17

### Added

- **OpenCode & OpenCode Go First-Class Upstream Adapter (`internal/opencode`, `cmd/firefly`, `frontend`)**:
  - Full support for both **OpenCode Free** (`Authorization: Bearer public`, `x-opencode-client: desktop`, `x-opencode-project: global`, `https://opencode.ai/zen/v1`) and **OpenCode Go** (paid subscription key, `https://opencode.ai/zen/go/v1`).
  - Keyless Free tier auto-provisioning: automatically injects a `opencode-free-public` key slot (`KeySlot.Secret = "public"`) when no credentials are provided.
  - **Deterministic Session Isolation (`x-opencode-session`)**: Preserves client-supplied session headers (up to 256 chars) or deterministically derives opaque SHA-256 session IDs (`ses_<hash>`) based on tenant name and request ID, completely preventing cross-tenant session leaks.
  - **Dual-Route Model Dispatching**:
    - **OpenAI Responses API Models** (`muse-*`, `grok-4.6`, `gpt-5.6-luna`, `responses/*`): Dispatches to `/responses` with automatic payload translation (`messages` -> typed `input` array, `max_tokens` -> `max_output_tokens`, `reasoning_effort` normalization, tool parameter schema sanitization) and translates Responses SSE streams back into OpenAI Chat Completion chunks.
    - **Standard Chat Models** (GLM, Kimi, Qwen, DeepSeek, Claude, etc.): Dispatches to `/chat/completions` with object parameter fallback (`properties: {}`), tool name clamping (128 chars), and standard SSE relay with idle watchdog and client-disconnect abort.
  - **Defensive Endpoint Tier Cross-Correction**:
    - Automatically normalizes `/zen/go/v1` to `/zen/v1` for keyless/free requests, and `/zen/v1` to `/zen/go/v1` for subscription keys across both the runtime adapter, health check prober, and model fetcher to prevent upstream 401 errors.
  - **Management Dashboard & Health Probing**:
    - Differentiated dropdown options in Upstream Modal for `OpenCode Free (zen/v1 - Community / Keyless)` and `OpenCode Go (zen/go/v1 - Subscription Key)` with respective auto-filled default URLs.
    - Added dedicated model probing (`/chat/completions` or `/responses`) and zero-token model catalog discovery for OpenCode in `internal/server/upstream_check.go`.
    - Added `OPENCODE` badge styling and provider description in `UpstreamCard.tsx`.
    - Preserved Layer 1 (Key/429/401) and Layer 2 (Host/5xx) error separation invariants via `upstream.ProcessAttemptOutcome`.

## [1.5.0] - 2026-09-17

### Added

- **Public Overview & Sanitized Settings Architecture (`internal/server`, `frontend`)**:
  - Unauthenticated visitors can view real-time overview analytics, active models, upstreams, latency charts, and live request logs on the dashboard.
  - Implemented strict server-side sanitization (`sanitizePublicSettings`): sensitive credentials (upstream API keys, credential pools, tenant keys, rate limits, Turso database credentials, and Auto-TLS certificates) are redacted or omitted for unauthenticated requests.
  - Sanitized public telemetry logs: tenant identifiers are masked as `"public"` and key references are cleared.
  - Added full test coverage for public versus authenticated settings and telemetry endpoints (`TestSettings_PublicSanitizationAndAdminAuth`).
- **Token Saver Optimization Suite (`internal/tokensaver`, `internal/domain`, `internal/server`, `internal/turso`, `frontend`)**:
  - **Compress Tool Output (RTK)**: Strips ANSI escape sequences, deduplicates consecutive repetitive log/progress lines, compacts unmodified git diff context lines, and applies smart head-and-tail truncation on tool results (default 12,000 chars), achieving 60-90% input token savings.
  - **Compress LLM Output (Caveman)**: Injects brevity directives into system prompts to eliminate pleasantries, preambles, and conversational filler, cutting ~65% of output tokens.
  - **Lazy Senior Dev (Ponytail)**: Biases model generation toward minimal code, YAGNI, standard library reuse, and deletion over addition.
  - **Compress Context (Headroom)**: Prunes excessive older middle tool outputs and messages when conversation history approaches configured token thresholds (default 32,000 tokens).
  - **Direct Compression Endpoint (`POST /v1/compress`)**: Standalone authenticated API for compressing arbitrary prompts and chat messages.
  - **Visual Configuration Card (`TokenSaverCard.tsx`)**: Embedded in the Settings tab with master enable toggle, granular sub-feature toggles, threshold inputs, and instant hot-swap persistence to local files or Turso database.
  - Zero-overhead fast path: when disabled, requests pass through with zero allocations.

### Changed & Improved

- **Settings Card Visual Consistency (`frontend`)**:
  - Standardized `TokenSaverCard` to strictly conform to established Settings page aesthetic: neutral uppercase mono headers, emerald/neutral status dots, standard checkboxes matching Auto-TLS, clean input borders, and minimal button styling.
- **English-Only Documentation & Repository Standardization**:
  - Removed `README_ID.md` and consolidated all documentation, UI, and diagnostics to 100% standard English.

## [1.4.0] - 2026-09-17

### Added

- **Designated Probe Model Architecture for Active Upstream Health Checks (`internal/domain`, `internal/server`, `internal/upstream`, `internal/turso`)**:
  - Added optional `probe_model` field to upstream configuration, schema, database persistence, and DTOs.
  - Implemented 1-token active inference probing (`/v1/chat/completions` or `/v1/messages`) against the designated probe model to verify real account quota/balance without false positives from empty-balance trial keys.
  - Supported protocol-specific payload formatting for OpenAI and Anthropic.
  - Auto-selects candidate probe models with keyword prioritization: `free` (1st preference) and `flash` (2nd preference).
  - Preserved Layer 1 / Layer 2 resilience invariants: HTTP 401 revokes key slot, HTTP 429 triggers dynamic cooldown, and host breaker reports healthy (`ok=true`) so quota issues never trip upstream circuit breakers.
- **Decoupled Zero-Token Model Discovery Endpoint (`POST /api/upstreams/models`)**:
  - Separated model list query from health check probing so `/api/upstreams/models` purely queries provider `/v1/models` without triggering inference or consuming token balance.
  - Integrated `useUpstreamModelsQuery` and `fetchUpstreamModels` in frontend services.
- **Rate-Paced Concurrent Batch Model Verification in Upstream Dialog (`frontend`)**:
  - Moved batch model testing out of the individual model route dialog into the Upstream dialog's **Live Models** tab.
  - Implemented non-blocking **Paced Concurrent Dispatcher** with safe rate options (`1`, `2`, `5`, `10 req/s`) and dynamic concurrency cap `Math.max(safeRps * 2, 2)`.
  - Added real-time visual progress indicators: in-flight badge (`[N in-flight]`), animated progress bar, and test result counts (`[Active]`, `[Failed]`).
  - Added real-time per-model status badges (`Active · {latency}ms`, `Failed (HTTP status)`), single-model retest button, and `Set as Probe` designation.
  - Added in-place one-click route creation (`+ Add Route`, `Route Active`, and `Add All Active as Routes`).
- **Streamlined Models Dialog (`ModelModal.tsx`)**:
  - Refactored model route configuration into a lean, focused dialog with a single connection test and retry fetch button.

## [1.3.3] - 2026-09-16

### Added

- **Application Version Display in Header/Navbar (`frontend`)**:
  - Rendered current application version (`v1.3.3`) immediately to the right of the brand title `firefly`.
  - Styled with muted typography (`font-mono text-xs text-neutral-500`) without a badge/pill container, seamlessly blending into the Nocturnal Celestial theme.
  - Centralized version identifier in `frontend/src/core/constants.ts` (`APP_VERSION`).

### Fixed

- **Auto-TLS Error Reporting & Status Code Semantics (`internal/server`, `frontend`)**:
  - Fixed an issue where failing to bind Auto-TLS ports (:80/:443) returned `503 Service Unavailable`, causing the frontend's hardcoded 503 catch block in `saveSettings` to report a misleading `"Service Unavailable: Gateway is draining"` toast instead of the real system error.
  - Updated `handleUpdateSettings` in `internal/server/settings.go` to return `400 Bad Request` with `openai.TypeInvalidRequest` when Auto-TLS fails to apply.
  - Updated `frontend/src/services/api.ts` to inspect and extract `body?.error?.message` on any HTTP 503 response in both `fetchSettings` and `saveSettings` instead of discarding the server message.
  - Added unit test `TestSettingsAutoTLSApplyFailureReturns400` in `internal/server/settings_test.go`.
- **Production Auto-TLS Hardening & Troubleshooting Guide (`docs/production.md`)**:
  - Documented Linux capability requirements (`CAP_NET_BIND_SERVICE` / `setcap`) for systemd non-root execution when Auto-TLS binds privileged ports 80 and 443.
  - Documented reverse proxy coexistence rules (Nginx/Caddy/Traefik).

## [1.3.2] - 2026-09-16

### Fixed

- **Config Watcher Burst Debounce Coalescing (`internal/watch`)**:
  - Fixed an issue where `w.debounce` did not actively drain incoming file-change triggers while waiting for the quiet window timer, causing bursts of rapid file writes in CI runners to execute multiple spurious reloads (`burst of 4 writes caused 3 reloads; debounce did not coalesce`) instead of coalescing into one reload.
  - Implemented an active quiet-window timer drain and reset loop that restarts the debounce timer upon every subsequent file change, ensuring exactly one clean reload per burst.

### Changed & Improved

- **Google Antigravity OAuth & Runtime Parity with 9Router (`internal/oauth`, `internal/antigravity`)**:
  - Made `OnboardUser` non-blocking via a detached background goroutine (`go func() { ... }()`), preventing OAuth code exchange callback timeouts during browser authentication.
  - Added Google anti-abuse rate limit protection matching 9router PR #3813 (`DefaultOnboardMaxAttempts = 2`, `DefaultOnboardBaseDelay = 12s` with random jitter).
  - Added `ResolveConnection` to `oauth.Manager` to allow protocol adapters to resolve stored connection metadata without storage coupling.
  - Updated `internal/antigravity.Adapter` to dynamically resolve and propagate the authentic Google Cloud Companion `project_id` from OAuth connection credentials into outbound inference requests, with automatic re-resolution on KeySlot failover.
  - Added Antigravity upstream (`google-antigravity`) and model definitions (`gemini-3.8-flash`, `gemini-3.7-flash`, `claude-sonnet-4-6`) to `configs.example/upstreams.json` and `configs.example/models.json`.

## [1.3.1] - 2026-09-16

### Fixed

- **Turso Sync Engine vs. SQL Transaction Deadlock & Stale Transaction Recovery (`internal/turso`)**:
  - Resolved `Database is busy` lock contention between the background syncer/flusher and database mutations (`SaveSettings`, `UsageFlusher`) by coordinating operations through an exclusive `sync.RWMutex` on `Client` (`Lock()`, `Unlock()`, `PushLocked()`, `PullLocked()`).
  - Added exponential backoff retry loops for transient `Database is busy` conditions on both `Pull` and `Push`.
  - Configured embedded replica single connection pooling (`db.SetMaxOpenConns(1)`, `db.SetMaxIdleConns(1)`, `db.SetConnMaxLifetime(0)`) and set `BusyTimeout: 10000` (10s) in `TursoSyncDbConfig`.
  - Fixed a driver issue in `tursogo` where failed `Commit()` calls marked transactions as `done = true` without executing SQLite `ROLLBACK`, leaving connections uncommitted in the pool and causing subsequent `BeginTx` calls to fail with `cannot start a transaction within a transaction`.
  - Implemented self-healing `beginTx` / `BeginTx` with automatic rollback recovery (using `context.WithoutCancel(ctx)` with a 2-second timeout) and hardened `SaveSettings` to issue an explicit `ROLLBACK` on commit failure.
- **Accurate Grok CLI Key Verification on Upstream Modal (`internal/server`, `internal/grok`)**:
  - Fixed false-positive key check validation in `POST /api/upstreams/check` where testing an invalid or expired Grok key previously swallowed upstream HTTP 401/403/429 errors in a debug log and unconditionally returned `Healthy: true` and `StatusCode: 200` with hardcoded 1ms latency.
  - Exported `HTTPError` in `internal/grok` and updated `FetchModels` to return `&HTTPError` capturing upstream HTTP status codes and error bodies.
  - Updated `handleCheckUpstream` to actively measure round-trip latency, validate bearer tokens against the live Grok CLI `/models` endpoint, and return `Healthy: false` with the exact upstream status code (e.g. `401 Unauthorized` or `429 Too Many Requests`).
  - Preserved discovery mode without credentials falling back to the curated static model list when no API key is provided.

## [1.3.0] - 2026-09-15

### Added

- **Grok CLI / Grok Build upstream adapter (`internal/grok`, protocol `grok-cli`)**: a new protocol adapter that talks to the xAI Grok CLI inference API (`https://cli-chat-proxy.grok.com/v1/responses`, the OpenAI **Responses API**) using an xAI OAuth bearer access token. It mirrors the existing adapter layout (`adapter.go` + `translate.go` + tests, analogous to `internal/antigravity`).
  - Translates OpenAI Chat Completions requests into the Grok CLI Responses payload: `messages[]` → Responses `input[]` (assistant turns use `output_text`, others `input_text`), with `stream:true`, `store:false`, `reasoning:{summary:"concise", effort?}`, and `include:["reasoning.encrypted_content"]` when the model supports reasoning effort.
  - Model mapping: `grok-build` (base) and the `grok-4.5` family (`grok-4.5`, `grok-4.5-high|medium|low`, the effort suffix collapsing onto `grok-4.5` with the corresponding `reasoning.effort`). Unknown/custom grok model ids are passed through verbatim.
  - Sends the grok CLI client fingerprint headers: `Authorization: Bearer <token>`, `x-xai-token-auth: xai-grok-cli`, `x-grok-client-identifier: grok-shell`, `x-grok-client-version`, `x-grok-session-id`/`x-grok-conv-id`/`x-grok-req-id`/`x-grok-turn-idx`, and `x-grok-model-override`.
  - Translates the Grok Responses-API SSE stream (`response.output_text.delta`, `response.reasoning_summary_text.delta`, `response.completed`/`response.done`, `error`/`response.failed`) back into OpenAI-compatible `chat.completion.chunk` events (with `reasoning_content` for thinking output and usage on the final chunk) for streaming clients, and aggregates it into a single `chat.completion` for non-streaming clients.
  - Credential resolution follows the same tiers as the other adapters and additionally falls back to the key slot's stored secret: the xAI OAuth access token is harvested into the Turso `providers`/`api_keys` tables (provider `grok`) and pooled into the key ring as `KeySlot.Secret`. Tokens are refreshed externally by the harvester; Firefly forwards the current bearer and load-balances / fails over across accounts.
  - Error classification honors the Layer 1 vs Layer 2 invariant via `upstream.ProcessAttemptOutcome`: an expired/invalid token (`401`/`403`) or quota exhaustion (`429`) is a credential-layer event (cooldown / failover across accounts) and never trips the upstream circuit breaker; only `5xx`/transport errors are reported to the breaker.
  - Wired into config validation (`internal/config/builder.go` accepts protocol `grok-cli` with aliases `grok_cli`/`grok`/`gcli`/`grok-build`) and registered in `cmd/firefly/main.go`.
  - Model discovery: the dashboard "Add Route" probe (`POST /api/upstreams/check`) fetches the live model list from the Grok CLI `/models` endpoint (`internal/grok.FetchModels`) using the resolved bearer token, and falls back to the curated static list (`grok.SupportedModels`) when no token is available or the call fails.
- **Container image (`Dockerfile` + `.dockerignore`)**: a multi-stage build — Bun builds the frontend SPA, Go compiles the binary embedding `frontend/dist`, and a Debian-slim (glibc) runtime runs it as a non-root user on port 8080. glibc is required because the tursogo engine extracts a glibc-linked native library at runtime (Alpine/musl would fail). The config dir `/etc/firefly` is a writable volume covering the Turso replica and native-library cache.

### Changed

- **Redesigned scrollbars to match the night-blue theme**: the global WebKit/Firefox scrollbar now uses a slim slate-blue pill (transparent track, `rgba(103,132,173,…)` thumb) that brightens on hover and turns the cyan accent on drag, replacing the flat grey bar. Added the previously-missing `.no-scrollbar` utility (fully hides the bar on horizontal tab/pill rows) and a refined `.custom-scrollbar` variant (slimmer, hover-tinted) applied to inner scroll panels such as the Telemetry KeyRing list.
- **Telemetry "Upstream KeyRing" simplified to a per-upstream expandable list**: the per-slot cooldown card grid (large cards, legend, gradients) is replaced with a compact list grouped by upstream. Each upstream is a collapsible row (name · cooldown/revoked count · key count) that expands to a plain table of its keys (Key / Status / In-flight / 429s / Requests). The whole section sits in a fixed-height (`max-h-[420px]`) scroll container, so large key rings (dozens–hundreds of keys) no longer stretch the page. No data was removed.
- **History table shows the credential used per connection**: each row in the Overview "Live History" panel now displays the key-ring credential reference (`key:<ref>`, e.g. `key:grok-key-1156`) that the connection was routed through, and the Request Telemetry Inspector drawer surfaces it as `Credential`. The value is the key-slot identifier (never the secret), populated from the resolved routing target (`analytics.RequestLog.KeyRef`, set in `forwardEndpoint`).
- **Static-model upstreams accept custom/unlisted models (`grok-cli`, `antigravity`)**: the "Test Connection" probe no longer hard-rejects a model absent from the curated list with `HTTP 400`. An unknown model is accepted as a custom model and routed as-is (for `grok-cli`, an unlisted id is passed through verbatim as the upstream model).

## [1.2.3] - 2026-09-15

### Fixed

- **Turso native library extraction panicked under systemd with `mkdir /home/firefly: permission denied`**:
  - The `tursogo` loader extracts a bundled native shared library at runtime into `os.UserCacheDir()` (`$XDG_CACHE_HOME`, else `$HOME/.cache`). A systemd service user such as `firefly` typically has no writable `$HOME`, so the very first Turso client initialization panicked and crashed the process (`status=2/INVALIDARGUMENT`, restart loop).
  - `internal/turso/client.go` now defaults the loader's `TURSO_GO_CACHE_DIR` to a writable directory next to the local replica (`<local-db-dir>/.turso-cache`) before the library is loaded, unless the operator already set `TURSO_GO_CACHE_DIR`. With the default systemd layout this resolves under `/etc/firefly`, which is covered by `ReadWritePaths`.

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
