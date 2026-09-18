# Firefly

[![Go Version](https://img.shields.io/badge/go-1.22+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](./LICENSE)
[![CI Status](https://github.com/dickymuliafiqri/firefly/actions/workflows/ci.yml/badge.svg)](https://github.com/dickymuliafiqri/firefly/actions)
[![Concurrency](https://img.shields.io/badge/Concurrency-1%2C000%2B%20SSE%20Streams-emerald)](./docs/loadtest.md)

Firefly is a high-concurrency, multi-tenant, OpenAI-wire-compatible AI reverse proxy and API gateway. Written in pure Go, Firefly is specifically engineered to serve thousands of simultaneous Server-Sent Events (SSE) streaming connections (1,000+ concurrent inference streams) with zero allocations on the hot path, Virtual Combos for model load balancing, lock-free API key rotation, and an embedded nocturnal React management dashboard.

---

## Backend System Architecture

Firefly strictly isolates the client data plane (request forwarding) from the admin plane (observability, metrics, and configuration management):

```
                          [ HTTP Clients / AI SDKs ]
                                    │
                                    ▼ 0.0.0.0:8080 (Data Plane)
 ┌────────────────────────────────────────────────────────────────────────┐
 │  [ Drain Guard ] ──► [ RequestID ] ──► [ Metrics ] ──► [ Recover ]    │
 │                           │                                            │
 │                           ▼                                            │
 │                 [ Logging (Masked slog) ]                              │
 │                           │                                            │
 │                           ▼                                            │
 │             [ Global Admission Limiter (1,500 slots) ]                 │
 │                           │                                            │
 │                           ▼                                            │
 │                     [ http.ServeMux ]                                  │
 │         ┌─────────────────┼─────────────────┬────────────────┐         │
 │         ▼                 ▼                 ▼                ▼         │
 │    GET /healthz     GET /api/settings  GET /v1/models   POST /v1/chat  │
 │    (Probe Bypass)   (Admin Config)     (Tenant Catalog) POST /v1/comp  │
 │                                       POST /v1/embeddings              │
 │                                             │                          │
 │                                             ▼                          │
 │                              ┌──────────────────────────────┐          │
 │                              │ Route Protection Middleware  │          │
 │                              │  - Auth (Bearer Token Check) │          │
 │                              │  - Tenant Admission (RPS)    │          │
 │                              └──────────────┬───────────────┘          │
 │                                             │                          │
 │                                             ▼                          │
 │                                   [ forwardEndpoint ]                  │
 │                                             │                          │
 │                  ┌──────────────────────────┴──────────────────────┐   │
 │                  ▼                                                 ▼   │
 │     [ Target & KeyRing Selection ]                   [ sync.Pool Buffer ]
 │     (Least-Inflight / Round-Robin)                   (Max 32MB / Cap Guard)
 │                  │                                                     │
 │                  ▼                                                     │
 │       [ Upstream Adapter ] (OpenAI / Anthropic Claude)                 │
 └──────────────────┬─────────────────────────────────────────────────────┘
                    │
                    ▼
 ┌────────────────────────────────────────────────────────────────────────┐
 │                         OUTBOUND CONNECTION LAYER                      │
 │  [ upstream.Pool ] ──► Persistent *http.Client (HTTP/2 Multiplex)      │
 │  [ Layer 1: KeyRing ] (429/401 Cooldown)  │ [ Layer 2: Breaker ] (5xx) │
 │                                           ▼                            │
 │                    [ Upstream AI Providers ]                           │
 └────────────────────────────────────────────────────────────────────────┘
```

---

## End-to-End Request Processing Flow

Every request entering Firefly follows a strict, deterministic lifecycle:

### 1. Ingress & Drain Guard
- Requests arrive on the data plane listener (default `0.0.0.0:8080`).
- **Zero-Config First Run:** Firefly boots smoothly even without initial configuration files, automatically generating default JSON schemas and waiting for configuration via the Admin UI/API (`/api/settings`).
- **Drain Guard:** Atomically checks the server lifecycle state. When graceful shutdown is initiated, new incoming requests are immediately rejected with `HTTP 503 Service Unavailable` (`Retry-After: 5`), while active SSE streaming connections are given up to `ShutdownGrace` (default 30s) to conclude naturally.

### 2. Global Middleware Pipeline (`httpx`)
1. **`RequestIDMiddleware`:** Ingests the client's `X-Request-ID` or generates a cryptographically random UUIDv4, attaching it to both `context.Context` and the response headers.
2. **`MetricsMiddleware`:** Records request duration, status codes, and in-flight request counters to Prometheus.
3. **`RecoverMiddleware`:** Protects against panics. Any unhandled panic is safely recovered, stack traces are captured to logs, and an `HTTP 500` JSON response is returned if headers have not yet been written.
4. **`LoggingMiddleware`:** Outputs structured JSON access logs via `log/slog` with automatic secret redaction (`RedactHandler`).
5. **`GlobalLimiter`:** A server-wide concurrency semaphore (default 1,500 slots). If saturated, requests queue for up to 1.5 seconds before failing fast with `HTTP 429 Too Many Requests` (`Retry-After: 2`). The `/healthz` probe bypasses this limiter entirely.

### 3. Route Multiplexing (`http.ServeMux` Go 1.22+)
- `GET /healthz` -> Returns `HTTP 200 "ok"` immediately without authentication.
- `GET /v1/models` & `GET /v1/models/{id}` -> Authenticated routes returning catalog models accessible to the requesting tenant.
- `GET /v1/usage` -> Authenticated endpoint returning current token quota, consumed tokens, remaining balance, and validity/expiration.
- `POST /v1/chat/completions`, `POST /v1/completions`, `POST /v1/embeddings` -> Routed to the shared proxy execution engine `forwardEndpoint`.
- `POST /api/tenants/topup` -> Admin/Webhook endpoint for topping up token balances and extending validity days.

### 4. Tenant Route Protection (`deps.protected`)
Before parsing the request body, requests pass through tenant protection:
1. **`AuthMiddleware`:** Validates `Authorization: Bearer sk-gw-...`. Tokens are verified against `TenantStore` via direct zero-allocation $O(1)$ memory lookup (`snap.TenantByKey`). Invalid tokens immediately yield `HTTP 401 Unauthorized` (`invalid_api_key`) using standard OpenAI JSON error schemas. Dependency interfaces are guarded with `httpx.IsNil` to eliminate typed-nil panics.
2. **`AdmissionMiddleware`:**
   - **Expiration Guard:** Checks if the key has expired (`expires_at < now`). Expired keys are rejected with `HTTP 401 Unauthorized` (`key_expired`).
   - **Token Quota Guard:** Checks if the accumulated tokens have exceeded `max_tokens`. Exhausted quotas fail fast with `HTTP 429 Too Many Requests` (`insufficient_quota`).
   - **Rate Limiting:** Enforces tenant RPS using Token Buckets (`golang.org/x/time/rate`) and limits concurrent in-flight requests (`MaxConcurrent`). Exceeded limits return `HTTP 429`.

### 5. Forward Handler Execution (`server.forwardEndpoint`)
The unified handler for inference endpoints:
- **Body Size Check:** Immediately rejects bodies larger than 32 MB with `HTTP 413 Payload Too Large`.
- **Buffer Recycling (`sync.Pool`):** Borrows `bytes.Buffer` instances from `requestBufferPool` and reads the body through `io.LimitReader`. Buffers with capacity `<= 512KB` are reset and returned to the pool; larger buffers are discarded to the Garbage Collector to avoid persistent heap bloat.
- **Fast Zero-Allocation JSON Decoding (`gjson`):** Extracts routing fields (`model` and `stream`) directly from raw byte slices using `tidwall/gjson`, bypassing AST allocations and `map[string]any` generation. Saves >90% heap allocations on large payloads.
- **Target & Fallback Resolution:** Resolves model-to-upstream mappings. If the primary upstream Circuit Breaker is `OPEN`, Firefly automatically reroutes traffic to configured fallback upstreams.
- **Lock-Free KeyRing Selection:** Selects an active API key using `least-inflight` or `round-robin` strategies. Keys under cooldown (due to upstream 429) or revoked (due to upstream 401) are bypassed lock-free.
- **Credential Concurrency Gate:** Limits concurrent requests per upstream API key using atomic CAS counters (`Limiter.AcquireKeySlot`). Saturated keys return `HTTP 429`.

### 6. Outbound Upstream Pooling (`upstream.Pool`)
- Outbound requests utilize singleton `*http.Client` instances pooled per upstream host.
- **High-Scale Transport Configuration:**
  - `MaxIdleConns: 2000`, `MaxIdleConnsPerHost: 1000`, `MaxConnsPerHost: 1500`.
  - `IdleConnTimeout: 90s`, `ForceAttemptHTTP2: true` for HTTP/2 connection multiplexing.
  - `ResponseHeaderTimeout: 30s`: Time-To-First-Byte (TTFB) timeout without cutting off long-running token streams.
  - `http.Client.Timeout = 0`: Explicitly unbounded so minutes-long LLM completions stream without premature interruption.

### 7. Upstream Response Handling & Streaming Engine (`openai.RelaySSE`)
- **Non-Streaming JSON:** Response bytes are buffered up to 32 MB and relayed directly to the client.
- **Streaming SSE (Server-Sent Events):**
  - Sends immediate headers: `text/event-stream`, `Cache-Control: no-cache`, `X-Accel-Buffering: no`.
  - Flushes tokens immediately using the `http.Flusher` interface.
  - **`StreamWatchdog`:** Continuously monitors data flow from upstream. If upstream stops sending bytes beyond the configured `stream_idle_timeout_ms`, the watchdog forcefully closes the socket to prevent hung goroutines.
  - **Client Disconnect Abort:** Listens to `ctx.Done()`. If the client disconnects or cancels the request, the upstream socket is closed instantly, halting further upstream token generation and billing.

### 8. Teardown & Observability
- In-flight concurrency tickets are released via `defer releaseCred()` and `defer releaseTenant()`.
- Latency, status codes, and saturation metrics are dispatched to Prometheus.
- Token consumption is recorded to `UsageRecorder`.
- Memory buffers are scrubbed and returned to `sync.Pool`.

---

## Multi-Layer Resilience Architecture

Firefly strictly decouples credential/quota failures from host infrastructure failures:

```
                       [Incoming Request]
                               │
               ┌───────────────┴───────────────┐
               ▼                               ▼
       [Layer 1: KeyRing]             [Layer 2: Breaker]
  (Credential & Quota Handling)   (Host Infrastructure Health)
  ─────────────────────────────   ────────────────────────────
  - Target: Individual API Keys   - Target: Upstream host (5xx/TCP)
  - HTTP 429: Dynamic cooldown    - Trip: 5 consecutive failures
    (reads Retry-After header)    - Cooldown: 10 seconds in StateOpen
  - HTTP 401: Key revocation      - State: Closed -> Open -> HalfOpen
  - Failover: Switch to next      - Failover: Reroute to backup
    key in keyring instantly        upstream when breaker is OPEN
```

| Dimension | Layer 1: KeyRing (Credentials & Quotas) | Layer 2: Circuit Breaker (Host Infrastructure) |
| :--- | :--- | :--- |
| **Target Scope** | Individual API Key (`KeySlot`) | Entire upstream host server |
| **Error Triggers** | HTTP `429 Too Many Requests` & `401 Unauthorized` | HTTP `5xx` & Transport Errors (TCP Reset, Dial Timeout) |
| **429 Action** | Dynamic cooldown parsed from `Retry-After` (RFC 7231, default 30s) | Never trips or affects Circuit Breakers |
| **401 Action** | Permanent revocation of key slot until next configuration reload | Never trips or affects Circuit Breakers |
| **Failover Mechanism** | Transparently retries with the next available key in keyring | Reroutes request to fallback upstream if breaker is `OPEN` |
| **Trip Threshold** | N/A | 5 consecutive failures trigger `StateOpen` (10s cooldown) |

### Active Health Checking (`golang.org/x/sync/errgroup`)
In addition to passive inline request error classification, Firefly runs an active background [`HealthChecker`](internal/transport/upstream/health.go):
- Periodically probes upstream endpoints using `errgroup.WithContext` with bounded concurrency (`SetLimit(10)`).
- Multi-protocol probe dispatching: automatically selects OpenAI, Anthropic (`/v1/messages`), or OpenCode (dual-route `/responses` or `/chat/completions` with authentic client headers and payload templates).
- Host reachability fallback: if a model-level probe encounters an error or timeout, verifies the upstream host reachability before tripping the Circuit Breaker.
- Respects Circuit Breaker cooldowns and safely triggers `StateHalfOpen` validation.
- Accurately classifies `< 500` status codes (including 401/404) as healthy host transport (Layer 2), preventing key credential errors or keyless free-tier responses from causing false infrastructure outages.
- Verified leak-free on server shutdown via `goleak`.

---

## Multi-Protocol Adapters & Native OAuth Integrations

Firefly provides clean interface contracts (`ports.UpstreamAdapter`) for multi-provider routing and native third-party authentication:
- **OpenAI Adapter (`internal/adapter/openai`):** Near-transparent proxy for OpenAI, Azure OpenAI, vLLM, and Ollama. Rewrites public model identifiers to private provider names, cleans hop-by-hop headers, and injects upstream secrets from environment variables or keyring storage.
- **Anthropic Claude Adapter (`internal/adapter/anthropic`):** Provides transparent bi-directional schema translation between OpenAI Chat Completions and the Anthropic Messages API (`/v1/messages`), supporting both non-streaming payloads and SSE streaming chunks.
- **Google Antigravity Cloud Code Adapter (`internal/adapter/antigravity`):** Translates incoming OpenAI inference requests into Google Cloud Code internal protocols with authentic Google Cloud companion project binding, supporting Gemini 3.8/3.7 Flash tiers, Gemini 2.5 Pro/Flash, and Claude 3.7/4.6 Sonnet inference models with non-blocking OAuth onboarding.
- **Cline OAuth Adapter (`internal/adapter/cline`):** Proxies completions to Cline API endpoints (`api.cline.bot`), automatically attaching client identification headers (`HTTP-Referer`, `X-Title`), unwrapping payload envelopes, and relaying SSE streams.
- **CodeBuddy China & International Adapters (`internal/adapter/codebuddy`):** Manages RFC 8628 device authorization grants, payload rewrites, and completion streaming for CodeBuddy China and International endpoints.
- **Grok CLI / Grok Build Adapter (`internal/adapter/grok`):** Routes to the xAI Grok CLI inference API (`cli-chat-proxy.grok.com`, OpenAI Responses API) with an xAI OAuth bearer token, translating OpenAI Chat Completions to/from the Responses wire format (including reasoning/thinking output) and pooling harvested account tokens across the key ring.
- **OpenCode & OpenCode Go Adapter (`internal/adapter/opencode`):** Routes to OpenCode Zen gateways (`https://opencode.ai/zen/v1` for keyless Free tier and `https://opencode.ai/zen/go/v1` for OpenCode Go subscription API keys). Supports deterministic session isolation (`x-opencode-session`), dual-route dispatching (translating OpenAI Chat Completions to/from Responses API for `muse-*`, `grok-*`, and `gpt-5.6-luna`, multi-turn conversation support with `output_text` for assistant turns, full Cline tool-calling compatibility via sequential 0-based delta streaming and `finish_reason: "tool_calls"`, while routing standard chat models to `/chat/completions`), tool schema sanitization, and automatic endpoint tier cross-correction.

---

## Third-Party AI OAuth & Multi-Account Rotation

Firefly natively integrates OAuth 2.0 authorization code and device code flows directly within the gateway:
- **Zero Environment Variable Restarts:** Connect accounts interactively from the web dashboard. OAuth tokens and refresh lifecycles are persisted in encrypted backend storage (`configs/oauth.json`).
- **Multi-Account Upstream Keyrings:** Bind multiple third-party accounts (e.g. 2+ Cline or Google accounts) to a single upstream host. Firefly pools and rotates requests across account slots using `least_inflight` or `round_robin` strategies, automatically failing over if any account hits rate limits (`HTTP 429`).
- **Dynamic Token Resolution:** References formatted as `oauth:<connection_id>` dynamically resolve valid bearer tokens on every outbound request, proactively refreshing tokens before expiration.

---

## Active Model Verification & Health Probing

Before exposing a model route to client applications, Firefly enables active model verification directly from the **Create Model Route / Edit Route** dialog:
- **Minimal Inference Ping:** Dispatches a minimal inference check (`max_tokens: 1`) to the upstream host (`POST /api/upstreams/check` with `model`).
- **Immediate Status Classification:**
  - **HTTP 200 (Success):** Confirms model connectivity, returns TTFB latency in milliseconds, and verifies authorization.
  - **HTTP 401 / 403 (Auth Rejected):** Details permission issues or expired credentials.
  - **HTTP 404 (Not Found):** Detects misspelled model identifiers or models unavailable on the provider tier.
  - **HTTP 429 (Quota Exhausted):** Identifies upstream account exhaustion before sending user traffic.
- **Zero Secret Leakage:** Upstream API keys and OAuth tokens are evaluated server-side and never exposed in client payloads.

## Virtual Combos (Model-Level Load Balancing)

Virtual Combos aggregate multiple models across different upstreams into a single virtual model route:
- **Load Balancing Strategies**:
  - `least_inflight`: Routes to the candidate model currently processing the fewest concurrent requests.
  - `round_robin`: Cycles evenly across candidate models in a circular sequence.
  - `failover`: Sequential chain attempting primary model first, falling back to subsequent models on transport failures.
- **Unified Upstream Distribution**: Member models can point to different AI providers, naturally distributing load across disparate infrastructure without provider lock-in.
- **Configuration Schema (`combos.json`)**:
  ```json
  {
    "combos": [
      {
        "name": "smart-router",
        "models": ["gpt-4o-main", "claude-3-5-sonnet", "deepseek-chat"],
        "strategy": "least_inflight",
        "enabled": true
      }
    ]
  }
  ```

---

## Token Saver Optimization Suite (Prompt & Output Reduction)

Firefly includes a built-in, native optimization suite that transparently intercepts requests before forwarding to upstreams to reduce input/output token usage and provider costs by up to 60-90%:

- **Compress Tool Output (RTK)**: Cleans massive shell/command outputs before feeding them back into inference context.
  - Strips ANSI escape sequences and colors.
  - Deduplicates consecutive repeated log/progress lines (`... [RTK: repeated N times] ...`).
  - Compacts unmodified git diff context lines while keeping every hunk intact.
  - Applies smart head-and-tail truncation (default: 12,000 chars) to preserve initial invocation commands and final execution errors/summaries.
- **Compress LLM Output (Caveman)**: Injects targeted brevity directives into system prompts to eliminate conversational filler, pleasantries, and preambles, reducing model output tokens by ~65% (up to 87%).
- **Lazy Senior Dev (Ponytail)**: Biases model code generation toward minimal implementations, standard library reuse, deletion over addition, and strict YAGNI principles.
- **Compress Context (Headroom)**: Prunes older middle tool outputs and verbose conversation history when total body size exceeds configured thresholds (default: 32,000 tokens), preserving the system prompt and latest messages.
- **Direct API Access (`POST /v1/compress`)**: Standalone authenticated endpoint for compressing arbitrary text prompts or chat message arrays on demand.
- **Zero Overhead**: When disabled, requests pass through with zero heap allocations via fast-path routing. Configurable via **Settings → Token Saver Optimization Suite** with atomic hot-swapping.

---

## Commercial Tenant Keys, Token Quotas, & Monetization

Firefly provides out-of-the-box support for API key commercialization, allowing operators to issue, meter, and sell tenant API keys with strict token balance control and expiration dates:

- **Plaintext API Key Storage**: Keys are generated (`sk-gw-...`) and stored directly in plaintext without SHA-256 hashing barriers, making it straightforward to distribute and resell API keys to customers or teams.
- **Token Quota (Prompt + Completion)**: Enforce upper token limits (`max_tokens`) or allow unlimited consumption (`0`).
- **High-Concurrency Lock-Free Accounting**: Post-request token deductions happen in memory via atomic CAS counters (`*atomic.Int64`). Firefly never executes synchronous database writes or locks on the inference data plane, guaranteeing zero `SQLITE_BUSY` lock contention across 1,000+ simultaneous streams.
- **Asynchronous Batch Flusher**: An automated background worker periodically flushes accumulated token consumption to Turso/SQLite in micro-batches.
- **Validity & Expiration Dates (`expires_at`)**: Keys can be provisioned with an expiration epoch timestamp (e.g. 7 days, 30 days, 1 year). Expired keys are rejected at pre-flight admission with `HTTP 401 Unauthorized` (`key_expired`).
- **Quota Exhaustion**: Requests from tenants that exceed their token limit fail fast at admission with `HTTP 429 Too Many Requests` (`insufficient_quota`).
- **Client Usage Endpoint (`GET /v1/usage`)**:
  Tenants can programmatically inspect their quota balance, consumed tokens, remaining balance, and expiration timestamp:
  ```bash
  curl -X GET http://localhost:8080/v1/usage \
    -H "Authorization: Bearer sk-gw-YOUR_KEY"
  ```
- **Automated Top-Up & Payment Webhooks (`POST /api/tenants/topup`)**:
  Protected administrative endpoint for topping up token balances and extending validity days upon checkout completion (e.g. Stripe, Midtrans, Lemon Squeezy webhooks):
  ```bash
  curl -X POST http://localhost:8080/api/tenants/topup \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer <ADMIN_TOKEN>" \
    -d '{
      "api_key": "sk-gw-YOUR_KEY",
      "add_tokens": 5000000,
      "extend_days": 30
    }'
  ```

---

## Embedded Web Dashboard & Navigation Security

Firefly ships with a nocturnal React 19 Single Page Application embedded directly into the standalone binary (`//go:embed all:dist`):
- **Responsive Mobile & Desktop**: Clean, unified navigation across all viewports with a nocturnal bioluminescent mobile burger menu, adaptive viewport stability (`min-h-[100dvh]`), and touch-friendly controls.
- **Public Overview & Sanitized Settings**: Unauthenticated visitors can view real-time traffic pulse, particle canvas physics, latency charts, active upstream/model names, and recent activity logs. Sensitive credentials (upstream API keys, credential pools, tenant keys, rate limits, Turso DB credentials, and Auto-TLS certificates) are strictly redacted or omitted on the backend until admin authentication.
- **Protected Route Navigation**: Accessing management modules (*Upstreams, Models, Combos, Tenants, Telemetry, Settings, Playground*) is gated behind backend session token authentication (default: `12345678`).
- **Backend Credential Vault**: Passwords and session tokens are stored and verified exclusively on the backend (`configs/auth.json`), eliminating sensitive credential storage in browser `localStorage`.
- **In-Browser LLM Playground**: Test model endpoints, inspect token streaming waterfalls, and review raw SSE packet diagnostics.

---

## One-Line Production Installation

Install and run Firefly as an automated background system service on any Linux server (Debian, Ubuntu, Fedora, CentOS, RHEL, Rocky, Arch, openSUSE, Alpine):

```bash
curl -fsSL https://raw.githubusercontent.com/dickymuliafiqri/firefly/main/install.sh | bash
```

Once installed, Firefly immediately runs as a managed `systemd` background daemon:
- **Dashboard Web UI**: `http://<SERVER_IP>:8080` (Default access password: `12345678`)
- **OpenAI API Surface**: `http://<SERVER_IP>:8080/v1/chat/completions`
- **Configuration Directory**: `/etc/firefly/`
- **Service Management**:
  ```bash
  sudo systemctl status firefly    # Check background status
  sudo systemctl restart firefly   # Restart gateway daemon
  sudo journalctl -u firefly -f    # Stream real-time logs
  ```

---

## Quick Start (Manual Build)

### Prerequisites
- Go 1.22 or higher.
- Bun 1.0+ or Node.js 18+ (optional, only required if rebuilding the embedded React frontend).

### Building & Running
```bash
# 1. Build the unified single-binary with embedded frontend
make build

# 2. Run Firefly with data plane and admin plane
./firefly \
  -config-dir=configs \
  -addr=0.0.0.0:8080 \
  -admin-addr=:9090 \
  -admin-token="your-admin-token" \
  -shutdown-grace-seconds=30
```

### CLI Command Flags & Environment Variables
- `-config-dir` / `FIREFLY_CONFIG_DIR`: Directory containing configuration files (`upstreams.json`, `models.json`, `tenants.json`, `combos.json`). Default: `configs`. If empty, Firefly boots in zero-config mode and auto-generates template files.
- `-addr` / `FIREFLY_ADDR`: Data plane listen address for incoming AI traffic. Default: `0.0.0.0:8080`.
- `-admin-addr` / `FIREFLY_ADMIN_ADDR`: Admin plane listen address for `/metrics` and `/api/*`. Disabled if empty. Default: `""`.
- `-admin-token` / `FIREFLY_ADMIN_TOKEN`: Bearer token protecting administrative endpoints.
- `-log-level` / `FIREFLY_LOG_LEVEL`: Structured logging level (`debug`, `info`, `warn`, `error`). Default: `info`.
- `-health-check-interval`: Frequency of background health probes (duration, e.g. `15s`; `0` disables). Default: `15s`.
- `-shutdown-grace-seconds` / `FIREFLY_SHUTDOWN_GRACE_SECONDS`: Maximum time allowed for active SSE streams to finish during shutdown. Default: `30`.
- `-version`: Print version information, commit hash, and build timestamp, then exit.

### Native Auto-TLS (Let’s Encrypt)

Auto-TLS is disabled by default and is configured from **Settings → Native Let's Encrypt Auto-TLS**. Enable it with one public hostname and an ACME notification email; Firefly persists the setting in `configs/tls.json` and its certificate/key cache in `configs/certificates/` (mode `0700`).

When enabled, Firefly starts an HTTP listener on `:80` for the HTTP-01 challenge and redirects other matching HTTP requests to HTTPS, then serves the normal Firefly handler on `:443`. DNS for the exact hostname must resolve to the server and TCP ports 80 and 443 must be reachable and unused. IP addresses, `localhost`, wildcard domains, and URL values are rejected; wildcard certificates require DNS-01 and are not supported by this native mode. The existing `-addr` HTTP listener remains unchanged for backward compatibility.

---

## Testing & Performance Validation

```bash
# Run entire unit & integration test suite with Go race detector enabled
go test -count=1 -race ./...

# Run goroutine leak detection tests (goleak)
go test -v -race -run Test.*Leak ./...

# Run memory allocation benchmarks
go test -benchmem -bench=. ./internal/server/...
```

---

## High-Concurrency Load Testing (100 & 1,000 Simultaneous Requests)

Firefly includes a standalone, detached benchmark CLI tool in `cmd/loadtest` with synchronized barrier concurrency:

```bash
# Build standalone load test tool
make build-loadtest

# Run 100 simultaneous requests against in-process mock (low load profile)
./bin/loadtest -mock -c 100 -profile low

# Run 1,000 simultaneous requests against in-process mock (low load profile)
./bin/loadtest -mock -c 1000 -profile low

# Run full automated 6-scenario benchmark suite (100 & 1,000 requests across low/med/heavy)
./bin/loadtest -mock -suite
```

For full workload profiles, metrics analysis, and CLI flags, see the [Load Testing & Benchmark Guide](./docs/loadtest.md).

---

## Documentation & References

- [Production Deployment & Hardening Guide](./docs/production.md)
- [Load Testing & Benchmark Guide](./docs/loadtest.md)
- [Architecture & AI Agent Guidelines](./AGENTS.md)
- [Frontend Design System & Specifications](./DESIGN.md)
- [Contribution Guidelines](./CONTRIBUTING.md)
- [Release Changelog](./CHANGELOG.md)
- [License (MIT)](./LICENSE)
- [LLMs Overview](./llms.txt)
