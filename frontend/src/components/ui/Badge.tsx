import React from 'react';
import { cn } from '@/lib/utils';

export type BadgeVariant = 'emerald' | 'amber' | 'rose' | 'cyan' | 'violet' | 'neutral';

export interface BadgeProps extends React.HTMLAttributes<HTMLSpanElement> {
  variant?: BadgeVariant;
  dot?: boolean;
  pulse?: boolean;
  children: React.ReactNode;
}

const VARIANT_STYLES: Record<BadgeVariant, { bg: string; border: string; text: string; dot: string }> = {
  emerald: {
    bg: 'bg-transparent',
    border: 'border-emerald-500/20',
    text: 'text-emerald-400/90',
    dot: 'bg-emerald-400',
  },
  amber: {
    bg: 'bg-transparent',
    border: 'border-amber-500/20',
    text: 'text-amber-400/90',
    dot: 'bg-amber-400',
  },
  rose: {
    bg: 'bg-transparent',
    border: 'border-rose-500/20',
    text: 'text-rose-400/90',
    dot: 'bg-rose-400',
  },
  cyan: {
    bg: 'bg-transparent',
    border: 'border-white/[0.08]',
    text: 'text-neutral-300',
    dot: 'bg-neutral-400',
  },
  violet: {
    bg: 'bg-transparent',
    border: 'border-white/[0.08]',
    text: 'text-neutral-300',
    dot: 'bg-neutral-400',
  },
  neutral: {
    bg: 'bg-transparent',
    border: 'border-white/[0.08]',
    text: 'text-neutral-400',
    dot: 'bg-neutral-500',
  },
};

/**
 * Minimalist Non-Intrusive Badge
 * Strips cartoonish rainbow backgrounds and animated pings in favor of subtle hairline borders.
 * Vercel React Best Practices: rerender-memo
 */
export const Badge = React.memo(function Badge({
  variant = 'neutral',
  dot = false,
  pulse = false,
  className,
  children,
  ...props
}: BadgeProps) {
  const styles = VARIANT_STYLES[variant];

  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 px-2 py-0.5 rounded text-[11px] font-mono border select-none',
        styles.bg,
        styles.border,
        styles.text,
        className
      )}
      {...props}
    >
      {dot ? (
        <span
          className={cn(
            'h-1.5 w-1.5 rounded-full flex-shrink-0',
            styles.dot
          )}
        />
      ) : null}
      {children}
    </span>
  );
});
