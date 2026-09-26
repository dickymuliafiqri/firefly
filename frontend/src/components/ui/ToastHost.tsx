import { X } from 'lucide-react';
import { useUiStore, type Toast as ToastItem } from '@/state/store';
import { cn } from '@/lib/utils';

const TONE_CLASS: Record<ToastItem['type'], string> = {
  info: 'info',
  success: 'ok',
  error: 'danger',
};

function ToastRow({ item }: { item: ToastItem }) {
  const dismissToast = useUiStore((s) => s.dismissToast);
  return (
    <div
      className="card"
      style={{
        display: 'flex',
        alignItems: 'flex-start',
        gap: 10,
        padding: '12px 14px',
        borderLeft: `3px solid var(--${TONE_CLASS[item.type]})`,
      }}
      role="status"
    >
      <div style={{ flex: 1 }}>
        <div style={{ fontSize: 13, fontWeight: 600 }}>{item.title}</div>
        {item.message ? <div style={{ fontSize: 12, color: 'var(--muted)', marginTop: 2 }}>{item.message}</div> : null}
      </div>
      <button className="icon-btn" style={{ width: 26, height: 26 }} onClick={() => dismissToast(item.id)} aria-label="Dismiss notification">
        <X aria-hidden="true" />
      </button>
    </div>
  );
}

/** Bottom-right toast — solid raised + status left-border, auto-close 4s (DESIGN_RULES §4). */
export function ToastHost() {
  const toasts = useUiStore((s) => s.toasts);
  if (toasts.length === 0) return null;

  return (
    <div
      style={{
        position: 'fixed',
        right: 16,
        bottom: 16,
        display: 'flex',
        flexDirection: 'column',
        gap: 8,
        zIndex: 70,
        width: 320,
      }}
      className={cn('toast-host')}
      aria-live="polite"
    >
      {toasts.map((item) => (
        <ToastRow key={item.id} item={item} />
      ))}
    </div>
  );
}
