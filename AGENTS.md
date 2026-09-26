# AGENTS.md — AI Agent Guidelines & Backend Architecture Manual

This document is the comprehensive technical manual for AI Coding Agents (Cline, Claude Code, GitHub Copilot, etc.) and backend engineers working on the **Firefly** codebase.

It details **backend architecture, the complete request lifecycle, upstream connection management, high-concurrency controls (1,000+ simultaneous SSE streams), and non-negotiable architectural invariants**.

---

## 1. Core Principles & Invariable Rules (Non-Negotiable Invariants)

Before modifying or adding code to Firefly, every AI Agent **must understand and strictly adhere to** the following invariants:

1. **Never Set `http.Server.WriteTimeout` on the Data Plane:**
   Firefly serves long-lived Server-Sent Events (SSE) streams (e.g. LLM inference runs lasting several minutes). Setting a `WriteTimeout` at the `http.Server` level abruptly terminates active streaming connections. Streaming timeout protection is handled granularly via `ResponseHeaderTimeout` on `http.Transport` (time-to-first-byte) and `StreamWatchdog` in `openai.RelaySSE` (idle gap between consecutive tokens).
2. **Strict Separation Between Layer 1 (Key/4xx) and Layer 2 (Host/5xx) Errors:**
   - Status `429 Too Many Requests` and `401 Unauthorized` from upstream indicate **credential/quota exhaustion**, **NOT** host infrastructure failure. These are handled exclusively by `KeyRing` (dynamic cooldown or key revocation) and retried across remaining keys in the keyring.
   - Only network transport errors (dial failure, TCP reset, DNS error) and HTTP `5xx` responses are reported to the upstream **Circuit Breaker**. Client 4xx errors or key 429 quota exhaustion **MUST NEVER** trip an upstream Circuit Breaker.
   - This classification is enforced in a single place — `upstream.ProcessAttemptOutcome` (`internal/transport/upstream/attempt.go`) — which every protocol adapter (`openai`, `anthropic`, `antigravity`, `cline`, `codebuddy`, `grok`, `opencode`) calls after each attempt. Adapters must not re-implement breaker/failover logic; doing so previously caused OpenAI/Anthropic to mis-report wrapped 4xx errors as host failures.
   - **Consecutive-Error Counter Resets on Anything but a Credential Error (Non-Negotiable):** `KeySlot.ConsecutiveErrors` drives the automated deactivate/delete threshold (`HandleKeyOutcome` in `policy.go`). It must be incremented **only** for genuine credential errors (`429`, `401`, `402`, `403`); **every other outcome resets it to `0`** — success, client cancellation (HTTP `499` / `context.Canceled`), transport drops (status `0`), and host `5xx`. A previous version only reset on a clean `< 400` response, so the very frequent client cancellations coding-agent clients produce (aborted streams retried on the next connection) never cleared the counter, and occasional genuine `429`s slowly accumulated across unrelated requests until healthy keys crossed the threshold and were deleted. Client cancellation is detected via `errors.Is(err, context.Canceled)` regardless of the accompanying status, so it never trips the breaker (Layer 2) and never advances the key counter (Layer 1).
3. **Fail-Closed & Typed-Nil Interface Safety (`httpx.IsNil`):**
   In Go, an interface holding a typed `nil` pointer (e.g. `(*Registry)(nil)`) evaluates `v != nil` as `true`. Firefly requires the `httpx.IsNil(v)` helper at every dependency injection boundary to prevent nil-pointer dereference panics inside handlers, immediately responding fail-closed (HTTP 401 or 503).
4. **Memory Allocation Discipline & Buffer Recycling (`sync.Pool`):**
   All request body reading and streaming relay pipelines must utilize `sync.Pool` (`requestBufferPool`, `copyBufferPool`, `readerPool`). Every buffer retrieved must check its capacity upon release (`buf.Cap() <= 512KB`); buffers exceeding this threshold must be discarded to the Garbage Collector to avoid persistent heap bloat.
