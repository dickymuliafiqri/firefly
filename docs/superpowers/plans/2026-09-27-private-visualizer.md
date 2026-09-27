# Visualizer Admin-Only (Private) Implementation Plan

> **For agentic workers:** this is the single source of truth for the Firefly AI-workflow
> Visualizer. It supersedes the earlier *public* visualizer plan: the page is now **admin-only**
> (richer data, no anonymous traffic, no overload risk). Work the phases in order; each item has
> files, acceptance criteria and a verification step.

Status: planned (not started).
Target URL: `GET /visualizer` (SPA boot) + `#/visualizer` (dashboard page, auth-guarded).
Auth: existing `deps.authorizeAdmin(r)` (`internal/server/router.go`) — dashboard HMAC session
token (`ff_sess_…`) or the static admin token. No new auth mechanism.

## 0. Locked decisions

| # | Decision | Why |
| :-- | :-- | :-- |
| 1 | Page lives at the real path `/visualizer`, served by `serveIndex` via the `tabs` list in `internal/server/frontend.go` | direct navigation + refresh work, same as every other tab |
| 2 | Admin-only, no public endpoint | dashboard CRUD already exposes tenant/upstream names to an admin, so richer payload adds no new exposure; and the recorder can idle to zero cost |
| 3 | SSE is consumed with `fetch()` + `ReadableStream` (Authorization header), **never** `EventSource` | avoids `?token=` in URLs (proxy/access-log/history leakage) |
| 4 | Activity label = coarse "what the AI is doing now" (`reasoning` / `writing` / `tools` / `waiting` / `connecting`), derived **only** from the SSE delta key type | decision 2 in the requirements: no reasoning summary, do not force it; no content is ever read, stored or forwarded |
| 5 | Tenant shown as a real name (admin scope) but never a key/secret | operator value > masking an identifier the admin already sees in `/api/tenants` |
| 6 | Views are pluggable via a frontend registry (`line` MVP; `tree`, `office`, `radar` later) | requirement: visual type is swappable per deployment |
| 7 | Traces are in-memory only (bounded ring); nothing persisted to Turso/analytics | no schema churn, no PII at rest, restart clears history |

## 1. Non-negotiable constraints (from AGENTS.md)

- Never set `http.Server.WriteTimeout`; the visualizer stream must drain on SIGINT/SIGTERM via the
  decoupled `streamCtx` and end with `event: closed`.
- Do **not** modify `upstream.ProcessAttemptOutcome` or any key/breaker policy. The visualizer is a
  read-only observer; it can never trip a breaker nor advance `ConsecutiveErrors`.
- Zero work when nobody is watching: `Recorder.Start()` returns a nil-safe no-op trace when the
  subscriber count is 0, so the 1,000-concurrency load test pays no cost.
- No streaming content in the payload: no prompt, no completion text, no reasoning text, no
  headers, no `KeySlot.Ref`, no plaintext secret. `keyRef` is masked (`sk-***c3f`).
- Fail-closed: `httpx.IsNil(deps.Visualizer)` → `503`, and unauthenticated → `401` (never 200 with
  an empty body).
- Buffers borrowed from `sync.Pool` stay paired with `defer` + `Cap() <= 512KB` guard.

## 2. Client-to-server data contract (single writer: `trace.Recorder`)

```jsonc
{
  "id": "req_7f3c1a",          // shortened request id, no tenant in it
  "t0": 1758900000000,          // start ms (epoch)
  "kind": "chat",               // chat | embeddings | compress
  "stream": true,
  "model": "grok-4.5",          // public model name as requested by the client
  "modelUpstream": "grok-4.5-fast",  // private rewrite, admin-only field
  "tenant": "dev-team",         // admin scope: real name
  "combo": "coding-pool",       // null when direct
  "candidates": ["grok-4.5", "claude-sonnet-4.5"], // combo members considered
  "chosen": "grok-4.5",
  "upstream": "xai-prod-1",     // instance name
  "keyRef": "sk-***c3f",        // masked, never the full secret
  "breaker": "closed",          // closed | half_open | open (read-only snapshot)
  "activity": "reasoning",      // connecting|reasoning|writing|tools|waiting|done|failed
  "chunks": 128,                // SSE events relayed
  "tokensIn": 812, "tokensOut": 512,
  "ttfbMs": 940, "durationMs": 4210,
  "ok": true, "status": 200,
  "stages": [                   // branch/stage timeline for the canvas
    { "at": 0,    "stage": "received" },
    { "at": 3,    "stage": "routed",     "detail": "combo:coding-pool" },
    { "at": 5,    "stage": "key",        "detail": "sk-***c3f" },
    { "at": 940,  "stage": "first-token", "detail": "reasoning" },
    { "at": 4210, "stage": "done",        "detail": "200" }
  ]
}
```

