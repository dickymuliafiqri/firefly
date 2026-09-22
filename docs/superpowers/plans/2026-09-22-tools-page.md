# Tools page (Playground → sidebar tool shell with Chat + Benchmark) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the single-purpose Playground module into a **Tools** page — an in-module tool
registry plus sidebar — keeping today's chat behaviour identical and adding a Benchmark tool
that fires N concurrent requests through the gateway and reports TTFT / TPS / duration / error
rate.

**Architecture:** The module folder `modules/7-playground/` becomes `modules/7-tools/` with one
folder per tool (`chat/`, `benchmark/`) and a `shared/` folder for the two pieces both tools
need. A local tool registry (`7-tools/tools.tsx`) mirrors `modules/registry.tsx` in shape, so
the sidebar, the Suspense outlet, and the chunk preloads all derive from a single source of
truth. New state lives in two Zustand slices (`toolsSlice`, `benchmarkSlice`) composed into the
existing root store; chat keeps its current `playground*` slice untouched.

**Tech Stack:** React 19.3.0 (`React.lazy`, `Suspense`, `useTransition`, `useSyncExternalStore`
via zustand 5), TypeScript 5.4, Vite 5, Tailwind 3.4, lucide-react 0.363, Zustand 5. No new
dependency of any kind.

**Spec:** `docs/superpowers/specs/2026-09-22-tools-page-design.md`

## Global Constraints

- **No new dependencies.** `frontend/package.json` dependencies and devDependencies stay
  exactly as they are (no router, no chart library, no test runner).
- **Frontend only.** No Go file changes; `cmd/`, `internal/`, `configs/` are untouched.
- **Chat behaviour must not change.** Same requests, same rendered markup, same metric
  formulas, same store fields. `TokenStreamWaterfall` and `StreamInspector` output must be
  class-for-class identical after the extractions.
- **Metric definitions are shared, not re-invented.** TTFT = `readEnd - startTime` of the first
  read batch carrying ≥1 content delta; tokens = content-delta count; TPS = `tokens /
  (totalMs / 1000)` rounded to 1 decimal; percentiles are nearest-rank `sorted[ceil(p*n)-1]`;
  error rate = `errors / (ok + errors)` with aborted excluded.
- **New files must be CRLF** in this checkout (`core.autocrlf=true`; every existing frontend
  source is CRLF). After writing a new file, normalise it:
  `sed -i 's/\r$//' <file> && sed -i 's/$/\r/' <file>` then verify
  `grep -c $'\r$' <file>` equals `wc -l <file>`.
- **Verification gates:** `cd frontend && npm run typecheck` and `npm run build` (both must be
  clean) plus the browser checks named in each task. There is no JS test runner in this repo;
  do not add one.
- **Comments:** only for non-obvious *why*. Exported components carry the repo's existing
  "Vercel React Best Practices" bullet block.
- **Commits:** conventional-commit subjects, one per task, tests/typecheck green first.

## File structure

| File | Responsibility |
| :--- | :--- |
| `frontend/src/modules/7-tools/ToolsView.tsx` | Module shell: sidebar + Suspense outlet for the active tool |
| `frontend/src/modules/7-tools/ToolSidebar.tsx` | Tool list as real buttons; `aria-current`; hover/focus chunk preload |
| `frontend/src/modules/7-tools/tools.tsx` | `ToolDefinition`, `TOOL_REGISTRY`, `ORDERED_TOOLS`, `ToolId` |
| `frontend/src/modules/7-tools/shared/MetricPill.tsx` | label + value metric card used by both tools |
| `frontend/src/modules/7-tools/shared/useModelOptions.ts` | enabled models/combos, availability, ordered options, `firstAvailableId` |
| `frontend/src/modules/7-tools/chat/ChatTool.tsx` | Chat pane + collapsible telemetry pane, toolbar toggle |
| `frontend/src/modules/7-tools/chat/ChatWindow.tsx` | Moved from `7-playground/`; consumes `useModelOptions` |
| `frontend/src/modules/7-tools/chat/TokenStreamWaterfall.tsx` | Moved; four metric cards swapped to `MetricPill` |
| `frontend/src/modules/7-tools/chat/StreamInspector.tsx` | Moved unchanged |
| `frontend/src/modules/7-tools/benchmark/BenchmarkTool.tsx` | Composition: form, status line, summary cards, results |
| `frontend/src/modules/7-tools/benchmark/BenchmarkForm.tsx` | Model / prompt / requests / concurrency + Run/Stop + hints |
| `frontend/src/modules/7-tools/benchmark/BenchmarkResults.tsx` | Latency bars + per-request table + empty state |
| `frontend/src/modules/7-tools/benchmark/useBenchmarkRun.ts` | Worker pool + module-scope AbortController + store transitions |
| `frontend/src/modules/7-tools/benchmark/benchmarkRunner.ts` | Framework-free single-request runner (fetch + SSE parse) |
| `frontend/src/modules/7-tools/benchmark/benchmarkStats.ts` | Pure `percentile` / `summarize` helpers |
| `frontend/src/core/state/toolsSlice.ts` | `activeToolId`, `telemetryCollapsed` |
| `frontend/src/core/state/benchmarkSlice.ts` | Benchmark form fields, run state, wall clock, per-request results |
| `frontend/src/core/state/store.ts` | Composes the new slices, adds selector hooks + action maps |
| `frontend/src/modules/registry.tsx` | Module 7 entry: key `tools`, title `Tools`, icon `Wrench` |
| `configs/e2e-playground/mock-upstream.mjs` | Local (gitignored) deterministic mock upstream on `127.0.0.1:19099` |

---

### Task 1: Sandbox harness and module restructure

Establishes the browser sandbox (so every later task can be verified in a real browser) and
moves the module to `7-tools/chat/` **without any behaviour change**. The registry keeps the
key `playground` and the label `Playground` in this task; Task 2 flips them together with the
sidebar, so the tab never shows a label that lies about what it renders.

**Files:**
- Create: `configs/e2e-playground/mock-upstream.mjs` (local only — `configs/` is gitignored)
- Move: `frontend/src/modules/7-playground/{ChatWindow,TokenStreamWaterfall,StreamInspector}.tsx`
  → `frontend/src/modules/7-tools/chat/`
- Move + rename: `frontend/src/modules/7-playground/PlaygroundView.tsx`
  → `frontend/src/modules/7-tools/chat/ChatTool.tsx`
- Modify: `frontend/src/modules/registry.tsx:21,86-87`

**Interfaces:**
- Consumes: nothing (first task).
- Produces: `frontend/src/modules/7-tools/chat/ChatTool.tsx` default-exporting a memoised
  component (consumed by `tools.tsx` in Task 2, which lazy-imports it).

- [ ] **Step 1: Write the mock upstream**

The verification recipe needs a deterministic streaming upstream. `cmd/loadtest -mock` binds an
ephemeral port and cannot be pointed at by `configs/e2e-playground/upstreams.json`, so this
small Node script owns port 19099.

Create `configs/e2e-playground/mock-upstream.mjs`:

```js
// Deterministic OpenAI-shaped mock upstream for the Firefly e2e sandbox.
// Run: node configs/e2e-playground/mock-upstream.mjs
// Fault injection: /_mock/force?status=500 | /_mock/reset | /_mock/seen
import http from 'node:http';

const PORT = Number(process.env.MOCK_PORT || 19099);
const FIRST_TOKEN_DELAY_MS = 120;
const CHUNK_INTERVAL_MS = 15;
const CHUNKS = 12;

let forced = null;
const seen = [];

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

const sendJson = (res, status, body) => {
  const payload = JSON.stringify(body);
  res.writeHead(status, {
    'Content-Type': 'application/json',
    'Content-Length': Buffer.byteLength(payload),
  });
  res.end(payload);
};

const readBody = (req) =>
  new Promise((resolve) => {
    let raw = '';
    req.on('data', (chunk) => {
      raw += chunk;
    });
    req.on('end', () => {
      try {
        resolve(JSON.parse(raw || '{}'));
      } catch {
        resolve({});
      }
    });
  });

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, `http://127.0.0.1:${PORT}`);

  if (url.pathname === '/_mock/force') {
    const status = Number(url.searchParams.get('status') || 0);
    forced = status
      ? { status, body: { error: { message: `forced ${status}`, type: 'mock_forced_error' } } }
      : null;
    sendJson(res, 200, { forced });
    return;
  }
  if (url.pathname === '/_mock/reset') {
    forced = null;
    seen.length = 0;
    sendJson(res, 200, { ok: true });
    return;
  }
  if (url.pathname === '/_mock/seen') {
    sendJson(res, 200, { count: seen.length, requests: seen });
    return;
  }

  if (req.method === 'POST' && url.pathname.endsWith('/chat/completions')) {
    const body = await readBody(req);
    seen.push({ at: Date.now(), model: body.model, stream: body.stream === true });

    if (forced) {
      sendJson(res, forced.status, forced.body);
      return;
    }

    const model = body.model || 'mock-alpha';
    const promptTokens = JSON.stringify(body.messages || []).length;

    if (body.stream === true) {
      res.writeHead(200, {
        'Content-Type': 'text/event-stream',
        'Cache-Control': 'no-cache',
        Connection: 'keep-alive',
        'X-Accel-Buffering': 'no',
      });
      res.write(': keep-alive\n\n');
      await sleep(FIRST_TOKEN_DELAY_MS);

      for (let i = 0; i < CHUNKS; i++) {
        const frame = {
          id: 'chatcmpl-mock',
          object: 'chat.completion.chunk',
          created: Math.floor(Date.now() / 1000),
          model,
          choices: [
            {
              index: 0,
              delta: { content: i === 0 ? 'A firefly' : ` glows_${i}` },
              finish_reason: null,
            },
          ],
        };
        res.write(`data: ${JSON.stringify(frame)}\n\n`);
        await sleep(CHUNK_INTERVAL_MS);
      }

      const finalFrame = {
        id: 'chatcmpl-mock',
        object: 'chat.completion.chunk',
        created: Math.floor(Date.now() / 1000),
        model,
        choices: [{ index: 0, delta: {}, finish_reason: 'stop' }],
      };
      res.write(`data: ${JSON.stringify(finalFrame)}\n\n`);
      res.write('data: [DONE]\n\n');
      res.end();
      return;
    }

    sendJson(res, 200, {
      id: 'chatcmpl-mock',
      object: 'chat.completion',
      created: Math.floor(Date.now() / 1000),
      model,
      choices: [
        {
          index: 0,
          message: { role: 'assistant', content: 'A firefly glows in the dark.' },
          finish_reason: 'stop',
        },
      ],
      usage: { prompt_tokens: promptTokens, completion_tokens: 7, total_tokens: promptTokens + 7 },
    });
    return;
  }

  sendJson(res, 404, { error: { message: `no mock route for ${req.method} ${url.pathname}` } });
});

