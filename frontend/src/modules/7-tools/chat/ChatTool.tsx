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
