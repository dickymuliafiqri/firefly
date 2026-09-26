import { getAdminToken, useAuthStore } from '@/state/auth';
import { useUiStore } from '@/state/store';
import { navigate, parseHash } from '@/lib/router';

/**
 * Single kick until a new token is provided. Parallel requests rejected with
 * 401 must not stack duplicate toasts or write the hash multiple times.
 */
let kicking = false;

useAuthStore.subscribe((state, prev) => {
  if (state.token && state.token !== prev.token) kicking = false;
});

/**
 * Dashboard session rejected by server (HTTP 401, or verify `authenticated: false`
 * while a token is still present — "SESSION INVALID" badge).
 * Local token is cleared and user is redirected to the login page.
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
    title: 'Invalid session',
    message: 'Session has expired or is unrecognized. Sign in again to continue.',
  });
  if (path !== 'login') {
    navigate('login', { redirect: path || 'overview' });
  }
}
