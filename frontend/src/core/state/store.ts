import { create } from 'zustand';
import { createSettingsSlice, type SettingsSlice } from './settingsSlice';
import { createTelemetrySlice, type TelemetrySlice } from './telemetrySlice';
import { createUISlice, type UISlice } from './uiSlice';
import { createPlaygroundSlice, type PlaygroundSlice } from './playgroundSlice';

export type AppStore = SettingsSlice & TelemetrySlice & UISlice & PlaygroundSlice;

/**
 * Root Zustand Store combining Settings, Telemetry, UI, and Playground slices.
 * Adheres strictly to Vercel React Best Practice: rerender-defer-reads
 * by exposing granular atomic hooks rather than raw full-store subscriptions.
 */
export const useAppStore = create<AppStore>()((...a) => ({
  ...createSettingsSlice(...a),
  ...createTelemetrySlice(...a),
  ...createUISlice(...a),
  ...createPlaygroundSlice(...a),
}));

// ================= ATOMIC SELECTOR HOOKS =================

export const useActiveTab = () => useAppStore((state) => state.activeTab);
export const useSetActiveTab = () => useAppStore((state) => state.setActiveTab);

export const useUpstreams = () => useAppStore((state) => state.upstreams);
export const useUpstreamBreakers = () => useAppStore((state) => state.upstreamBreakers);
export const useModels = () => useAppStore((state) => state.models);
export const useCombos = () => useAppStore((state) => state.combos);
export const useTenants = () => useAppStore((state) => state.tenants);

export const useAdminToken = () => useAppStore((state) => state.adminToken);
export const useSetAdminToken = () => useAppStore((state) => state.setAdminToken);

export const useIsAuthenticated = () => useAppStore((state) => state.isAuthenticated);

export const useTelemetryStats = () => useAppStore((state) => state.stats);
export const useRecentLogs = () => useAppStore((state) => state.recentLogs);

export const useActiveModal = () => useAppStore((state) => state.activeModal);
export const useModalPayload = () => useAppStore((state) => state.modalPayload);

export const useActiveDrawer = () => useAppStore((state) => state.activeDrawer);
export const useDrawerPayload = () => useAppStore((state) => state.drawerPayload);

export const useToasts = () => useAppStore((state) => state.toasts);

const STORE_ACTIONS = {
  setSettings: (...args: Parameters<AppStore['setSettings']>) => useAppStore.getState().setSettings(...args),
  setUpstreamBreakers: (...args: Parameters<AppStore['setUpstreamBreakers']>) => useAppStore.getState().setUpstreamBreakers(...args),
  setUpstreams: (...args: Parameters<AppStore['setUpstreams']>) => useAppStore.getState().setUpstreams(...args),
  addOrUpdateUpstream: (...args: Parameters<AppStore['addOrUpdateUpstream']>) => useAppStore.getState().addOrUpdateUpstream(...args),
  removeUpstream: (...args: Parameters<AppStore['removeUpstream']>) => useAppStore.getState().removeUpstream(...args),
  setBreakerState: (...args: Parameters<AppStore['setBreakerState']>) => useAppStore.getState().setBreakerState(...args),
  toggleUpstreamBreaker: (...args: Parameters<AppStore['toggleUpstreamBreaker']>) => useAppStore.getState().toggleUpstreamBreaker(...args),
  setModels: (...args: Parameters<AppStore['setModels']>) => useAppStore.getState().setModels(...args),
  addOrUpdateModel: (...args: Parameters<AppStore['addOrUpdateModel']>) => useAppStore.getState().addOrUpdateModel(...args),
  removeModel: (...args: Parameters<AppStore['removeModel']>) => useAppStore.getState().removeModel(...args),
  setCombos: (...args: Parameters<AppStore['setCombos']>) => useAppStore.getState().setCombos(...args),
  addOrUpdateCombo: (...args: Parameters<AppStore['addOrUpdateCombo']>) => useAppStore.getState().addOrUpdateCombo(...args),
  removeCombo: (...args: Parameters<AppStore['removeCombo']>) => useAppStore.getState().removeCombo(...args),
  setTenants: (...args: Parameters<AppStore['setTenants']>) => useAppStore.getState().setTenants(...args),
  addOrUpdateTenant: (...args: Parameters<AppStore['addOrUpdateTenant']>) => useAppStore.getState().addOrUpdateTenant(...args),
  removeTenant: (...args: Parameters<AppStore['removeTenant']>) => useAppStore.getState().removeTenant(...args),
  setAdminToken: (...args: Parameters<AppStore['setAdminToken']>) => useAppStore.getState().setAdminToken(...args),
  login: (...args: Parameters<AppStore['login']>) => useAppStore.getState().login(...args),
  logout: () => useAppStore.getState().logout(),
  verifySession: () => useAppStore.getState().verifySession(),
  updatePassword: (...args: Parameters<AppStore['updatePassword']>) => useAppStore.getState().updatePassword(...args),
  updateStats: (...args: Parameters<AppStore['updateStats']>) => useAppStore.getState().updateStats(...args),
  incrementUsage: (...args: Parameters<AppStore['incrementUsage']>) => useAppStore.getState().incrementUsage(...args),
  addLog: (...args: Parameters<AppStore['addLog']>) => useAppStore.getState().addLog(...args),
  setRecentLogs: (...args: Parameters<AppStore['setRecentLogs']>) => useAppStore.getState().setRecentLogs(...args),
  clearLogs: () => useAppStore.getState().clearLogs(),
  openModal: (...args: Parameters<AppStore['openModal']>) => useAppStore.getState().openModal(...args),
  closeModal: () => useAppStore.getState().closeModal(),
  openDrawer: (...args: Parameters<AppStore['openDrawer']>) => useAppStore.getState().openDrawer(...args),
  closeDrawer: () => useAppStore.getState().closeDrawer(),
  addToast: (...args: Parameters<AppStore['addToast']>) => useAppStore.getState().addToast(...args),
  removeToast: (...args: Parameters<AppStore['removeToast']>) => useAppStore.getState().removeToast(...args),
};

