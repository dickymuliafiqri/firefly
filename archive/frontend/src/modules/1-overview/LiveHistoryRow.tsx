import React, { useState, useEffect } from 'react';
import type { LiveConnectionLog } from '@/services/schema';
import { cn } from '@/lib/utils';

export interface LiveHistoryRowProps {
  log: LiveConnectionLog;
  onInspect?: (log: LiveConnectionLog) => void;
}

interface LiveCountUpTimerProps {
  startTime: number;
  durationMs: number;
  isInFlight: boolean;
}

/**
 * Isolated countup timer for in-flight requests.
 * Ticks only when the request is in-flight, preventing unnecessary re-renders
 * on completed rows (adheres to Vercel React Best Practice: rerender-memo).
 */
const LiveCountUpTimer = React.memo(function LiveCountUpTimer({
  startTime,
  durationMs,
  isInFlight,
}: LiveCountUpTimerProps) {
  const [elapsed, setElapsed] = useState(() =>
    isInFlight ? Math.max(0, Date.now() - startTime) : durationMs
  );

  useEffect(() => {
    if (!isInFlight) {
      setElapsed(durationMs);
      return;
    }

    const update = () => {
      setElapsed(Math.max(0, Date.now() - startTime));
    };

    update();
    const interval = setInterval(update, 50);

    return () => clearInterval(interval);
  }, [isInFlight, startTime, durationMs]);

  const val = isInFlight ? elapsed : durationMs;
  const formatted =
    val >= 1000 ? `${(val / 1000).toFixed(1)}s` : `${Math.round(val)}ms`;

  return (
    <span
      className={cn(
        'tabular-nums transition-colors',
        isInFlight ? 'text-cyan-400 font-medium' : 'text-neutral-500'
      )}
    >
      {formatted}
    </span>
  );
});

export const LiveHistoryRow = React.memo(function LiveHistoryRow({
  log,
  onInspect,
}: LiveHistoryRowProps) {
  const isInFlight = log.status === 0;
  const isOk = log.status >= 200 && log.status < 300;
  const is4xx = log.status >= 400 && log.status < 500;

  const timeStr = new Date(log.timestamp).toTimeString().split(' ')[0];

  const statusText = isOk ? '200 OK' : is4xx ? `${log.status} REQ` : `${log.status} ERR`;

  return (
    <div
      onClick={() => onInspect?.(log)}
      className="flex items-baseline justify-between py-1 text-xs font-mono text-neutral-300 transition-opacity duration-200 cursor-pointer select-none hover:bg-white/[0.03] px-1 rounded"
    >
      <div className="flex items-baseline gap-2.5 truncate">
        <span className="text-neutral-500 text-[11px] tabular-nums">{timeStr}</span>
        <span className="text-neutral-200 font-medium text-[12px]">{log.upstream}</span>
        <span className="text-neutral-500 text-[11px] truncate">{log.model}</span>
        {log.keyRef ? (
          <span
            className="text-neutral-600 text-[10px] truncate"
            title={`Credential used: ${log.keyRef}`}
          >
            key:{log.keyRef}
          </span>
        ) : null}
      </div>
      <div className="flex items-baseline gap-2 flex-shrink-0 text-[11px] tabular-nums">
        {isInFlight ? (
          <span className="flex items-center gap-1.5 text-cyan-400/90 font-medium">
            <span className="w-1.5 h-1.5 rounded-full bg-cyan-400 shadow-[0_0_6px_rgba(34,211,238,0.8)] animate-pulse" />
            <span>{log.stream ? 'STREAMING' : 'ACTIVE'}</span>
          </span>
        ) : (
          <span
            className={cn(
              isOk ? 'text-emerald-400/90' : is4xx ? 'text-amber-400/90' : 'text-rose-400/90'
            )}
          >
            {statusText}
          </span>
        )}
        <LiveCountUpTimer
          startTime={log.timestamp}
          durationMs={log.durationMs}
          isInFlight={isInFlight}
        />
      </div>
    </div>
  );
});
