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

export const useDashboardPassword = () => useAppStore((state) => state.dashboardPassword);
export const useIsAuthenticated = () => useAppStore((state) => state.isAuthenticated);
export const useSetDashboardPassword = () => useAppStore((state) => state.setDashboardPassword);

export const useTelemetryStats = () => useAppStore((state) => state.stats);
export const useRecentLogs = () => useAppStore((state) => state.recentLogs);

export const useActiveModal = () => useAppStore((state) => state.activeModal);
export const useModalPayload = () => useAppStore((state) => state.modalPayload);

export const useActiveDrawer = () => useAppStore((state) => state.activeDrawer);
export const useDrawerPayload = () => useAppStore((state) => state.drawerPayload);

export const useToasts = () => useAppStore((state) => state.toasts);

export const useStoreActions = () =>
  useAppStore((state) => ({
    setSettings: state.setSettings,
    addOrUpdateUpstream: state.addOrUpdateUpstream,
    removeUpstream: state.removeUpstream,
    setBreakerState: state.setBreakerState,
    toggleUpstreamBreaker: state.toggleUpstreamBreaker,
    addOrUpdateModel: state.addOrUpdateModel,
    removeModel: state.removeModel,
    setCombos: state.setCombos,
    addOrUpdateCombo: state.addOrUpdateCombo,
    removeCombo: state.removeCombo,
    addOrUpdateTenant: state.addOrUpdateTenant,
    removeTenant: state.removeTenant,
    setDashboardPassword: state.setDashboardPassword,
    login: state.login,
    logout: state.logout,
    updateStats: state.updateStats,
    incrementUsage: state.incrementUsage,
    addLog: state.addLog,
    setRecentLogs: state.setRecentLogs,
    clearLogs: state.clearLogs,
    openModal: state.openModal,
    closeModal: state.closeModal,
    openDrawer: state.openDrawer,
    closeDrawer: state.closeDrawer,
    addToast: state.addToast,
    removeToast: state.removeToast,
  }));

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

export const usePlaygroundActions = () =>
  useAppStore((state) => ({
    setPlaygroundMessages: state.setPlaygroundMessages,
    setPlaygroundPrompt: state.setPlaygroundPrompt,
    setPlaygroundSelectedModel: state.setPlaygroundSelectedModel,
    setPlaygroundApiKey: state.setPlaygroundApiKey,
    setPlaygroundTemperature: state.setPlaygroundTemperature,
    setPlaygroundMaxTokens: state.setPlaygroundMaxTokens,
    setPlaygroundIsStreamMode: state.setPlaygroundIsStreamMode,
    setPlaygroundIsGenerating: state.setPlaygroundIsGenerating,
    setPlaygroundErrorMessage: state.setPlaygroundErrorMessage,
    updatePlaygroundTimings: state.updatePlaygroundTimings,
    addPlaygroundRawPacket: state.addPlaygroundRawPacket,
    clearPlaygroundDiagnostics: state.clearPlaygroundDiagnostics,
    clearPlaygroundChat: state.clearPlaygroundChat,
  }));