5. **Decoupled Stream Context on Graceful Shutdown:**
   When SIGINT/SIGTERM is received, the root application context is canceled to cease accepting new requests and terminate background workers. Active streaming connections are governed by a decoupled `streamCtx`, allowing ongoing token streams to drain naturally until `ShutdownGrace` (default 30 seconds) expires without sudden disconnection.
6. **Zero-Leaking Secret Masking:**
   Sensitive credentials (`Authorization: Bearer ...`, `x-api-key`, `KeySlot.Ref`, tenant keys) must never be logged in plain-text to `slog` nor returned in client error JSON. Use redacted masking formats (`[REDACTED]` or `sk-gw-***`).
7. **OAuth-Managed Upstream Endpoints Are Immutable:**
   An upstream whose `protocol` authenticates with a provider OAuth token (`antigravity`, `cline`, `codebuddy-cn`, `codebuddy-intl`, `grok-cli`) must never take its `base_url` — or any fallback `base_urls` entry — from operator input. The adapter forwards the provider's bearer token to that host, so an editable endpoint is a credential-exfiltration path. The endpoints live in exactly one place, `domain.OAuthManagedBaseURL` (`internal/domain/protocol_endpoint.go`); `config.Build` (`pinOAuthManagedEndpoint`) replaces whatever a hand-edited file, a Turso row, or an API payload supplies with the managed value, and `config.PinOAuthManagedEndpoints` normalizes the settings payload *before* it is persisted — so the JSON files, the database rows, the rebuilt snapshot, and `GET /api/settings` can never disagree. Adapters keep honoring `Upstream.BaseURL` (their tests inject mock hosts through it), but the value they receive is always the pinned one in production. The dashboard mirrors the lock: `lockedOAuthBaseUrl` / `resolveBaseUrl` (`frontend/src/components/upstream/GeneralTab.tsx`) render the field read-only, refuse fallback hosts, and freeze the protocol of an existing OAuth upstream.
   The grok-cli pin (`https://cli-chat-proxy.grok.com/v1`, adapter appends `/responses`) lives in the same table; the provider login itself stays served by the `grok-cli` OAuth provider (`internal/security/oauth/providers/grokcli`, `POST /api/oauth/authorize` + poll), whose refresh lifecycle is independent of the endpoint pin.

---

## 2. Package Map

