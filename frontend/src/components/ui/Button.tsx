import React from 'react';
import { Loader2 } from 'lucide-react';
import { cn } from '@/lib/utils';

export type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger' | 'minimal';
export type ButtonSize = 'sm' | 'md' | 'lg';

export interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  isLoading?: boolean;
  leftIcon?: React.ReactNode;
  rightIcon?: React.ReactNode;
  children: React.ReactNode;
}

const VARIANT_CLASSES: Record<ButtonVariant, string> = {
  primary:
    'bg-biolum-aura/15 border-biolum-aura/40 text-biolum-glow hover:bg-biolum-aura/25 hover:border-biolum-aura/60 shadow-biolum-sm font-medium',
  secondary:
    'bg-slate-800/80 border-white/10 text-slate-200 hover:bg-slate-700/80 hover:border-white/20 hover:text-white',
  ghost:
    'bg-transparent border-transparent text-slate-400 hover:text-slate-100 hover:bg-white/5',
  danger:
    'bg-rose-500/15 border-rose-500/30 text-rose-300 hover:bg-rose-500/25 hover:border-rose-500/50',
  minimal:
    'bg-transparent border-white/[0.08] text-neutral-300 hover:text-white hover:bg-white/[0.04] hover:border-white/[0.20] active:bg-white/[0.08] shadow-none',
};

const SIZE_CLASSES: Record<ButtonSize, string> = {
  sm: 'px-2.5 py-1 text-xs rounded-lg gap-1.5',
  md: 'px-3.5 py-1.5 text-xs rounded-xl gap-2',
  lg: 'px-5 py-2.5 text-sm rounded-xl gap-2.5',
};

export const Button = React.memo(function Button({
  variant = 'secondary',
  size = 'md',
  isLoading = false,
  leftIcon,
  rightIcon,
  disabled,
  className,
  children,
  ...props
}: ButtonProps) {
  return (
    <button
      disabled={disabled || isLoading}
      className={cn(
        'inline-flex items-center justify-center font-mono border transition-all duration-150 select-none cursor-pointer group',
        'active:scale-[0.98] disabled:opacity-50 disabled:pointer-events-none disabled:active:scale-100',
        'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-biolum-aura',
        VARIANT_CLASSES[variant],
        SIZE_CLASSES[size],
        className
      )}
      {...props}
    >
      {isLoading ? (
        <Loader2 className="w-3.5 h-3.5 animate-spin" />
      ) : leftIcon ? (
        <span>{leftIcon}</span>
      ) : null}
      <span>{children}</span>
      {!isLoading && rightIcon ? <span>{rightIcon}</span> : null}
    </button>
  );
});
