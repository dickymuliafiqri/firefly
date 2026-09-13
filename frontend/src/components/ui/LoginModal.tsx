import React, { useState, useEffect, useTransition } from 'react';
import { Modal } from './Modal';
import { Button } from './Button';
import { Eye, EyeOff, Lock, ArrowRight, Loader2 } from 'lucide-react';
import type { TabId } from '@/core/layout/Header';

export interface LoginModalProps {
  isOpen: boolean;
  onClose: () => void;
  targetTab: TabId | null;
  onSuccess: (targetTab: TabId) => void;
  onLogin: (password: string) => Promise<boolean> | boolean;
}

/**
 * LoginModal
 * Minimalist, non-slop authentication dialog presented to unauthenticated users
 * attempting to access protected dashboard routes.
 * Default backend master password: 12345678
 */
export const LoginModal = React.memo(function LoginModal({
  isOpen,
  onClose,
  targetTab,
  onSuccess,
  onLogin,
}: LoginModalProps) {
  const [password, setPassword] = useState('');
  const [showPassword, setShowPassword] = useState(false);
  const [error, setError] = useState(false);
  const [isSubmitting, startSubmitTransition] = useTransition();

  useEffect(() => {
    if (isOpen) {
      setPassword('');
      setError(false);
      setShowPassword(false);
    }
  }, [isOpen]);

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!password || isSubmitting) {
      setError(true);
      return;
    }

    startSubmitTransition(async () => {
      setError(false);
      try {
        const ok = await onLogin(password);
        if (ok) {
          setError(false);
          onSuccess(targetTab || 'upstreams');
        } else {
          setError(true);
        }
      } catch {
        setError(true);
      }
    });
  };

  const tabName = targetTab
    ? targetTab.charAt(0).toUpperCase() + targetTab.slice(1)
    : 'Dashboard';

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title={
        <div className="flex items-center gap-2 text-white font-mono text-xs uppercase tracking-wider">
          <Lock className="w-3.5 h-3.5 text-neutral-400" />
          <span>Backend Authorization Required</span>
        </div>
      }
      description={`Enter the backend access password to unlock ${tabName}. (Default: 12345678)`}
      size="sm"
    >
      <form onSubmit={handleSubmit} className="flex flex-col gap-4 font-mono text-xs">
        <div className="flex flex-col gap-1.5">
          <label className="text-neutral-400 font-medium">Access Password</label>
          <div className="relative">
            <input
              type={showPassword ? 'text' : 'password'}
              autoFocus
              disabled={isSubmitting}
              value={password}
              onChange={(e) => {
                setPassword(e.target.value);
                if (error) setError(false);
              }}
              placeholder="Enter password (default 12345678)..."
              className="w-full px-3 py-2 pr-9 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/30 disabled:opacity-50"
            />
            <button
              type="button"
              disabled={isSubmitting}
              onClick={() => setShowPassword((s) => !s)}
              className="absolute right-2.5 top-2.5 text-neutral-500 hover:text-white transition-colors cursor-pointer disabled:opacity-50"
              title={showPassword ? 'Hide password' : 'Show password'}
            >
              {showPassword ? <EyeOff className="w-3.5 h-3.5" /> : <Eye className="w-3.5 h-3.5" />}
            </button>
          </div>

          {error ? (
            <span className="text-[11px] text-rose-400">
              Incorrect password or unauthorized. Please try again.
            </span>
          ) : null}
        </div>

        <div className="flex items-center justify-end gap-2 pt-2 border-t border-white/[0.04]">
          <Button
            type="button"
            variant="minimal"
            size="sm"
            disabled={isSubmitting}
            onClick={onClose}
          >
            Cancel
          </Button>
          <Button
            type="submit"
            variant="minimal"
            size="sm"
            disabled={isSubmitting}
            rightIcon={isSubmitting ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <ArrowRight className="w-3.5 h-3.5" />}
          >
            {isSubmitting ? 'Verifying...' : 'Unlock'}
          </Button>
        </div>
      </form>
    </Modal>
  );
});