| Package | Path | Primary Responsibilities |
| :--- | :--- | :--- |
| `main` | `cmd/firefly/main.go` | CLI entrypoint, flag parsing, OS signal coordinator, data and admin HTTP servers, adapter registration, graceful shutdown orchestration. |
| `loadtest` | `cmd/loadtest/` | Standalone CLI load tester, in-process mock server, and 100/1,000 concurrency low/medium/heavy synchronized benchmark runner. |
| `domain` | `internal/domain/` | Core domain models: `CatalogSnapshot`, `Tenant`, `Upstream`, `Model`, `Combo`, `KeyRing`, `KeySlot`, `Target`. Pure data structures without side-effects. |
| `ports` | `internal/ports/` | Go interface contracts: `UpstreamAdapter`, `TenantStore`, `UsageRecorder`, `AdapterRegistry`, `ForwardRequest`. |
| `server` | `internal/server/` | HTTP server construction, Go 1.22+ `http.ServeMux` routing, middleware chain assembly, shared forward handler (`forwardEndpoint`), drain guard. Admin surfaces live here too: `/api/providers*` + `/api/keys/{id}` operator CRUD (`providers_admin.go`) and the single catalog-file writer (`catalog_files.go`). Machine callers (the external harvester) use that same CRUD with the admin token. |
| `config` | `internal/config/` | JSON configuration loader (`upstreams.json`, `models.json`, `tenants.json`, `combos.json`) with strict schema validation and keyless free tier auto-provisioning. |
| `registry` | `internal/registry/` | Thread-safe catalog snapshot store backed by `atomic.Pointer[domain.CatalogSnapshot]`. Zero-downtime hot-swap configuration reloads. |
| `limits` | `internal/limits/` | Token-bucket rate limiters (`golang.org/x/time/rate`) per tenant, and CAS atomic concurrency gates per-credential/keyslot. |
| `tokensaver` | `internal/tokensaver/` | Token Saver optimization suite: tool output compression (RTK - ANSI stripping, log deduplication, git diff compacting, head/tail truncation), brevity prompt injection (Caveman), minimal code bias (Ponytail), and conversation middle context pruning (Headroom). Standalone compression via `POST /v1/compress`. |
| `watch` | `internal/watch/` | File watcher combining `fsnotify`, periodic polling, and SIGHUP signals for atomic configuration hot-reloading with quiet-window debounce coalescing. |
| `reqid` | `internal/reqid/` | Leaf package managing context-bound request identifiers without external dependencies. |
| `binx` | `internal/binx/` | Global binary management utilities: platform-specific executable naming (`ExecutableName`), fallback directory resolution (`DefaultDir`), binary resolution (`Find` across PATH, home, local paths, and custom directories), archive extraction (`ExtractTarGz`), and atomic HTTP binary downloading (`Download`). |
| `httpx` | `internal/transport/httpx/` | Middleware pipeline: `RequestID`, `Metrics`, `Recover`, `Logging`, `GlobalLimiter` (admission semaphore), `Auth`, context utilities, `IsNil`. |
| `upstream` | `internal/transport/upstream/` | Outbound HTTP client pool (`upstream.Pool`), Circuit Breaker (`Breaker`), KeyRing selection & 429/401 cooldown handler, key error policy (`HandleKeyOutcome` with granular per-status `KeyErrorRule` support), multi-protocol active health checker (`HealthChecker` with host reachability fallback and probe key error policy routing), and the shared post-attempt decision engine (`ProcessAttemptOutcome` in `attempt.go`) that centralizes breaker classification, key failover, and metrics for every adapter. Also houses centralized OpenCode ID generation and session utilities (`health_opencode.go`). |
| `warp` | `internal/transport/warp/` | Embedded Cloudflare WARP and Egress Proxy Engine. Implements zero-privilege userspace WireGuard via `golang.zx2c4.com/wireguard` and `tun/netstack` (gVisor TCP/IP), dynamic device registration with Cloudflare edge (`https://api.cloudflareclient.com`), SOCKS5/HTTP egress routing, singleflight-coalesced lock-free session rotation, eager cold-start tunnel warm-up (`WarmUp`, called by `main` so `/api/warp/status` reports a live session from boot instead of after the first rotation tick), periodic background auto-rotation scheduler (`StartAutoRotation`, `-warp-rotate-interval`), and non-disruptive session drain. |
| `openai` | `internal/adapter/openai/` | OpenAI wire-compatible adapter, SSE streaming relay engine (`RelaySSE`), stream idle watchdog, transparent gzip/deflate response decompression (`decodeResponseBody`), OpenAI error formatting (`WriteError`), and a configurable output-token default/floor policy (`DefaultMaxTokens`/`MinMaxTokens`, `applyMaxTokensPolicy`) that gives reasoning models enough completion budget without breaking transparent pass-through (both `0` = disabled). |
| `tunnel` | `internal/transport/tunnel/` | Embedded Cloudflare Tunnel (cloudflared) Ingress Engine. Orchestrates managed child process lifecycle tied to root context, extracts public trycloudflare URLs or operates persistent named tunnels via tokens, and provides real-time API status via `GET /api/tunnel/status`. |

