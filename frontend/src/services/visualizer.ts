/**
 * Visualizer contract — admin-only, in-memory. Mirrors the Go DTOs in
 * internal/observability/trace. Nothing here is persisted server-side, and
 * capture only runs while this page holds an SSE subscription open.
 */
import { handleSessionInvalid } from '@/lib/session';
import { getAdminToken } from '@/state/auth';

export interface TraceStage {
  name: string;
  at: number;
  delta?: number;
  detail?: string;
}

export interface TraceCandidate {
  upstream: string;
  key?: string;
  state: 'chosen' | 'skipped';
  note?: string;
}

export interface TraceActivity {
  kind: string;
  state: string;
  stage?: string;
  reason?: string;
  bytes: number;
  deltas: number;
  at: number;
}

export interface TraceEvent {
  at: number;
  kind: string;
  name?: string;
  detail?: string;
}

export interface RoutingTrace {
  id: string;
  state: string;
  started_at: number;
  ended_at?: number;
  method?: string;
  path?: string;
  model?: string;
  tenant?: string;
  stream: boolean;
  upstream?: string;
  protocol?: string;
  key_ref?: string;
  status: number;
  tokens_in?: number;
  tokens_out?: number;
  bytes: number;
  deltas: number;
  duration_ms?: number;
  ttfb_ms?: number;
  error?: string;
  stages: TraceStage[];
  candidates?: TraceCandidate[];
  events?: TraceEvent[];
  phases?: string[];
  activity: TraceActivity;
}

export interface VisualizerStats {
  enabled: boolean;
  live: boolean;
  retention: number;
  captured: number;
  inflight: number;
  dropped: number;
  subscribers: number;
}

export interface VisualizerSnapshot {
  traces: RoutingTrace[];
  stats: VisualizerStats;
}

export type VisualizerFrame =
  | { type: 'trace'; trace: RoutingTrace }
  | { type: 'done'; trace: RoutingTrace }
  | { type: 'stats'; stats: VisualizerStats };

async function call<T>(path: string, init?: RequestInit): Promise<T> {
  const token = getAdminToken();
  const res = await fetch(path, {
    ...init,
    headers: {
      Accept: 'application/json',
      ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
  });
  if (res.status === 401) {
    handleSessionInvalid();
    throw new Error('Unauthorized: valid dashboard session or admin token required');
  }
  if (!res.ok) throw new Error(`Visualizer request failed: ${res.status}`);
  return (await res.json()) as T;
}

export function fetchVisualizerSnapshot(): Promise<VisualizerSnapshot> {
  return call<VisualizerSnapshot>('/api/visualizer/traces');
}

export function clearVisualizer(): Promise<{ stats: VisualizerStats }> {
  return call('/api/visualizer/clear', { method: 'POST' });
}

/**
 * Opens the live trace stream. Returns an abort function; aborting (or the
 * browser navigating away) drops the last subscriber and stops capture.
 */
export function openVisualizerStream(
  onFrame: (frame: VisualizerFrame) => void,
  onError?: (err: unknown) => void,
): () => void {
  const controller = new AbortController();
  const token = getAdminToken();

  void (async () => {
    try {
      const res = await fetch('/api/visualizer/events', {
        signal: controller.signal,
        headers: {
          Accept: 'text/event-stream',
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
        },
      });
      if (!res.ok || !res.body) throw new Error(`Visualizer stream failed: ${res.status}`);
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buffer = '';
      for (;;) {
        const { value, done } = await reader.read();
        if (done) return;
        buffer += decoder.decode(value, { stream: true });
        const chunks = buffer.split('\n\n');
        buffer = chunks.pop() ?? '';
        for (const chunk of chunks) {
          const line = chunk.split('\n').find((l) => l.startsWith('data: '));
          if (!line) continue;
          try {
            onFrame(JSON.parse(line.slice(6)) as VisualizerFrame);
          } catch {
            /* ignore malformed frames */
          }
        }
      }
    } catch (err) {
      if (!controller.signal.aborted) onError?.(err);
    }
  })();

  return () => controller.abort();
}
