import type { StateCreator } from 'zustand';
import type { UpstreamDTO, ModelDTO, TenantDTO, ComboDTO, SettingsDTO } from '@/services/schema';

export interface SettingsSlice {
  upstreams: UpstreamDTO[];
  models: ModelDTO[];
  tenants: TenantDTO[];
  combos: ComboDTO[];
  adminToken: string;
  dashboardPassword: string;
  isAuthenticated: boolean;
  isSettingsLoading: boolean;
  settingsError: string | null;
  lastSavedAt: number | null;
  upstreamBreakers: Record<string, 'OPEN' | 'CLOSED' | 'HALF-OPEN'>;

  setSettings: (settings: SettingsDTO) => void;
  setUpstreams: (upstreams: UpstreamDTO[]) => void;
  addOrUpdateUpstream: (upstream: UpstreamDTO) => void;
  removeUpstream: (name: string) => void;
  setBreakerState: (name: string, state: 'OPEN' | 'CLOSED' | 'HALF-OPEN') => void;
  toggleUpstreamBreaker: (name: string) => void;

  setModels: (models: ModelDTO[]) => void;
  addOrUpdateModel: (model: ModelDTO) => void;
  removeModel: (publicName: string) => void;

  setCombos: (combos: ComboDTO[]) => void;
  addOrUpdateCombo: (combo: ComboDTO) => void;
  removeCombo: (name: string) => void;

  setTenants: (tenants: TenantDTO[]) => void;
  addOrUpdateTenant: (tenant: TenantDTO) => void;
  removeTenant: (keyHashOrName: string) => void;

  setAdminToken: (token: string) => void;
  setDashboardPassword: (password: string) => void;
  login: (password: string) => boolean;
  logout: () => void;
  setSettingsLoading: (loading: boolean) => void;
  setSettingsError: (error: string | null) => void;
}

const ADMIN_TOKEN_KEY = 'firefly_admin_token_v1';
const BREAKER_STATES_KEY = 'firefly_upstream_breakers_v1';
const DASHBOARD_PASSWORD_KEY = 'firefly_dashboard_password_v1';
const AUTH_SESSION_KEY = 'firefly_auth_session_v1';
export const DEFAULT_DASHBOARD_PASSWORD = '12345678';

function loadPersistedPassword(): string {
  if (typeof window === 'undefined') return DEFAULT_DASHBOARD_PASSWORD;
  try {
    const val = localStorage.getItem(DASHBOARD_PASSWORD_KEY);
    return val && val.length > 0 ? val : DEFAULT_DASHBOARD_PASSWORD;
  } catch {
    return DEFAULT_DASHBOARD_PASSWORD;
  }
}

function loadPersistedAuthSession(): boolean {
  if (typeof window === 'undefined') return false;
  try {
    return sessionStorage.getItem(AUTH_SESSION_KEY) === 'true';
  } catch {
    return false;
  }
}

function loadPersistedBreakers(): Record<string, 'OPEN' | 'CLOSED' | 'HALF-OPEN'> {
  if (typeof window === 'undefined') return {};
  try {
    const raw = localStorage.getItem(BREAKER_STATES_KEY);
    return raw ? JSON.parse(raw) : {};
  } catch {
    return {};
  }
}

function savePersistedBreakers(breakers: Record<string, 'OPEN' | 'CLOSED' | 'HALF-OPEN'>) {
  if (typeof window === 'undefined') return;
  try {
    localStorage.setItem(BREAKER_STATES_KEY, JSON.stringify(breakers));
  } catch {
    // ignore
  }
}

