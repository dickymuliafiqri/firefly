import React, { useRef, useImperativeHandle, forwardRef } from 'react';
import type { FireflyCanvasProps, FireflyCanvasHandle } from './types';
import { useFireflyPhysics } from './useFireflyPhysics';
import { cn } from '@/lib/utils';

export const FireflyCanvas = React.memo(
  forwardRef<FireflyCanvasHandle, FireflyCanvasProps>(function FireflyCanvas(
    { upstreams = [], onToggleUpstream, className, height = 560, isPaused = false, reducedMotion = false },
    ref
  ) {
    const containerRef = useRef<HTMLDivElement>(null);
    const canvasRef = useRef<HTMLCanvasElement>(null);
    const tooltipRef = useRef<HTMLDivElement>(null);

    const { triggerBurst } = useFireflyPhysics({
      canvasRef,
      containerRef,
      tooltipRef,
      upstreams,
      onToggleUpstream,
      isPaused,
      reducedMotion,
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
        className={cn('relative w-full overflow-hidden select-none flex-shrink-0 bg-transparent', className)}
        style={{ minHeight: 520, height, position: 'relative', background: 'transparent', margin: 0 }}
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
  })
);
