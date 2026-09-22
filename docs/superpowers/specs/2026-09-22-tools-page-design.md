# Tools page — Playground restructured into a sidebar tool shell (Chat + Benchmark)

Date: 2026-09-22
Status: design approved, pending implementation plan

## Context

The Playground tab (`frontend/src/modules/7-playground/`) is single-purpose: an SSE chat
tester with a token-stream waterfall and a raw-packet inspector. The request is to turn it
into a **Tools** page with a sidebar so several tools can live side by side, starting with
**Chat** (today's Playground, behaviour unchanged) and **Benchmark** (new).

Brainstorming classified this as an architectural change (a module → sub-tool navigation
layer that does not exist in the repo yet, plus a new tool). Layout option **C** was picked
in the browser companion: sidebar + a collapsible telemetry panel.

## Goals

- Rename the module to Tools and add an in-module sub-tool registry plus sidebar.
- Give every tool its own component folder so tools never mix, with a `shared/` folder for
  the few pieces more than one tool needs.
- Keep chat behaviour, diagnostics, and state-across-tab-switches exactly as today.
- Add a Benchmark tool at "basic functional" depth: N requests at concurrency C through the
  gateway, reporting TTFT / TPS / duration / error rate, visualised in the existing
  waterfall style.
- Apply current React 19 practice (see "React 19 standards applied") without adopting a new
  state or data library.
- No new dependencies (no router, no chart library, no test runner).

## Non-goals (this iteration)

- Cross-reload persistence of UI state. The store has no `persist` middleware; `activeTab`
  resets on reload too, and the new state follows that convention.
- Renaming the historical `playground*` identifiers (`playgroundSlice`, `playgroundApiKey`,
  `playgroundIsGenerating`, …). Mechanical rename across 5 files, deferred to keep this
  diff reviewable.
- Extracting a shared SSE parser out of `chat/ChatWindow.tsx` (see the duplication note).
- Deep-linking or routing to an individual tool; there is no router in this app.

## Locked decisions

1. **Approach 1** — a local tool registry inside the module, mirroring the shape of
   `modules/registry.tsx`, instead of a generic layout component in `core/` (premature, one
   consumer today) or a nested global navigation model (blast radius far beyond the need).
2. **Layout C** — sidebar + collapsible right telemetry panel on the Chat tool.
3. **In-memory state** — no `localStorage`/`persist`; consistent with every other UI slice.
4. **The benchmark shares `playgroundApiKey`** with chat, so the existing auto-pick
   (admin session token → demo key) keeps working and there is only one key field that can
   drift.
5. **No new dependencies** — result bars are CSS/div based, like `TokenStreamWaterfall`.
6. **Folder per tool** — `chat/`, `benchmark/`, and `shared/` under `7-tools/`, rather than a
   flat module folder, so the two tools never share a namespace.
7. **React 19.3.0 idioms** — documented in the next section and enforced by the plan.

## React 19 standards applied

React 19.3.0 is what is installed (`frontend/node_modules/react/package.json`). The rules
below are the ones this work actually exercises; they match the idioms already used in the
codebase (`App.tsx`, `core/layout/Shell.tsx`, the existing module views).

- **One folder per tool, no barrel files.** `chat/`, `benchmark/`, `shared/`; imports point
  at the concrete file. Barrels would blur the code-splitting boundary and invite
  cross-tool coupling.
- **Derived state is computed during render, never stored.** TTFT p50/p95, aggregate TPS,
  error rate, and the bar scale factor are `useMemo` results over `benchmarkResults` — they
  are deliberately *not* slice fields, and no effect writes them. (React docs: "You Might
  Not Need an Effect".)
- **Effects only synchronize with the outside world.** The benchmark run is started by the
  click handler, not by an effect; the only effect is abort-on-unmount cleanup, mirroring
  `chat/ChatWindow.tsx`'s existing teardown.
- **Suspense per tool.** `React.lazy` + `<Suspense fallback={<ModuleSkeleton />}>` per tool
  chunk, exactly like `App.tsx:169`, plus `preload()` on sidebar hover/focus like the header
  tabs. Tool switching wraps the store setter in `startTransition`, mirroring
  `App.tsx:102-104`; note that zustand is an external store, so React cannot truly defer that
  update — the anti-flicker mechanism is the hover preload, not the transition.
- **Atomic external-store selectors.** One `useAppStore((state) => state.field)` hook per
  field (the existing store idiom), so streaming chat updates never re-render the benchmark
  tree and result appends never re-render the form.
- **`React.memo` at the leaves** with stable props, and static JSX/icon nodes hoisted to
  module scope (`rendering-hoist-jsx`), as the existing components do.
- **Refs for transient high-frequency values.** The worker queue, abort controller, and
  in-flight bookkeeping live in refs/closures; only the bounded result list (≤ 100 entries)
  enters the store.
- **Stable identities.** Result rows are keyed by their 1-based request index, which is
  unique and never reordered.
- **Accessibility by markup.** Sidebar entries and the collapse toggle are real `<button>`
  elements with `aria-current`, `aria-expanded`, and `aria-controls`; the progress counter is
  `aria-live="polite"`; the result grid is a real `<table>` with `<th scope="col">`.
- **`prefers-reduced-motion`** gates the collapse transition, reusing `useReducedMotion`.

## Module restructure and registry

Rename the folder `frontend/src/modules/7-playground/` → `frontend/src/modules/7-tools/`,
with one folder per tool:

```
7-tools/
  ToolsView.tsx                     module shell: sidebar + Suspense outlet
  ToolSidebar.tsx                   tool list (button per tool, preload on hover/focus)
  tools.tsx                         ToolDefinition, TOOL_REGISTRY, ORDERED_TOOLS, ToolId
  shared/
    useModelOptions.ts              enabled models/combos + availability + auto-pick
    MetricPill.tsx                  metric card primitive (label + value)
  chat/
    ChatTool.tsx                    chat pane + collapsible telemetry pane
    ChatWindow.tsx                  moved; model/combo filtering swapped to shared hook
    TokenStreamWaterfall.tsx        moved; adopts MetricPill
    StreamInspector.tsx             moved unchanged
  benchmark/
    BenchmarkTool.tsx               composition: form + summary + results
    BenchmarkForm.tsx               model/prompt/requests/concurrency + Run/Stop
    BenchmarkResults.tsx            bars + result table
    useBenchmarkRun.ts              worker pool + AbortController + store transitions
    benchmarkRunner.ts              framework-free single-request runner
    benchmarkStats.ts               pure percentile/summary helpers
```

`modules/registry.tsx` change, in one entry:

- key `playground` → `tools`
- `title: 'Tools'`, description `'Interactive tooling: SSE chat testing and gateway benchmarking'`
- icon `Terminal` → `Wrench`
- `order: 7` unchanged, `component: ToolsView`, `preload: () => import('./7-tools/ToolsView')`

Safe because: no `'playground'` string literal is compared outside the registry
(`Header.tsx` compares `tab.id`), hotkeys 1–9 are derived from `ORDERED_MODULES`
(`hooks/useKeyboardShortcuts.ts`), and `activeTab` is never persisted.

The tool registry reuses the module definition shape rather than duplicating it:

```tsx
import React from 'react';
import { Gauge, MessageSquare } from 'lucide-react';
import type { FireflyModuleDefinition } from '../types';

export type ToolDefinition = FireflyModuleDefinition;

const ChatTool = React.lazy(() => import('./chat/ChatTool'));
const BenchmarkTool = React.lazy(() => import('./benchmark/BenchmarkTool'));

export const TOOL_REGISTRY = {
  chat: {
    title: 'Chat',
    description: 'SSE chat with token waterfall and raw packet inspector',
    order: 1,
    icon: MessageSquare,
    component: ChatTool,
    preload: () => import('./chat/ChatTool'),
  },
  benchmark: {
    title: 'Benchmark',
    description: 'Run N concurrent requests and compare latency',
    order: 2,
    icon: Gauge,
    component: BenchmarkTool,
    preload: () => import('./benchmark/BenchmarkTool'),
  },
} satisfies Record<string, ToolDefinition>;

export type ToolId = keyof typeof TOOL_REGISTRY;

export const ORDERED_TOOLS: readonly (ToolDefinition & { id: ToolId })[] = (
  Object.keys(TOOL_REGISTRY) as ToolId[]
)
  .map((id) => ({ ...TOOL_REGISTRY[id], id }))
  .toSorted((a, b) => a.order - b.order);
```

As in `modules/registry.tsx`, every `component` is a `React.lazy` wrapper created at module
scope and `preload` imports the same path.

## Sub-navigation, sidebar, and the collapse behaviour

- `ToolsView` uses the Providers master-detail pattern (`ProvidersView.tsx:255`):
  `grid grid-cols-1 lg:grid-cols-[minmax(190px,230px)_1fr] gap-4 items-start`; below `lg` the
  sidebar stacks above the tool area, exactly like the Providers list does.
- `ToolSidebar` renders `ORDERED_TOOLS` as a `<button>` per tool with icon, title, and a
  one-line description; the active entry is highlighted the way the Providers list highlights
  the selected provider and carries `aria-current="true"`.
- Hover and focus call the entry's `preload()`, mirroring the header tab hover-preload, so
  switching to a not-yet-loaded tool is instant.
- The tool area renders inside `<Suspense fallback={<ModuleSkeleton />}>` with `React.lazy`,
  so a non-active tool never enters the initial chunk.
- Chat tool only: a `◀/▶` toggle collapses the telemetry column, after which chat spans the
  full width. The toggle sits in a small toolbar above the chat grid so it stays reachable
  while the panel is collapsed (the layout mock put it inside the panel header, which would
  hide the reopen affordance), and it exposes `aria-expanded` plus `aria-controls` pointing at
  the panel's `id`. The transition is skipped under `prefers-reduced-motion`.
- `shared/useModelOptions()` centralises what `chat/ChatWindow.tsx:127-199` does today (enabled
  models/combos, availability via upstream enabled state and breaker state, auto-pick of a
  default selection). Chat consumes it unchanged; Benchmark uses the same list for its model
  dropdown. This is a pure extraction — the chat's rendered output must not change.
- `shared/MetricPill.tsx` is the metric card primitive (`label`, `value`) already spelled out
  inline four times in `TokenStreamWaterfall` (`p-2 rounded-lg border border-white/[0.04]`,
  `text-[10px] text-neutral-500` label, `text-neutral-200 font-medium text-sm tabular-nums`
  value). The waterfall is migrated to it with byte-identical class strings, and the benchmark
  summary reuses it, so both tools read as one system.

## State

Two new slices registered in `core/state/store.ts` with the existing pattern (slice + atomic
selector hooks + a map entry in the singleton action dispatcher):

`core/state/toolsSlice.ts`

| Field | Default | Notes |
| :--- | :--- | :--- |
| `activeToolId: ToolId` | `'chat'` | resets on reload, like `activeTab` |
| `telemetryCollapsed: boolean` | `false` | added with the collapse work; chat keeps today's look on first visit |

Actions: `setActiveToolId`, `toggleTelemetry`.

`core/state/benchmarkSlice.ts`

| Field | Default | Notes |
| :--- | :--- | :--- |
| `benchmarkModel: string` | `''` | seeded from `shared/useModelOptions()` |
| `benchmarkPrompt: string` | `'Write a short paragraph about a firefly.'` | editable |
| `benchmarkRequests: number` | `10` | clamped 1–100 on input |
| `benchmarkConcurrency: number` | `2` | clamped 1–20 on input |
| `benchmarkStatus: 'idle' \| 'running' \| 'done' \| 'aborted'` | `'idle'` | |
| `benchmarkTotal: number` | `0` | frozen at run start |
| `benchmarkCompleted: number` | `0` | drives the `completed/total` progress |
| `benchmarkResults: BenchmarkRequestResult[]` | `[]` | **replaced** at each run start, never appended across runs |

Actions: the field setters, `startBenchmark(total)`, `appendBenchmarkResult(result)`,
`finishBenchmark(status)`, `resetBenchmark()`.

Aggregate statistics are **not** slice fields — they are `useMemo` derivations in
`BenchmarkTool`/`BenchmarkResults` from `benchmarkResults` plus `benchmarkStats.ts` helpers.

Benchmark state lives in the slice, not in component state, so the results survive switching
to Chat and back — the same guarantee chat messages already have.

## Benchmark tool

### Form

- Model: dropdown over `shared/useModelOptions()` (combos + models).
- Prompt: textarea, editable, default `Write a short paragraph about a firefly.`
- Requests: 1–100. Concurrency: 1–20. Both clamped on input.
- Fixed parameters, stated in the UI so runs are comparable: `stream: true`,
  `temperature: 0`, `max_tokens: 128`.
- **Run** and **Stop** buttons; one line reminding that a run sends real requests upstream.

### Execution

`benchmark/benchmarkRunner.ts` is framework-free (no React, no store) and exports a single
request:

```ts
export type BenchmarkStatus = 'ok' | 'error' | 'aborted';

export interface BenchmarkRequestResult {
  index: number;            // 1-based dispatch order
  status: BenchmarkStatus;
  ttftMs: number | null;    // null when no content delta ever arrived
  totalMs: number;          // until [DONE] / socket close / abort
  tokens: number;           // content deltas received
  tps: number | null;       // tokens / (totalMs / 1000), 1 decimal
  errorMessage: string | null;
}

export async function benchmarkRequest(opts: {
  index: number;
  apiKey: string;
  model: string;
  prompt: string;
  signal: AbortSignal;
}): Promise<BenchmarkRequestResult>;
```

It POSTs to `/v1/chat/completions` on the same origin with
`Authorization: Bearer <key>`, `cache: 'no-store'`, and parses the SSE stream with a small
loop over `data:` frames (the same shape `chat/ChatWindow` handles, including `[DONE]`,
keep-alive comments, and `parsed.error` payloads).

`benchmark/useBenchmarkRun.ts` owns the loop and the `AbortController` ref and calls into the
slice for every state transition (`startBenchmark` → `appendBenchmarkResult` per result →
`finishBenchmark` when the pool drains). The pool runs `min(concurrency, requests)` workers
pulling from a queue of request indices; each finished request is appended to the slice
immediately, so results stream into the UI. Stop aborts the controller, in-flight requests
resolve as `aborted`, and not-yet-dispatched indices are never run. A run never touches
`playgroundIsGenerating`, so chat and benchmark cannot interfere.

### Metric definitions

Deliberately identical to the definitions the chat waterfall already uses, so numbers are
comparable across the two tools:

- **TTFT** — `readEnd - startTime` of the first read batch that carried at least one content
  delta (`chat/ChatWindow.tsx:495-500`). Reasoning-only deltas do not start the clock.
- **Tokens** — count of content deltas (`chunkCount` in chat).
- **TPS** — `tokens / (totalMs / 1000)`, rounded to one decimal, i.e. end-to-end including
  TTFT, exactly as `chat/ChatWindow.tsx:335-336`.
- **Error rate** — `errors / (ok + errors)`; aborted requests are excluded from both sides and
  reported separately.
- **TTFT p50 / p95** — nearest-rank on the sorted `ttftMs` of `ok` results:
  `sorted[ceil(p * n) - 1]`.
- **Aggregate TPS** — `sum(tokens) / sum(totalMs)` over `ok` results.

### Summary and visualisation

- Cards, built from `shared/MetricPill`: wall-clock of the whole run, TTFT p50, TTFT p95,
  aggregate TPS, error rate, total tokens; plus `completed/total` while running.
- Bars per result, styled like `TokenStreamWaterfall`: one row per request, TTFT segment in the
  cyan accent followed by the generation segment in the neutral tone, both on a shared time
  scale (`max(totalMs)` across results). Error rows render in the error tone, aborted rows are
  dimmed.
- Result table under the bars: index, status chip, TTFT, tokens, duration, TPS, and the error
  message when present.
- Empty state before the first run: the results area shows a placeholder instead of bars and
  table.

### Edge cases

| Situation | Behaviour |
| :--- | :--- |
| No model available | Run disabled with a hint, same "unavailable because upstream is closed" wording chat uses |
| Empty API key | Run disabled with a hint pointing at the Chat tool's key field |
| Run in progress | Run disabled, Stop enabled, progress counter live |
| Stop pressed | In-flight requests recorded as `aborted`, queue drained, summary marks the run aborted |
| Every request failed | Error rate 100%, per-request messages still readable in the table |
| More than 100 requests / 20 concurrency | Input clamped; results array bounded by 100 |

### Deliberate duplication

The runner re-implements ~30 lines of SSE frame handling that also exist in
`chat/ChatWindow.tsx`. Extracting a shared parser would touch a 921-line file with subtle
timing/throttle behaviour, so the duplication is accepted for this iteration and recorded as a
follow-up.

## Verification

- `cd frontend && npm run typecheck` and `npm run build` (no Go changes; `frontend/dist` is
  embedded by the Go build but stays untouched here).
- The repo has **no JavaScript test runner** (no vitest/jest and no `test` script). Adding one
  is a separate tooling decision, so behavioural verification is done in a real browser and
  the static gates above stay the automated ones.
- Browser sandbox (reusing the committed `configs/e2e-playground/`, whose upstream points at
  `http://127.0.0.1:19099/v1` and whose tenant key is the demo key the UI auto-picks):
  1. start a mock upstream on `127.0.0.1:19099` that streams OpenAI-shaped SSE;
  2. `go run ./cmd/firefly -config-dir configs/e2e-playground -addr 127.0.0.1:8080`;
  3. `cd frontend && npm run dev` (Vite on :3000, proxying `/v1` and `/api` to :8080);
  4. drive http://localhost:3000 with the `agent-browser` skill, logging in with the dashboard
     password (`12345678`) when the protected Tools tab opens the login modal.
- Checks: tab `7` opens **Tools**; Chat renders identically to today's Playground; collapse and
  re-expand the telemetry panel; switch to Benchmark and back (chat history preserved); run 5
  requests at concurrency 2 and confirm bars/table/summary plus a plausible TTFT against the
  mock's injected first-token delay; Stop mid-run marks in-flight rows `aborted`; a forced
  upstream failure (mock fault injection) shows the error rate and per-request message.
- Regression: header label and hotkey `7` unchanged in position; no other module's behaviour
  changes; `SettingsView` tab-guard text matches the new label.

## Documentation to update in the same commit

- `frontend/src/modules/6-settings/SettingsView.tsx:527` — protected-tab list says Playground.
- `README.md:288` (protected route list) and `README.md:291` (in-browser playground bullet).
- `DESIGN.md:139` (`playgroundSlice.ts`), `:148` (`7-playground/` tree entry, now `7-tools/`
  with `chat/`, `benchmark/`, `shared/` subfolders), `:211` (hotkey list "7: Playground").
- `CHANGELOG.md` entry is written at release time, per the project convention.

## Follow-ups (not this iteration)

1. Rename the historical `playground*` identifiers if the naming debt starts to bite.
2. Extract the shared SSE parser used by both chat and the benchmark runner.
3. Optional: persist UI preferences (active tool, telemetry collapsed) across reloads — would
   be a store-wide decision, not a Tools-page one.
4. Optional: adopt a JS test runner (vitest) so `benchmarkStats`/`benchmarkRunner` can be unit
   tested in CI; a repo-wide tooling decision, not part of this change.
