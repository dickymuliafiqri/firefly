import { useRef, useCallback, useMemo, useEffect } from 'react';
import { FireflyCanvas } from '@/core/canvas/FireflyCanvas';
import type { FireflyCanvasHandle, FireflyUpstream } from '@/core/canvas/types';
import { LiveHistoryPanel } from './LiveHistoryPanel';
import { useUpstreams, useUpstreamBreakers, useRecentLogs, useTelemetryStats, useIsAuthenticated, useStoreActions } from '@/core/state/store';
import type { LiveConnectionLog } from '@/services/schema';
import { AnimatedCountUp } from '@/components/ui/AnimatedCountUp';
import { formatNumber } from '@/lib/format';
import { useReducedMotion } from '@/hooks/useReducedMotion';

/** Counters here show whole units, so the shared formatter is floored first. */
const formatCount = (n: number) => formatNumber(Math.floor(n));

export default function OverviewView() {
  const upstreams = useUpstreams();
  const upstreamBreakers = useUpstreamBreakers();
  const recentLogs = useRecentLogs();
  const stats = useTelemetryStats();
  const isAuthenticated = useIsAuthenticated();
  const { openDrawer, openModal, toggleUpstreamBreaker, addToast } = useStoreActions();

  const canvasRef = useRef<FireflyCanvasHandle>(null);
  const lastLogIdRef = useRef<string | null>(null);
  const reducedMotion = useReducedMotion();

  // Base upstream definitions strictly from real backend configuration (zero dummy fallbacks)
  const fireflyUpstreams: FireflyUpstream[] = useMemo(() => {
    return upstreams.map((u) => {
      const breakerState = upstreamBreakers[u.name] || (u.enabled === false ? 'OPEN' : 'CLOSED');
      const isConnected = u.enabled !== false && breakerState === 'CLOSED';
      const inflightCount = recentLogs.filter(
        (l) => l.status === 0 && l.upstream?.toLowerCase() === u.name.toLowerCase()
      ).length;
      return {
        name: u.name,
        protocol: u.protocol || 'openai',
        base_url: u.base_url || (u.base_urls && u.base_urls[0]) || 'https://api.openai.com/v1',
        connected: isConnected,
        breaker_state: breakerState,
        inflight: inflightCount,
      };
    });
  }, [upstreams, upstreamBreakers, recentLogs]);

  // Pulse firefly whenever a new real request log arrives
  useEffect(() => {
    if (recentLogs.length > 0) {
      if (lastLogIdRef.current === null) {
        lastLogIdRef.current = recentLogs[0].id;
        if (Date.now() - recentLogs[0].timestamp < 2500 && recentLogs[0].upstream) {
          canvasRef.current?.triggerBurst(recentLogs[0].upstream);
        }
        return;
      }

      const prevId = lastLogIdRef.current;
      const newItems: LiveConnectionLog[] = [];
      for (const log of recentLogs) {
        if (log.id === prevId) break;
        newItems.push(log);
      }

      lastLogIdRef.current = recentLogs[0].id;

      for (const log of newItems) {
        if (log.upstream) {
          canvasRef.current?.triggerBurst(log.upstream);
        }
      }
    }
  }, [recentLogs]);

  // Toggle upstream state in sync with Upstreams page (protected: requires admin auth)
  const handleToggleUpstream = useCallback(
    (name: string) => {
      if (!isAuthenticated) {
        addToast({
          title: 'Authentication Required',
          message: 'Please log in with dashboard credentials to manage upstream circuits.',
          type: 'warning',
        });
        openModal('login');
        return;
      }

      const u = upstreams.find((item) => item.name === name);
      const defaultState = u?.enabled === false ? 'OPEN' : 'CLOSED';
      const currentState = upstreamBreakers[name] || defaultState;
      const nextState = currentState === 'OPEN' ? 'CLOSED' : 'OPEN';
      toggleUpstreamBreaker(name);

      if (nextState === 'CLOSED') {
        canvasRef.current?.triggerBurst(name);
        addToast({
          title: name,
          message: 'Upstream activated. Network active.',
          type: 'success',
        });
      } else {
        addToast({
          title: name,
          message: 'Upstream disabled. Network dormant.',
          type: 'info',
        });
      }
    },
    [isAuthenticated, upstreams, upstreamBreakers, toggleUpstreamBreaker, addToast, openModal]
  );

  const handleInspectLog = useCallback(
    (log: LiveConnectionLog) => {
      openDrawer('log-detail', log);
    },
    [openDrawer]
  );

  return (
    <div id="tab-overview" className="tab-pane flex flex-col justify-between min-h-[calc(100dvh-140px)]">
      {/* 70% Fireflies Viewport & 30% Connection History */}
      <div className="flex flex-col lg:flex-row items-stretch gap-6 lg:gap-8 w-full">
        {/* Live Fireflies Sky Viewport (70% Screen Width on desktop, full width on mobile) */}
        <div
          className="relative w-full lg:w-[calc(70%-1.5rem)] overflow-hidden select-none flex-shrink-0"
          id="fireflyContainer"
        >
          <FireflyCanvas
            ref={canvasRef}
            upstreams={fireflyUpstreams}
            onToggleUpstream={handleToggleUpstream}
            reducedMotion={reducedMotion}
          />

          {fireflyUpstreams.length === 0 ? (
            <div className="absolute inset-0 flex items-center justify-center pointer-events-none text-neutral-500 font-mono text-xs select-none">
              <span className="opacity-50">No active upstreams configured</span>
            </div>
          ) : null}
        </div>

        {/* Minimalist Divider: Vertical on lg, horizontal subtle gradient on mobile */}
        <div
          className="hidden lg:block w-px h-[560px] bg-gradient-to-b from-transparent via-white/10 to-transparent flex-shrink-0"
          aria-hidden="true"
        />
        <div
          className="block lg:hidden w-full h-px bg-gradient-to-r from-transparent via-white/10 to-transparent my-1 flex-shrink-0"
          aria-hidden="true"
        />

        {/* Connection History (Right Side on desktop, below on mobile) */}
        <LiveHistoryPanel onInspectLog={handleInspectLog} />
      </div>

      {/* Minimalist Subtle Telemetry Statistics (Responsive Spacing & Grid) */}
      <div className="w-full mt-10 sm:mt-20 lg:mt-28 pb-4 select-none">
        <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-5 gap-4 sm:gap-6 lg:gap-8">
          {/* 1. Token Input */}
          <div className="flex flex-col">
            <AnimatedCountUp
              id="statTokenInput"
              className="text-xs sm:text-sm font-mono text-neutral-200 tabular-nums stat-value"
              value={stats.inputTokens}
              formatter={formatCount}
            />
            <span className="text-[10px] sm:text-[11px] font-mono text-neutral-400 uppercase tracking-wider mt-0.5 stat-label">
              Token Input
            </span>
          </div>

          {/* 2. Token Output */}
          <div className="flex flex-col">
            <AnimatedCountUp
              id="statTokenOutput"
              className="text-xs sm:text-sm font-mono text-neutral-200 tabular-nums stat-value"
              value={stats.outputTokens}
              formatter={formatCount}
            />
            <span className="text-[10px] sm:text-[11px] font-mono text-neutral-400 uppercase tracking-wider mt-0.5 stat-label">
              Token Output
            </span>
          </div>

          {/* 3. Total Token */}
          <div className="flex flex-col">
            <AnimatedCountUp
              id="statTotalToken"
              className="text-xs sm:text-sm font-mono text-neutral-200 tabular-nums stat-value"
              value={stats.totalTokens}
              formatter={formatCount}
            />
            <span className="text-[10px] sm:text-[11px] font-mono text-neutral-400 uppercase tracking-wider mt-0.5 stat-label">
              Total Token
            </span>
          </div>

          {/* 4. Total Request */}
          <div className="flex flex-col">
            <AnimatedCountUp
              id="statTotalRequest"
              className="text-xs sm:text-sm font-mono text-neutral-200 tabular-nums stat-value"
              value={stats.totalRequests}
              formatter={formatCount}
            />
            <span className="text-[10px] sm:text-[11px] font-mono text-neutral-400 uppercase tracking-wider mt-0.5 stat-label">
              Total Request
            </span>
          </div>

          {/* 5. Estimasi Harga */}
          <div className="flex flex-col">
            <AnimatedCountUp
              id="statCost"
              className="text-xs sm:text-sm font-mono text-neutral-200 tabular-nums stat-value"
              value={stats.estimatedCostUsd}
              prefix="$"
              decimals={2}
            />
            <span className="text-[10px] sm:text-[11px] font-mono text-neutral-400 uppercase tracking-wider mt-0.5 stat-label">
              Estimated Price
            </span>
          </div>
        </div>
      </div>
    </div>
  );
}
