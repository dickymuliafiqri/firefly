import React from 'react';
import { ChatWindow } from './ChatWindow';
import { TokenStreamWaterfall } from './TokenStreamWaterfall';
import { StreamInspector } from './StreamInspector';
import {
  usePlaygroundIsGenerating,
  usePlaygroundTtftMs,
  usePlaygroundTotalDurationMs,
  usePlaygroundTps,
  usePlaygroundTimings,
  usePlaygroundRawPackets,
} from '@/core/state/store';

/**
 * PlaygroundView
 * Interactive LLM test playground with real-time SSE waterfall telemetry.
 * All chat messages, timings, and inspector state are persisted in the Zustand store,
 * preserving state across tab navigation.
 *
 * Vercel React Best Practices:
 * - rerender-memo
 * - rerender-defer-reads: uses atomic selector hooks
 */
export default React.memo(function PlaygroundView() {
  const isStreaming = usePlaygroundIsGenerating();
  const ttftMs = usePlaygroundTtftMs();
  const totalDurationMs = usePlaygroundTotalDurationMs();
  const tps = usePlaygroundTps();
  const timings = usePlaygroundTimings();
  const rawPackets = usePlaygroundRawPackets();

  return (
    <div className="space-y-6 animate-in fade-in duration-300">
      {/* 2-Column Layout */}
      <div className="grid grid-cols-1 lg:grid-cols-12 gap-6">
        {/* Left 7 cols: Interactive Chat Window */}
        <div className="lg:col-span-7">
          <ChatWindow />
        </div>

        {/* Right 5 cols: Waterfall Telemetry & SSE Inspector */}
        <div className="lg:col-span-5 space-y-6">
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