server.listen(PORT, '127.0.0.1', () => {
  console.log(`mock upstream listening on http://127.0.0.1:${PORT}`);
});
```

- [ ] **Step 2: Recreate the sandbox config if it is missing**

`configs/` is gitignored (`.gitignore:168`), so these files exist only on the machine that
created them. If `configs/e2e-playground/` is absent, recreate it. `upstreams.json`:

```json
{
  "upstreams": [
    {
      "name": "mock-lab",
      "protocol": "openai",
      "base_url": "http://127.0.0.1:19099/v1",
      "api_key": "sk-mock-e2e-secret-0001",
      "api_keys": ["sk-mock-e2e-secret-0001"],
      "key_strategy": "round_robin",
      "credential_pool": [
        {
          "ref": "mock-lab-key-local-1",
          "api_key": "sk-mock-e2e-secret-0001",
          "secret": "sk-mock-e2e-secret-0001"
        }
      ],
      "timeout_ms": 30000,
      "idle_timeout_ms": 90000,
      "stream_idle_timeout_ms": 120000,
      "max_idle_conns_per_host": 1000,
      "max_conns_per_host": 1500,
      "allow_insecure": true,
      "enabled": true,
      "key_error_threshold": 0,
      "key_error_action": "deactivate",
      "key_cooldown_duration_ms": 300000,
      "probe_model": "mock-alpha",
      "egress_mode": "direct",
      "warp_auto_rotate_on_429": false
    }
  ]
}
```

`models.json`:

```json
{
  "models": [
    {
      "public_name": "labs-mock",
      "upstream": "mock-lab",
      "upstream_model": "mock-alpha",
      "capabilities": { "stream": true, "tools": true, "json_mode": true },
      "max_context": 128000,
      "enabled": true
    }
  ]
}
```

`tenants.json`:

```json
{
  "tenants": [
    {
      "api_key": "sk-gw-demo-000000000000000000000000",
      "name": "demo",
      "status": "active",
      "max_tokens": 10000000,
      "used_tokens": 0,
      "expires_at": 1893456000000,
      "allowed_models": ["*"],
      "rate_limit": { "rps": 50, "burst": 100, "max_concurrent": 20 }
    }
  ]
}
```

Also required in the same directory: `combos.json` (`{"combos": []}`), `tls.json`
(`{"enabled": false}`), `tokensaver.json` (`{"enabled": false, "compress_tool_output": true,
"terse_output": false, "minimal_code": false, "compress_context": false,
"max_tool_output_chars": 12000, "context_threshold": 32000}`) and `auth.json` (generated by the
server on first run with the dashboard password `12345678`).

- [ ] **Step 3: Verify the mock streams**

```bash
node configs/e2e-playground/mock-upstream.mjs &
curl -sN -X POST http://127.0.0.1:19099/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"mock-alpha","stream":true,"messages":[{"role":"user","content":"hi"}]}' | head -5
curl -s http://127.0.0.1:19099/_mock/seen
```

Expected: `data: {...}` frames appear one at a time (the first only after ~120 ms), the
`/_mock/seen` call reports `count: 1`. Leave the process running.

- [ ] **Step 4: Move the module files**

```bash
cd frontend/src/modules
mkdir -p 7-tools/chat
git mv 7-playground/ChatWindow.tsx 7-tools/chat/ChatWindow.tsx
git mv 7-playground/TokenStreamWaterfall.tsx 7-tools/chat/TokenStreamWaterfall.tsx
git mv 7-playground/StreamInspector.tsx 7-tools/chat/StreamInspector.tsx
git mv 7-playground/PlaygroundView.tsx 7-tools/chat/ChatTool.tsx
rmdir 7-playground
cd "C:/Users/Dicky Mulia Fiqri/go/src/github.com/dickymuliafiqri/firefly"
git status --short
```

Expected: four `R` (renamed) entries, no deletions.

- [ ] **Step 5: Rename the component in the moved file**

In `frontend/src/modules/7-tools/chat/ChatTool.tsx`, the imports of
`./ChatWindow`, `./TokenStreamWaterfall`, `./StreamInspector` stay valid (same folder) and the
`@/core/state/store` imports are untouched. Only the component identity and doc comment change:

```tsx
/**
 * ChatTool
 * Interactive LLM test chat with real-time SSE waterfall telemetry.
 * All chat messages, timings, and inspector state live in the Zustand store,
 * preserving state across tool and tab navigation.
 *
 * Vercel React Best Practices:
 * - rerender-memo
 * - rerender-defer-reads: uses atomic selector hooks
 */
