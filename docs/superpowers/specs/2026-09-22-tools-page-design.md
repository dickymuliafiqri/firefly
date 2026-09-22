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
- Keep chat behaviour, diagnostics, and state-across-tab-switches exactly as today.
- Add a Benchmark tool at "basic functional" depth: N requests at concurrency C through the
  gateway, reporting TTFT / TPS / duration / error rate, visualised in the existing
  waterfall style.
- No new dependencies (no router, no chart library).

## Non-goals (this iteration)

- Cross-reload persistence of UI state. The store has no `persist` middleware; `activeTab`
  resets on reload too, and the new state follows that convention.
- Renaming the historical `playground*` identifiers (`playgroundSlice`, `playgroundApiKey`,
  `playgroundIsGenerating`, …). Mechanical rename across 5 files, deferred to keep this
  diff reviewable.
- Extracting a shared SSE parser out of `ChatWindow.tsx` (see the duplication note below).
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

## A. Module restructure and registry

Rename the folder `frontend/src/modules/7-playground/` → `frontend/src/modules/7-tools/`,
flat inside (the convention used by `8-providers/`).

| Path | Status |
| :--- | :--- |
| `7-tools/ToolsView.tsx` | new — shell: sidebar + active tool area |
| `7-tools/tools.tsx` | new — `ToolDefinition`, `TOOL_REGISTRY`, `ORDERED_TOOLS`, `ToolId` |
| `7-tools/ChatTool.tsx` | new — chat plus the collapsible telemetry panel (today's `PlaygroundView` body) |
| `7-tools/ChatWindow.tsx` | moved; model/combo filtering swapped to `useModelOptions()` (no behaviour change) |
| `7-tools/TokenStreamWaterfall.tsx` | moved unchanged |
| `7-tools/StreamInspector.tsx` | moved unchanged |
| `7-tools/useModelOptions.ts` | new — shared model/combo filtering hook (extracted, no behaviour change) |
| `7-tools/BenchmarkTool.tsx` | new — form, run control, summary cards |
| `7-tools/benchmarkRunner.ts` | new — framework-free per-request runner |
| `7-tools/BenchmarkResults.tsx` | new — waterfall-style bars and the result table |

`modules/registry.tsx` change, in one entry:

- key `playground` → `tools`
- `title: 'Tools'`, description `'Interactive tooling: SSE chat testing and gateway benchmarking'`
- icon `Terminal` → `Wrench`
- `order: 7` unchanged, `component: ToolsView`, `preload: () => import('./7-tools/ToolsView')`

Safe because: no `'playground'` string literal is compared outside the registry
(`Header.tsx` compares `tab.id`), hotkeys 1–9 are derived from `ORDERED_MODULES`
(`hooks/useKeyboardShortcuts.ts`), and `activeTab` is never persisted.

The tool registry reuses the module definition shape rather than duplicating it:

```ts
import type { FireflyModuleDefinition } from '../types';
export type ToolDefinition = FireflyModuleDefinition;

export const TOOL_REGISTRY = {
  chat: {
    title: 'Chat',
    description: 'SSE chat with token waterfall and raw packet inspector',
    order: 1,
    icon: MessageSquare,
    component: ChatTool,
    preload: () => import('./ChatTool'),
  },
  benchmark: {
    title: 'Benchmark',
    description: 'Run N concurrent requests and compare latency',
    order: 2,
    icon: Gauge,
    component: BenchmarkTool,
    preload: () => import('./BenchmarkTool'),
  },
} satisfies Record<string, ToolDefinition>;

export type ToolId = keyof typeof TOOL_REGISTRY;

export const ORDERED_TOOLS: readonly (ToolDefinition & { id: ToolId })[] = (
  Object.keys(TOOL_REGISTRY) as ToolId[]
)
  .map((id) => ({ ...TOOL_REGISTRY[id], id }))
  .toSorted((a, b) => a.order - b.order);
```

As in `modules/registry.tsx`, every `component` above is a `React.lazy(() => import(...))`
wrapper created at module scope, and `preload` imports the same path.

## B. Sub-navigation, sidebar, and the collapse behaviour

- `ToolsView` uses the Providers master-detail pattern (`ProvidersView.tsx:255`):
  `grid grid-cols-1 lg:grid-cols-[minmax(190px,230px)_1fr] gap-4 items-start`; below `lg` the
  sidebar stacks above the tool area, exactly like the Providers list does.
- Sidebar entries come from `ORDERED_TOOLS`: a `<button>` per tool with icon, title, and a
  one-line description; the active entry is highlighted the way the Providers list
  highlights the selected provider, and carries `aria-current="true"`.
- Hover and focus call the entry's `preload()`, mirroring the header tab hover-preload, so
  switching to a not-yet-loaded tool is instant.
- The tool area renders inside `<Suspense fallback={<ModuleSkeleton />}>` with
  `React.lazy`, so a non-active tool never enters the initial chunk.
- Chat tool only: a `◀/▶` toggle in the telemetry panel header collapses the right column,
  after which chat spans the full width. The transition is skipped under
  `prefers-reduced-motion`, matching the app-wide convention.
- `useModelOptions()` centralises what `ChatWindow.tsx:127-199` does today (enabled
  models/combos, availability via upstream enabled state and breaker state, auto-pick of a
  default selection). Chat consumes it unchanged; Benchmark uses the same list for its model
  dropdown. This is a pure extraction — the chat's rendered output must not change.

## C. State

Two new slices registered in `core/state/store.ts` with the existing pattern (slice + atomic
selector hooks + a map entry in the singleton action dispatcher):

`core/state/toolsSlice.ts`

| Field | Default | Notes |
| :--- | :--- | :--- |
| `activeToolId: ToolId` | `'chat'` | resets on reload, like `activeTab` |
| `telemetryCollapsed: boolean` | `false` | chat keeps today's look on first visit |

Actions: `setActiveToolId`, `toggleTelemetry`.

`core/state/benchmarkSlice.ts`

| Field | Default | Notes |
| :--- | :--- | :--- |
| `benchmarkModel: string` | `''` | seeded from `useModelOptions()` |
| `benchmarkPrompt: string` | `'Write a short paragraph about a firefly.'` | editable |
| `benchmarkRequests: number` | `10` | clamped 1–100 |
| `benchmarkConcurrency: number` | `2` | clamped 1–20 |
| `benchmarkStatus: 'idle' \| 'running' \| 'done' \| 'aborted'` | `'idle'` | |
| `benchmarkCompleted: number` | `0` | drives the `completed/total` progress |
| `benchmarkResults: BenchmarkRequestResult[]` | `[]` | **replaced** at each run start, never appended |

Actions: the field setters, `startBenchmark`, `appendBenchmarkResult`, `stopBenchmark`,
`resetBenchmark`.

Benchmark state lives in the slice, not in component state, so the results survive switching
to Chat and back — the same guarantee chat messages already have.

## D. Benchmark tool

### Form

- Model: dropdown over `useModelOptions()` (combos + models).
- Prompt: textarea, editable, default `Write a short paragraph about a firefly.`
- Requests: 1–100. Concurrency: 1–20. Both clamped on input.
- Fixed parameters, stated in the UI so runs are comparable: `stream: true`,
  `temperature: 0`, `max_tokens: 128`.
- **Run** and **Stop** buttons; one line reminding that a run sends real requests upstream.

### Execution

`benchmarkRunner.ts` is framework-free (no React, no store) and exports a single request:

```ts
export interface BenchmarkRequestResult {
  index: number;            // 1-based dispatch order
  status: 'ok' | 'error' | 'aborted';
  ttftMs: number | null;    // null when no content delta ever arrived
  totalMs: number;          // until [DONE] / socket close / abort
  tokens: number;           // content deltas received
  tps: number | null;       // tokens / (totalMs / 1000), 1 decimal
  errorMessage: string | null;
}

export async function benchmarkRequest(opts: {
  apiKey: string;
  model: string;
  prompt: string;
  signal: AbortSignal;
}): Promise<BenchmarkRequestResult>;
```

It POSTs to `/v1/chat/completions` on the same origin with
`Authorization: Bearer <key>`, `cache: 'no-store'`, and parses the SSE stream with a small
loop over `data:` frames (the same shape `ChatWindow` handles, including `[DONE]`, keep-alive
comments, and `parsed.error` payloads).

`BenchmarkTool` owns the run loop and the `AbortController` ref, and calls into the slice for
every state transition (`startBenchmark` → `appendBenchmarkResult` per result → `stopBenchmark`
on abort; `resetBenchmark` clears results). The pool runs `benchmarkConcurrency` workers pulling
from a queue of `benchmarkRequests` indices; each finished request is appended to the slice
immediately, so results stream into the UI. One `AbortController` covers the whole run; Stop
aborts it, in-flight requests resolve as `aborted`, and not-yet-dispatched indices are simply
never run. A run never touches `playgroundIsGenerating`, so chat and benchmark cannot interfere.

### Metric definitions

Deliberately identical to the definitions the chat waterfall already uses, so numbers are
comparable across the two tools:

- **TTFT** — `readEnd - startTime` of the first read batch that carried at least one content
  delta (`ChatWindow.tsx:495-500`). Reasoning-only deltas do not start the clock, matching chat.
- **Tokens** — count of content deltas (`chunkCount` in chat).
- **TPS** — `tokens / (totalMs / 1000)`, rounded to one decimal, i.e. end-to-end including
  TTFT, exactly as `ChatWindow.tsx:335-336`.
- **Error rate** — `errors / (ok + errors)`; aborted requests are excluded from both sides and
  reported separately.
- **TTFT p50 / p95** — nearest-rank on the sorted `ttftMs` of `ok` results:
  `sorted[ceil(p * n) - 1]`.
- **Aggregate TPS** — `sum(tokens) / sum(totalMs)` over `ok` results.

### Summary and visualisation

- Cards: wall-clock of the whole run, TTFT p50, TTFT p95, aggregate TPS, error rate, total
  tokens; plus `completed/total` while running.
- Bars per result, styled like `TokenStreamWaterfall`: one row per request, TTFT segment in a
  dim tone followed by the generation segment in the accent tone, both on a shared time scale
  (`max(totalMs)` across results). Error rows render full-width in the error tone, aborted rows
  are dimmed.
- Result table under the bars: index, status chip, TTFT, tokens, duration, TPS, and the error
  message when present.
- Empty state before the first run: the results area shows a placeholder instead of bars and table.

### Edge cases

| Situation | Behaviour |
| :--- | :--- |
| No model available | Run disabled with a hint, same "unavailable because upstream is closed" wording chat uses |
| Empty API key | Run disabled with a hint pointing at the Chat tool's key field |
| Run in progress | Run disabled, Stop enabled, progress counter live |
| Stop pressed | In-flight requests recorded as `aborted`, queue drained, summary marks the run aborted |
| Every request failed | Error rate 100%, per-request messages still readable in the table |
| More than 100 requests / 20 concurrency | Input clamped; no unbounded memory growth (results array bounded by 100) |

### Deliberate duplication

The runner re-implements ~30 lines of SSE frame handling that also exist in `ChatWindow.tsx`.
Extracting a shared parser would touch a 921-line file with subtle timing/throttle behaviour,
so the duplication is accepted for this iteration and recorded as a follow-up.

## E. Verification

- `cd frontend && npm run typecheck` and `npm run build` (no Go changes; `frontend/dist` is
  embedded by the Go build but stays untouched here).
- Browser end-to-end against a local Firefly in file-config mode with the Bun mock upstream
  used by the 2026-09-22 audit:
  1. Tab `7` opens **Tools**; Chat renders identically to today's Playground.
  2. Collapse the telemetry panel → chat spans full width; toggle back.
  3. Switch to Benchmark and back → chat history and diagnostics preserved.
  4. Run 5 requests at concurrency 2 → bars, table, and summary populate; progress reaches
     `5/5`; TTFT/TPS values are plausible against the mock's forced latency.
  5. Press Stop mid-run → in-flight rows become `aborted` and the summary reports it.
  6. Force an upstream error (`/_mock/force?status=500`) → error rate and per-request message
     render correctly.
- Regression: header label and hotkey `7` unchanged in position; no other module's behaviour
  changes; `SettingsView` tab-guard text matches the new label.

## F. Documentation to update in the same commit

- `frontend/src/modules/6-settings/SettingsView.tsx:527` — protected-tab list says Playground.
- `README.md:288` (protected route list) and `README.md:291` (in-browser playground bullet).
- `DESIGN.md:139` (`playgroundSlice.ts`), `:148` (`7-playground/` tree entry), `:211`
  (hotkey list "7: Playground").
- `CHANGELOG.md` entry is written at release time, per the project convention.

## G. Follow-ups (not this iteration)

1. Rename the historical `playground*` identifiers if the naming debt starts to bite.
2. Extract the shared SSE parser used by both chat and the benchmark runner.
3. Optional: persist UI preferences (active tool, telemetry collapsed) across reloads — would
   be a store-wide decision, not a Tools-page one.
