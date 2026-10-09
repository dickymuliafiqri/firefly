# Tasks: Firefly MCP Server (Direction 1)

Source plan: `tasks/plan.md`. Do not start until the plan is approved.

## Task 1: MCP protocol core — types, JSON-RPC 2.0 dispatcher, tool registry

**Description:** Create package `internal/mcp` with JSON-RPC 2.0 request/response envelopes, a dispatcher that routes methods to handlers, and a `Tool` interface (name, description, input schema, `Call(ctx, args)`) plus a `Registry` for tools.

**Acceptance criteria:**
- [ ] Dispatcher returns `-32601` for unknown methods, `-32602` for invalid params, `-32603` for handler panics (recovered, logged)
- [ ] Batch requests either supported or explicitly rejected with a clear error
- [ ] No external dependencies added to `go.mod`

**Verification:**
- [ ] `go test -race ./internal/mcp/...` passes
- [ ] `go build ./...` succeeds

**Dependencies:** None
**Files likely touched:** `internal/mcp/protocol.go`, `internal/mcp/dispatcher.go`, `internal/mcp/tool.go`, `internal/mcp/dispatcher_test.go`
**Estimated scope:** Medium

## Task 2: MCP protocol methods — initialize, tools/list, tools/call

**Description:** Implement the three stateless-server methods: `initialize` (returns protocol version `2025-06-18`, server info `firefly/<version>`, capabilities `tools`), `tools/list` (from registry, with JSON schemas), `tools/call` (executes registered tool, returns content array).

**Acceptance criteria:**
- [ ] `initialize` handshake matches MCP Streamable HTTP for a stateless server
- [ ] `tools/list` output includes all registered tools with valid JSON Schema
- [ ] `tools/call` returns `content: [{type: "text", text: ...}]` and `isError` on tool failure

**Verification:**
- [ ] `go test -race ./internal/mcp/...` passes (golden JSON for each method)

**Dependencies:** Task 1
**Files likely touched:** `internal/mcp/methods.go`, `internal/mcp/methods_test.go`
**Estimated scope:** Small

## Checkpoint: Foundation
- [ ] `go test -race ./internal/mcp/...` green, `go build ./...` clean

## Task 3: Server wiring — RouterDeps.MCPHandler, route POST /api/mcp, auth

**Description:** Add `MCPHandler http.Handler` to `RouterDeps`; register `POST /api/mcp` (and `OPTIONS` for CORS) in `buildHandler`. Auth reuses `authorizeAdmin` semantics: valid dashboard session OR `AdminToken` (constant-time), fail-closed otherwise; `httpx.IsNil` on the auth manager dependency.

**Acceptance criteria:**
- [ ] No auth header → HTTP 401 JSON-RPC error, never a 5xx
- [ ] Valid `Authorization: Bearer <adminToken>` is accepted
- [ ] Route appears only after auth gate in middleware order

**Verification:**
- [ ] `go test -race ./internal/server/...` passes

**Dependencies:** Tasks 1–2
**Files likely touched:** `internal/server/router.go`, `internal/server/mcp_handlers.go`, `internal/server/mcp_handlers_test.go`
**Estimated scope:** Medium

## Task 4: HTTP-level integration tests for /api/mcp

**Description:** Cover: 401 fail-closed, admin-token happy path, dashboard session honored across instances (pattern: `TestAuthEndpoints_SessionHonoredByAnotherInstance`), unknown method, tools/list over HTTP.

**Acceptance criteria:**
- [ ] All scenarios tested with `-race`
- [ ] Test asserts JSON-RPC envelope shape over raw HTTP

**Verification:**
- [ ] `go test -race ./internal/server/...` passes

**Dependencies:** Task 3
**Files likely touched:** `internal/server/mcp_handlers_test.go`
**Estimated scope:** Small

## Checkpoint: Wiring
- [ ] End-to-end `curl` JSON-RPC round-trip works locally

## Task 5: Tools batch 1 — list_upstreams, list_models, get_breaker_status

**Description:** Implement three read-only tools against `SnapshotProvider` and the breaker DTO mapping from `breaker_handlers.go`. Outputs reuse existing masked DTO shapes (no raw `KeySlot.Secret`/`Ref`).