export default React.memo(function ChatTool() {
```

(The body, including the 12-column grid, is unchanged.)

- [ ] **Step 6: Point the module registry at the new path**

In `frontend/src/modules/registry.tsx`, change line 21 to:

```tsx
const PlaygroundView = lazy(() => import('./7-tools/chat/ChatTool'));
```

and the `playground` entry (lines 81-88) to:

```tsx
  playground: {
    title: 'Playground',
    description: 'Real-time SSE token stream tester and direct proxy verification',
    order: 7,
    icon: Terminal,
    component: PlaygroundView,
    preload: () => import('./7-tools/chat/ChatTool'),
  },
```

The key and title stay `playground` / `Playground` on purpose — Task 2 flips them together with
the UI that makes the label true.

- [ ] **Step 7: Typecheck, build, and verify the browser sandbox end to end**

```bash
cd frontend && npm run typecheck && npm run build
```

Then, from the repo root (three terminals):

```bash
# 1. mock upstream (already running from Step 3)
node configs/e2e-playground/mock-upstream.mjs
# 2. gateway
go run ./cmd/firefly -config-dir configs/e2e-playground -addr 127.0.0.1:8080
# 3. frontend dev server
cd frontend && npm run dev
```

With the `agent-browser` skill: open `http://localhost:3000`, press `7`, log in with
`12345678` when the login modal appears. Expected: the tab labelled **Playground** renders the
chat exactly as before (model dropdown shows `labs-mock`, API key auto-filled with
`sk-gw-demo-000000000000000000000000`); sending a prompt streams tokens and updates the
waterfall.

- [ ] **Step 8: Commit**

```bash
git add frontend/src/modules configs
git commit -m "refactor(frontend): move the playground module into 7-tools/chat"
```

(`configs/` is gitignored, so nothing from the sandbox enters the commit.)

---

### Task 2: Tool registry, sidebar, and the Tools module entry

**Files:**
- Create: `frontend/src/modules/7-tools/tools.tsx`
- Create: `frontend/src/core/state/toolsSlice.ts`
- Create: `frontend/src/modules/7-tools/ToolSidebar.tsx`
- Create: `frontend/src/modules/7-tools/ToolsView.tsx`
- Modify: `frontend/src/core/state/store.ts`
- Modify: `frontend/src/modules/registry.tsx:21,81-88`
- Modify: `frontend/src/modules/6-settings/SettingsView.tsx:527`
- Modify: `README.md:288,291`
- Modify: `DESIGN.md:136-142,148,211`

**Interfaces:**
- Consumes: `ChatTool` from Task 1.
- Produces: `ToolId` (`'chat'` until Task 6 appends `'benchmark'`), `TOOL_REGISTRY`,
  `ORDERED_TOOLS`, `ToolDefinition`; `useActiveToolId()`, `useSetActiveToolId()`,
  `useToolsActions()` from the store.

- [ ] **Step 1: Create the tool registry**

Create `frontend/src/modules/7-tools/tools.tsx`:

```tsx
import React from 'react';
import { MessageSquare } from 'lucide-react';
import type { FireflyModuleDefinition } from '../types';

export type ToolDefinition = FireflyModuleDefinition;

const ChatTool = React.lazy(() => import('./chat/ChatTool'));

/**
 * Pluggable registry for the tools inside the Tools module, mirroring the shape of
 * `modules/registry.tsx`: the sidebar, the Suspense outlet, and the preload
 * handlers all derive from these keys and `order` values.
 */
export const TOOL_REGISTRY = {
  chat: {
    title: 'Chat',
    description: 'SSE chat with token waterfall and raw packet inspector',
    order: 1,
    icon: MessageSquare,
    component: ChatTool,
    preload: () => import('./chat/ChatTool'),
  },
} satisfies Record<string, ToolDefinition>;

/** Every registry key is a valid active-tool identity. */
export type ToolId = keyof typeof TOOL_REGISTRY;

/** Registry entries flattened into sorted tools, each carrying its own id. */
export const ORDERED_TOOLS: readonly (ToolDefinition & { id: ToolId })[] = (
  Object.keys(TOOL_REGISTRY) as ToolId[]
)
  .map((id) => ({ ...TOOL_REGISTRY[id], id }))
  .toSorted((a, b) => a.order - b.order);
```

Do not add the `benchmark` entry yet — Task 6 appends it once that component exists.

- [ ] **Step 2: Create the tools slice**

Create `frontend/src/core/state/toolsSlice.ts`:

```ts
import type { StateCreator } from 'zustand';
import type { ToolId } from '@/modules/7-tools/tools';

export interface ToolsSlice {
  activeToolId: ToolId;
  setActiveToolId: (id: ToolId) => void;
}

export const createToolsSlice: StateCreator<ToolsSlice, [], [], ToolsSlice> = (set) => ({
  activeToolId: 'chat',
  setActiveToolId: (id) => set(() => ({ activeToolId: id })),
});
```

- [ ] **Step 3: Register the slice and its hooks in the root store**

In `frontend/src/core/state/store.ts`:

1. Import: `import { createToolsSlice, type ToolsSlice } from './toolsSlice';`
2. Compose the type and the creator:

```ts
export type AppStore = SettingsSlice & TelemetrySlice & UISlice & ToolsSlice & PlaygroundSlice;

export const useAppStore = create<AppStore>()((...a) => ({
  ...createSettingsSlice(...a),
  ...createTelemetrySlice(...a),
  ...createUISlice(...a),
  ...createToolsSlice(...a),
  ...createPlaygroundSlice(...a),
}));
```

3. Add a selector section before the playground hooks:

```ts
// ================= TOOLS SELECTOR HOOKS =================

export const useActiveToolId = () => useAppStore((state) => state.activeToolId);
export const useSetActiveToolId = () => useAppStore((state) => state.setActiveToolId);

const TOOLS_ACTIONS = {
  setActiveToolId: (...args: Parameters<AppStore['setActiveToolId']>) => useAppStore.getState().setActiveToolId(...args),
};

export const useToolsActions = () => TOOLS_ACTIONS;
```

`PLAYGROUND_ACTIONS` is the closest existing analogue, so the new actions get their own map
rather than an entry in `STORE_ACTIONS`.

- [ ] **Step 4: Create the sidebar**

Create `frontend/src/modules/7-tools/ToolSidebar.tsx`:

```tsx
import React from 'react';
import { cn } from '@/lib/utils';
import { ORDERED_TOOLS, type ToolId } from './tools';

export interface ToolSidebarProps {
  activeToolId: ToolId;
  onSelect: (id: ToolId) => void;
}

/**
 * ToolSidebar
 * Renders the tool registry as real buttons, so keyboard activation, focus rings,
 * and `aria-current` come from the platform instead of from ARIA-less divs.
 *
 * Vercel React Best Practices:
 * - rerender-memo
 * - bundle-dynamic-imports: preloads the tool chunk on hover/focus
 */
export const ToolSidebar = React.memo(function ToolSidebar({
  activeToolId,
  onSelect,
}: ToolSidebarProps) {
  return (
    <nav
      aria-label="Tools"
      className="rounded-xl border border-white/[0.06] bg-transparent overflow-hidden"
    >
      <div className="px-3 py-2 border-b border-white/[0.06] text-[10px] font-mono uppercase tracking-wider text-neutral-500">
        {ORDERED_TOOLS.length} tool{ORDERED_TOOLS.length === 1 ? '' : 's'}
      </div>
      <ul className="flex flex-col">
        {ORDERED_TOOLS.map((tool) => {
          const isActive = tool.id === activeToolId;
          const Icon = tool.icon;
          return (
            <li key={tool.id}>
              <button
                type="button"
                onClick={() => onSelect(tool.id)}
                onMouseEnter={() => void tool.preload()}
                onFocus={() => void tool.preload()}
                aria-current={isActive ? 'true' : undefined}
                className={cn(
                  'w-full text-left px-3 py-2.5 border-b border-white/[0.04] transition-colors cursor-pointer',
                  isActive ? 'bg-white/[0.05]' : 'hover:bg-white/[0.02]'
                )}
              >
                <div className="flex items-center gap-2">
                  <Icon
                    className={cn(
                      'w-3.5 h-3.5 flex-shrink-0',
                      isActive ? 'text-biolum-glow' : 'text-neutral-400'
                    )}
                  />
                  <span className="font-mono text-[12px] text-white truncate">{tool.title}</span>
                </div>
                <p className="mt-1 font-mono text-[10px] text-neutral-500 leading-snug">
                  {tool.description}
                </p>
              </button>
            </li>
          );
        })}
      </ul>
    </nav>
  );
});
```

- [ ] **Step 5: Create the module shell**

Create `frontend/src/modules/7-tools/ToolsView.tsx`:

```tsx
import React, { Suspense, useCallback, useTransition } from 'react';
import { ModuleSkeleton } from '../ModuleSkeleton';
import { ToolSidebar } from './ToolSidebar';
import { TOOL_REGISTRY, type ToolId } from './tools';
import { useActiveToolId, useSetActiveToolId } from '@/core/state/store';

/**
 * ToolsView
 * Module shell for the Tools page: a tool sidebar plus the active tool's chunk in its
 * own Suspense boundary, so a tool that has never been opened stays out of the
 * initial bundle and never blocks the sidebar (Vercel Best Practice:
 * async-suspense-boundaries).
 *
 * Vercel React Best Practices:
 * - rerender-memo
 * - rerender-defer-reads: atomic selector hooks
 */
export default React.memo(function ToolsView() {
  const activeToolId = useActiveToolId();
  const setActiveToolId = useSetActiveToolId();
  const [, startTransition] = useTransition();

  // zustand reads through useSyncExternalStore, so startTransition cannot defer this
  // update itself — it only marks the click as non-urgent. Instant tool switching
  // comes from the sidebar's hover/focus preload instead, like the header tabs.
  const handleSelect = useCallback(
    (id: ToolId) => {
      startTransition(() => setActiveToolId(id));
    },
    [setActiveToolId]
  );

  const activeTool = TOOL_REGISTRY[activeToolId] || TOOL_REGISTRY.chat;
  const ActiveTool = activeTool.component;

  return (
    <div className="space-y-6 animate-in fade-in duration-300">
      <div className="grid grid-cols-1 lg:grid-cols-[minmax(190px,230px)_1fr] gap-4 items-start">
        <ToolSidebar activeToolId={activeToolId} onSelect={handleSelect} />

        <Suspense fallback={<ModuleSkeleton />}>
          <ActiveTool />
        </Suspense>
      </div>
    </div>
  );
});
```

- [ ] **Step 6: Flip the module registry entry**

In `frontend/src/modules/registry.tsx`:

1. Line 21 becomes `const ToolsView = lazy(() => import('./7-tools/ToolsView'));`
2. The lucide import list swaps `Terminal` for `Wrench` (keep the position in the list).
3. The entry becomes:

```tsx
  tools: {
    title: 'Tools',
    description: 'Interactive tooling: SSE chat testing and gateway benchmarking',
    order: 7,
    icon: Wrench,
    component: ToolsView,
    preload: () => import('./7-tools/ToolsView'),
  },
```

Renaming the key (`playground` → `tools`) is safe: no string literal `'playground'` is compared
anywhere outside this file, the header hotkeys derive from `ORDERED_MODULES`, and `activeTab` is
never persisted.

- [ ] **Step 7: Update the labels that name the tab**

- `frontend/src/modules/6-settings/SettingsView.tsx:527`: in the protected-route sentence,
  replace `<span className="text-neutral-300">Playground</span>` with
  `<span className="text-neutral-300">Tools</span>`.
- `README.md:288`: replace `Settings, Playground, Providers` with `Settings, Tools, Providers`.
- `README.md:291`: replace the bullet with
  `- **In-Browser LLM Tools**: Chat against a model endpoint while inspecting token streaming waterfalls and raw SSE packet diagnostics, or run N concurrent requests through the gateway and compare TTFT/TPS in the Benchmark tool.`
- `DESIGN.md` (state slice list, after `playgroundSlice.ts`) add:

```
│   │       ├── toolsSlice.ts    # Active Tool & Telemetry Panel Collapse
│   │       ├── benchmarkSlice.ts# Benchmark Run State & Per-Request Results
```

- `DESIGN.md:148`: replace the `7-playground/` tree line with
  `│   │   └── 7-tools/             # Module 7: Tools (chat/ + benchmark/ sub-tools)`
- `DESIGN.md:211`: replace `7: Playground` with `7: Tools`.

- [ ] **Step 8: Typecheck, build, and check the shell in the browser**

```bash
cd frontend && npm run typecheck && npm run build
```

With the sandbox still running, reload `http://localhost:3000` (logged in), press `7`.
Expected: header tab reads **Tools**; the sidebar lists `1 tool` with a **Chat** entry marked
active; the chat renders and streams exactly as in Task 1. The header tab order and hotkeys 1-8
are unchanged.

- [ ] **Step 9: Commit**

```bash
git add frontend/src docs README.md DESIGN.md
git commit -m "feat(frontend): add the Tools page shell with a tool sidebar"
```

---

### Task 3: MetricPill and the waterfall migration

**Files:**
- Create: `frontend/src/modules/7-tools/shared/MetricPill.tsx`
- Modify: `frontend/src/modules/7-tools/chat/TokenStreamWaterfall.tsx:78-106`

**Interfaces:**
- Consumes: nothing.
- Produces: `MetricPill({ label, value, className })` — consumed by the waterfall here and by
  the benchmark summary in Task 6.

- [ ] **Step 1: Create the primitive**

Create `frontend/src/modules/7-tools/shared/MetricPill.tsx`:

```tsx
import React from 'react';
import { cn } from '@/lib/utils';

export interface MetricPillProps {
  label: string;
  value: React.ReactNode;
  className?: string;
}

/**
 * MetricPill
 * The metric card primitive both tools render: a 10px muted label over a tabular
 * value, on the shared translucent panel surface.
 */
export const MetricPill = React.memo(function MetricPill({
  label,
  value,
  className,
}: MetricPillProps) {
  return (
    <div className={cn('p-2 rounded-lg bg-transparent border border-white/[0.04]', className)}>
      <div className="text-[10px] text-neutral-500">{label}</div>
      <div className="text-neutral-200 font-medium text-sm tabular-nums mt-0.5">{value}</div>
    </div>
  );
});
```

- [ ] **Step 2: Migrate the four waterfall cards**

In `frontend/src/modules/7-tools/chat/TokenStreamWaterfall.tsx`, add the import after the
existing `cn` import:

```tsx
import { MetricPill } from '../shared/MetricPill';
```

and replace the whole four-card grid body (the `<div className="grid grid-cols-2
sm:grid-cols-4 gap-2 text-center">` block, currently lines 78-106) with:

```tsx
      <div className="grid grid-cols-2 sm:grid-cols-4 gap-2 text-center">
        <MetricPill label="TTFT" value={ttftMs !== null ? `${ttftMs}ms` : '—'} />
        <MetricPill label="Velocity" value={tps !== null ? `${tps} t/s` : '—'} />
        <MetricPill label="Chunks" value={totalTokens} />
        <MetricPill
          label="Elapsed"
          value={totalDurationMs !== null ? `${(totalDurationMs / 1000).toFixed(2)}s` : '—'}
        />
      </div>
```

The rendered classes must be identical to the inline version — the primitive carries the exact
same strings.

- [ ] **Step 3: Typecheck and visually confirm no drift**

```bash
cd frontend && npm run typecheck && npm run build
```

In the browser, compare the Chat waterfall against the pre-change screenshot/state: the four
pill labels, values, and spacing are unchanged (TTFT/Velocity/Chunks/Elapsed).

- [ ] **Step 4: Commit**

```bash
git add frontend/src/modules/7-tools/shared frontend/src/modules/7-tools/chat/TokenStreamWaterfall.tsx
git commit -m "refactor(frontend): extract the MetricPill card primitive"
```

---

### Task 4: Collapsible telemetry panel

**Files:**
- Modify: `frontend/src/core/state/toolsSlice.ts`
- Modify: `frontend/src/core/state/store.ts`
- Modify: `frontend/src/modules/7-tools/chat/ChatTool.tsx`

**Interfaces:**
- Consumes: `useActiveToolId` / tools slice from Task 2.
- Produces: `telemetryCollapsed: boolean`, `toggleTelemetry()`; store hooks
  `useTelemetryCollapsed()`, `useToggleTelemetry()`, and `useToolsActions().toggleTelemetry`.

- [ ] **Step 1: Extend the tools slice**

In `frontend/src/core/state/toolsSlice.ts`:

```ts
export interface ToolsSlice {
  activeToolId: ToolId;
  telemetryCollapsed: boolean;
  setActiveToolId: (id: ToolId) => void;
  toggleTelemetry: () => void;
}

export const createToolsSlice: StateCreator<ToolsSlice, [], [], ToolsSlice> = (set) => ({
  activeToolId: 'chat',
  telemetryCollapsed: false,
  setActiveToolId: (id) => set(() => ({ activeToolId: id })),
  toggleTelemetry: () => set((state) => ({ telemetryCollapsed: !state.telemetryCollapsed })),
});
```

- [ ] **Step 2: Add the store hooks**

In `frontend/src/core/state/store.ts`, inside the tools selector section:

```ts
export const useTelemetryCollapsed = () => useAppStore((state) => state.telemetryCollapsed);
export const useToggleTelemetry = () => useAppStore((state) => state.toggleTelemetry);
```

and extend `TOOLS_ACTIONS` with:

```ts
  toggleTelemetry: () => useAppStore.getState().toggleTelemetry(),
```

- [ ] **Step 3: Rework ChatTool with a toolbar toggle**

Replace the whole body of `frontend/src/modules/7-tools/chat/ChatTool.tsx` with:

```tsx
import React from 'react';
import { PanelRightClose, PanelRightOpen } from 'lucide-react';
import { Button } from '@/components/ui/Button';
import { ChatWindow } from './ChatWindow';
import { TokenStreamWaterfall } from './TokenStreamWaterfall';
import { StreamInspector } from './StreamInspector';
import { cn } from '@/lib/utils';
import { useReducedMotion } from '@/hooks/useReducedMotion';
import {
  usePlaygroundIsGenerating,
  usePlaygroundTtftMs,
  usePlaygroundTotalDurationMs,
  usePlaygroundTps,
  usePlaygroundTimings,
  usePlaygroundRawPackets,
  useTelemetryCollapsed,
  useToggleTelemetry,
} from '@/core/state/store';

const TELEMETRY_PANEL_ID = 'chat-telemetry-panel';

/**
 * ChatTool
 * Interactive LLM test chat with real-time SSE waterfall telemetry.
 * All chat messages, timings, and inspector state live in the Zustand store,
 * preserving state across tool and tab navigation.
 *
 * Vercel React Best Practices:
 * - rerender-memo
 * - rerender-defer-reads: uses atomic selector hooks
 */
export default React.memo(function ChatTool() {
  const isStreaming = usePlaygroundIsGenerating();
  const ttftMs = usePlaygroundTtftMs();
  const totalDurationMs = usePlaygroundTotalDurationMs();
  const tps = usePlaygroundTps();
  const timings = usePlaygroundTimings();
  const rawPackets = usePlaygroundRawPackets();
  const telemetryCollapsed = useTelemetryCollapsed();
  const toggleTelemetry = useToggleTelemetry();
  const reducedMotion = useReducedMotion();

  return (
    <div className="space-y-6 animate-in fade-in duration-300">
      {/* The toggle lives in its own always-visible toolbar: inside the panel it
          would collapse away with the panel, leaving no way to bring it back. */}
      <div className="flex items-center justify-between gap-3">
        <p className="text-[11px] font-mono text-neutral-500">
          Interactive SSE chat tester with live token telemetry.
        </p>
        <Button
          variant="minimal"
          size="sm"
          onClick={toggleTelemetry}
          aria-expanded={!telemetryCollapsed}
          aria-controls={TELEMETRY_PANEL_ID}
          title={telemetryCollapsed ? 'Show telemetry panel' : 'Hide telemetry panel'}
          leftIcon={
            telemetryCollapsed ? (
              <PanelRightOpen className="w-3.5 h-3.5 text-neutral-400 group-hover:text-white transition-colors" />
            ) : (
              <PanelRightClose className="w-3.5 h-3.5 text-neutral-400 group-hover:text-white transition-colors" />
            )
          }
        >
          {telemetryCollapsed ? 'Show telemetry' : 'Hide telemetry'}
        </Button>
      </div>

      {/* 2-Column Layout. The panel stays mounted and is hidden with the `hidden`
          attribute, which is what aria-controls promises and what keeps the chat
          column reflow free of animation work. */}
      <div
        className={cn('grid grid-cols-1 gap-6', !telemetryCollapsed && 'lg:grid-cols-12')}
      >
        <div className={telemetryCollapsed ? undefined : 'lg:col-span-7'}>
          <ChatWindow />
        </div>

        <div
          key={telemetryCollapsed ? 'collapsed' : 'expanded'}
          id={TELEMETRY_PANEL_ID}
          hidden={telemetryCollapsed}
          className={cn(
            'lg:col-span-5 space-y-6',
            !reducedMotion && !telemetryCollapsed && 'animate-in fade-in duration-200'
          )}
        >
          <TokenStreamWaterfall
            ttftMs={ttftMs}
            totalDurationMs={totalDurationMs}
            tps={tps}
            totalTokens={timings.length}
            timings={timings}
            isStreaming={isStreaming}
          />

          <StreamInspector rawPackets={rawPackets} />
        </div>
      </div>
    </div>
  );
});
```

- [ ] **Step 4: Typecheck and verify the collapse in the browser**

```bash
cd frontend && npm run typecheck
```

In the browser, on the Chat tool: click **Hide telemetry** — the chat expands to full width and
the panel disappears; click **Show telemetry** — the panel returns with the waterfall and
inspector intact (streaming a reply while collapsed still updates the waterfall on re-expand).
With the OS "reduce motion" setting enabled, the re-expand must not fade.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/core/state frontend/src/modules/7-tools/chat/ChatTool.tsx
git commit -m "feat(frontend): collapse the chat telemetry panel from a toolbar toggle"
```

---

### Task 5: Extract useModelOptions and rewire the chat

Pure extraction: after this task the chat must render and behave exactly as before, with the
availability logic living in `shared/` so the benchmark can reuse it.

**Files:**
- Create: `frontend/src/modules/7-tools/shared/useModelOptions.ts`
- Modify: `frontend/src/modules/7-tools/chat/ChatWindow.tsx:17-35,82-87,127-199`

**Interfaces:**
- Consumes: `useModels`, `useCombos`, `useUpstreams`, `useUpstreamBreakers` from the store.
- Produces:

```ts
export interface ModelOption { id: string; kind: 'combo' | 'model'; available: boolean }

export interface ModelOptions {
  models: ModelDTO[];
  combos: ComboDTO[];
  enabledModels: ModelDTO[];
  enabledCombos: ComboDTO[];
  isModelAvailable: (model: ModelDTO) => boolean;
  isComboAvailable: (combo: ComboDTO) => boolean;
  options: ModelOption[];
  firstAvailableId: string | null;
}

export function useModelOptions(): ModelOptions;
```

- [ ] **Step 1: Create the hook**

Create `frontend/src/modules/7-tools/shared/useModelOptions.ts`:

```ts
import { useCallback, useMemo } from 'react';
import {
  useCombos,
  useModels,
  useUpstreamBreakers,
  useUpstreams,
} from '@/core/state/store';
import type { ComboDTO, ModelDTO } from '@/services/schema';

export interface ModelOption {
  id: string;
  kind: 'combo' | 'model';
  available: boolean;
}

export interface ModelOptions {
  models: ModelDTO[];
  combos: ComboDTO[];
  enabledModels: ModelDTO[];
  enabledCombos: ComboDTO[];
  isModelAvailable: (model: ModelDTO) => boolean;
  isComboAvailable: (combo: ComboDTO) => boolean;
  options: ModelOption[];
  firstAvailableId: string | null;
}

/**
 * useModelOptions
 * Single source of truth for what this gateway can actually serve: a model is
 * available when it is enabled and at least one of its upstreams is enabled with a
 * circuit breaker that is not OPEN; a combo is available when at least one member
 * model is. Extracted verbatim from ChatWindow so the Benchmark tool can reuse it.
 */
export function useModelOptions(): ModelOptions {
  const models = useModels();
  const combos = useCombos();
  const upstreams = useUpstreams();
  const upstreamBreakers = useUpstreamBreakers();

  const isUpstreamActive = useCallback(
    (name: string) => {
      const u = upstreams.find((up) => up.name === name);
      if (u && u.enabled === false) return false;
      const st = upstreamBreakers[name] || 'CLOSED';
      return st !== 'OPEN';
    },
    [upstreams, upstreamBreakers]
  );

  const isModelAvailable = useCallback(
    (model: ModelDTO) => {
      if (model.enabled === false) return false;
      const candidates = [model.upstream, ...(model.fallback_upstreams || [])].filter(Boolean);
      if (candidates.length === 0) return false;
      return candidates.some(isUpstreamActive);
    },
    [isUpstreamActive]
  );

  const isComboAvailable = useCallback(
    (combo: ComboDTO) => {
      if (combo.enabled === false) return false;
      if (!combo.models || combo.models.length === 0) return false;
      return combo.models.some((memberPublicName) => {
        const m = models.find((mod) => mod.public_name === memberPublicName);
        return m ? isModelAvailable(m) : false;
      });
    },
    [models, isModelAvailable]
  );

  const enabledModels = useMemo(() => models.filter((m) => m.enabled !== false), [models]);
  const enabledCombos = useMemo(() => combos.filter((c) => c.enabled !== false), [combos]);

  const options = useMemo<ModelOption[]>(
    () => [
      ...enabledCombos.map((c) => ({
        id: c.name,
        kind: 'combo' as const,
        available: isComboAvailable(c),
      })),
      ...enabledModels.map((m) => ({
        id: m.public_name,
        kind: 'model' as const,
        available: isModelAvailable(m),
      })),
    ],
    [enabledCombos, enabledModels, isComboAvailable, isModelAvailable]
  );

  // Falls back to the first configured option when nothing is available, matching the
  // old auto-pick, so the dropdown is never empty while the catalog is.
  const firstAvailableId = useMemo(
    () => options.find((o) => o.available)?.id ?? options[0]?.id ?? null,
    [options]
  );

  return {
    models,
    combos,
    enabledModels,
    enabledCombos,
    isModelAvailable,
    isComboAvailable,
    options,
    firstAvailableId,
  };
}
```

- [ ] **Step 2: Rewire ChatWindow**

In `frontend/src/modules/7-tools/chat/ChatWindow.tsx`:

1. Delete `useModels`, `useCombos`, `useUpstreams`, `useUpstreamBreakers` from the
   `@/core/state/store` import list (keep `useTenants` and everything else), and add:

```tsx
import { useModelOptions } from '../shared/useModelOptions';
```

2. Replace lines 82-87 (`const adminToken` … `const upstreamBreakers = useUpstreamBreakers();`)
   with:

```tsx
  const adminToken = useAdminToken();
  const {
    models,
    combos,
    enabledModels,
    enabledCombos,
    isModelAvailable,
    isComboAvailable,
    options,
    firstAvailableId,
  } = useModelOptions();
  const tenants = useTenants();
```

3. Delete the `enabledModels` / `enabledCombos` / `isUpstreamActive` / `isModelAvailable` /
   `isComboAvailable` definitions (lines 127-160) — they now come from the hook. Keep
   `currentCombo`, `currentModel`, `currentUpstream`, and `isCurrentModelClosed` exactly as they
   are; they consume the hook's `combos`, `models`, `isComboAvailable`, `isModelAvailable`.

4. Replace the auto-pick effect (lines 187-199) with:

```tsx
  // Auto-pick default model/combo if not selected yet
  useEffect(() => {
    if (!firstAvailableId) return;
    if (!selectedModel || !options.some((o) => o.id === selectedModel)) {
      setPlaygroundSelectedModel(firstAvailableId);
    }
  }, [options, firstAvailableId, selectedModel, setPlaygroundSelectedModel]);
```

- [ ] **Step 3: Typecheck and diff the behaviour**

```bash
cd frontend && npm run typecheck && npm run build
```

In the browser: the Chat tool's model dropdown still lists `labs-mock`, the status pill still
reads Ready, and sending a prompt streams tokens with the waterfall updating. Nothing in the
rendered markup changes.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/modules/7-tools
git commit -m "refactor(frontend): extract useModelOptions for reuse by both tools"
```

---

### Task 6: Benchmark engine, form, and summary

Delivers a benchmark that runs end to end from the UI: the data layer (`benchmarkSlice`,
`benchmarkRunner`, `benchmarkStats`), the run hook, the form, and the summary cards. Per-request
bars and the result table are Task 7.

**Files:**
- Create: `frontend/src/core/state/benchmarkSlice.ts`
- Create: `frontend/src/modules/7-tools/benchmark/benchmarkRunner.ts`
- Create: `frontend/src/modules/7-tools/benchmark/benchmarkStats.ts`
- Create: `frontend/src/modules/7-tools/benchmark/useBenchmarkRun.ts`
- Create: `frontend/src/modules/7-tools/benchmark/BenchmarkForm.tsx`
- Create: `frontend/src/modules/7-tools/benchmark/BenchmarkTool.tsx`
- Modify: `frontend/src/core/state/store.ts`
- Modify: `frontend/src/modules/7-tools/tools.tsx` (append the benchmark entry)

**Interfaces:**
- Consumes: `MetricPill` (Task 3), `useModelOptions` (Task 5), the tools slice pattern (Task 2).
- Produces:

```ts
// benchmarkRunner.ts
export type BenchmarkRequestStatus = 'ok' | 'error' | 'aborted';
export interface BenchmarkRequestResult {
  index: number; status: BenchmarkRequestStatus; ttftMs: number | null;
  totalMs: number; tokens: number; tps: number | null; errorMessage: string | null;
}
export function benchmarkRequest(opts: {
  index: number; apiKey: string; model: string; prompt: string; signal: AbortSignal;
}): Promise<BenchmarkRequestResult>;

// benchmarkStats.ts
export function percentile(values: number[], p: number): number | null;
export function summarize(results: BenchmarkRequestResult[]): BenchmarkSummary;

// benchmarkSlice.ts
export type BenchmarkRunState = 'idle' | 'running' | 'done' | 'aborted';
export const BENCHMARK_MIN_REQUESTS = 1;   // …and MAX_REQUESTS 100, MIN/MAX_CONCURRENCY 1/20
```

- [ ] **Step 1: Create the slice**

Create `frontend/src/core/state/benchmarkSlice.ts`:

```ts
import type { StateCreator } from 'zustand';
import type { BenchmarkRequestResult } from '@/modules/7-tools/benchmark/benchmarkRunner';

export type BenchmarkRunState = 'idle' | 'running' | 'done' | 'aborted';

export const BENCHMARK_MIN_REQUESTS = 1;
export const BENCHMARK_MAX_REQUESTS = 100;
export const BENCHMARK_MIN_CONCURRENCY = 1;
export const BENCHMARK_MAX_CONCURRENCY = 20;

const clamp = (value: number, min: number, max: number) =>
  Math.min(Math.max(Math.round(value) || min, min), max);

export interface BenchmarkSlice {
  benchmarkModel: string;
  benchmarkPrompt: string;
  benchmarkRequests: number;
  benchmarkConcurrency: number;
  benchmarkStatus: BenchmarkRunState;
  benchmarkTotal: number;
  benchmarkCompleted: number;
  benchmarkResults: BenchmarkRequestResult[];
  benchmarkWallClockMs: number | null;

  setBenchmarkModel: (model: string) => void;
  setBenchmarkPrompt: (prompt: string) => void;
  setBenchmarkRequests: (count: number) => void;
  setBenchmarkConcurrency: (count: number) => void;
  startBenchmark: (total: number) => void;
  appendBenchmarkResult: (result: BenchmarkRequestResult) => void;
  finishBenchmark: (status: BenchmarkRunState, wallClockMs: number) => void;
  resetBenchmark: () => void;
}

export const createBenchmarkSlice: StateCreator<BenchmarkSlice, [], [], BenchmarkSlice> = (set) => ({
  benchmarkModel: '',
  benchmarkPrompt: 'Write a short paragraph about a firefly.',
  benchmarkRequests: 10,
  benchmarkConcurrency: 2,
  benchmarkStatus: 'idle',
  benchmarkTotal: 0,
  benchmarkCompleted: 0,
  benchmarkResults: [],
  benchmarkWallClockMs: null,

  setBenchmarkModel: (model) => set(() => ({ benchmarkModel: model })),
  setBenchmarkPrompt: (prompt) => set(() => ({ benchmarkPrompt: prompt })),
  setBenchmarkRequests: (count) =>
    set(() => ({ benchmarkRequests: clamp(count, BENCHMARK_MIN_REQUESTS, BENCHMARK_MAX_REQUESTS) })),
  setBenchmarkConcurrency: (count) =>
    set(() => ({
      benchmarkConcurrency: clamp(count, BENCHMARK_MIN_CONCURRENCY, BENCHMARK_MAX_CONCURRENCY),
    })),

  startBenchmark: (total) =>
    set(() => ({
      benchmarkStatus: 'running',
      benchmarkTotal: total,
      benchmarkCompleted: 0,
      benchmarkResults: [],
      benchmarkWallClockMs: null,
    })),

  appendBenchmarkResult: (result) =>
    set((state) => ({
      benchmarkResults: [...state.benchmarkResults, result],
      benchmarkCompleted: state.benchmarkCompleted + 1,
    })),

  finishBenchmark: (status, wallClockMs) =>
    set(() => ({ benchmarkStatus: status, benchmarkWallClockMs: wallClockMs })),

  resetBenchmark: () =>
    set(() => ({
      benchmarkStatus: 'idle',
      benchmarkTotal: 0,
      benchmarkCompleted: 0,
      benchmarkResults: [],
      benchmarkWallClockMs: null,
    })),
});
```

- [ ] **Step 2: Create the runner**

Create `frontend/src/modules/7-tools/benchmark/benchmarkRunner.ts`:

```ts
export type BenchmarkRequestStatus = 'ok' | 'error' | 'aborted';

export interface BenchmarkRequestResult {
  index: number;
  status: BenchmarkRequestStatus;
  ttftMs: number | null;
  totalMs: number;
  tokens: number;
  tps: number | null;
  errorMessage: string | null;
}

export interface BenchmarkRequestOptions {
  index: number;
  apiKey: string;
  model: string;
  prompt: string;
  signal: AbortSignal;
}

/**
 * benchmarkRequest
 * One measured streaming chat completion through the gateway. Framework-free on
 * purpose (no React, no store) so the pool in useBenchmarkRun owns all state.
 *
 * Metrics use the same definitions as the chat waterfall: TTFT is the first read
 * batch that carried content, tokens count content deltas, TPS is end-to-end.
 */
export async function benchmarkRequest({
  index,
  apiKey,
  model,
  prompt,
  signal,
}: BenchmarkRequestOptions): Promise<BenchmarkRequestResult> {
  const startTime = performance.now();
  let firstTokenTime: number | null = null;
  let tokens = 0;

  const finish = (
    status: BenchmarkRequestStatus,
    errorMessage: string | null
  ): BenchmarkRequestResult => {
    const totalMs = Math.round(performance.now() - startTime);
    return {
      index,
      status,
      ttftMs: firstTokenTime,
      totalMs,
      tokens,
      tps: tokens > 0 ? Math.round((tokens / (totalMs / 1000)) * 10) / 10 : null,
      errorMessage,
    };
  };

  try {
    const response = await fetch('/v1/chat/completions', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${apiKey}`,
      },
      signal,
      // Same reason as the chat window: a cacheable SSE body can be buffered whole
      // by some browsers, which would destroy every timing measured here.
      cache: 'no-store',
      body: JSON.stringify({
        model,
        messages: [{ role: 'user', content: prompt }],
        temperature: 0,
        max_tokens: 128,
        stream: true,
      }),
    });

    if (!response.ok) {
      const errJson = await response.json().catch(() => ({}));
      return finish(
        'error',
        errJson?.error?.message || `HTTP error ${response.status}: ${response.statusText}`
      );
    }
    if (!response.body) {
      return finish('error', 'response carried no body');
    }

    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';

    for (;;) {
      const { done, value } = await reader.read();
      const readEnd = performance.now();
      if (done) break;

      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split('\n');
      buffer = lines.pop() || '';

      let batchTokens = 0;
      let streamError: string | null = null;
      let sawDone = false;

      for (const line of lines) {
        const trimmed = line.trim();
        if (!trimmed || trimmed.startsWith(':')) continue;
        if (!trimmed.startsWith('data: ')) continue;

        const payload = trimmed.slice(6);
        if (payload === '[DONE]') {
          sawDone = true;
          continue;
        }

        try {
          const parsed = JSON.parse(payload);
          if (parsed?.error) {
            streamError = parsed.error.message || parsed.error.type || 'upstream stream error';
            continue;
          }
          if (parsed?.choices?.[0]?.delta?.content) batchTokens++;
        } catch {
          // Ignore a partial frame; the next read completes it.
        }
      }

      if (batchTokens > 0) {
        tokens += batchTokens;
        if (firstTokenTime === null) firstTokenTime = Math.round(readEnd - startTime);
      }
      if (streamError) return finish('error', streamError);
      if (sawDone) break;
    }

    return finish('ok', null);
  } catch (err) {
    if (signal.aborted) return finish('aborted', null);
    return finish('error', err instanceof Error ? err.message : 'request failed');
  }
}
```

- [ ] **Step 3: Create the stats helpers**

Create `frontend/src/modules/7-tools/benchmark/benchmarkStats.ts`:

```ts
import type { BenchmarkRequestResult } from './benchmarkRunner';

