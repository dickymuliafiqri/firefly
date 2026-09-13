/**
 * types.ts — Canvas Firefly Physics & Rendering types
 */

export interface FireflyUpstream {
  name: string;
  protocol: string;
  base_url?: string;
  base_urls?: string[];
  connected?: boolean;
  breaker_state?: 'CLOSED' | 'OPEN' | 'HALF-OPEN' | string;
  latency_ms?: number;
  inflight?: number;
}

export interface Ripple {
  r: number;
  alpha: number;
  maxR: number;
}

export interface TailSpark {
  x: number;
  y: number;
  alpha: number;
  size: number;
}

export interface FireflyCanvasHandle {
  triggerBurst: (upstreamName: string) => void;
}

export interface FireflyCanvasProps {
  upstreams?: FireflyUpstream[];
  onToggleUpstream?: (name: string) => void;
  className?: string;
  height?: number | string;
  isPaused?: boolean;
  reducedMotion?: boolean;
}
