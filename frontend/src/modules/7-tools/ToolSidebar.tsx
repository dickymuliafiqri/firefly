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