export interface BenchmarkSummary {
  ok: number;
  errors: number;
  aborted: number;
  errorRate: number | null;
  totalTokens: number;
  aggregateTps: number | null;
  ttftP50: number | null;
  ttftP95: number | null;
}

/** Nearest-rank percentile over unsorted input: sorted[ceil(p * n) - 1]. */
export function percentile(values: number[], p: number): number | null {
  if (values.length === 0) return null;
  const sorted = [...values].sort((a, b) => a - b);
  const rank = Math.ceil(p * sorted.length);
  return sorted[Math.min(Math.max(rank, 1), sorted.length) - 1];
}

/**
 * summarize
 * Aggregates over the successful requests only. Aborted requests are reported
 * separately and excluded from the error rate, because a user-initiated stop is not
 * a gateway failure.
 */
export function summarize(results: BenchmarkRequestResult[]): BenchmarkSummary {
  const okResults = results.filter((r) => r.status === 'ok');
  const errors = results.filter((r) => r.status === 'error').length;
  const aborted = results.filter((r) => r.status === 'aborted').length;

  const ttfts = okResults.map((r) => r.ttftMs).filter((v): v is number => v !== null);
  const totalTokens = okResults.reduce((sum, r) => sum + r.tokens, 0);
  const totalMs = okResults.reduce((sum, r) => sum + r.totalMs, 0);

  return {
    ok: okResults.length,
    errors,
    aborted,
    errorRate: okResults.length + errors > 0 ? errors / (okResults.length + errors) : null,
    totalTokens,
    aggregateTps: totalMs > 0 ? Math.round((totalTokens / (totalMs / 1000)) * 10) / 10 : null,
    ttftP50: percentile(ttfts, 0.5),
    ttftP95: percentile(ttfts, 0.95),
  };
}
```

- [ ] **Step 4: Create the run hook**

Create `frontend/src/modules/7-tools/benchmark/useBenchmarkRun.ts`:

```ts
import { useCallback } from 'react';
import { benchmarkRequest } from './benchmarkRunner';
import {
  useBenchmarkActions,
  useBenchmarkConcurrency,
  useBenchmarkModel,
  useBenchmarkPrompt,
  useBenchmarkRequests,
  usePlaygroundApiKey,
} from '@/core/state/store';

