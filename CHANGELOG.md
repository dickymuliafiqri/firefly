# Changelog

All notable changes to the Firefly project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
  - **Layer 2 (Circuit Breaker)**: Host-level circuit breaker protecting gateway from upstream host outages and 5xx errors without penalizing client quota errors.
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
- Zero-leaking secret masking in structured logs (`slog`) and client error JSON.
- Fail-closed typed-nil safety (`httpx.IsNil`) across all dependency injection boundaries.
- Adherence to architectural invariants: zero `WriteTimeout` on the data plane to protect long-lived streaming connections.
