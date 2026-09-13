import type { StateCreator } from 'zustand';
import type { TabId } from '@/core/layout/Header';

export interface ToastItem {
  id: string;
  title: string;
  message: string;
  type?: 'success' | 'warning' | 'error' | 'info';
  durationMs?: number;
}

export interface UISlice {
  activeTab: TabId;
  activeModal: string | null;
  modalPayload: unknown | null;
  activeDrawer: string | null;
  drawerPayload: unknown | null;
  toasts: ToastItem[];

  setActiveTab: (tab: TabId) => void;
  openModal: (modalId: string, payload?: unknown) => void;
  closeModal: () => void;
  openDrawer: (drawerId: string, payload?: unknown) => void;
  closeDrawer: () => void;
  addToast: (toast: Omit<ToastItem, 'id'>) => void;
  removeToast: (id: string) => void;
}

export const createUISlice: StateCreator<UISlice, [], [], UISlice> = (set) => ({
  activeTab: 'overview',
  activeModal: null,
  modalPayload: null,
  activeDrawer: null,
  drawerPayload: null,
  toasts: [],

  setActiveTab: (tab) => set(() => ({ activeTab: tab })),

  openModal: (modalId, payload = null) =>
    set(() => ({ activeModal: modalId, modalPayload: payload })),

  closeModal: () => set(() => ({ activeModal: null, modalPayload: null })),

  openDrawer: (drawerId, payload = null) =>
    set(() => ({ activeDrawer: drawerId, drawerPayload: payload })),

  closeDrawer: () => set(() => ({ activeDrawer: null, drawerPayload: null })),

  addToast: (toast) =>
    set((state) => {
      const id = `${Date.now()}-${Math.random().toString(36).slice(2, 6)}`;
      const nextToasts = [...state.toasts, { ...toast, id }];
      return { toasts: nextToasts };
    }),

  removeToast: (id) =>
    set((state) => ({
      toasts: state.toasts.filter((t) => t.id !== id),
    })),
});