export const createSettingsSlice: StateCreator<SettingsSlice, [], [], SettingsSlice> = (set, get) => ({
  upstreams: [],
  models: [],
  tenants: [],
  combos: [],
  adminToken:
    typeof window !== 'undefined'
      ? localStorage.getItem(ADMIN_TOKEN_KEY) || ''
      : '',
  dashboardPassword: loadPersistedPassword(),
  isAuthenticated: loadPersistedAuthSession(),
  isSettingsLoading: false,
  settingsError: null,
  lastSavedAt: null,
  upstreamBreakers: loadPersistedBreakers(),

  setSettings: (settings) =>
    set((state) => {
      const persisted = loadPersistedBreakers();
      const updatedBreakers: Record<string, 'OPEN' | 'CLOSED' | 'HALF-OPEN'> = {
        ...persisted,
        ...state.upstreamBreakers,
      };

      const serverUpstreams = settings.upstreams || [];
      for (const u of serverUpstreams) {
        if (u.enabled === false) {
          updatedBreakers[u.name] = 'CLOSED';
        } else if (u.enabled === true && !updatedBreakers[u.name]) {
          updatedBreakers[u.name] = 'OPEN';
        }
      }
      savePersistedBreakers(updatedBreakers);

      const resolvedUpstreams = serverUpstreams.map((u) => ({
        ...u,
        enabled: updatedBreakers[u.name] !== 'CLOSED' && u.enabled !== false,
      }));

      return {
        upstreams: resolvedUpstreams,
        models: settings.models || [],
        tenants: settings.tenants || [],
        combos: settings.combos || [],
        upstreamBreakers: updatedBreakers,
        settingsError: null,
        lastSavedAt: Date.now(),
      };
    }),

  setUpstreams: (upstreams) => set(() => ({ upstreams })),

  addOrUpdateUpstream: (upstream) =>
    set((state) => {
      const idx = state.upstreams.findIndex((u) => u.name === upstream.name);
      if (idx >= 0) {
        const next = [...state.upstreams];
        next[idx] = { ...next[idx], ...upstream };
        return { upstreams: next };
      }
      return { upstreams: [...state.upstreams, upstream] };
    }),

  removeUpstream: (name) =>
    set((state) => {
      const nextBreakers = { ...state.upstreamBreakers };
      delete nextBreakers[name];
      savePersistedBreakers(nextBreakers);
      return {
        upstreams: state.upstreams.filter((u) => u.name !== name),
        upstreamBreakers: nextBreakers,
      };
    }),

  setBreakerState: (name, breakerState) =>
    set((state) => {
      const nextBreakers: Record<string, 'OPEN' | 'CLOSED' | 'HALF-OPEN'> = {
        ...state.upstreamBreakers,
        [name]: breakerState,
      };
      savePersistedBreakers(nextBreakers);

      const nextUpstreams = state.upstreams.map((u) =>
        u.name === name ? { ...u, enabled: breakerState === 'OPEN' } : u
      );

      return {
        upstreamBreakers: nextBreakers,
        upstreams: nextUpstreams,
      };
    }),

  toggleUpstreamBreaker: (name) =>
    set((state) => {
      const current = state.upstreamBreakers[name] || 'OPEN';
      const nextState: 'OPEN' | 'CLOSED' = current === 'OPEN' ? 'CLOSED' : 'OPEN';
      const nextBreakers: Record<string, 'OPEN' | 'CLOSED' | 'HALF-OPEN'> = {
        ...state.upstreamBreakers,
        [name]: nextState,
      };
      savePersistedBreakers(nextBreakers);

      const nextUpstreams = state.upstreams.map((u) =>
        u.name === name ? { ...u, enabled: nextState === 'OPEN' } : u
      );

      return {
        upstreamBreakers: nextBreakers,
        upstreams: nextUpstreams,
      };
    }),

  setModels: (models) => set(() => ({ models })),

  addOrUpdateModel: (model) =>
    set((state) => {
      const idx = state.models.findIndex((m) => m.public_name === model.public_name);
      if (idx >= 0) {
        const next = [...state.models];
        next[idx] = { ...next[idx], ...model };
        return { models: next };
      }
      return { models: [...state.models, model] };
    }),

  removeModel: (publicName) =>
    set((state) => ({
      models: state.models.filter((m) => m.public_name !== publicName),
    })),

  setCombos: (combos) => set(() => ({ combos })),

  addOrUpdateCombo: (combo) =>
    set((state) => {
      const idx = state.combos.findIndex((c) => c.name === combo.name);
      if (idx >= 0) {
        const next = [...state.combos];
        next[idx] = { ...next[idx], ...combo };
        return { combos: next };
      }
      return { combos: [...state.combos, combo] };
    }),

  removeCombo: (name) =>
    set((state) => ({
      combos: state.combos.filter((c) => c.name !== name),
    })),

  setTenants: (tenants) => set(() => ({ tenants })),

  addOrUpdateTenant: (tenant) =>
    set((state) => {
      const identifier = tenant.key_hash || tenant.name;
      const idx = state.tenants.findIndex((t) => (t.key_hash || t.name) === identifier);
      if (idx >= 0) {
        const next = [...state.tenants];
        next[idx] = { ...next[idx], ...tenant };
        return { tenants: next };
      }
      return { tenants: [...state.tenants, tenant] };
    }),

  removeTenant: (keyHashOrName) =>
    set((state) => ({
      tenants: state.tenants.filter((t) => (t.key_hash || t.name) !== keyHashOrName),
    })),

  setAdminToken: (token) => {
    if (typeof window !== 'undefined') {
      if (token) {
        localStorage.setItem(ADMIN_TOKEN_KEY, token);
      } else {
        localStorage.removeItem(ADMIN_TOKEN_KEY);
      }
    }
    set(() => ({ adminToken: token }));
  },

  setDashboardPassword: (password) => {
    const trimmed = password.trim() || DEFAULT_DASHBOARD_PASSWORD;
    if (typeof window !== 'undefined') {
      try {
        localStorage.setItem(DASHBOARD_PASSWORD_KEY, trimmed);
      } catch {
        // ignore
      }
    }
    set(() => ({ dashboardPassword: trimmed }));
  },

  login: (password) => {
    const state = get();
    const targetPassword = state.dashboardPassword || DEFAULT_DASHBOARD_PASSWORD;
    if (password === targetPassword) {
      if (typeof window !== 'undefined') {
        try {
          sessionStorage.setItem(AUTH_SESSION_KEY, 'true');
        } catch {
          // ignore
        }
      }
      set(() => ({ isAuthenticated: true }));
      return true;
    }
    return false;
  },

  logout: () => {
    if (typeof window !== 'undefined') {
      try {
        sessionStorage.removeItem(AUTH_SESSION_KEY);
      } catch {
        // ignore
      }
    }
    set(() => ({ isAuthenticated: false }));
  },

  setSettingsLoading: (loading) => set(() => ({ isSettingsLoading: loading })),
  setSettingsError: (error) => set(() => ({ settingsError: error })),
});
