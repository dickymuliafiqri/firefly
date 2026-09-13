import { clsx, type ClassValue } from 'clsx';
import { twMerge } from 'tailwind-merge';

/**
 * Merges Tailwind classes cleanly with clsx conditional handling
 */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
