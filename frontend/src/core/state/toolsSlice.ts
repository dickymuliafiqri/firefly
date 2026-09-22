import type { StateCreator } from 'zustand';
import type { ToolId } from '@/modules/7-tools/tools';

export interface ToolsSlice {
  activeToolId: ToolId;
  setActiveToolId: (id: ToolId) => void;
}

export const createToolsSlice: StateCreator<ToolsSlice, [], [], ToolsSlice> = (set) => ({
  activeToolId: 'chat',
  setActiveToolId: (id) => set(() => ({ activeToolId: id })),
});
