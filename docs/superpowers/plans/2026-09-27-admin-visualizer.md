# Admin-only Request Visualizer Implementation Plan

> **For agentic workers:** work the milestones top to bottom. Every task that touches Go code must end with `go test -race ./...` on the affected packages. Never change request-path semantics to make the visualizer work; the visualizer is a passive observer.

## Goal

A private, admin-only page at the real path `/visualizer` that renders the live work of downstream AI models as a branching diagram (line / office / other views), plus a richer per-request trace panel. Data comes from the Firefly data plane, never from upstream content, and never leaks credentials.

## Architecture

```
forwardEndpoint (internal/server/handlers.go)
  ├── trace.Recorder.Start(reqID)                    ← M1
  ├── stage events: received → resolve → route → key → attempt → ttfb → stream → done
  ├── deps.recordLog(log)  (existing funnel)         ← finalize trace
  └── openai.RelaySSE(ctx, …)                        ← M2 observer via ctx: activity + chunk ticks
            │
            ▼  (non-blocking, drop-on-backpressure)
      trace.Hub  ──►  GET /api/visualizer/stream   (SSE, admin-guarded)   ← M3
              └─────►  GET /api/visualizer/traces  (ring snapshot, admin-guarded)
            │
            ▼
   VisualizerPage (#/visualizer, booted from path /visualizer)            ← M4/M5
            └── view registry: line (MVP) | tree | office | radar (stubs)
```

Key property: when `Hub.Subscribers() == 0` the recorder is a no-op (no allocations, no timers, no publish). Zero cost for normal traffic.

## Tech Stack

Go 1.22+ `net/http` ServeMux, `log/slog`, `sync/atomic`, `encoding/json`. Frontend: React + TypeScript, `frontend/src/registry.tsx` for routes, `frontend/src/services/api.ts` for auth, lucide-react icons, existing Tailwind tokens.

## Global Constraints (non-negotiable)

- Never set `http.Server.WriteTimeout`; the SSE stream relies on `ResponseHeaderTimeout` + client context only.
- Admin-guard **both** endpoints with `deps.authorizeAdmin(r)` (`internal/server/router.go`). No query-string token fallback, no CORS header, no `/api/visualizer/*` entry in any public bypass list.
- Do **not** touch `upstream.ProcessAttemptOutcome`, breaker classification, or key policy. The recorder only reads state (`deps.Breakers.Allow`, `KeyRing` selection results, `recordLog` fields).
- Zero content: never publish, buffer, log, or persist prompt text, response text, reasoning text, tool arguments, headers, `KeySlot.Ref`, raw upstream API keys, or upstream base URLs.
- Buffer discipline: any borrow from `sync.Pool` keeps the existing paired release + `Cap() <= 512KB` guard.
- Streaming shutdown stays decoupled (`streamCtx`, `ShutdownGrace`) — the visualizer stream must end with a final `event: closed` frame, never a hard cut.
- Use `httpx.IsNil` at every new dependency boundary (handler `deps`, hub, recorder).

## Data Contract (admin, richer than the public draft)

```json
{
  "id": "req_9f3c",
  "started_ms": 1758900000000,
  "duration_ms": 4210,
  "status": 200,
  "ok": true,
  "stream": true,
  "kind": "chat",
  "tenant": "acme",
  "model_public": "grok-4.5",
  "model_upstream": "grok-4.5-high",
  "combo": "fast-pool",
  "combo_strategy": "least_inflight",
  "candidates": [
    {"label": "prov A / grok-4.5", "chosen": true,  "key_ref": "sk-…c3f", "attempts": 1, "breaker": "closed"},
    {"label": "prov B / grok-4.5", "chosen": false, "reason": "429 cooldown 30s"}
  ],
  "stages": [
    {"at_ms": 0,    "stage": "received"},
    {"at_ms": 3,    "stage": "resolved",  "detail": "combo fast-pool → prov A"},
    {"at_ms": 9,    "stage": "key",       "detail": "least_inflight, 4/6 idle"},
    {"at_ms": 12,   "stage": "attempt",   "attempt": 1},
    {"at_ms": 640,  "stage": "ttfb"},
    {"at_ms": 4210, "stage": "done"}
  ],
  "activity": [{"at_ms": 640, "kind": "reasoning"}, {"at_ms": 3110, "kind": "writing"}],
  "activity_now": "writing",
  "chunks": 128,
  "tokens_in": 812,
  "tokens_out": 512,
  "drops": 0
}
```

