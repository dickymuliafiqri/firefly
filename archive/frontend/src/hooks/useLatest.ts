import { useRef, useLayoutEffect, useEffect } from 'react';

const useIsomorphicLayoutEffect = typeof window !== 'undefined' ? useLayoutEffect : useEffect;

/**
 * Keeps a ref updated with the latest value without triggering re-renders.
 * Standard pattern from Vercel React Best Practices: advanced-use-latest.
 */
export function useLatest<T>(value: T) {
  const ref = useRef(value);
  useIsomorphicLayoutEffect(() => {
    ref.current = value;
  }, [value]);
  return ref;
}