Rules that make this contract safe and cheap:

- The DTO is an explicit allow-list struct. Internal `domain` types are **never** marshalled directly.
- `stages` is capped (default 24 entries, oldest inner entries coalesced) — a trace cannot grow
  unbounded even if a client retries in a loop.
- Per-token events are **not** published. Only stage transitions, activity *changes* (throttled to
  250 ms) and a coarse tick counter go over the wire.

## 3. TODO

### Phase A — `internal/observability/trace` (new leaf package, no edits elsewhere)

- [ ] **A1** `recorder.go`: `type Recorder` holding `ring []*Trace` (default 500), an `atomic.Pointer`
      snapshot for lock-free reads, a subscriber set behind `sync.Mutex`, and a `dropped atomic.Uint64`.
      `Start(id, meta) *Trace`, `Finish(t)`, `Snapshot() []TraceDTO`, `Subscribe(ctx) (<-chan Event, cancel)`,
      `Subscribers() int`. Nil-receiver safe for every method.
- [ ] **A2** `recorder.go`: zero-work fast path — when `Subscribers() == 0`, `Start` returns `nil` and
      every `Trace` method returns immediately; `sample_rate` (0.0–1.0) is applied at `Start` with a
      rotate-and-compare on `globalRotation` (no new RNG, same idea as `domain` key rotation).
- [ ] **A3** `event.go`: `Stage` and `Activity` string enums, `Event` struct, and the allow-list
      `TraceDTO`/`StageDTO`. `MarshalJSON` must be the only serialisation path; no `domain` import.
- [ ] **A4** `activity.go`: pure `ClassifyDelta(line []byte) Activity` — `bytes.Contains` on the raw
      SSE data line only (`"reasoning_content"`/`"reasoning"` → reasoning; `"tool_calls"`/
      `"function_call"` → tools; `"content"` → writing; otherwise `ActivityUnchanged`). No JSON parse,
      no allocation, returns `ActivityUnchanged` for anything unrecognised (never guesses).
- [ ] **A5** `mask.go`: `MaskKeyRef(ref string) string` (`sk-***c3f`, keeps first 3 + last 3 of any
      ≥8-char ref, else `[REDACTED]`), `ShortID(id string) string`.
- [ ] **A6** `doc.go`: package doc stating the invariants from section 1 (no content, no secret,
      in-memory only, read-only observer).

**Acceptance:** `go test -race ./internal/observability/trace/...` green, including ring bound, drop
counter, nil-receiver no-op, `ClassifyDelta` table test (10 cases incl. unknown → unchanged), and a
concurrent `Snapshot`+`Publish` race test.

### Phase B — Observation hooks (server + OpenAI relay)

- [ ] **B1** `internal/server/handlers.go` `forwardEndpoint`: after the routing decision, call
      `deps.Visualizer.Start(reqID, trace.Meta{...})` with kind/model/stream/tenant/combo/candidates/
      chosen/upstream/`MaskKeyRef`. Record `received` and `routed` stages.
- [ ] **B2** `internal/server/handlers.go` `forwardEndpoint`: record `key`, `first-token` (on first
      byte flushed), `done`/`failed` (status, tokens, `ttfbMs`, `durationMs`); finalise the trace in the
      same place the existing `deps.recordLog(...)` funnel fires so there is exactly one exit path.
- [ ] **B3** `internal/adapter/openai/relay.go` `RelaySSE`: read the observer from context
      (`trace.FromContext(ctx)`, nil-safe) — **no signature change** for the 6 existing callers.
      On each relayed event call `obs.OnDelta(trace.ClassifyDelta(line))` and, at most every 250 ms,
      `obs.OnTick(chunkCount)`. When the observer is nil, the loop must be byte-identical to today
      (guard before any work, no allocation).
- [ ] **B4** `internal/server/handlers.go`: attach the observer to the request context right before
      dispatch (`ctx = trace.WithContext(ctx, t)`) so every protocol adapter inherits it.
- [ ] **B5** Non-OpenAI adapters (`anthropic`, `cline`, `grok`, `codebuddy`, `antigravity`, `opencode`):
      no code change. They surface stage-level traces only (no activity detail). Document as a known
      limitation in `doc.go` — do not touch their relay loops (YAGNI).

**Acceptance:** with an observer attached, an OpenAI SSE stream reports `reasoning → writing → done`;
with no subscriber, the Phase F3 benchmark numbers are unchanged (allocation and ns/op).

### Phase C — Admin endpoints, wiring, config

- [ ] **C1** `internal/server/visualizer.go`: `handleVisualizerTraces` → `GET /api/visualizer/traces`
      returns `{"traces":[…],"stats":{…},"config":{…}}`; `deps.authorizeAdmin(r)` first, else `401`
      JSON. `httpx.IsNil(deps.Visualizer)` → `503`.
