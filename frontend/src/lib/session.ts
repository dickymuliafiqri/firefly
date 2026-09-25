import { getAdminToken, useAuthStore } from '@/state/auth';
import { useUiStore } from '@/state/store';
import { navigate, parseHash } from '@/lib/router';

/**
 * Satu tendangan saja sampai ada token baru. Request paralel yang sama-sama
 * ditolak 401 tidak boleh menumpuk toast atau menulis hash berulang kali.
 */
let kicking = false;

useAuthStore.subscribe((state, prev) => {
  if (state.token && state.token !== prev.token) kicking = false;
});

/**
 * Sesi dashboard ditolak server (HTTP 401, atau verify `authenticated: false`
 * padahal token masih tersimpan — badge "SESSION INVALID").
 * Token lokal dibuang dan pengguna dikirim ke halaman login.
 */
export function handleSessionInvalid() {
  if (kicking) return;

  const { path } = parseHash();
  const token = getAdminToken();
  if (!token) {
    if (path !== 'login') navigate('login', { redirect: path || 'overview' });
    return;
  }

  kicking = true;
  useAuthStore.getState().clearToken();
  useUiStore.getState().pushToast({
    type: 'error',
    title: 'Sesi tidak valid',
    message: 'Sesi berakhir atau tidak dikenali. Masuk kembali untuk melanjutkan.',
  });
  if (path !== 'login') {
    navigate('login', { redirect: path || 'overview' });
  }
}