`activity` kinds — coarse, best effort, derived **only** from which SSE delta key is present: `connecting | reasoning | writing | tools | waiting | done | failed`. When no delta has arrived for > 2 s the label degrades to `waiting`; when the payload shape carries no recognizable delta key, no activity event is emitted at all (never guess).

---

## M0 — Baseline & guardrails

- [ ] Read `AGENTS.md` §1 invariants plus `internal/server/handlers.go` (`forwardEndpoint`, the attempt loop, the `recordLog` funnel) and `internal/adapter/openai/relay.go` (`RelaySSE`) before editing.
- [ ] Record the baseline: `go test -count=1 -race ./internal/server/... ./internal/adapter/openai/...` and `go test -bench=. -benchmem ./internal/server/...`; paste the numbers into the PR description.
- [ ] Add a short "Admin Visualizer" section to `README.md`: admin-only, content-free, no persistence, no CORS.

## M1 — `internal/observability/trace` (leaf package, no server imports)

- [ ] `event.go`: `Event{TraceID, Kind, Stage, Activity, Detail, AtMs, Chunks, TokensIn, TokensOut, Status}` with enums `EventKind` (`start|stage|activity|chunk|finish`), `Stage`, `Activity`.
- [ ] `recorder.go`: `Recorder` with `NewRecorder(Config)`, `Config{RingSize, SampleRate, IdleTimeout}`, `Start(ctx, StartInfo) *Trace`, `Stage`, `Activity`, `Chunk(n)`, `Finish(status, in, out)`, `Snapshot() []Trace`, `Subscribe() (id, <-chan Event, cancel)`, `Subscribers() int`.
  - Ring: fixed slice, one `sync.Mutex` (≈10 writes per request, never per token); `Snapshot()` returns a copy.
  - Fast path: `if r.subs.Load() == 0 { return }` at the top of every mutator.
  - Sampling at `Start` only: `SampleRate < 1` skips whole traces deterministically; no per-event cost.
  - Publish never blocks: `select { case ch <- ev: default: dropped.Add(1) }`.
- [ ] `hub.go`: subscriber registry, `MaxSubscribers` (default 8) with a warn log on cap, buffered channels (256), cancel-once.
- [ ] `activity.go`: `ActivityFromDelta(line []byte) (Activity, bool)` — `bytes.Contains` on `reasoning_content` / `tool_calls` / `content` / `[DONE]`; no unmarshal, no allocation.
- [ ] Tests `recorder_test.go`, `hub_test.go`, `activity_test.go`: ring bound; no-op with zero subscribers; a stalled subscriber never blocks the publisher and bumps `drops`; unknown delta shape returns `false`; `SampleRate=0` records nothing.
- [ ] Verify: `go test -race ./internal/observability/trace/...`



## M2 — Instrument the data plane (passive only)