- [ ] **C2** `internal/server/visualizer.go`: `handleVisualizerStream` → `GET /api/visualizer/stream`
      SSE. Authorization **header only** (a `?token=` value is ignored → `401`). Headers
      `text/event-stream`, `Cache-Control: no-cache`, `Connection: keep-alive`, `X-Accel-Buffering: no`;
      `: hb` comment every 15 s; `event: closed` on ctx cancel; `subscriber_count` cap → `429`.
- [ ] **C3** `internal/server/router.go`: register both handlers next to the telemetry routes (mux
      level, not under `deps.protected` — they are admin-gated, not tenant-key-gated).
- [ ] **C4** `internal/server/deps.go`: add `Visualizer *trace.Recorder` to `RouterDeps`; build it in
      `cmd/firefly/main.go` (created with the root ctx, `Close()` awaited in the shutdown path).
- [ ] **C5** `internal/config`: `visualizer` block — `enabled` (default `true`), `ring_size` (500),
      `sample_rate` (1.0), `max_subscribers` (4), `stage_limit` (24); validated in `config.Build`
      (clamp `sample_rate` to [0,1], `ring_size` to [50,2000]). Two files only: the config struct plus
      `Build` defaults/validation. No dashboard editor in MVP.
- [ ] **C6** `internal/server/frontend.go`: append `"visualizer"` to the `tabs` slice so `GET /visualizer`
      serves the embedded SPA shell (`serveIndex`) and the SPA can deep-link + refresh.

**Acceptance:** `curl -i /api/visualizer/traces` → `401`; with `Authorization: Bearer <admin>` → `200`
with the section-2 shape; `?token=<admin>` → `401`; visualizer disabled → `404`.

### Phase D — Frontend client + page (admin UI)

- [ ] **D1** `frontend/src/services/visualizer.ts` (new): `streamVisualizer(onEvent, signal)` using
      `fetch('/api/visualizer/stream')` with the dashboard token header injected the same way
      `request()` in `services/api.ts` does it, plus a `ReadableStream` line reader (same
      `TextDecoder` + line-split loop as `streamChat` in `services/api.ts`). No `EventSource`.
- [ ] **D2** `frontend/src/services/visualizer.ts`: reconnect with capped exponential backoff
      (500 ms → 1 s → 2 s → 4 s → 8 s), abort cleanly on unmount, and on `401` call
      `handleSessionInvalid()` (`lib/session.ts`) instead of retrying.
- [ ] **D3** `frontend/src/services/visualizer.ts`: `fetchVisualizerTraces()` for the initial ring
      snapshot (a page reload shows history before the first live event).
- [ ] **D4** `frontend/src/pages/VisualizerPage.tsx` (new): layout = left `TraceList` (live + history),
      centre view stage, right `TraceDetail` (stages, tokens, ttfb, tenant, upstream, masked keyRef).
      Reads `?view=` from the hash query (`lib/router.ts`) as the view override.
- [ ] **D5** `frontend/src/components/visualizer/TraceList.tsx`, `TraceDetail.tsx`,
      `PhaseTimeline.tsx`, `ActivityBadge.tsx` (colour-coded `reasoning|writing|tools|waiting`),
      `RawTable.tsx` (always-available tabular fallback = the "varied data" requirement).
- [ ] **D6** `frontend/src/registry.tsx`: add `'visualizer'` to the `PageId` union (line 33) and a
      `PAGES.visualizer` entry (line 58 block) — `group: 'MONITORING'`, `icon: Activity`,
      `element: () => <VisualizerPage />`.
- [ ] **D7** Degradation: when the stream is down show `stale` + last-event age; when
      `stats.dropped > 0` show "behind by N events" (never silently pretend to be live).

**Acceptance:** `cd frontend; npm run build` clean; the page renders live traces with zero console
errors and no `?token=` in any network request.

### Phase E — View registry (line MVP, then tree/office/radar)

- [ ] **E1** `frontend/src/components/visualizer/views/registry.ts`: `ViewDef` =
      `{ id, label, available(trace): boolean, Component }`, plus `resolveView(preferred, trace)` that
      falls back to `line` → `raw`. Adding a view = one registry entry, no page edit.
- [ ] **E2** `views/LineView.tsx` (MVP): per-trace horizontal timeline with stage markers and an
      activity ribbon; pure SVG + Tailwind, no chart dependency.
- [ ] **E3** `views/TreeView.tsx`: the requested branching diagram — root `request` → `combo` or
      `model` → `candidate*` (failed candidates dimmed with reason) → `upstream/key` → `stream/done`.
      Same data, SVG orthogonal edges. Flagship view.
- [ ] **E4** `views/OfficeView.tsx` (animated nodes = agents working) and `views/RadarView.tsx`
      (polar sweep of in-flight requests by latency). Both optional after E3 ships; one file each.