| `anthropic` | `internal/adapter/anthropic/` | Anthropic Claude Messages adapter (`/v1/messages`), bi-directional schema translation for payloads and SSE chunks. |
| `antigravity` | `internal/adapter/antigravity/` | Google Antigravity Cloud Code adapter, translating OpenAI requests to Google Cloud Code internal protobuf/JSON protocols with authentic Google Cloud companion project ID binding and non-blocking OAuth onboarding. |
| `cline` | `internal/adapter/cline/` | Cline OAuth adapter, request rewriting with `HTTP-Referer`/`X-Title` headers, envelope unwrapping, and SSE streaming relay. |
| `codebuddy` | `internal/adapter/codebuddy/` | CodeBuddy (CN & Intl) adapter supporting device authorization flows, request forwarding, and SSE relays. |
| `grok` | `internal/adapter/grok/` | Grok CLI / Grok Build adapter (`grok-cli` protocol) for the xAI Grok CLI inference API (`cli-chat-proxy.grok.com/v1/responses`, OpenAI Responses API). Authenticates with an xAI OAuth bearer token — either harvested into the key ring as `KeySlot.Secret` or resolved from a native OAuth connection (`oauth:<id>`, onboarded via the `grokcli` provider and refreshed proactively by the background `Refresher`). Translates OpenAI Chat Completions → Responses `input`, sends the `x-grok-*` client fingerprint headers, and translates the Responses-API SSE stream back to OpenAI SSE/JSON. Recognizes `grok-build` plus the `grok-4.5`/`grok-4.6`/`grok-4.7` families with synthesized `-low`/`-medium`/`-high` effort variants; unknown ids route verbatim without `reasoning.effort` (a family that may reject the field is never sent one). Credential errors (401/403/429) stay in Layer 1 (cooldown/failover) and never trip the breaker. Base URL is pinned in `domain.OAuthManagedBaseURL`. |
| `opencode` | `internal/adapter/opencode/` | OpenCode adapter (`opencode` & `opencode-go` protocols) for OpenCode Zen gateways (`https://opencode.ai/zen/go/v1` and `https://opencode.ai/zen/v1`). Supports both keyless OpenCode Free tier (`Bearer public`, `desktop` client fingerprint, global project ID) and OpenCode Go subscription API keys. Dispatches dual-route traffic (`/responses` for `muse-*`, `grok-*`, `gpt-5.6-luna` with Chat $\rightarrow$ Responses payload translation, multi-turn conversation support with `output_text` for assistant turns, full Cline tool-calling compatibility via sequential 0-based delta streaming and `finish_reason: "tool_calls"`; `/chat/completions` for standard models with tool schema sanitization and OpenAI SSE relay). Enforces session isolation via canonical `x-opencode-session: ses_<descending_id>` conforming to upstream free-tier regex validation. |
| `auth` | `internal/security/auth/` | Master password vault, PBKDF2/salted SHA-256 session token manager, and tenant key verification. |
| `oauth` | `internal/security/oauth/` | Third-party AI OAuth manager, token refresh lifecycle, encrypted JSON credential vault (`oauth.json`), and provider implementations (`providers/antigravity`, `providers/cline`, `providers/codebuddy`, `providers/grokcli`). The `grokcli` provider runs the xAI RFC 8628 device flow (`auth.x.ai`) and rotates refresh tokens; it plugs into the existing `Refresher`/`TokenSource` lifecycle, so `oauth:<id>` grok-cli credentials refresh proactively like every other provider. |
| `logging` | `internal/observability/logging/` | Structured `slog.Logger` wrapper with custom `RedactHandler` for automatic token and credential header masking. |
| `metrics` | `internal/observability/metrics/` | Isolated Prometheus registry exposing latencies, in-flight gauges, cooldown counters, and circuit breaker states. |
| `usage` | `internal/observability/usage/` | Usage recorder and token ledger for metering inference traffic. |
| `turso` | `internal/storage/turso/` | Optional Turso/libSQL backing store: catalog + settings persistence (`SaveSettings`/`LoadSettings`), the provider/key CRUD store (`provider_store.go`), periodic syncer, and the usage flusher that persists key lifecycle actions. Deletes are gated by authoritative `manage_*` flags so a partial/stale save never wipes the catalog. Coordinated via `Client.Lock`/`RLock` with single-connection pooling, exponential busy backoff, and self-healing rollback to prevent stale transaction deadlocks. |
| `analytics` | `internal/storage/analytics/` | Persistent disk store for request execution logs, token ledger metrics, and manual/automatic circuit breaker overrides. |

