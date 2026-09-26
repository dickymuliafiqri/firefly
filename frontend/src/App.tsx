import { useEffect, useState } from 'react';
import { AppShell } from '@/components/shell/AppShell';
import { useHashPage, navigate } from '@/lib/router';
import { PAGE_IDS, PAGES } from '@/registry';
import { useAuthStore } from '@/state/auth';
import { useUiStore } from '@/state/store';
import { verifyAuthApi, ApiError } from '@/services/api';
import { handleSessionInvalid } from '@/lib/session';
import { LoginPage } from '@/pages/LoginPage';
import { ToastHost } from '@/components/ui/ToastHost';

export default function App() {
  const pageId = useHashPage(PAGE_IDS, 'overview');
  const def = PAGES[pageId] ?? PAGES.overview;
  const closeSidebar = useUiStore((s) => s.closeSidebar);

  const token = useAuthStore((s) => s.token);
  const [authChecked, setAuthChecked] = useState(false);

  // 1. Check session on mount
  useEffect(() => {
    let canceled = false;
    async function check() {
      if (!token) {
        setAuthChecked(true);
        return;
      }
      try {
        const res = await verifyAuthApi(token);
        if (!res.authenticated && !canceled) {
          handleSessionInvalid();
        }
      } catch (err) {
        // 401 = session dead. Network drop is not invalid session — keep token stored.
        if (!canceled && err instanceof ApiError && err.status === 401) {
          handleSessionInvalid();
        }
      } finally {
        if (!canceled) setAuthChecked(true);
      }
    }
    void check();
    return () => {
      canceled = true;
    };
  }, [token]);

  // 2. Auth Guard: If not logged in and not on login page, redirect to login
  useEffect(() => {
    if (!authChecked) return;
    if (!token && pageId !== 'login') {
      navigate('login', { redirect: pageId });
    }
  }, [authChecked, token, pageId]);

  // 3. Page mount: document title + scroll to top + close mobile sidebar
  useEffect(() => {
    document.title = 'Firefly — ' + def.title;
    window.scrollTo(0, 0);
    closeSidebar();
  }, [def.title, closeSidebar]);

  // Standalone login page: render without shell/sidebar
  if (pageId === 'login') {
    return (
      <>
        <LoginPage />
        <ToastHost />
      </>
    );
  }

  // Without session / verifying session: show brief loading state
  // (shell should not flicker before guard redirects to #login).
  if (!token || !authChecked) {
    return (
      <div className="login-screen" aria-busy="true">
        <span className="faint">Checking session…</span>
        <ToastHost />
      </div>
    );
  }

  return (
    <AppShell page={def}>
      {def.element()}
    </AppShell>
  );
}

export { navigate };