- [ ] `internal/adapter/openai/observer.go`: `StreamObserver interface { Delta(kind string); Chunk(n int) }`, `WithObserver(ctx, StreamObserver) context.Context`, `observerFromContext(ctx)`. Keep `RelaySSE(ctx, w, body, idle)` signature unchanged so every protocol adapter keeps compiling.
- [ ] `internal/adapter/openai/relay.go`: inside the existing line loop, after `Write`+`Flush`, call `obs.Delta(kind)` **only when the kind changed** (local `last` variable) and `obs.Chunk(1)` behind a 250 ms throttle using `time.Since` + a local `time.Time` (no timer, no goroutine, no extra allocation). Leave the `sync.Pool` reader release path untouched.
- [ ] Tests `observer_test.go` + extend the relay tests: a reasoning-only stream emits `reasoning` once then `writing` on the first content delta; with no observer the writer output is byte-identical to today (golden comparison).
- [ ] `internal/server/handlers.go`: in `forwardEndpoint`, once the model is parsed and the target resolved, call `deps.Visualizer.Start(...)` with tenant name, requested model, combo name + strategy, and candidate labels; emit stages `received → resolved → key (masked ref via the existing masking helper) → attempt(i) → ttfb (first byte written) → done`; on failover record `reason:"429 cooldown"` / `"401 revoked"` / `"5xx"` for the skipped candidate; put the stream observer into the adapter context.
- [ ] Terminal state: reuse the existing `recordLog` funnel to call `Visualizer.Finish(status, tokensIn, tokensOut)` — no second plumbing path.
- [ ] The recorder only reads breaker/key state already computed by the router; it must never call breaker or key-policy code.
- [ ] Verify: `go test -race ./internal/server/... ./internal/adapter/openai/...` then full `go test -count=1 -race ./...` to prove zero behavior change.

## M3 — Admin-guarded endpoints + config

- [ ] New `internal/server/visualizer_handlers.go`: `handleVisualizerTraces` (JSON snapshot, `Cache-Control: no-store`) and `handleVisualizerStream` (SSE: `text/event-stream`, `Cache-Control: no-cache`, `X-Accel-Buffering: no`, immediate `Flush` on subscribe, heartbeat every 15 s, final `event: closed` on shutdown or ctx cancel, `unsubscribe` in `defer`).
- [ ] Both handlers start with `if !deps.authorizeAdmin(r) { 401 JSON; return }` then `if httpx.IsNil(deps.Visualizer) || !deps.Visualizer.Enabled() { 404 JSON; return }` (fail-closed typed-nil safety).
- [ ] Register in `internal/server/router.go` beside the other `/api/*` admin routes — never in a public bypass list, and no `Access-Control-Allow-Origin` header anywhere.
- [ ] Config: `domain.VisualizerConfig{Enabled, RingSize, SampleRate, MaxSubscribers, IdleTimeoutMs}` + `domain.DefaultVisualizerConfig()`; wire through `internal/config/builder.go` (DTO + defaults + `Build`, same shape as the token-saver section) and into the settings payload behind `GET /api/settings`.
- [ ] Frontend settings surface: `frontend/src/services/schema.ts`, defaults in `frontend/src/services/api.ts`, and a "Visualizer (admin only)" block in `frontend/src/pages/SettingsPage.tsx` (enable toggle, ring size, sample rate, max subscribers).
- [ ] Document both endpoints and the trace schema in the OpenAPI spec.
- [ ] Tests `internal/server/visualizer_handlers_test.go`: 401 with no token; 401 with `?token=`; 404 when disabled; 200 for both a session token and the static admin token; SSE emits `closed` on shutdown; table-driven forbidden-substring test asserting the body never contains a tenant key, `KeySlot.Ref`, raw upstream key, or upstream base URL.
- [ ] Verify: `go test -v -race ./internal/server/... ./internal/config/...`

## M4 — Frontend page, data plumbing, boot path

