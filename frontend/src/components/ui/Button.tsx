import type { ButtonHTMLAttributes, ReactNode } from 'react';
import { cn } from '@/lib/utils';

type Variant = 'primary' | 'secondary' | 'ghost';

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant;
  leftIcon?: ReactNode;
}

export function Button({ variant = 'secondary', leftIcon, className, children, type, ...rest }: ButtonProps) {
  return (
    <button type={type ?? 'button'} className={cn('btn', `btn-${variant}`, className)} {...rest}>
      {leftIcon}
      {children}
    </button>
  );
}
