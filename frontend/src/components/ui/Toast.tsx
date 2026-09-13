import React, { useEffect } from 'react';
import { CheckCircle2, AlertTriangle, XCircle, Info, X } from 'lucide-react';
import { useToasts, useStoreActions } from '@/core/state/store';
import type { ToastItem } from '@/core/state/uiSlice';
import { cn } from '@/lib/utils';

const TOAST_ICONS = {
  success: <CheckCircle2 className="w-4 h-4 text-emerald-400" />,
  warning: <AlertTriangle className="w-4 h-4 text-amber-400" />,
  error: <XCircle className="w-4 h-4 text-rose-400" />,
  info: <Info className="w-4 h-4 text-cyan-400" />,
};

const TOAST_BORDER = {
  success: 'border-emerald-500/20 bg-emerald-950/20',
  warning: 'border-amber-500/20 bg-amber-950/20',
  error: 'border-rose-500/20 bg-rose-950/20',
  info: 'border-cyan-500/20 bg-cyan-950/20',
};

const ToastMessage = React.memo(function ToastMessage({
  toast,
  onDismiss,
}: {
  toast: ToastItem;
  onDismiss: (id: string) => void;
}) {
  const { id, title, message, type = 'info', durationMs = 4000 } = toast;

  useEffect(() => {
    const timer = setTimeout(() => {
      onDismiss(id);
    }, durationMs);

    return () => clearTimeout(timer);
  }, [id, durationMs, onDismiss]);

  return (
    <div
      role="alert"
      className={cn(
        'relative flex items-start gap-3 p-4 rounded-xl backdrop-blur-xl border shadow-xl text-xs font-mono max-w-sm w-full transition-all duration-200 animate-in slide-in-from-bottom-2',
        TOAST_BORDER[type]
      )}
    >
      <div className="pt-0.5">{TOAST_ICONS[type]}</div>
      <div className="flex-1">
        <div className="font-semibold text-white tracking-tight">{title}</div>
        <div className="text-slate-300 text-[11px] mt-0.5 leading-relaxed">{message}</div>
      </div>
      <button
        onClick={() => onDismiss(id)}
        className="text-slate-400 hover:text-white p-1 rounded transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-biolum-aura"
        aria-label="Dismiss toast"
      >
        <X className="w-3.5 h-3.5" />
      </button>
    </div>
  );
});

export const ToastContainer = React.memo(function ToastContainer() {
  const toasts = useToasts();
  const { removeToast } = useStoreActions();

  // Vercel React Best Practice: rendering-conditional-render
  return toasts.length > 0 ? (
    <aside
      aria-label="Notifications"
      className="fixed bottom-12 right-6 z-[110] flex flex-col gap-2 pointer-events-auto"
    >
      {toasts.map((toast) => (
        <ToastMessage key={toast.id} toast={toast} onDismiss={removeToast} />
      ))}
    </aside>
  ) : null;
});
