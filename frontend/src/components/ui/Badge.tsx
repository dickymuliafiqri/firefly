import type { ReactNode } from 'react';
import type { Tone } from '@/data/mock';
import { cn } from '@/lib/utils';

export interface BadgeProps {
  tone?: Tone;
  children: ReactNode;
  className?: string;
  title?: string;
}

/** Status selalu berteks — tidak pernah hanya lewat warna (DESIGN_RULES §8). */
export function Badge({ tone = 'neutral', children, className, title }: BadgeProps) {
  return (
    <span className={cn('badge', tone, className)} title={title}>
      {children}
    </span>
  );
}
