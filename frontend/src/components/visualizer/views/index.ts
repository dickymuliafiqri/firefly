/**
 * Pluggable visualizer views. Every view renders the same trace data through a
 * different lens; adding one means appending an entry here.
 */
import type { ReactNode } from 'react';
import type { RoutingTrace } from '@/services/visualizer';
import { lineView } from './line';
import { treeView } from './tree';
import { officeView } from './office';
import { radarView } from './radar';

export interface ViewProps {
  /** The selected trace (may be undefined before the first request arrives). */
  trace?: RoutingTrace;
  /** Recent traces, oldest first — the fleet context for aggregate views. */
  traces: RoutingTrace[];
}

export interface VisualizerView {
  id: string;
  label: string;
  hint: string;
  render: (props: ViewProps) => ReactNode;
}

export const VISUALIZER_VIEWS: VisualizerView[] = [lineView, treeView, officeView, radarView];

export function findView(id: string): VisualizerView {
  return VISUALIZER_VIEWS.find((v) => v.id === id) ?? lineView;
}
