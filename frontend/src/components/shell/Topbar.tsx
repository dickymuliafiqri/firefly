import { useEffect, useState } from 'react';
import { LogOut } from 'lucide-react';
import { SidebarToggles } from '@/components/shell/Sidebar';
import { useHealthQuery, logoutApi } from '@/services/api';
import { useAuthStore } from '@/state/auth';
import { useUiStore } from '@/state/store';
import { navigate } from '@/lib/router';

interface TopbarProps {
  title: string;
}

function pad(n: number) {
  return n < 10 ? '0' + n : '' + n;
}

export function Topbar({ title }: TopbarProps) {
  const [clock, setClock] = useState('');
  const health = useHealthQuery();
  const token = useAuthStore((s) => s.token);
  const clearToken = useAuthStore((s) => s.clearToken);
  const pushToast = useUiStore((s) => s.pushToast);

  const live = health.data?.status === 'ok' && !health.data.__mock;
  const mock = health.data?.__mock === true;
  const statusLabel = mock ? 'demo data' : live ? 'backend healthy' : 'backend offline';
  const statusColor = mock ? 'var(--info)' : live ? 'var(--ok)' : 'var(--danger)';

  useEffect(() => {
    const tick = () => {
      const d = new Date();
      setClock(pad(d.getHours()) + ':' + pad(d.getMinutes()) + ':' + pad(d.getSeconds()));
    };
    tick();
    const t = setInterval(tick, 1000);
    return () => clearInterval(t);
  }, []);

  async function logout() {
    try {
      if (token) await logoutApi(token);
    } catch {
      // best-effort — revoke local session regardless
    } finally {
      clearToken();
      pushToast({ type: 'success', title: 'Logout', message: 'Session revoked.' });
      navigate('login');
    }
  }

  return (
    <header className="topbar">
      <SidebarToggles />
      <h1 className="topbar-title">{title}</h1>
      <div className="topbar-right">
        <span
          className="backend-status"
          style={{ color: statusColor }}
          title={mock ? 'Gateway unreachable — displaying demo data' : undefined}
        >
          <span className="dot" style={{ background: statusColor }} />
          {statusLabel}
        </span>
        <span className="topbar-clock">{clock}</span>
        <button className="icon-btn" type="button" aria-label="Logout" title="Logout" onClick={() => void logout()}>
          <LogOut />
        </button>
      </div>
    </header>
  );
}
