/**
 * Pluggable visualizer views. Every view renders the same trace data through a
 * different lens; adding one means appending an entry here.
 */
import type { ReactNode } from 'react';
import type { RoutingTrace } from '@/services/visualizer';
import { lineView } from './line';
import { townView } from './town';

export interface ViewProps {
  /** The selected trace (may be undefined before the first request arrives). */
  trace?: RoutingTrace;
  /** Recent traces, oldest first — the fleet context for aggregate views. */
  traces: RoutingTrace[];
  /**
   * Names of every upstream in the live catalog (telemetry order). Views that
   * draw a fixed topology keep one permanent node per entry instead of only
   * the upstreams the selected trace happened to touch. Empty when telemetry
   * is unavailable.
   */
  catalogUpstreams?: string[];
}

export interface VisualizerView {
  id: string;
  label: string;
  hint: string;
  render: (props: ViewProps) => ReactNode;
}

export const VISUALIZER_VIEWS: VisualizerView[] = [lineView, townView];

export function findView(id: string): VisualizerView {
  return VISUALIZER_VIEWS.find((v) => v.id === id) ?? lineView;
}