---

## 3. End-to-End Request Flow Diagram

```
[HTTP Client]
    │
    ▼ 1. Incoming Connection
[Drain Guard (server.Server)] ──(If Server Is Shutting Down)──► HTTP 503 Service Unavailable
    │
    ▼ 2. Global Middleware Pipeline (httpx)
    ├── RequestIDMiddleware      : Generate UUIDv4 / extract X-Request-ID -> Context
    ├── MetricsMiddleware        : Record total requests, status codes, & duration
    ├── RecoverMiddleware        : Catch panics, log stack trace, return HTTP 500 JSON
    ├── LoggingMiddleware        : Output structured JSON logs with secret masking
    └── GlobalLimiter (Admission): Server-wide in-flight semaphore (Default 1,500 slots).
                                   If full, queues up to 1.5s. If timeout -> HTTP 429
    │
    ▼ 3. Routing (http.ServeMux - Go 1.22+)
    ├── GET  /healthz             : Bypass admission & auth -> HTTP 200 "ok"
    ├── GET  /api/docs{,/admin}   : Interactive OpenAPI 3.1 reference docs
    ├── GET  /api/openapi*.yaml   : OpenAPI 3.1 specs with build version stamping
    ├── CRUD /api/tenants{,/{name}}: Admin tenant management CRUD
    ├── CRUD /api/providers{,/{id}}: Admin provider catalog CRUD (create/update/delete, operator-token only)
    ├── GET  /api/providers/{id}/keys : Hint-only key listing (`api_key_hint`, never the raw secret)
    ├── POST /api/providers/{id}/keys : Batch key upsert; `PATCH /api/keys/{id}` rotates a secret
    │                                   in place preserving the row `id` (refs stay stable)
    ├── GET  /v1/models           : AuthMiddleware -> Filter catalog by tenant access -> JSON
    ├── GET  /v1/models/{id}      : AuthMiddleware -> Check model access -> JSON / 404
    └── POST /v1/chat/completions : Protected Handler (Auth -> Admission -> forwardEndpoint)
        POST /v1/completions
        POST /v1/embeddings
        POST /v1/compress
    │
    ▼ 4. Route Protection Middleware (deps.protected)
    ├── AuthMiddleware            : Validate `Authorization: Bearer sk-gw-...`. Lookup tenant.
    │                               If invalid -> HTTP 401 Unauthorized JSON
    └── AdmissionMiddleware       : Verify tenant RPS token bucket & in-flight limits.
                                    If tenant overlimit -> HTTP 429 (Retry-After: 1)
    │
    ▼ 5. Forward Handler Execution (server.forwardEndpoint)
    ├── Fast-fail Size Check      : Reject if Content-Length > 32 MB -> HTTP 413
    ├── Buffer Pooling            : Borrow `bytes.Buffer` from `requestBufferPool` (sync.Pool)
    │                               Read body up to 32 MB via `io.LimitReader`
    ├── Fast Decode Routing Field : Zero-alloc extract `model` & `stream` via gjson.
    │                               Validate `model != ""` -> If empty HTTP 400
    ├── Target & Model Resolution : Snapshot resolves model:
    │                               - If Virtual Combo: load balance across member models (least_inflight, round_robin, failover).
    │                               - If Direct Model: map directly to target Upstream & KeySlot.
    │                               Check Circuit Breaker status (`Breakers.Allow(name)`).
    │                               If primary breaker open, route to candidate fallback.
    ├── KeyRing Key Selection     : Select best KeySlot (LeastInflight or RoundRobin)
    │                               excluding keys in cooldown (429) or revoked (401).
    │                               If all keys cooldown -> HTTP 429 (Retry-After: 1)
    ├── Credential In-Flight Gate : `deps.Limiter.AcquireKeySlot()` acquires key concurrency ticket.
    │                               If key limit reached -> HTTP 429
    │
    ▼ 6. Upstream Adapter Execution (ports.UpstreamAdapter)
    ├── Protocol Lookup           : Select adapter (`openai` or `anthropic`)
    ├── Header & Body Prep        : Strip hop-by-hop, client auth, and Accept-Encoding headers
    │                               (so the transport negotiates encoding and gzip/deflate
    │                               responses are transparently decompressed, never relayed raw).
    │                               Inject secret upstream API key (`KeySlot.Ref`).
    │                               Rewrite public model name to upstream private name.
    │                               (On Anthropic: convert OpenAI schema to Anthropic Messages).
    ├── Outbound HTTP Call        : Borrow persistent `http.Client` from `upstream.Pool`.
    │                               Execute HTTP POST with client context.
    │
    ▼ 7. Upstream Response Handling
    ├── Case 1: Upstream 429      : KeyRing sets dynamic cooldown via `Retry-After` header.
    │                               Failover instantly to next available key in KeyRing!
    ├── Case 2: Upstream 401      : KeyRing marks key permanently revoked (`MarkRevoked`).
    ├── Case 3: Upstream 5xx/Err  : Report failure to Circuit Breaker (`breaker.Report(ok=false)`).
    │                               Retry fallback if zero response bytes sent to client.
    └── Case 4: Upstream Success (200 OK)
        ├── Non-Streaming Body    : Buffer JSON response -> Write to ResponseWriter
        └── Streaming SSE Body    : Activate `openai.RelaySSE()`:
                                    - Set headers: `text/event-stream`, `Cache-Control: no-cache`
                                    - Attach `StreamWatchdog` (idle gap timeout & client abort)
                                    - Read line-by-line using buffer from `readerPool`
                                    - Call `flusher.Flush()` immediately after each event
                                    - Recycle buffer back to pool upon stream completion
    │
    ▼ 8. Request Completion & Teardown
    ├── Release Credential Slot   : Release key concurrency ticket (`releaseCred()`)
    ├── Release Tenant Slot       : Release tenant in-flight ticket (`releaseTenant()`)
    ├── Record Usage & Metrics    : Record token/request usage to `UsageRecorder` & Prometheus
    └── Release Buffer            : Reset and return `bytes.Buffer` to `requestBufferPool`
```

