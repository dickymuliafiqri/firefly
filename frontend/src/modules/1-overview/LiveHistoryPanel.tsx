import React from 'react';
import { useRecentLogs } from '@/core/state/store';
import type { LiveConnectionLog } from '@/services/schema';
import { LiveHistoryRow } from './LiveHistoryRow';

export interface LiveHistoryPanelProps {
  onInspectLog?: (log: LiveConnectionLog) => void;
}

export const LiveHistoryPanel = React.memo(function LiveHistoryPanel({
  onInspectLog,
}: LiveHistoryPanelProps) {
  const logs = useRecentLogs();

  return (
    <div
      id="historyContainer"
      className="w-full lg:flex-1 h-[560px] flex flex-col justify-start select-none pt-1 bg-transparent border-0 shadow-none"
    >
      {/* History Header */}
      <div className="pb-3 text-xs tracking-wider uppercase font-mono text-neutral-400">
        <span className="font-medium text-neutral-300 section-title">History</span>
      </div>

      {/* Real connection logs list */}
      <div
        id="historyList"
        className="flex-1 overflow-y-auto font-mono text-xs space-y-1.5 custom-scrollbar"
        style={{ background: 'transparent' }}
      >
        {logs.length > 0 ? (
          logs.slice(0, 15).map((log) => (
            <LiveHistoryRow key={log.id} log={log} onInspect={onInspectLog} />
          ))
        ) : (
          <div className="h-full flex flex-col items-center justify-center text-center p-6 text-neutral-500 font-mono text-xs">
            <span className="opacity-60">Awaiting inbound requests...</span>
          </div>
        )}
      </div>
    </div>
  );
});