- [ ] `frontend/src/visualizer/types.ts` mirroring the Go contract.
- [ ] `frontend/src/visualizer/useTraceStream.ts`: initial `GET /api/visualizer/traces` for history, then live updates with `fetch('/api/visualizer/stream', {headers: Authorization})` + `ReadableStream` line parser (not `EventSource`, so the token stays out of the URL). Reconnect with capped backoff; on 401 call the existing session-invalid path; abort the reader on unmount.
- [ ] `frontend/src/pages/VisualizerPage.tsx`: layout of canvas + trace list + detail panel; filters (status, tenant, model, min duration); "X dropped" indicator fed by the `drops` field; empty state when the hub has no subscribers.
- [ ] `frontend/src/registry.tsx`: register `PageId` `visualizer` with an icon and `public: false` so it appears in the sidebar for authenticated admins only.
- [ ] `internal/server/frontend.go`: serve the real path `/visualizer` as an SPA boot path (mirroring the existing tab/path list) so a direct hit loads the shell; the SPA itself redirects to login when no token exists.
- [ ] Test: extend `internal/server/frontend_test.go` to assert `/visualizer` returns the SPA shell.
- [ ] Verify: `npm --prefix frontend run build` (or the repo's existing frontend build/test script) is clean and TypeScript reports no errors.

## M5 — Pluggable views (only `line` implemented)

- [ ] `frontend/src/visualizer/views/registry.ts`: `ViewDef{id, label, icon, Component, supports: TraceShape[]}` plus a `registerView` list — adding a view must not touch `VisualizerPage`.
- [ ] `frontend/src/visualizer/graph.ts`: pure `toGraph(trace, view)` producing nodes/edges in view-neutral form (`trunk`, `candidate`, `stage`, `activity`), so views never read raw traces.
- [ ] `frontend/src/visualizer/views/LineView.tsx`: the MVP view — one branch per candidate, the chosen branch as the trunk, stage markers on the timeline, activity colour band, duration-scaled spacing.
- [ ] Views `TreeView`, `OfficeView`, `RadarView` exist only as registry stubs that render "not implemented" — no partial implementations.
- [ ] Unit tests for `toGraph` and the view registry (unknown/empty trace yields an empty graph instead of throwing).


## M6 — Load, memory, and release hardening

- [ ] Bench: 1,000 concurrent streams with **zero** visualizer subscribers → assert the recorder fast path adds no measurable allocations (`-benchmem` before/after, target ≤ 1 alloc/request difference and no ns/op regression beyond noise).
- [ ] Bench/soak: same 1,000 concurrent streams with one subscriber and `SampleRate=1` → the data plane must never block, `drops` grows instead, and RSS stays flat (ring is fixed-size, channels bounded).
- [ ] Test that a killed/unresponsive browser (client disconnect) unsubscribes and leaves `Subscribers() == 0` within one heartbeat interval.
- [ ] Test that the SSE stream survives config hot-reload (catalog swap does not cancel the stream) and ends with `closed` during graceful shutdown within `ShutdownGrace`.
- [ ] Update `CHANGELOG.md` and the `AGENTS.md` package table with the `trace` package and the two admin endpoints.

## Acceptance Criteria

- `/api/visualizer/traces` and `/api/visualizer/stream` return 401 without a valid session **or** static admin token, 404 when the feature is disabled, and 200 for an authenticated admin.
- No visualizer endpoint is reachable from a tenant key, and no CORS header is emitted for it.
- Response bodies and SSE frames contain no prompt/response/reasoning content, no raw credential, and no upstream base URL (forbidden-substring test in M3).
- With zero subscribers, a 1,000-concurrency load run shows no measurable cost versus the pre-change baseline (M0 vs M6).
- The page is reachable at the real path `/visualizer`, appears in the sidebar only for authenticated admins, and renders the `line` view from live traces; the other three views are explicit stubs.
- The whole suite passes with `go test -count=1 -race ./...`.

## Verification Commands

```bash
go test -count=1 -race ./...
go test -v -race ./internal/server/...
go test -race ./internal/observability/trace/... ./internal/adapter/openai/...
go test -benchmem -bench=. ./internal/server/...
```

## Out of Scope

- Persisting traces (no Turso table, no analytics row, no replay across restarts).
- Any token-by-token stream relay to the browser: the visualizer sends coarse activity ticks only.
- Public/tenant access, embeddable widgets, and CORS for the visualizer.
- Implementing `tree`, `office`, or `radar` rendering in this plan.

## Open Decisions

- `SampleRate` default: `1.0` (private page, zero-subscriber fast path already removes the cost when nobody is watching) vs `0.25` for a conservative first release.
- Whether the visualizer config lives in a `configs/visualizer.json` file (consistent with the token-saver section) or as `-visualizer-*` flags in `main.go` (smaller surface, no settings UI).
- `IdleTimeoutMs` for the "waiting" degradation: 2,000 ms is the proposed default.