---

## 4. Connection Architecture & Upstream Components

### 4.1. Client Pool Management (`upstream.Pool`)
Outbound HTTP connections to upstream AI providers carry substantial TLS and TCP handshake overhead.
- Firefly maintains **one singleton `*http.Client` per upstream configuration**, cached indefinitely in `upstream.Pool`.
- Configured with high-concurrency transport tuning:
  ```go
  MaxIdleConns:          2000,
  MaxIdleConnsPerHost:   1000,
  MaxConnsPerHost:       1500,
  IdleConnTimeout:       90 * time.Second,
  ForceAttemptHTTP2:     true,
  ResponseHeaderTimeout: 30 * time.Second, // Timeout to first byte (TTFB)
  ```
- **Important Note:** `http.Client.Timeout` **is not set** (remains 0) so long-lived SSE streaming inference connections are never cut off mid-generation.
- `CheckRedirect` returns `http.ErrUseLastResponse` because AI API endpoints should never issue redirects; redirects indicate misconfiguration and must not be followed silently.

### 4.2. 2-Layer Resilience Architecture

1. **Layer 1 — `upstream.KeyRing` (429 & 401 Handling):**
   - Supports multiple keys per upstream using `round_robin` or `least_inflight` rotation strategies.
   - **Dynamic Cooldown:** When an upstream returns `429 Too Many Requests`, cooldown duration is parsed from the `Retry-After` header (RFC 7231 / integer seconds, default 30s, max 5m) and recorded via `MarkCooldownAt(nowNano)`.
   - Keys under cooldown are bypassed in a lock-free manner without lock contention.
   - If an upstream returns `401 Unauthorized`, the key is marked permanently revoked (`MarkRevoked`) until the next configuration reload.
   - **Automated Threshold Key Removal from KeyRing:** When a key crosses the consecutive-error threshold (`KeyErrorThreshold` or a matching `KeyErrorRule`) and executes a terminal action (`delete` or `deactivate`), the key is not only persisted to the database sink (`ports.KeyActionNotifier`), but also immediately removed from the live in-memory `KeyRing` via lock-free Copy-On-Write (`KeyRing.RemoveSlot`). This ensures subsequent background health probes and incoming requests immediately stop targeting the dead key, and request failover (`selectNextKey`) cleanly rotates across any remaining keys in the ring.
   - A 429 on a key triggers immediate failover to the next available key in the keyring as long as zero response bytes have been transmitted to the client.
   - **Rotation Must Survive Hot-Swaps (Non-Negotiable):** Key selection fairness is anchored to a process-wide `globalRotation` counter in `internal/domain`, NOT to a per-`KeyRing` cursor. Every catalog hot-swap rebuilds each `KeyRing` via `NewKeyRing`; a per-ring cursor would reset to 0 on every rebuild and, combined with frequent reloads, would collapse all traffic onto the first few keys of a large ring. `NewKeyRing` (and `Combo.NextCursor`) therefore seed their starting offset from `globalRotation` so rotation progress persists across reloads. Distribution stays uniform across the entire ring regardless of ring size (1,000+ keys) or reload frequency. The `inflight == 0` early-`break` in `selectLeastInflight` is a valid optimization (0 is the minimum possible inflight) — fairness is guaranteed by the rotating start offset, not by where the scan stops.
   - **Do Not Trigger Reloads on Usage Metering:** The Turso syncer treats `MAX(api_keys.updated_at)` as a structural-change signal. Pure usage metering (`total_requests`, `last_used_at`) MUST NOT bump `updated_at`, otherwise active traffic forces a full catalog reload every sync interval (rebuilding every `KeyRing`). Only structural changes (key added/removed/revoked/deactivated, catalog revision bump) may advance that signal.
   - **Never Fabricate or Retain Empty-Secret Slots:** DB-sourced credentials whose secret has not yet replicated (empty `secret`, no joined `api_keys` row) are dropped at load time in `LoadCatalogSnapshot`. `config.translateUpstream` only resolves a credential `ref` from the environment when it matches POSIX ENV var naming (`envVarNameRe`); a DB-style ref (e.g. `openai-cred-3`) with an empty secret fails with a clear `empty secret` error rather than being misread as an ENV var (which previously collapsed the whole build into zero-config mode).

