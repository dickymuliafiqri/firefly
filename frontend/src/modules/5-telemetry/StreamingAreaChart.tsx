import React, { useEffect, useRef, useState, useCallback } from 'react';
import { Activity, Play, Pause } from 'lucide-react';
import { cn } from '@/lib/utils';

export interface DataPoint {
  timestamp: number;
  rps: number;
  inflight: number;
}

export interface StreamingAreaChartProps {
  currentRps?: number;
  currentInflight?: number;
  totalRequests?: number;
}

const HISTORY_SECONDS = 60;

/**
 * StreamingAreaChart
 * High-performance 60-second real-time streaming area chart rendered via pure HTML5 Canvas 2D.
 * Charts authentic real-time gateway throughput and in-flight concurrency with zero dummy data.
 *
 * Vercel React Best Practices:
 * - bundle-defer-third-party: zero third-party chart library overhead
 * - rerender-use-ref-transient-values: buffers rolling timeline inside ref
 * - rerender-memo
 */
export const StreamingAreaChart = React.memo(function StreamingAreaChart({
  currentRps = 0,
  currentInflight = 0,
}: StreamingAreaChartProps) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const canvasRef = useRef<HTMLCanvasElement | null>(null);

  const [activeMetric, setActiveMetric] = useState<'rps' | 'inflight'>('rps');
  const [isPaused, setIsPaused] = useState(false);

  // Rolling history buffer (60 points) held in ref to avoid React re-renders every second
  const historyRef = useRef<DataPoint[]>([]);
  const isPausedRef = useRef(false);
  isPausedRef.current = isPaused;

  const currentRpsRef = useRef(currentRps);
  currentRpsRef.current = currentRps;

  const currentInflightRef = useRef(currentInflight);
  currentInflightRef.current = currentInflight;

  // Initialize 60 seconds of baseline with actual zero/current values
  useEffect(() => {
    const now = Date.now();
    const data: DataPoint[] = [];
    for (let i = HISTORY_SECONDS; i >= 0; i--) {
      data.push({
        timestamp: now - i * 1000,
        rps: 0,
        inflight: 0,
      });
    }
    historyRef.current = data;
  }, []);

  // 1Hz timeline recording tick from real gateway metrics
  useEffect(() => {
    const interval = setInterval(() => {
      if (isPausedRef.current) return;

      const nextPoint: DataPoint = {
        timestamp: Date.now(),
        rps: currentRpsRef.current,
        inflight: currentInflightRef.current,
      };

      const history = historyRef.current;
      history.push(nextPoint);
      if (history.length > HISTORY_SECONDS + 1) {
        history.shift();
      }
    }, 1000);

    return () => clearInterval(interval);
  }, []);

  // Canvas render loop
  const renderChart = useCallback(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const ctx = canvas.getContext('2d');
    if (!ctx) return;

    const dpr = window.devicePixelRatio || 1;
    const width = canvas.width / dpr;
    const height = canvas.height / dpr;

    ctx.clearRect(0, 0, width, height);

    const data = historyRef.current;
    if (data.length < 2) return;

    // Determine max value for Y scale with 25% headroom (minimum ceiling 5 for clean grid lines)
    let maxVal = 5;
    for (let i = 0; i < data.length; i++) {
      const v = activeMetric === 'rps' ? data[i].rps : data[i].inflight;
      if (v > maxVal) maxVal = v;
    }
    maxVal = Math.max(5, Math.ceil(maxVal * 1.25));

    // Padding
    const padTop = 15;
    const padBottom = 25;
    const padLeft = 40;
    const padRight = 15;
    const chartWidth = width - padLeft - padRight;
    const chartHeight = height - padTop - padBottom;

    // Draw horizontal grid lines
    ctx.strokeStyle = 'rgba(255, 255, 255, 0.05)';
    ctx.lineWidth = 1;
    const gridLines = 4;
    ctx.fillStyle = 'rgba(148, 163, 184, 0.4)';
    ctx.font = '10px monospace';
    ctx.textAlign = 'right';

    for (let i = 0; i <= gridLines; i++) {
      const y = padTop + (chartHeight / gridLines) * i;
      const val = Math.round(maxVal - (maxVal / gridLines) * i);

      ctx.beginPath();
      ctx.moveTo(padLeft, y);
      ctx.lineTo(width - padRight, y);
      ctx.stroke();

      ctx.fillText(val.toString(), padLeft - 8, y + 3);
    }

    // Coordinates calculation
    const points: { x: number; y: number }[] = [];
    const stepX = chartWidth / HISTORY_SECONDS;

    for (let i = 0; i < data.length; i++) {
      const val = activeMetric === 'rps' ? data[i].rps : data[i].inflight;
      const x = padLeft + i * stepX;
      const y = padTop + chartHeight - (val / maxVal) * chartHeight;
      points.push({ x, y });
    }

    if (points.length < 2) return;

    // Create subtle nocturnal gradient fill
    const gradient = ctx.createLinearGradient(0, padTop, 0, height - padBottom);
    if (activeMetric === 'rps') {
      gradient.addColorStop(0, 'rgba(52, 211, 153, 0.15)'); // emerald
      gradient.addColorStop(0.7, 'rgba(52, 211, 153, 0.02)');
      gradient.addColorStop(1, 'rgba(52, 211, 153, 0.0)');
    } else {
      gradient.addColorStop(0, 'rgba(96, 165, 250, 0.15)'); // blue
      gradient.addColorStop(0.7, 'rgba(96, 165, 250, 0.02)');
      gradient.addColorStop(1, 'rgba(96, 165, 250, 0.0)');
    }

    // Draw filled area under curve
    ctx.beginPath();
    ctx.moveTo(points[0].x, height - padBottom);
    ctx.lineTo(points[0].x, points[0].y);

    for (let i = 0; i < points.length - 1; i++) {
      const curr = points[i];
      const next = points[i + 1];
      const mx = (curr.x + next.x) / 2;
      ctx.bezierCurveTo(mx, curr.y, mx, next.y, next.x, next.y);
    }

    ctx.lineTo(points[points.length - 1].x, height - padBottom);
    ctx.closePath();
    ctx.fillStyle = gradient;
    ctx.fill();

    // Draw main hairline stroke
    ctx.beginPath();
    ctx.moveTo(points[0].x, points[0].y);

    for (let i = 0; i < points.length - 1; i++) {
      const curr = points[i];
      const next = points[i + 1];
      const mx = (curr.x + next.x) / 2;
      ctx.bezierCurveTo(mx, curr.y, mx, next.y, next.x, next.y);
    }

    ctx.strokeStyle =
      activeMetric === 'rps' ? 'rgba(52, 211, 153, 0.7)' : 'rgba(96, 165, 250, 0.7)';
    ctx.lineWidth = 1.5;
    ctx.stroke();

    // Subtle dot at latest point
    const lastPoint = points[points.length - 1];
    ctx.beginPath();
    ctx.arc(lastPoint.x, lastPoint.y, 3, 0, Math.PI * 2);
    ctx.fillStyle = activeMetric === 'rps' ? '#34d399' : '#60a5fa';
    ctx.fill();

    // Time indicators on X-axis (0s, -30s, -60s)
    ctx.fillStyle = 'rgba(255, 255, 255, 0.25)';
    ctx.textAlign = 'center';
    ctx.fillText('-60s', padLeft, height - 8);
    ctx.fillText('-30s', padLeft + chartWidth / 2, height - 8);
    ctx.fillText('now', width - padRight, height - 8);
  }, [activeMetric]);

  // Resize handler with High-DPI support
  useEffect(() => {
    const handleResize = () => {
      const container = containerRef.current;
      const canvas = canvasRef.current;
      if (!container || !canvas) return;

      const rect = container.getBoundingClientRect();
      const dpr = window.devicePixelRatio || 1;

      canvas.width = rect.width * dpr;
      canvas.height = 200 * dpr;
      canvas.style.width = `${rect.width}px`;
      canvas.style.height = `200px`;

      const ctx = canvas.getContext('2d');
      if (ctx) {
        ctx.scale(dpr, dpr);
      }

      renderChart();
    };

    handleResize();
    window.addEventListener('resize', handleResize);
    return () => window.removeEventListener('resize', handleResize);
  }, [renderChart]);

  // Redraw when data or metric changes (at ~30fps max to save CPU)
  useEffect(() => {
    let animFrame: number;
    let lastRenderTime = 0;

    const loop = (timestamp: number) => {
      if (timestamp - lastRenderTime >= 50) {
        renderChart();
        lastRenderTime = timestamp;
      }
      animFrame = requestAnimationFrame(loop);
    };

    animFrame = requestAnimationFrame(loop);
    return () => cancelAnimationFrame(animFrame);
  }, [renderChart]);

  return (
    <div className="p-5 rounded-xl bg-transparent border border-white/[0.06] flex flex-col font-mono text-xs select-none">
      <div className="flex flex-wrap items-center justify-between gap-3 pb-3 border-b border-white/[0.04]">
        <div className="flex items-center gap-3">
          <Activity className="w-4 h-4 text-neutral-400" />
          <div>
            <h3 className="text-xs font-mono uppercase tracking-wider text-neutral-300 font-medium flex items-center gap-2">
              Streaming Throughput Observatory
              <span className="text-[10px] text-neutral-500 border border-white/[0.06] px-1.5 py-0.5 rounded font-normal">
                60s
              </span>
            </h3>
            <p className="text-[11px] text-neutral-500 font-mono">
              Live Gateway Metrics &middot; Pure Canvas 2D
            </p>
          </div>
        </div>

        {/* Action controls & Metric selector */}
        <div className="flex items-center gap-2">
          <div className="inline-flex p-0.5 rounded-lg border border-white/[0.08] bg-transparent font-mono text-xs">
            <button
              onClick={() => setActiveMetric('rps')}
              className={cn(
                'px-2.5 py-1 rounded transition-all',
                activeMetric === 'rps'
                  ? 'bg-white/[0.08] text-white font-medium'
                  : 'text-neutral-500 hover:text-neutral-300'
              )}
            >
              RPS ({Math.round(currentRps * 10) / 10})
            </button>
            <button
              onClick={() => setActiveMetric('inflight')}
              className={cn(
                'px-2.5 py-1 rounded transition-all',
                activeMetric === 'inflight'
                  ? 'bg-white/[0.08] text-white font-medium'
                  : 'text-neutral-500 hover:text-neutral-300'
              )}
            >
              In-flight ({currentInflight})
            </button>
          </div>

          <button
            onClick={() => setIsPaused((p) => !p)}
            className="p-1.5 rounded-lg border border-white/[0.08] bg-transparent text-neutral-400 hover:text-white hover:bg-white/[0.04] transition-colors"
            title={isPaused ? 'Resume stream' : 'Pause stream'}
          >
            {isPaused ? <Play className="w-3.5 h-3.5" /> : <Pause className="w-3.5 h-3.5" />}
          </button>
        </div>
      </div>

      {/* Canvas container */}
      <div ref={containerRef} className="w-full relative mt-3 h-[200px]">
        <canvas ref={canvasRef} className="block w-full h-[200px]" />
      </div>
    </div>
  );
});