// Module scope, not React state: a run must survive switching tools (the tool
// component unmounts) and Stop must still reach it after coming back.
let activeRunId = 0;
let activeController: AbortController | null = null;

/**
 * useBenchmarkRun
 * Owns the worker pool: min(concurrency, requests) workers pull request indices from
 * a shared cursor, and every finished request is appended to the slice immediately so
 * results stream into the UI. The run is started by a click handler — never by an
 * effect — and it never touches `playgroundIsGenerating`, so chat and benchmark
 * cannot interfere with each other.
 */
export function useBenchmarkRun() {
  const model = useBenchmarkModel();
  const prompt = useBenchmarkPrompt();
  const requests = useBenchmarkRequests();
  const concurrency = useBenchmarkConcurrency();
  const apiKey = usePlaygroundApiKey();
  const { startBenchmark, appendBenchmarkResult, finishBenchmark } = useBenchmarkActions();

  const start = useCallback(async () => {
    if (activeController && !activeController.signal.aborted) return;
    const trimmedKey = apiKey.trim();
    if (!model || !trimmedKey || !prompt.trim()) return;

    const controller = new AbortController();
    activeController = controller;
    const runId = ++activeRunId;
    const isCurrentRun = () => runId === activeRunId;

    startBenchmark(requests);
    const startedAt = performance.now();

    let nextIndex = 1;
    const worker = async () => {
      while (!controller.signal.aborted && isCurrentRun()) {
        const index = nextIndex++;
        if (index > requests) return;

        const result = await benchmarkRequest({
          index,
          apiKey: trimmedKey,
          model,
          prompt,
          signal: controller.signal,
        });

        // A newer run has replaced this one: drop the stale result instead of
        // appending it to a fresh run's list.
        if (!isCurrentRun()) return;
        appendBenchmarkResult(result);
      }
    };

    await Promise.all(Array.from({ length: Math.min(concurrency, requests) }, worker));

    if (isCurrentRun()) {
      activeController = null;
      finishBenchmark(
        controller.signal.aborted ? 'aborted' : 'done',
        Math.round(performance.now() - startedAt)
      );
    }
  }, [
    model,
    prompt,
    requests,
    concurrency,
    apiKey,
    startBenchmark,
    appendBenchmarkResult,
    finishBenchmark,
  ]);

  const stop = useCallback(() => {
    activeController?.abort();
  }, []);

  return { start, stop };
}
```

- [ ] **Step 5: Register the slice and hooks in the store**

In `frontend/src/core/state/store.ts`:

1. Import `createBenchmarkSlice, type BenchmarkSlice`, add `BenchmarkSlice` to `AppStore`, and
   spread `...createBenchmarkSlice(...a)` after the tools slice.
2. Add a selector section:

```ts
// ================= BENCHMARK SELECTOR HOOKS =================

