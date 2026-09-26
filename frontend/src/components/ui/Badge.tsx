import type { ReactNode } from 'react';
import { cn } from '@/lib/utils';

export type Tone = 'ok' | 'warn' | 'danger' | 'info' | 'neutral';

export interface BadgeProps {
  tone?: Tone;
  children: ReactNode;
  className?: string;
  title?: string;
}

/** Status always includes text — never communicated by color alone (DESIGN_RULES §8). */
export function Badge({ tone = 'neutral', children, className, title }: BadgeProps) {
  return (
    <span className={cn('badge', tone, className)} title={title}>
      {children}
    </span>
  );
}

