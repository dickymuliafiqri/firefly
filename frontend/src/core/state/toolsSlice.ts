import type { StateCreator } from 'zustand';
import type { ToolId } from '@/modules/7-tools/tools';

export interface ToolsSlice {
  activeToolId: ToolId;
  telemetryCollapsed: boolean;
  setActiveToolId: (id: ToolId) => void;
  toggleTelemetry: () => void;
}

export const createToolsSlice: StateCreator<ToolsSlice, [], [], ToolsSlice> = (set) => ({
  activeToolId: 'chat',
  telemetryCollapsed: false,
  setActiveToolId: (id) => set(() => ({ activeToolId: id })),
  toggleTelemetry: () => set((state) => ({ telemetryCollapsed: !state.telemetryCollapsed })),
});
