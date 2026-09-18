# Contributing to Firefly

Thank you for your interest in contributing to **Firefly**! We welcome bug reports, feature suggestions, documentation enhancements, and pull requests.

This document guides you through setting up your local environment, building the unified binary, running tests, and understanding Firefly's non-negotiable architectural rules.

---

## 1. Prerequisites

To build and test Firefly locally, ensure you have:
- **Go**: Version 1.22 or higher.
- **Bun** (or Node.js 18+): Required only when modifying the embedded React frontend in `frontend/`.
- **Make**: Standard utility to run Makefile recipes.
- **Git**: For version control.

---

## 2. Quick Local Setup

```bash
# 1. Clone the repository
git clone https://github.com/dickymuliafiqri/firefly.git
cd firefly

# 2. Build the unified single-binary with embedded frontend
make build

# 3. Run all tests with Go race detector enabled
make test

# 4. Start Firefly in development mode with live-reload (Air)
make dev
```

> [!TIP]
> **Hot-Reload Filtering (`.air.toml`)**: When using `make dev`, Air automatically ignores runtime state and telemetry files (`configs/analytics.json`, `configs/telemetry.json`, `configs/auth.json`, and `*.tmp` buffers) to prevent infinite rebuild loops when requests mutate runtime state.

---

## 3. Repository Architecture & Layout

```
firefly/
├── cmd/
│   ├── firefly/          # Main application daemon entrypoint
│   └── loadtest/         # Standalone high-concurrency CLI benchmark tool
├── frontend/             # React 19 + Vite + Tailwind SPA (embedded via //go:embed)
├── internal/
│   ├── adapter/          # Multi-provider AI protocol adapters (openai, anthropic, opencode, grok, cline, codebuddy, antigravity)
│   ├── transport/        # Ingress & egress networking (httpx middleware, upstream pool & breaker, warp WireGuard egress)
│   ├── security/         # Authentication & identity (auth credential vault, oauth manager & providers)
│   ├── observability/    # Telemetry & monitoring (slog logging, Prometheus metrics, usage tracking)
│   ├── storage/          # Data persistence (turso/libSQL syncer, analytics & breaker state store)
│   ├── domain/           # Pure domain models (CatalogSnapshot, Model, Tenant, Combo, KeyRing)
│   ├── ports/            # Go interface contracts (UpstreamAdapter, OAuthProvider, TokenStore)
│   ├── config/           # Dynamic JSON loader (upstreams, models, tenants, combos, tls)
│   ├── registry/         # Atomic snapshot store (atomic.Pointer[CatalogSnapshot])
│   ├── limits/           # Token-bucket rate limiters & CAS concurrency gates
│   ├── tokensaver/       # Prompt & context token optimization engine (RTK, Caveman, Ponytail, Headroom)
│   ├── server/           # HTTP mux routing, forwardEndpoint, autotls, and dashboard SPA
│   ├── reqid/            # Context-bound request ID leaf package
│   └── watch/            # File watcher & atomic configuration hot-reloading
├── docs/                 # Detailed production & load testing documentation
├── install.sh            # Universal one-line installer for Linux distributions
└── .github/workflows/    # CI and multi-platform release pipelines
```

---

## 4. Architectural Invariants (Non-Negotiable)

Every contributor and AI coding agent **must strictly follow** these core rules:

1. **Never Set `http.Server.WriteTimeout` on the Data Plane:**
   Firefly serves long-lived SSE streams. Setting `WriteTimeout` terminates inference streams prematurely. Timeout protection is handled granularly via `ResponseHeaderTimeout` and `StreamWatchdog`.
2. **Strict Separation Between Layer 1 (Key/4xx) and Layer 2 (Host/5xx) Errors:**
   - Status 429 and 401 indicate credential or quota limits handled by `KeyRing` cooldown/failover.
   - Only network transport failures and 5xx status codes trigger the upstream `Breaker`. 4xx errors **must never** trip circuit breakers.
   - This is enforced centrally by `upstream.ProcessAttemptOutcome`; adapters must call it rather than re-implementing breaker/failover logic.
3. **Fail-Closed & Typed-Nil Interface Safety (`httpx.IsNil`):**
   Always use `httpx.IsNil(v)` at dependency boundaries to prevent typed-nil dereference panics.
4. **Buffer Pooling Discipline (`sync.Pool`):**
   Always return borrowed buffers with capacity `<= 512KB`. Discard larger buffers to avoid heap bloat.
5. **Zero-Leaking Secret Masking:**
   Sensitive credentials (`Authorization: Bearer ...`, `api_key`, `secret`) must be redacted in logs and error responses.

---

## 5. Verification Checklist Before Submitting a PR

Before opening a pull request, run the following verification commands:

```bash
# 1. Run full test suite with race detection
go test -count=1 -race ./...

# 2. Verify frontend builds cleanly with zero TypeScript errors
cd frontend && bun run build && cd ..

# 3. Verify standalone binary compiles and prints version
go build -o firefly ./cmd/firefly
./firefly -version

# 4. Verify memory allocation benchmarks
go test -benchmem -bench=. ./internal/server/...
```

Ensure your commit messages follow [Conventional Commits](https://www.conventionalcommits.org/) (e.g. `feat:`, `fix:`, `docs:`, `perf:`).
