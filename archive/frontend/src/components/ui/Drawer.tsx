import React, { useEffect } from 'react';
import { createPortal } from 'react-dom';
import { X } from 'lucide-react';
import { cn } from '@/lib/utils';

export interface DrawerProps {
  isOpen: boolean;
  onClose: () => void;
  title?: React.ReactNode;
  description?: string;
  width?: 'md' | 'lg' | 'xl';
  children: React.ReactNode;
}

const WIDTH_CLASSES = {
  md: 'max-w-md',
  lg: 'max-w-xl',
  xl: 'max-w-2xl',
};

export const Drawer = React.memo(function Drawer({
  isOpen,
  onClose,
  title,
  description,
  width = 'lg',
  children,
}: DrawerProps) {
  useEffect(() => {
    if (!isOpen) return;

    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        onClose();
      }
    };

    const originalOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';

    window.addEventListener('keydown', handleKeyDown);
    return () => {
      window.removeEventListener('keydown', handleKeyDown);
      document.body.style.overflow = originalOverflow;
    };
  }, [isOpen, onClose]);

  // Vercel React Best Practice: rendering-conditional-render
  if (!isOpen || typeof document === 'undefined') {
    return null;
  }

  return createPortal(
    <div className="fixed inset-0 z-[100] overflow-hidden">
      {/* Backdrop */}
      <div
        className="fixed inset-0 bg-black/80 backdrop-blur-sm transition-opacity animate-in fade-in duration-200"
        onClick={onClose}
        aria-hidden="true"
      />

      {/* Drawer Slide-in Panel */}
      <div className="fixed inset-y-0 right-0 flex max-w-full pl-0 sm:pl-10 z-[101]">
        <div
          role="dialog"
          aria-modal="true"
          className={cn(
            'w-screen bg-[#090b10] border-l border-white/[0.08] shadow-2xl flex flex-col animate-in slide-in-from-right duration-200',
            WIDTH_CLASSES[width]
          )}
        >
          {/* Header */}
          <div className="flex items-center justify-between p-4 sm:p-6 border-b border-white/[0.04]">
            <div>
              {title ? (
                <h3 className="text-sm font-semibold text-white tracking-tight">
                  {title}
                </h3>
              ) : null}
              {description ? (
                <p className="text-xs text-neutral-500 mt-0.5">{description}</p>
              ) : null}
            </div>

            <button
              onClick={onClose}
              className="rounded-lg p-1.5 text-neutral-400 hover:text-white hover:bg-white/[0.05] transition-colors focus-visible:outline-none cursor-pointer"
              aria-label="Close panel"
            >
              <X className="w-4 h-4" />
            </button>
          </div>

          {/* Scrollable Content Body */}
          <div className="flex-1 overflow-y-auto p-4 sm:p-6">{children}</div>
        </div>
      </div>
    </div>,
    document.body
  );
});