### Phase F — Tests, benchmarks, verification

- [ ] **F1** `internal/observability/trace/recorder_test.go`: ring bound, drop counter,
      subscriber add/remove, nil-receiver no-op, `sample_rate=0` → no trace, `sample_rate=1` → all.
- [ ] **F2** `internal/observability/trace/activity_test.go`: `ClassifyDelta` table test.
- [ ] **F3** `internal/server/visualizer_test.go`: `401` without token, `401` with `?token=`,
      `200` with admin header, `503` when the recorder dependency is nil, payload contains no full
      secret and no prompt/completion text (assert on the raw JSON string), subscriber cap → `429`.
- [ ] **F4** `internal/server/handlers_test.go`: a streamed request produces exactly the expected
      stage sequence; a client-cancelled request finishes the trace as canceled and does not leak a
      subscriber.
- [ ] **F5** Benchmark in `internal/server`: `forwardEndpoint` with zero subscribers vs one — assert
      the zero-subscriber path allocates the same as before the feature.
- [ ] **F6** Full gate: `go test -count=1 -race ./...`; `go test -v -race ./internal/server/...`;
      `go test -v -race ./internal/security/auth/...`; `cd frontend; npm run build`.

## 4. Out of scope for the MVP (explicitly deferred)

- Persisting traces (Turso/analytics) or replaying a run from disk.
- Prompt / completion / reasoning **content** in the payload or on the page.
- Dashboard UI for editing `visualizer.*` config (JSON config only for now).
- Activity detail for non-OpenAI adapters (they emit stage-level traces only).
- Public/anonymous access, sharing a trace by URL, embedded `<iframe>` mode.
- Multi-subscriber fan-out tuning beyond `max_subscribers` (4).

## 5. Existing-code touch map (delta only)

| File | Change | Phase |
| :-- | :-- | :-- |
| `internal/observability/trace/*` | new package (recorder, event, activity, mask, doc) | A |
| `internal/server/handlers.go` | start/finish trace; attach observer to ctx | B1, B2, B4 |
| `internal/adapter/openai/relay.go` | nil-safe observer call inside the relay loop | B3 |
| `internal/server/visualizer.go` | new admin handlers (`/api/visualizer/*`) | C1, C2 |
| `internal/server/router.go` | register the two routes | C3 |
| `internal/server/deps.go`, `cmd/firefly/main.go` | recorder dependency + lifecycle | C4 |
| `internal/config/config.go` | `visualizer` block + validation | C5 |
| `internal/server/frontend.go` | `"visualizer"` in the `tabs` slice | C6 |
| `frontend/src/services/visualizer.ts`, `pages/VisualizerPage.tsx`, `components/visualizer/**` | new UI + stream client | D, E |
| `frontend/src/registry.tsx` | `PageId` union + `PAGES.visualizer` | D6 |

Nothing in `internal/transport/upstream/**`, `internal/registry/**`, `internal/limits/**` or the
key/breaker policy is modified by this plan.



---

## Implementation notes (post-build)

The shipped code follows the design above; these are the names it landed under, plus the two intentional deviations.

| Plan term | Shipped |
| :-- | :-- |
| `recorder.go` + `event.go` | `trace.go` (Trace/Stage/Candidate/Event/Activity, `MaskRef`, `Classify`), `capture.go` (per-request `Capture`), `recorder.go` (ring + fan-out) |
| `Recorder.Start` | `Recorder.Start(Meta)`; needs `enabled && subscribers > 0`, otherwise it returns `nil` and every `Capture` method is nil-safe (zero cost while unwatched) |
| `GET /api/visualizer/stream` | `GET /api/visualizer/events` — SSE frames `{"type":"trace"|"stats"}`, `: keepalive`, `event: closed` on shutdown |
| `POST /api/visualizer/capture`, `.../clear` | same paths, both answer `{"stats":…}` |
| `recorder_test.go` | `internal/observability/trace/trace_test.go` + `internal/server/visualizer_test.go` (401/503, snapshot, toggle, clear, SSE framing) |
| `deps.go` | the `Traces *trace.Recorder` field lives on `RouterDeps` in `internal/server/router.go` |

Deviations:

1. **Bounds are validated, not clamped.** `config.Build` rejects `retention`/`keep_events` outside `1..4096` / `1..256` with a `ValidationError`, so a typo fails the reload loudly instead of quietly changing behaviour. Defaults: `enabled: true`, `retention: 500`, `keep_events: 24` (`domain.DefaultVisualizerConfig`, `configs/visualizer.json`); recording still costs nothing until a dashboard subscriber attaches, and at most `trace.MaxSubscribers` streams are served (further ones get `429`).
2. **No `sample_rate`.** Activity frames are coalesced per trace and the frontend batches renders at 250 ms, so a sampling knob would add configuration without changing load.
