# Implementation Plan: Firefly MCP Server (Direction 1 — Admin/Ops Tools)

> **Status**: Planning — awaiting human approval before implementation.
> **Target version**: `1.43.0` (new feature)

## Overview

Expose Firefly's admin/operational capabilities as an **MCP (Model Context Protocol) server** so AI agents (Cline, Claude Code, Cursor, …) can operate the gateway conversationally: inspect upstreams, check breaker status, list models, read usage reports, and trigger health checks. The MCP surface is a thin, read-mostly transport layer on top of existing internal handlers — it never touches the streaming data plane.

## Architecture Decisions

1. **New package `internal/mcp`** — protocol types, JSON-RPC 2.0 dispatcher, tool registry. Matches the existing one-package-per-concern layout (`internal/server`, `internal/transport/…`).
2. **Hand-rolled minimal JSON-RPC/MCP core, no external SDK.** The project keeps `go.mod` deliberately lean (14 direct deps). MCP's required surface for a stateless tools-only server is small: `initialize`, `tools/list`, `tools/call`. The official Go SDK would add a large dependency tree. Tradeoff: we implement ~300 lines of protocol plumbing we own fully. Re-evaluate if we later need sampling/resources/prompts.
3. **Transport: MCP Streamable HTTP** at `POST /api/mcp` (JSON-RPC request → JSON response, single POST per call, no long-lived SSE session for MVP). This rides the existing `http.ServeMux` and reuses dashboard-session/admin-token auth. A stdio variant (`cmd/firefly-mcp`) is explicitly **out of scope** for v1.43.0 (noted as future work).
4. **Auth: same gate as the admin plane.** The handler reuses the `authorizeAdmin` semantics (valid dashboard session token OR `AdminToken`, constant-time compare, fail-closed on missing/invalid — `httpx.IsNil` discipline for the `*auth.Manager` dependency).
5. **Read-only MVP + one action tool.** Five tools: `list_upstreams`, `list_models`, `get_breaker_status`, `get_usage_report` (all read-only), plus `check_upstream_health` (action, reuses `POST /api/upstreams/check` logic). Mutating tools (`add_key`, `toggle_upstream`) are Phase 2 — deliberately deferred so the risk surface stays minimal while the value proposition is proven.
6. **Secret masking inherited, never re-implemented.** Tool responses must go through the same hint-only key patterns (`api_key_hint`, `KeySlot.Ref` never returned raw) already enforced by `providers_admin.go` telemetry DTOs. Where possible the MCP layer reuses the existing DTO builders rather than touching `domain` structs directly.

## Integration Points (verified in code)

| Concern | Existing code | Reuse |
|---|---|---|
| Routing & deps | `internal/server/router.go` (`RouterDeps`, `buildHandler`) | Add route `POST /api/mcp`; new handler method |
| Admin auth | `RouterDeps.authorizeAdmin` (session or `AdminToken`, constant-time) | Call directly |
| Upstream/model listing | `RouterDeps.Snapshots` (`SnapshotProvider.Current()` → `domain.CatalogSnapshot`) | Read directly |
| Breaker status | `internal/server/breaker_handlers.go` (`handleGetBreakers` DTOs) | Extract/reuse DTO mapping |
| Usage | `ports.UsageRecorder.Snapshot()` + `internal/storage/analytics` | Read via ports |
| Health check | `internal/server/upstream_check.go` (`handleCheckUpstream`) | Refactor the probe core into a callable function (currently HTTP-coupled) |
| OpenAPI docs | `internal/server/openapi.go` | Add MCP endpoint to admin spec (docs completeness) |

## Task List

### Phase 1: Foundation (protocol core)
- [ ] Task 1: `internal/mcp` protocol types + JSON-RPC 2.0 dispatcher + tool registry interface
- [ ] Task 2: MCP protocol methods `initialize`, `tools/list`, `tools/call` with error envelopes

### Checkpoint: Foundation
- [ ] `go test -race ./internal/mcp/...` passes; `go build ./...` clean

### Phase 2: Server wiring + auth
- [ ] Task 3: `MCPHandler` on `RouterDeps` — bearer/session auth (fail-closed), route registration `POST /api/mcp` + CORS OPTIONS
- [ ] Task 4: HTTP-level integration tests (no token → 401; valid session honored; admin token honored; second-instance session honored)

### Checkpoint: Wiring
- [ ] `go test -race ./internal/server/...` passes; endpoint reachable end-to-end

### Phase 3: Tools (vertical slices)
- [ ] Task 5: Tools batch 1 (read-only) — `list_upstreams`, `list_models`, `get_breaker_status` with JSON schemas + secret-masked outputs
- [ ] Task 6: Tool `get_usage_report` (usage recorder + analytics aggregation)
- [ ] Task 7: Tool `check_upstream_health` — refactor probe core out of `handleCheckUpstream` into a reusable function, then expose as tool
- [ ] Task 8: Tool contract tests — schema validation, masked output assertions, empty-snapshot edge cases

### Checkpoint: Tools
- [ ] All tool tests pass with `-race`; manual `curl` JSON-RPC round-trip verified

### Phase 4: Docs & release
- [ ] Task 9: README (MCP section: endpoint, auth, client config snippet for Cline/Claude Code), OpenAPI admin spec entry, `CHANGELOG.md` `1.43.0`, version bumps (`frontend/package.json`, `AppShell.tsx`), `llms.txt`
- [ ] Task 10: Full verification (`go test -count=1 -race ./...`), commit, tag `v1.43.0`, push

### Checkpoint: Complete
- [ ] All acceptance criteria met; release pushed

## Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Hand-rolled protocol drift vs MCP spec | Med | Pin to MCP spec version `2025-06-18`; cover `initialize` handshake with golden-file tests; Cline smoke test before release |
| `check_upstream_health` refactor breaks dashboard check | Med | Refactor is extract-only (Task 7); keep existing `upstream_check_test.go` green as regression gate |
| Secret leakage through new output path | High | Reuse existing masked DTOs; add explicit tests asserting no `Secret`/raw-`Ref` fields appear in tool output |
| Agent-driven health probes spamming upstreams | Low | Tool is rate-limited by admin auth + global admission middleware; document one-probe semantics |
| Scope creep into mutating tools | Med | Explicit non-goal for v1.43.0; deferred to a follow-up plan |

## Open Questions

- Should `/api/mcp` sit on the data-plane port (8080) or the admin plane (`-admin-addr`)? **Assumption for plan: data-plane port** (it is where the dashboard already lives, and auth is identical) — confirm if you prefer admin-plane isolation.
- Tool naming: plain snake_case above vs. `firefly_*` prefix. Assumption: plain (server name is already `firefly`).