**Acceptance criteria:**
- [ ] `list_upstreams` returns name, protocol, base_url, key count, cooldown/revoked counts, enabled state
- [ ] `list_models` supports optional `search` substring filter
- [ ] `get_breaker_status` returns state per upstream without exposing internals
- [ ] Test asserts no secret material appears in serialized output

**Verification:**
- [ ] `go test -race ./internal/mcp/... ./internal/server/...` passes

**Dependencies:** Tasks 1–3
**Files likely touched:** `internal/mcp/tools_upstreams.go`, `internal/mcp/tools_upstreams_test.go`
**Estimated scope:** Medium

## Task 6: Tool get_usage_report

**Description:** Expose usage counters (`ports.UsageRecorder.Snapshot()`) and, where available, analytics aggregates, filtered by optional `tenant`, `upstream`, `since` args.

**Acceptance criteria:**
- [ ] Returns per-key/tenant request counts and token totals consistent with `/api/telemetry` data
- [ ] Optional filters work; empty result is a valid (not error) response

**Verification:**
- [ ] Focused test with a stub `UsageRecorder` passes with `-race`

**Dependencies:** Task 5
**Files likely touched:** `internal/mcp/tools_usage.go`, `internal/mcp/tools_usage_test.go`
**Estimated scope:** Small

## Task 7: Tool check_upstream_health + probe core refactor

**Description:** Extract the probe execution core from `RouterDeps.handleCheckUpstream` into a reusable function, keep the HTTP handler behavior identical, then expose it as MCP tool `check_upstream_health` (args: `upstream`, optional `model`).

**Acceptance criteria:**
- [ ] Existing `upstream_check_test.go` remains green — refactor is behavior-preserving
- [ ] Tool returns verdict, latency, status; secrets masked
- [ ] Probe respects the same Layer-1/Layer-2 outcome routing (no bypass of `ProcessAttemptOutcome` semantics)

**Verification:**
- [ ] `go test -race ./internal/server/... ./internal/mcp/...` passes

**Dependencies:** Task 5
**Files likely touched:** `internal/server/upstream_check.go`, `internal/mcp/tools_health.go`, `internal/mcp/tools_health_test.go`
**Estimated scope:** Medium (high-risk refactor — keep small and extract-only)

## Task 8: Tool contract tests — schemas and masking

**Description:** Table-driven tests validating every tool's input schema rejects malformed args (`-32602`) and every registered tool's output passes a secret-scan (no raw `Secret`, `api_key`, `Bearer` fragments).

**Acceptance criteria:**
- [ ] Schema rejection covered for each tool
- [ ] Secret-scan helper consistent with masking patterns in `providers_admin.go`

**Verification:**
- [ ] `go test -race ./internal/mcp/...` passes

**Dependencies:** Tasks 5–7
**Files likely touched:** `internal/mcp/tools_contract_test.go`
**Estimated scope:** Small

## Checkpoint: Tools
- [ ] Manual JSON-RPC round-trip for all five tools via `curl` verified

## Task 9: Documentation & version bump 1.43.0

**Description:** README MCP section (endpoint, auth, Cline/Claude Code client config snippets), OpenAPI admin spec entry for `/api/mcp`, `CHANGELOG.md` `[1.43.0]`, version bumps in `frontend/package.json` + `AppShell.tsx`, `llms.txt` mention.

**Acceptance criteria:**
- [ ] README follows existing doc style (English, no promo tone)
- [ ] CHANGELOG entry follows the Keep-a-Changelog format used by the file
- [ ] Version constants bumped consistently

**Verification:**
- [ ] `go build ./...` clean after doc-adjacent spec edits

**Dependencies:** Tasks 4–8
**Files likely touched:** `README.md`, `CHANGELOG.md`, `internal/server/openapi/…`, `frontend/package.json`, `frontend/src/components/shell/AppShell.tsx`, `llms.txt`
**Estimated scope:** Medium

## Task 10: Full verification, commit, tag v1.43.0, push

**Description:** Run `go test -count=1 -race ./...`, `go vet ./...`, then commit (`feat: MCP server exposing gateway admin tools 1.43.0`), tag `v1.43.0`, push branch + tag, verify remote.

**Acceptance criteria:**
- [ ] Full suite green with race detector
- [ ] Tag visible on remote (`git ls-remote --tags origin v1.43.0`)

**Dependencies:** Task 9
**Files likely touched:** none (release step)
**Estimated scope:** Small

