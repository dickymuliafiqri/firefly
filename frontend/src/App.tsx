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

  // 1. Cek sesi saat mount
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
        // 401 = sesi mati. Putus jaringan bukan sesi invalid — token tetap disimpan.
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

  // 2. Auth Guard: Jika belum login dan bukan di halaman login, redirect ke login
  useEffect(() => {
    if (!authChecked) return;
    if (!token && pageId !== 'login') {
      navigate('login', { redirect: pageId });
    }
  }, [authChecked, token, pageId]);

  // 3. Masuk halaman: judul dokumen + scroll ke atas + tutup nav mobile
  useEffect(() => {
    document.title = 'Firefly — ' + def.title;
    window.scrollTo(0, 0);
    closeSidebar();
  }, [def.title, closeSidebar]);

  // Halaman login mandiri: render tanpa shell/sidebar
  if (pageId === 'login') {
    return (
      <>
        <LoginPage />
        <ToastHost />
      </>
    );
  }

  // Tanpa sesi / sesi sedang diverifikasi: tampilkan papan tunggu singkat
  // (shell tidak boleh berkedip sebelum guard redirect ke #login).
  if (!token || !authChecked) {
    return (
      <div className="login-screen" aria-busy="true">
        <span className="faint">Memeriksa sesi…</span>
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