export const useBenchmarkModel = () => useAppStore((state) => state.benchmarkModel);
export const useBenchmarkPrompt = () => useAppStore((state) => state.benchmarkPrompt);
export const useBenchmarkRequests = () => useAppStore((state) => state.benchmarkRequests);
export const useBenchmarkConcurrency = () => useAppStore((state) => state.benchmarkConcurrency);
export const useBenchmarkStatus = () => useAppStore((state) => state.benchmarkStatus);
export const useBenchmarkTotal = () => useAppStore((state) => state.benchmarkTotal);
export const useBenchmarkCompleted = () => useAppStore((state) => state.benchmarkCompleted);
export const useBenchmarkResults = () => useAppStore((state) => state.benchmarkResults);
export const useBenchmarkWallClockMs = () => useAppStore((state) => state.benchmarkWallClockMs);

const BENCHMARK_ACTIONS = {
  setBenchmarkModel: (...args: Parameters<AppStore['setBenchmarkModel']>) => useAppStore.getState().setBenchmarkModel(...args),
  setBenchmarkPrompt: (...args: Parameters<AppStore['setBenchmarkPrompt']>) => useAppStore.getState().setBenchmarkPrompt(...args),
  setBenchmarkRequests: (...args: Parameters<AppStore['setBenchmarkRequests']>) => useAppStore.getState().setBenchmarkRequests(...args),
  setBenchmarkConcurrency: (...args: Parameters<AppStore['setBenchmarkConcurrency']>) => useAppStore.getState().setBenchmarkConcurrency(...args),
  startBenchmark: (...args: Parameters<AppStore['startBenchmark']>) => useAppStore.getState().startBenchmark(...args),
  appendBenchmarkResult: (...args: Parameters<AppStore['appendBenchmarkResult']>) => useAppStore.getState().appendBenchmarkResult(...args),
  finishBenchmark: (...args: Parameters<AppStore['finishBenchmark']>) => useAppStore.getState().finishBenchmark(...args),
  resetBenchmark: () => useAppStore.getState().resetBenchmark(),
};

export const useBenchmarkActions = () => BENCHMARK_ACTIONS;
```

- [ ] **Step 6: Create the form**

Create `frontend/src/modules/7-tools/benchmark/BenchmarkForm.tsx`:

```tsx
import React, { useMemo } from 'react';
import { Play, Square } from 'lucide-react';
import { Button } from '@/components/ui/Button';
import { cn } from '@/lib/utils';
import { useModelOptions } from '../shared/useModelOptions';
import {
  BENCHMARK_MAX_CONCURRENCY,
  BENCHMARK_MAX_REQUESTS,
  BENCHMARK_MIN_CONCURRENCY,
  BENCHMARK_MIN_REQUESTS,
} from '@/core/state/benchmarkSlice';
import {
  useBenchmarkActions,
  useBenchmarkConcurrency,
  useBenchmarkModel,
  useBenchmarkPrompt,
  useBenchmarkRequests,
  useBenchmarkStatus,
  usePlaygroundApiKey,
} from '@/core/state/store';

export interface BenchmarkFormProps {
  onStart: () => void;
  onStop: () => void;
}

const INPUT_CLASS =
  'px-2 py-1 rounded bg-transparent border border-white/[0.08] text-neutral-200 font-mono text-xs focus:outline-none focus:border-white/20';

/**
 * BenchmarkForm
 * Run parameters for the benchmark. Every knob is fixed except these four, so two
 * runs of the same numbers are comparable.
 *
 * Vercel React Best Practices:
 * - rerender-memo
 * - rerender-defer-reads: atomic selector hooks
 */
