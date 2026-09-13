import React, { useEffect, useRef, useState } from 'react';

export interface AnimatedCountUpProps {
  value: number;
  duration?: number;
  decimals?: number;
  prefix?: string;
  suffix?: string;
  formatter?: (val: number) => string;
  className?: string;
  id?: string;
}

/**
 * AnimatedCountUp — High-performance memoized countup animation.
 * Smoothly transitions from the previous value to the target value using cubic easing.
 *
 * Adheres to Vercel React Best Practices:
 * - rerender-memo: Isolated component preventing parent re-renders on animation frames.
 * - rerender-use-ref-transient-values: Animation timing and transient values stored in refs.
 */
export const AnimatedCountUp = React.memo(function AnimatedCountUp({
  value,
  duration = 800,
  decimals = 0,
  prefix = '',
  suffix = '',
  formatter,
  className,
  id,
}: AnimatedCountUpProps) {
  const [displayValue, setDisplayValue] = useState(value);
  const currentValRef = useRef(value);
  const animFrameRef = useRef<number | null>(null);

  useEffect(() => {
    const startVal = currentValRef.current;
    const targetVal = value;

    if (startVal === targetVal) {
      setDisplayValue(targetVal);
      return;
    }

    const startTime = performance.now();
    const diff = targetVal - startVal;

    // Cubic ease-out: starts fast, slows down softly
    const easeOutCubic = (t: number) => 1 - Math.pow(1 - t, 3);

    const step = (now: number) => {
      const elapsed = now - startTime;
      const progress = Math.min(1, Math.max(0, elapsed / duration));
      const eased = easeOutCubic(progress);
      const next = startVal + diff * eased;

      currentValRef.current = next;
      setDisplayValue(next);

      if (progress < 1) {
        animFrameRef.current = requestAnimationFrame(step);
      } else {
        currentValRef.current = targetVal;
        setDisplayValue(targetVal);
        animFrameRef.current = null;
      }
    };

    animFrameRef.current = requestAnimationFrame(step);

    return () => {
      if (animFrameRef.current !== null) {
        cancelAnimationFrame(animFrameRef.current);
        animFrameRef.current = null;
      }
    };
  }, [value, duration]);

  const formatted = formatter
    ? formatter(displayValue)
    : decimals > 0
    ? displayValue.toFixed(decimals)
    : Math.floor(displayValue).toLocaleString('en-US');

  return (
    <span id={id} className={className}>
      {prefix}
      {formatted}
      {suffix}
    </span>
  );
});
