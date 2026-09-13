import React, { useTransition } from 'react';
import { useRecentLogs, useAdminToken, useIsAuthenticated, useStoreActions, useAppStore } from '@/core/state/store';
import type { LiveConnectionLog } from '@/services/schema';
import { LiveHistoryRow } from './LiveHistoryRow';
import { deleteHistoryApi } from '@/services/api';
import { Trash2 } from 'lucide-react';

export interface LiveHistoryPanelProps {
  onInspectLog?: (log: LiveConnectionLog) => void;
}

export const LiveHistoryPanel = React.memo(function LiveHistoryPanel({
  onInspectLog,
}: LiveHistoryPanelProps) {
  const logs = useRecentLogs();
  const adminToken = useAdminToken();
  const isAuthenticated = useIsAuthenticated();
  const { addToast, openModal } = useStoreActions();
  const clearLogs = useAppStore((state) => state.clearLogs);
  const [isClearing, startClearTransition] = useTransition();

  const handleClearHistory = () => {
    if (!isAuthenticated) {
      addToast({
        title: 'Authentication Required',
        message: 'Please log in with dashboard credentials to clear request history.',
        type: 'warning',
      });
      openModal('login');
      return;
    }
    if (isClearing) return;

    startClearTransition(async () => {
      try {
        await deleteHistoryApi(adminToken);
        clearLogs();
        addToast({
          title: 'History Cleared',
          message: 'Request logs successfully cleared from backend.',
          type: 'info',
        });
      } catch (err) {
        addToast({
          title: 'Clear Failed',
          message: err instanceof Error ? err.message : 'Failed to clear history',
          type: 'error',
        });
      }
    });
  };

  return (
    <div
      id="historyContainer"
      className="w-full lg:flex-1 h-[360px] sm:h-[420px] lg:h-[560px] flex flex-col justify-start select-none pt-1 bg-transparent border-0 shadow-none"
    >
      {/* History Header */}
      <div className="pb-3 text-xs tracking-wider uppercase font-mono text-neutral-400 flex items-center justify-between">
        <span className="font-medium text-neutral-300 section-title">History</span>
        {logs.length > 0 ? (
          <button
            type="button"
            onClick={handleClearHistory}
            disabled={isClearing}
            className="flex items-center gap-1 text-[10px] text-neutral-500 hover:text-neutral-300 transition-colors uppercase tracking-wider cursor-pointer disabled:opacity-50"
            title="Clear request history from backend"
          >
            <Trash2 className="w-3 h-3" />
            <span>{isClearing ? 'Clearing...' : 'Clear'}</span>
          </button>
        ) : null}
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