export const useStoreActions = () => STORE_ACTIONS;

// ================= PLAYGROUND SELECTOR HOOKS =================

export const usePlaygroundMessages = () => useAppStore((state) => state.playgroundMessages);
export const usePlaygroundPrompt = () => useAppStore((state) => state.playgroundPrompt);
export const usePlaygroundSelectedModel = () => useAppStore((state) => state.playgroundSelectedModel);
export const usePlaygroundApiKey = () => useAppStore((state) => state.playgroundApiKey);
export const usePlaygroundTemperature = () => useAppStore((state) => state.playgroundTemperature);
export const usePlaygroundMaxTokens = () => useAppStore((state) => state.playgroundMaxTokens);
export const usePlaygroundIsStreamMode = () => useAppStore((state) => state.playgroundIsStreamMode);
export const usePlaygroundIsGenerating = () => useAppStore((state) => state.playgroundIsGenerating);
export const usePlaygroundErrorMessage = () => useAppStore((state) => state.playgroundErrorMessage);

export const usePlaygroundTtftMs = () => useAppStore((state) => state.playgroundTtftMs);
export const usePlaygroundTotalDurationMs = () => useAppStore((state) => state.playgroundTotalDurationMs);
export const usePlaygroundTps = () => useAppStore((state) => state.playgroundTps);
export const usePlaygroundTimings = () => useAppStore((state) => state.playgroundTimings);
export const usePlaygroundRawPackets = () => useAppStore((state) => state.playgroundRawPackets);

const PLAYGROUND_ACTIONS = {
  setPlaygroundMessages: (...args: Parameters<AppStore['setPlaygroundMessages']>) => useAppStore.getState().setPlaygroundMessages(...args),
  setPlaygroundPrompt: (...args: Parameters<AppStore['setPlaygroundPrompt']>) => useAppStore.getState().setPlaygroundPrompt(...args),
  setPlaygroundSelectedModel: (...args: Parameters<AppStore['setPlaygroundSelectedModel']>) => useAppStore.getState().setPlaygroundSelectedModel(...args),
  setPlaygroundApiKey: (...args: Parameters<AppStore['setPlaygroundApiKey']>) => useAppStore.getState().setPlaygroundApiKey(...args),
  setPlaygroundTemperature: (...args: Parameters<AppStore['setPlaygroundTemperature']>) => useAppStore.getState().setPlaygroundTemperature(...args),
  setPlaygroundMaxTokens: (...args: Parameters<AppStore['setPlaygroundMaxTokens']>) => useAppStore.getState().setPlaygroundMaxTokens(...args),
  setPlaygroundIsStreamMode: (...args: Parameters<AppStore['setPlaygroundIsStreamMode']>) => useAppStore.getState().setPlaygroundIsStreamMode(...args),
  setPlaygroundIsGenerating: (...args: Parameters<AppStore['setPlaygroundIsGenerating']>) => useAppStore.getState().setPlaygroundIsGenerating(...args),
  setPlaygroundErrorMessage: (...args: Parameters<AppStore['setPlaygroundErrorMessage']>) => useAppStore.getState().setPlaygroundErrorMessage(...args),
  updatePlaygroundTimings: (...args: Parameters<AppStore['updatePlaygroundTimings']>) => useAppStore.getState().updatePlaygroundTimings(...args),
  addPlaygroundRawPacket: (...args: Parameters<AppStore['addPlaygroundRawPacket']>) => useAppStore.getState().addPlaygroundRawPacket(...args),
  clearPlaygroundDiagnostics: () => useAppStore.getState().clearPlaygroundDiagnostics(),
  clearPlaygroundChat: () => useAppStore.getState().clearPlaygroundChat(),
};

export const usePlaygroundActions = () => PLAYGROUND_ACTIONS;