2. **Layer 2 — `upstream.Breaker` (Upstream Host Circuit Breaker):**
   - Protects the gateway from upstream host outages, network partitions, or repeated 5xx errors.
   - States:
     - `StateClosed`: Normal traffic forwarding.
     - `StateOpen`: Triggered by 5 consecutive failures (`FailureThreshold = 5`). Requests fail fast or redirect to fallback upstreams without touching the network.
     - `StateHalfOpen`: After a 10-second cooldown, 2 trial requests (`SuccessThreshold = 2`) are allowed through. If both succeed, status transitions back to `StateClosed`.
   - **False Outage Prevention:** Client 4xx errors or key 429 quota exhaustion **NEVER** count as circuit breaker failures.

---

## 5. SSE Streaming Engine (`openai.RelaySSE`) & Disconnect Abort

Streaming LLM inference requires strict lifecycle management to prevent memory leaks, socket hangs, and upstream billing waste:

1. **Immediate Token Flusher:**
   The handler checks `flusher, ok := w.(http.Flusher)`. Stream headers (`text/event-stream`, `Cache-Control: no-cache`, `Connection: keep-alive`, `X-Accel-Buffering: no`) are sent immediately, and `flusher.Flush()` is called right after each token line is received from upstream.
2. **Stream Watchdog & Idle Timeout:**
   `newStreamWatchdog(ctx, body, idleTimeout)` spawns a background watchdog goroutine:
   - If upstream stops transmitting bytes exceeding `idleTimeout` (e.g. upstream hangs without closing the socket), the watchdog immediately calls `body.Close()`.
   - Calling `body.Close()` forces active blocking `Read()` calls to produce an IO error, terminating the loop and releasing the goroutine and socket.