export const BenchmarkForm = React.memo(function BenchmarkForm({
  onStart,
  onStop,
}: BenchmarkFormProps) {
  const model = useBenchmarkModel();
  const prompt = useBenchmarkPrompt();
  const requests = useBenchmarkRequests();
  const concurrency = useBenchmarkConcurrency();
  const status = useBenchmarkStatus();
  const apiKey = usePlaygroundApiKey();
  const { models, options } = useModelOptions();
  const {
    setBenchmarkModel,
    setBenchmarkPrompt,
    setBenchmarkRequests,
    setBenchmarkConcurrency,
  } = useBenchmarkActions();

  const isRunning = status === 'running';
  const selected = options.find((o) => o.id === model);
  const upstreamName = models.find((m) => m.public_name === model)?.upstream;

  const blockReason = useMemo(() => {
    if (!apiKey.trim()) return 'Tenant API Key is required. Enter one in the Chat tool first.';
    if (options.length === 0) return 'No model or combo is available on this gateway.';
    if (!selected?.available) {
      return selected?.kind === 'combo'
        ? `Combo "${model}" is unavailable because all member models or upstreams are offline.`
        : `Model "${model}" is unavailable because upstream "${upstreamName || 'unknown'}" is closed.`;
    }
    if (!prompt.trim()) return 'Prompt is empty.';
    return null;
  }, [apiKey, options.length, selected, model, upstreamName, prompt]);

  return (
    <div className="p-3 rounded-xl bg-transparent border border-white/[0.06] space-y-3 text-xs">
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3">
        <label className="space-y-1">
          <span className="text-[11px] text-neutral-500">Model</span>
          <select
            value={model}
            onChange={(e) => setBenchmarkModel(e.target.value)}
            disabled={isRunning}
            className={cn(INPUT_CLASS, 'w-full max-w-full truncate')}
          >
            {options.map((o) => (
              <option key={o.id} value={o.id} className="bg-[#0a0d14] text-neutral-200">
                {o.id}
                {o.kind === 'combo' ? ' [combo]' : ''}
                {o.available ? '' : ' [CLOSED]'}
              </option>
            ))}
          </select>
        </label>

        <label className="space-y-1">
          <span className="text-[11px] text-neutral-500">Requests (1–{BENCHMARK_MAX_REQUESTS})</span>
          <input
            type="number"
            min={BENCHMARK_MIN_REQUESTS}
            max={BENCHMARK_MAX_REQUESTS}
            value={requests}
            disabled={isRunning}
            onChange={(e) => setBenchmarkRequests(Number(e.target.value))}
            className={cn(INPUT_CLASS, 'w-full')}
          />
        </label>

        <label className="space-y-1">
          <span className="text-[11px] text-neutral-500">
            Concurrency (1–{BENCHMARK_MAX_CONCURRENCY})
          </span>
          <input
            type="number"
            min={BENCHMARK_MIN_CONCURRENCY}
            max={BENCHMARK_MAX_CONCURRENCY}
            value={concurrency}
            disabled={isRunning}
            onChange={(e) => setBenchmarkConcurrency(Number(e.target.value))}
            className={cn(INPUT_CLASS, 'w-full')}
          />
        </label>

        <div className="flex items-end">
          {isRunning ? (
            <Button
              variant="minimal"
              size="md"
              onClick={onStop}
              className="text-rose-400 hover:text-rose-300"
              leftIcon={<Square className="w-3.5 h-3.5 fill-current" />}
            >
              Stop
            </Button>
          ) : (
            <Button
              variant="minimal"
              size="md"
              onClick={onStart}
              disabled={blockReason !== null}
              title={blockReason ?? 'Run the benchmark'}
              rightIcon={
                <Play className="w-3.5 h-3.5 text-neutral-400 group-hover:text-white transition-colors" />
              }
            >
              Run
            </Button>
          )}
        </div>
      </div>

      <label className="space-y-1 block">
        <span className="text-[11px] text-neutral-500">Prompt</span>
        <textarea
          value={prompt}
          onChange={(e) => setBenchmarkPrompt(e.target.value)}
          disabled={isRunning}
          rows={2}
          className={cn(INPUT_CLASS, 'w-full resize-none')}
        />
      </label>

      <div className="flex flex-wrap items-center justify-between gap-2 text-[10px] text-neutral-500">
        <span>Fixed per request: stream: true · temperature: 0 · max_tokens: 128</span>
        <span>A run sends real requests upstream and keeps running while you switch tools.</span>
      </div>

      {blockReason ? <p className="text-[11px] text-amber-300/90">{blockReason}</p> : null}
    </div>
  );
});
```

- [ ] **Step 7: Create the tool**

Create `frontend/src/modules/7-tools/benchmark/BenchmarkTool.tsx`:

```tsx
import React, { useEffect, useMemo } from 'react';
import { BenchmarkForm } from './BenchmarkForm';
import { useBenchmarkRun } from './useBenchmarkRun';
import { summarize } from './benchmarkStats';
import { MetricPill } from '../shared/MetricPill';
import { useModelOptions } from '../shared/useModelOptions';
import {
  useBenchmarkActions,
  useBenchmarkCompleted,
  useBenchmarkModel,
  useBenchmarkResults,
  useBenchmarkStatus,
  useBenchmarkTotal,
  useBenchmarkWallClockMs,
} from '@/core/state/store';
import type { BenchmarkRunState } from '@/core/state/benchmarkSlice';

const STATUS_LABEL: Record<BenchmarkRunState, string> = {
  idle: 'Idle',
  running: 'Running',
  done: 'Completed',
  aborted: 'Stopped',
};

/**
 * BenchmarkTool
 * Fires N requests at concurrency C through the gateway and reports the same
 * latency/token metrics the chat waterfall shows, aggregated over the run.
 *
 * Vercel React Best Practices:
 * - rerender-memo
 * - rerender-defer-reads: atomic selector hooks
 * - rendering-conditional-render
 */
export default React.memo(function BenchmarkTool() {
  const model = useBenchmarkModel();
  const results = useBenchmarkResults();
  const status = useBenchmarkStatus();
  const total = useBenchmarkTotal();
  const completed = useBenchmarkCompleted();
  const wallClockMs = useBenchmarkWallClockMs();
  const { options, firstAvailableId } = useModelOptions();
  const { setBenchmarkModel } = useBenchmarkActions();
  const { start, stop } = useBenchmarkRun();

  // Same contract as the chat tool: normalise the selection when the catalog
  // changes, without writing the store during render.
  useEffect(() => {
    if (!firstAvailableId) return;
    if (!model || !options.some((o) => o.id === model)) {
      setBenchmarkModel(firstAvailableId);
    }
  }, [model, options, firstAvailableId, setBenchmarkModel]);

  // Derived, never stored: the cards must not be able to drift from the results.
  const summary = useMemo(() => summarize(results), [results]);

  return (
    <div className="space-y-4 font-mono">
      <BenchmarkForm onStart={start} onStop={stop} />

      <div className="flex items-center justify-between text-xs">
        <span className="text-neutral-500">{STATUS_LABEL[status]}</span>
        <span aria-live="polite" className="text-neutral-400 tabular-nums">
          {total > 0 ? `${completed}/${total}` : '—'}
        </span>
      </div>

      <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-2 text-center">
        <MetricPill
          label="Wall clock"
          value={wallClockMs !== null ? `${(wallClockMs / 1000).toFixed(2)}s` : '—'}
        />
        <MetricPill
          label="TTFT p50"
          value={summary.ttftP50 !== null ? `${summary.ttftP50}ms` : '—'}
        />
        <MetricPill
          label="TTFT p95"
          value={summary.ttftP95 !== null ? `${summary.ttftP95}ms` : '—'}
        />
        <MetricPill
          label="Aggregate TPS"
          value={summary.aggregateTps !== null ? `${summary.aggregateTps} t/s` : '—'}
        />
        <MetricPill
          label="Error rate"
          value={summary.errorRate !== null ? `${Math.round(summary.errorRate * 100)}%` : '—'}
        />
        <MetricPill label="Tokens" value={summary.totalTokens} />
      </div>
    </div>
  );
});
```

`BenchmarkResults` is imported and rendered here in Task 7.

- [ ] **Step 8: Register the tool**

In `frontend/src/modules/7-tools/tools.tsx`:

1. Add `Gauge` to the lucide import: `import { Gauge, MessageSquare } from 'lucide-react';`
2. Add the lazy component after `ChatTool`:
   `const BenchmarkTool = React.lazy(() => import('./benchmark/BenchmarkTool'));`
3. Append the entry after `chat`:

```tsx
  benchmark: {
    title: 'Benchmark',
    description: 'Run N concurrent requests and compare gateway latency',
    order: 2,
    icon: Gauge,
    component: BenchmarkTool,
    preload: () => import('./benchmark/BenchmarkTool'),
  },
```

- [ ] **Step 9: Typecheck, build, and run a benchmark in the browser**

```bash
cd frontend && npm run typecheck && npm run build
```

In the browser, open Tools → **Benchmark**, leave the defaults (10 requests, concurrency 2),
press **Run**. Expected: status flips to `Running`, the counter climbs `1/10` … `10/10`, then
`Completed`; cards show a wall clock around 2.0 s, TTFT p50 ≈ 120-160 ms, aggregate TPS ≈ 30,
error rate `0%`, tokens `120` (12 content deltas × 10 requests). Cross-check the mock:

```bash
curl -s http://127.0.0.1:19099/_mock/seen
```

Expected: the request count grew by exactly 10.

- [ ] **Step 10: Commit**

```bash
git add frontend/src/core/state frontend/src/modules/7-tools
git commit -m "feat(frontend): add the benchmark tool with a concurrent run engine"
```

---

### Task 7: Benchmark results bars and table

**Files:**
- Create: `frontend/src/modules/7-tools/benchmark/BenchmarkResults.tsx`
- Modify: `frontend/src/modules/7-tools/benchmark/BenchmarkTool.tsx`

**Interfaces:**
- Consumes: `BenchmarkRequestResult[]` from the slice.
- Produces: `BenchmarkResults({ results })`, rendered by `BenchmarkTool`.

- [ ] **Step 1: Create the results view**

Create `frontend/src/modules/7-tools/benchmark/BenchmarkResults.tsx`:

```tsx
import React, { useMemo } from 'react';
import { cn } from '@/lib/utils';
import type { BenchmarkRequestResult } from './benchmarkRunner';

export interface BenchmarkResultsProps {
  results: BenchmarkRequestResult[];
}

