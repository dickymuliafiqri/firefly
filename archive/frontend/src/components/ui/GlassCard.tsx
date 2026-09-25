import React from 'react';
import { cn } from '@/lib/utils';

export interface GlassCardProps extends React.HTMLAttributes<HTMLDivElement> {
  variant?: 'default' | 'surface' | 'panel' | 'interactive';
  children: React.ReactNode;
}

/**
 * Reusable Minimalist Transparent Surface Card
 * Harmonized with Overview and Upstream design language.
 * Vercel React Best Practices: rerender-memo & rerender-no-inline-components
 */
export const GlassCard = React.memo(function GlassCard({
  variant = 'default',
  className,
  children,
  ...props
}: GlassCardProps) {
  return (
    <div
      className={cn(
        'rounded-xl border border-white/[0.06] bg-transparent shadow-none transition-all duration-200',
        variant === 'interactive' &&
          'hover:border-white/[0.14] hover:bg-white/[0.015] cursor-pointer',
        className
      )}
      {...props}
    >
      {children}
    </div>
  );
});
