import type { StateCreator } from 'zustand';
import type { UpstreamDTO, ModelDTO, TenantDTO, ComboDTO, SettingsDTO } from '@/services/schema';
import { loginApi, verifyAuthApi, logoutApi, updatePasswordApi, updateBreakerApi } from '@/services/api';

export interface SettingsSlice {
  upstreams: UpstreamDTO[];
  models: ModelDTO[];
  tenants: TenantDTO[];
  combos: ComboDTO[];
  adminToken: string;
  isAuthenticated: boolean;
  isSettingsLoading: boolean;
  settingsError: string | null;
  lastSavedAt: number | null;
  upstreamBreakers: Record<string, 'OPEN' | 'CLOSED' | 'HALF-OPEN'>;

  setSettings: (settings: SettingsDTO) => void;
  setUpstreamBreakers: (breakers: Record<string, 'OPEN' | 'CLOSED' | 'HALF-OPEN'>) => void;
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
  login: (password: string) => Promise<boolean>;
  verifySession: () => Promise<boolean>;
  logout: () => Promise<void>;
  updatePassword: (currentPassword: string, newPassword: string) => Promise<{ ok: boolean; message?: string }>;
  setSettingsLoading: (loading: boolean) => void;
  setSettingsError: (error: string | null) => void;
}

const SESSION_TOKEN_KEY = 'firefly_session_token_v1';
const AUTH_SESSION_KEY = 'firefly_auth_session_v1';

function loadPersistedSessionToken(): string {
  if (typeof window === 'undefined') return '';
  try {
    return sessionStorage.getItem(SESSION_TOKEN_KEY) || '';
  } catch {
    return '';
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

export const createSettingsSlice: StateCreator<SettingsSlice, [], [], SettingsSlice> = (set, get) => ({
  upstreams: [],
  models: [],
  tenants: [],
  combos: [],
  adminToken: loadPersistedSessionToken(),
  isAuthenticated: loadPersistedAuthSession() && !!loadPersistedSessionToken(),
  isSettingsLoading: false,
  settingsError: null,
  lastSavedAt: null,
  upstreamBreakers: {},

  setUpstreamBreakers: (breakers) => set(() => ({ upstreamBreakers: breakers })),

  setSettings: (settings) =>
    set((state) => {
      const serverUpstreams = settings.upstreams || [];
      const updatedBreakers: Record<string, 'OPEN' | 'CLOSED' | 'HALF-OPEN'> = {
        ...state.upstreamBreakers,
      };

      for (const u of serverUpstreams) {
        if (!updatedBreakers[u.name]) {
          updatedBreakers[u.name] = u.enabled === false ? 'OPEN' : 'CLOSED';
        }
      }

      return {
        upstreams: serverUpstreams,
        models: settings.models || [],
        tenants: settings.tenants || [],
        combos: settings.combos || [],
        upstreamBreakers: updatedBreakers,
        lastSavedAt: Date.now(),
        settingsError: null,
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
    set((state) => ({
      upstreams: state.upstreams.filter((u) => u.name !== name),
      combos: state.combos.map((c) => ({
        ...c,
        models: c.models.filter((m) => {
          const modelDef = state.models.find((mod) => mod.public_name === m);
          return modelDef ? modelDef.upstream !== name : true;
        }),
      })),
    })),

  setBreakerState: (name, breakerState) => {
    set((state) => ({
      upstreamBreakers: { ...state.upstreamBreakers, [name]: breakerState },
    }));
    updateBreakerApi(name, breakerState, get().adminToken).catch(() => {});
  },

  toggleUpstreamBreaker: (name) => {
    const current = get().upstreamBreakers[name] || 'CLOSED';
    const nextState: 'OPEN' | 'CLOSED' = current === 'OPEN' ? 'CLOSED' : 'OPEN';
    set((state) => {
      const nextBreakers: Record<string, 'OPEN' | 'CLOSED' | 'HALF-OPEN'> = {
        ...state.upstreamBreakers,
        [name]: nextState,
      };
      const nextUpstreams = state.upstreams.map((u) => {
        if (u.name === name) {
          return { ...u, enabled: nextState === 'CLOSED' };
        }
        return u;
      });
      return {
        upstreamBreakers: nextBreakers,
        upstreams: nextUpstreams,
      };
    });
    updateBreakerApi(name, nextState, get().adminToken).catch(() => {
      // Revert optimistic update if API call failed (e.g. 401 Unauthorized)
      set((state) => ({
        upstreamBreakers: {
          ...state.upstreamBreakers,
          [name]: current,
        },
        upstreams: state.upstreams.map((u) =>
          u.name === name ? { ...u, enabled: current === 'CLOSED' } : u
        ),
      }));
    });
  },

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
      combos: state.combos.map((c) => ({
        ...c,
        models: c.models.filter((m) => m !== publicName),
      })),
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
      try {
        if (token) {
          sessionStorage.setItem(SESSION_TOKEN_KEY, token);
        } else {
          sessionStorage.removeItem(SESSION_TOKEN_KEY);
        }
      } catch {}
    }
    set(() => ({ adminToken: token }));
  },

  login: async (password: string) => {
    try {
      const res = await loginApi(password);
      if (res && res.token) {
        if (typeof window !== 'undefined') {
          try {
            sessionStorage.setItem(SESSION_TOKEN_KEY, res.token);
            sessionStorage.setItem(AUTH_SESSION_KEY, 'true');
          } catch {}
        }
        set(() => ({
          adminToken: res.token,
          isAuthenticated: true,
        }));
        return true;
      }
      return false;
    } catch {
      return false;
    }
  },

  verifySession: async () => {
    const token = get().adminToken;
    if (!token) {
      if (get().isAuthenticated) {
        set(() => ({ isAuthenticated: false }));
      }
      return false;
    }
    try {
      const res = await verifyAuthApi(token);
      if (res && res.authenticated) {
        if (!get().isAuthenticated) {
          set(() => ({ isAuthenticated: true }));
        }
        return true;
      }
    } catch {}
    if (typeof window !== 'undefined') {
      try {
        sessionStorage.removeItem(SESSION_TOKEN_KEY);
        sessionStorage.removeItem(AUTH_SESSION_KEY);
      } catch {}
    }
    if (get().isAuthenticated || get().adminToken !== '') {
      set(() => ({ adminToken: '', isAuthenticated: false }));
    }
    return false;
  },

  logout: async () => {
    const token = get().adminToken;
    if (token) {
      try {
        await logoutApi(token);
      } catch {}
    }
    if (typeof window !== 'undefined') {
      try {
        sessionStorage.removeItem(SESSION_TOKEN_KEY);
        sessionStorage.removeItem(AUTH_SESSION_KEY);
      } catch {}
    }
    set(() => ({ adminToken: '', isAuthenticated: false }));
  },

  updatePassword: async (currentPassword: string, newPassword: string) => {
    const token = get().adminToken;
    try {
      const res = await updatePasswordApi(currentPassword, newPassword, token);
      return { ok: true, message: res.message || 'Password successfully updated' };
    } catch (err: any) {
      return { ok: false, message: err.message || 'Failed to update password' };
    }
  },

  setSettingsLoading: (loading) => set(() => ({ isSettingsLoading: loading })),
  setSettingsError: (error) => set(() => ({ settingsError: error })),
});
