import React, { useRef, useImperativeHandle, useMemo } from 'react';
import type { FireflyCanvasProps } from './types';
import { useFireflyPhysics } from './useFireflyPhysics';
import { useUpstreams, useUpstreamBreakers, useRecentLogs, useIsAuthenticated, useAppStore } from '@/core/state/store';
import { cn } from '@/lib/utils';

export const FireflyCanvas = React.memo(function FireflyCanvas({
  upstreams: propUpstreams,
  onToggleUpstream: propOnToggle,
  className,
  height,
  isPaused = false,
  reducedMotion = false,
  ref,
}: FireflyCanvasProps) {
    const containerRef = useRef<HTMLDivElement>(null);
    const canvasRef = useRef<HTMLCanvasElement>(null);
    const tooltipRef = useRef<HTMLDivElement>(null);

    // Direct subscription to Zustand store
    const isAuthenticated = useIsAuthenticated();
    const storeUpstreams = useUpstreams();
    const storeBreakers = useUpstreamBreakers();
    const recentLogs = useRecentLogs();
    const toggleUpstreamBreaker = useAppStore((state) => state.toggleUpstreamBreaker);

    const onToggleUpstream = propOnToggle || toggleUpstreamBreaker;

    // Synchronize upstreams: if propUpstreams is explicitly provided and non-empty, use it; otherwise compute directly from store
    const upstreams = useMemo(() => {
      if (propUpstreams && propUpstreams.length > 0) {
        return propUpstreams;
      }
      return storeUpstreams.map((u) => {
        const breakerState = storeBreakers[u.name] || (u.enabled === false ? 'OPEN' : 'CLOSED');
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
    }, [propUpstreams, storeUpstreams, storeBreakers, recentLogs]);

    const { triggerBurst } = useFireflyPhysics({
      canvasRef,
      containerRef,
      tooltipRef,
      upstreams,
      onToggleUpstream,
      isPaused,
      reducedMotion,
      canToggle: isAuthenticated,
    });

    useImperativeHandle(
      ref,
      () => ({
        triggerBurst,
      }),
      [triggerBurst]
    );

    return (
      <div
        ref={containerRef}
        id="fireflyContainer"
        className={cn('relative w-full h-[360px] sm:h-[460px] lg:h-[560px] overflow-hidden select-none flex-shrink-0 bg-transparent', className)}
        style={{
          position: 'relative',
          background: 'transparent',
          margin: 0,
          ...(height ? { height } : {}),
        }}
      >
        {/* Living Canvas 2D */}
        <canvas
          ref={canvasRef}
          id="fireflyCanvas"
          className="w-full h-full block bg-transparent"
          style={{ width: '100%', height: '100%', display: 'block', background: 'transparent' }}
        />

        {/* Hover Tooltip matching index.html lines 293-296 */}
        <div
          ref={tooltipRef}
          id="fireflyTooltip"
          className="absolute pointer-events-none opacity-0 transition-opacity duration-150 px-2.5 py-1.5 rounded-lg bg-neutral-900 text-white text-[11px] font-mono shadow-lg -translate-x-1/2 -translate-y-full mb-2 z-20"
          style={{ left: 0, top: 0 }}
        />
      </div>
    );
});
