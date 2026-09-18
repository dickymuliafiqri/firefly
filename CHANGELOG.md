# Changelog

All notable changes to the Firefly project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
