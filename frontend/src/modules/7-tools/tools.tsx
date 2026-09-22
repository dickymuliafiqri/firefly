import React from 'react';
import { Gauge, MessageSquare } from 'lucide-react';
import type { FireflyModuleDefinition } from '../types';

export type ToolDefinition = FireflyModuleDefinition;

const ChatTool = React.lazy(() => import('./chat/ChatTool'));
const BenchmarkTool = React.lazy(() => import('./benchmark/BenchmarkTool'));

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
  benchmark: {
    title: 'Benchmark',
    description: 'Run N concurrent requests and compare gateway latency',
    order: 2,
    icon: Gauge,
    component: BenchmarkTool,
    preload: () => import('./benchmark/BenchmarkTool'),
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
