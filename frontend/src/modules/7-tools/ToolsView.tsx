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
