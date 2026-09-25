import { useState, useEffect, useCallback } from 'react';
import { AudioManager } from './AudioManager';

/**
 * Hook to consume and control the procedural Lofi Audio Engine.
 * Follows Vercel React Best Practices:
 * - rerender-derived-state: Subscribes only to atomic isPlaying boolean
 * - client-event-listeners: Deduplicates and cleans up global hotkey listener ('m')
 */
export function useAudioState() {
  const [isPlaying, setIsPlaying] = useState<boolean>(() => AudioManager.getInstance().getIsPlaying());

  useEffect(() => {
    const manager = AudioManager.getInstance();
    const unsubscribe = manager.subscribe((playing) => {
      setIsPlaying(playing);
    });

    return () => {
      unsubscribe();
    };
  }, []);

  const toggle = useCallback(() => {
    AudioManager.getInstance().toggle();
  }, []);

  const start = useCallback(() => {
    AudioManager.getInstance().start().catch(console.error);
  }, []);

  const stop = useCallback(() => {
    AudioManager.getInstance().stop();
  }, []);

  return {
    isPlaying,
    toggle,
    start,
    stop,
  };
}