const STATUS_CLASS: Record<BenchmarkRequestResult['status'], string> = {
  ok: 'text-emerald-300 border-emerald-400/30',
  error: 'text-rose-300 border-rose-400/30',
  aborted: 'text-neutral-400 border-white/[0.08]',
};

/**
 * BenchmarkResults
 * One row per request: a two-segment latency bar (TTFT in the waterfall's cyan
 * accent, generation in the neutral tone) over the raw numbers, all on one shared
 * time scale so bars are comparable.
 *
 * Vercel React Best Practices:
 * - rerender-memo
 * - rendering-conditional-render
 */
export const BenchmarkResults = React.memo(function BenchmarkResults({
  results,
}: BenchmarkResultsProps) {
  // The slowest request sets the scale; 1 avoids a divide by zero before the first
  // result lands.
  const maxTotalMs = useMemo(
    () => results.reduce((max, r) => Math.max(max, r.totalMs), 0) || 1,
    [results]
  );

  if (results.length === 0) {
    return (
      <div className="p-6 rounded-xl bg-transparent border border-white/[0.06] text-center text-neutral-500 text-[11px] font-mono">
        No run yet. Configure the benchmark above and press Run.
      </div>
    );
  }

  return (
    <div className="p-4 rounded-xl bg-transparent border border-white/[0.06] flex flex-col font-mono text-xs space-y-3">
      <div className="flex items-center justify-between pb-2 border-b border-white/[0.04]">
        <h4 className="text-xs uppercase tracking-wider text-neutral-300 font-medium">
          Per-request latency
        </h4>
        <div className="flex items-center gap-3 text-[10px] text-neutral-500">
          <span className="flex items-center gap-1.5">
            <span className="w-2 h-2 rounded-sm bg-cyan-400/80" /> TTFT
          </span>
          <span className="flex items-center gap-1.5">
            <span className="w-2 h-2 rounded-sm bg-white/[0.35]" /> Generation
          </span>
        </div>
      </div>

      <div className="space-y-1">
        {results.map((r) => {
          const ttft = r.ttftMs ?? 0;
          const generation = Math.max(0, r.totalMs - ttft);
          const isOk = r.status === 'ok';
          return (
            <div
              key={r.index}
              className="flex items-center gap-2 text-[11px] hover:bg-white/[0.015] p-0.5 rounded"
            >
              <span className="w-8 text-neutral-500 text-right tabular-nums text-[10px]">
                #{r.index}
              </span>
              <div className="flex-1 h-2 rounded-sm bg-white/[0.04] overflow-hidden flex">
                <div
                  className={cn('h-full', isOk ? 'bg-cyan-400/80' : 'bg-rose-400/70')}
                  style={{ width: `${(ttft / maxTotalMs) * 100}%` }}
                />
                <div
                  className={cn('h-full', isOk ? 'bg-white/[0.35]' : 'bg-rose-400/40')}
                  style={{ width: `${(generation / maxTotalMs) * 100}%` }}
                />
              </div>
              <span className="w-16 text-right tabular-nums text-[10px] text-neutral-300">
                {r.totalMs}ms
              </span>
            </div>
          );
        })}
      </div>

      <div className="overflow-x-auto pt-1">
        <table className="w-full text-[11px]">
          <thead>
            <tr className="text-neutral-500 text-[10px] uppercase tracking-wider">
              <th scope="col" className="text-left font-normal py-1">
                #
              </th>
              <th scope="col" className="text-left font-normal py-1">
                Status
              </th>
              <th scope="col" className="text-right font-normal py-1">
                TTFT
              </th>
              <th scope="col" className="text-right font-normal py-1">
                Tokens
              </th>
              <th scope="col" className="text-right font-normal py-1">
                Duration
              </th>
              <th scope="col" className="text-right font-normal py-1">
                TPS
              </th>
            </tr>
          </thead>
          <tbody>
            {results.map((r) => (
              <tr
                key={r.index}
                className={cn(
                  'border-t border-white/[0.04]',
                  r.status === 'aborted' && 'opacity-60'
                )}
              >
                <td className="py-1 text-neutral-500 tabular-nums">{r.index}</td>
                <td className="py-1">
                  <span
                    className={cn(
                      'px-1.5 py-0.5 rounded text-[10px] border',
                      STATUS_CLASS[r.status]
                    )}
                  >
                    {r.status}
                  </span>
                  {r.errorMessage ? (
                    <span className="ml-2 text-neutral-500">{r.errorMessage}</span>
                  ) : null}
                </td>
                <td className="py-1 text-right tabular-nums text-neutral-300">
                  {r.ttftMs !== null ? `${r.ttftMs}ms` : '—'}
                </td>
                <td className="py-1 text-right tabular-nums text-neutral-300">{r.tokens}</td>
                <td className="py-1 text-right tabular-nums text-neutral-300">{r.totalMs}ms</td>
                <td className="py-1 text-right tabular-nums text-neutral-300">
                  {r.tps !== null ? r.tps : '—'}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
});
```

- [ ] **Step 2: Render it from the tool**

In `frontend/src/modules/7-tools/benchmark/BenchmarkTool.tsx`, add the import

```tsx
import { BenchmarkResults } from './BenchmarkResults';
```

and render it as the last child of the root `<div>`, right after the metric card grid:

```tsx
      <BenchmarkResults results={results} />
```

- [ ] **Step 3: Typecheck and verify the visuals**

```bash
cd frontend && npm run typecheck && npm run build
```

In the browser: before any run the results area shows the placeholder; after a run, ten bar rows
whose TTFT segments are all similar and short against the generation segments, and a table whose
`#` column matches the bar rows. Force a slow upstream to sanity-check the scale: temporarily run
25 requests at concurrency 5 and confirm bars stay proportional (the longest bar fills the row).

- [ ] **Step 4: Commit**

```bash
git add frontend/src/modules/7-tools
git commit -m "feat(frontend): add benchmark latency bars and the per-request table"
```

---

### Task 8: Full verification sweep

**Files:** none (verification only; fix anything found, in the file it belongs to).

- [ ] **Step 1: Static gates**

```bash
cd frontend && npm run typecheck && npm run build
git status --short   # only intended source files; dist/ and configs/ are ignored
```

- [ ] **Step 2: Restart the stack from a clean state**

Stop the `go run` process, then restart all three (mock upstream, gateway, Vite) so the server
picks up an empty analytics dir. `go run ./cmd/firefly -config-dir configs/e2e-playground -addr
127.0.0.1:8080` must print a clean boot (no config warnings about `7-playground`).

- [ ] **Step 3: Browser sweep (agent-browser)**

Against `http://localhost:3000`, logged in with `12345678`:

| # | Check | Expected |
| :--- | :--- | :--- |
| 1 | Header tab 7 label; press `7` | reads **Tools**; the tab activates; hotkeys 1-8 unchanged |
| 2 | Sidebar | lists 2 tools; Chat active by default with `aria-current="true"` |
| 3 | Chat streaming | prompt streams tokens; waterfall pills update; raw inspector fills |
| 4 | Telemetry collapse | Hide → chat is full width; Show → panel returns with data intact |
| 5 | Switch Tool → Benchmark → Tool | chat messages and waterfall survive; benchmark summary survives |
| 6 | Benchmark 5 requests @ 2 | progress reaches 5/5, status `Completed`; `curl /_mock/seen` grew by exactly 5 |
| 7 | TTFT plausibility | TTFT p50 within ~100-200 ms (mock delays the first token by 120 ms) |
| 8 | Stop mid-run | status `Stopped`; in-flight rows `aborted`; queued indices never dispatched (`/_mock/seen` lower than the requested count) |
| 9 | `curl 'http://127.0.0.1:19099/_mock/force?status=500'` then run 3 | error rate `100%`; each row shows the gateway's upstream-error message; run status `Completed` |
| 10 | `curl http://127.0.0.1:19099/_mock/reset` then run 1 | error rate back to `0%` |
| 11 | Other modules | tabs 1-6 and 8 render as before; Settings tab-guard text names **Tools** |
| 12 | Console | no React key/act warnings, no unhandled rejections released by a tool switch |

- [ ] **Step 4: Report and commit any fixes**

If a check fails, fix it in the owning file and re-run Steps 1-3 before committing:

```bash
git add frontend/src
git commit -m "fix(frontend): close the tools-page findings from the verification sweep"
```

- [ ] **Step 5: Confirm the spec's follow-ups are still accurate**

Re-read `docs/superpowers/specs/2026-09-22-tools-page-design.md` "Follow-ups" and the
"Documentation to update" list, and confirm nothing in the shipped code contradicts them. The
`CHANGELOG.md` entry is written at release time, per the project convention — do not add one
here.

---

## Self-review notes

- **Spec coverage:** module rename + registry (Tasks 1-2), sidebar/sub-navigation (Task 2),
  `MetricPill` (Task 3), collapse behaviour (Task 4), `useModelOptions` (Task 5), state slices
  (Tasks 2, 4, 6), benchmark form/execution/metrics/summary/visualisation (Tasks 6-7), edge-case
  table (Tasks 6-7 implement, Task 8 verifies), docs list (Task 2), verification (Tasks 1-8).
  The spec's "no cross-reload persistence" and "no `playground*` rename" non-goals are honoured —
  no `persist` middleware and no `playground*` identifier is renamed anywhere in this plan.
- **Type consistency:** `BenchmarkRequestStatus` (runner, per-request) is distinct from
  `BenchmarkRunState` (slice, per-run); `finishBenchmark(status, wallClockMs)` matches the
  spec's updated state table; `ToolId` starts with only `chat` and gains `benchmark` in Task 6,
  so every intermediate commit typechecks.
- **Two intentional deviations from the spec, both recorded there:** the run's wall clock is a
  slice field rather than a derivation (it cannot be recovered from overlapping per-request
  durations), and the run survives tool switching via a module-scope controller, which is why
  `useBenchmarkRun` has no unmount effect at all.
- **Two expectations corrected at verification time (2026-09-22):** Task 6's check said
  `aggregate TPS ≈ 300`, an arithmetic slip — 120 tokens over ~2.0 s at concurrency 2 caps the
  aggregate at ≤60 t/s and the sandbox measures ≈30 t/s, matching the ~30 t/s the chat reports
  against the same mock. Task 8 row 9's `each row shows forced 500` is unreachable: the gateway
  retries internally before surfacing a failure, so one client request emits several breaker
  failures and crosses the 5-failure threshold (the first row carries the raw upstream message,
  later rows the breaker's `circuit open`).
