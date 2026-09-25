import { create } from 'zustand';
import { persist, createJSONStorage } from 'zustand/middleware';

interface AuthState {
  /** Session token dari POST /api/auth/login, atau admin token operator. */
  token: string;
  setToken: (t: string) => void;
  clearToken: () => void;
}

/** Token persist di localStorage — tidak pernah dikirim ke mana pun selain gateway sendiri. */
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
