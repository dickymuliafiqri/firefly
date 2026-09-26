import { create } from 'zustand';
import { persist, createJSONStorage } from 'zustand/middleware';

interface AuthState {
  /** Session token from POST /api/auth/login, or operator admin token. */
  token: string;
  setToken: (t: string) => void;
  clearToken: () => void;
}

/** Token persisted in localStorage — never transmitted anywhere except the gateway itself. */
export const useAuthStore = create<AuthState>()(
  persist(
    (set) => ({
      token: '',
      setToken: (t) => set({ token: t }),
      clearToken: () => set({ token: '' }),
    }),
    { name: 'firefly-admin-token', storage: createJSONStorage(() => localStorage) },
  ),
);

export function getAdminToken(): string {
  return useAuthStore.getState().token;
}