3. **Client Disconnect Abort:**
   The main loop in `RelaySSE` checks `ctx.Done()` on every iteration. Once the client disconnects (browser window closed or client-side cancellation), the context cancels, `body.Close()` is called, and upstream reading ceases immediately. This stops ongoing token generation and billing on the provider account.
4. **Zero-Allocation Event Framing:**
   Employs `bufio.Reader` borrowed from `readerPool` (`sync.Pool`) with a 32 KiB buffer to process lines without per-chunk heap allocations.

---

## 6. Multi-Layer Admission & Rate Limiting

Firefly enforces three layers of traffic control:

```
[Inbound TCP]
      │
      ▼ (Layer 1: Server-Wide In-Flight Semaphore)
[GlobalLimiter] ──(1,500 slots saturated & 1.5s timeout)──► HTTP 429 Fail-Fast
      │
      ▼ (Layer 2: Per-Tenant RPS & In-Flight Gate)
[AdmissionMiddleware] ──(Tenant RPS / MaxConcurrent exhausted)──► HTTP 429
      │
      ▼ (Layer 3: Per-Credential / KeySlot In-Flight Gate)
[Limiter.AcquireKeySlot] ──(Provider account slot saturated)──► HTTP 429
      │
      ▼
[Upstream Call]
```

1. **Layer 1 — `httpx.GlobalLimiter` (Global Admission Gate):**
   - Channel semaphore `chan struct{}` with 1,500 slots.
   - When saturated, requests queue up to `waitTimeout` (default 1.5s).
   - If queue expires, requests fail fast with `HTTP 429` and `Retry-After: 2` to protect the process from memory exhaustion.
   - Endpoint `/healthz` bypasses this check.
2. **Layer 2 — `httpx.AdmissionMiddleware` (Per-Tenant Limiter):**
   - Enforces tenant RPS via Token Bucket (`golang.org/x/time/rate`).
   - Limits tenant concurrency (`Tenant.MaxConcurrent`).
3. **Layer 3 — `Limiter.AcquireKeySlot` (Per-Credential / KeySlot Limiter):**
   - Restricts concurrent requests per individual upstream API key account.
   - Enforced via atomic CAS counters (`atomic.Int64`) with zero mutex overhead.

---

## 7. Developer & AI Agent Guidelines

When making bug fixes, refactorings, or feature additions:

### Pre-Commit / Pre-Completion Verification Checklist:
- [ ] **Interface Safety:** Always use `httpx.IsNil` when validating dependencies (avoid bare `if iface == nil`).
- [ ] **Error Classification:** Ensure upstream 4xx status codes never invoke `breaker.Report(false)`.
- [ ] **Context Propagation:** Ensure client `r.Context()` is consistently propagated to outbound adapter calls.
- [ ] **Buffer Cleanup:** Ensure every borrow from `sync.Pool` has a paired `defer` that resets and returns the buffer if capacity `<= 512KB`.
- [ ] **Goroutine Leak Check:** Background goroutines must always terminate cleanly upon `ctx.Done()`.

### Verification Commands:
```bash
# 1. Run complete test suite with Go race detector enabled
go test -count=1 -race ./...

# 2. Run server unit tests specifically
go test -v -race ./internal/server/...

# 3. Run middleware & admission tests
go test -v -race ./internal/transport/httpx/...

# 4. Run memory allocation benchmarks
go test -benchmem -bench=. ./internal/server/...
```
