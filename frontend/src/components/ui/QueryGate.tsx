import type { ReactNode } from 'react';
import { ApiError } from '@/services/api';

export function QueryLoading({ label = 'Memuat data gateway…' }: { label?: string }) {
  return (
    <div className="card">
      <div className="card-body">
        <span className="faint" style={{ fontSize: 13 }}>{label}</span>
      </div>
    </div>
  );
}

export function QueryError({ error }: { error: unknown }) {
  const isAuth = error instanceof ApiError && error.status === 401;
  return (
    <div className="card" style={{ borderLeft: '3px solid var(--danger)' }}>
      <div className="card-body">
        <div style={{ fontSize: 13, fontWeight: 600 }}>Gagal memuat dari gateway</div>
        <div className="hint" style={{ marginTop: 4, fontSize: 12, color: 'var(--muted)' }}>
          {error instanceof Error ? error.message : 'Unknown error'}
        </div>
        {isAuth ? (
          <div className="hint" style={{ marginTop: 4, fontSize: 12, color: 'var(--faint)' }}>
            Buka Settings → Access untuk login dengan dashboard password.
          </div>
        ) : null}
      </div>
    </div>
  );
}

/** Render helper: loading → error → children(data). */
export function QueryGate({
  isLoading,
  error,
  children,
}: {
  isLoading: boolean;
  error: unknown;
  children: ReactNode;
}) {
  if (isLoading) return <QueryLoading />;
  if (error) return <QueryError error={error} />;
  return <>{children}</>;
}
