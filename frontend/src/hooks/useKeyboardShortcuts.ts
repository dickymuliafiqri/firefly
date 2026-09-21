import { useEffect } from 'react';
import type { TabId } from '@/core/layout/Header';
import { useLatest } from './useLatest';

export interface KeyboardShortcutHandlers {
  onTabSelect: (tab: TabId) => void;
  onToggleAudio: () => void;
  onTriggerPulse: () => void;
  onTogglePauseCanvas: () => void;
  onCloseOverlays: () => void;
}

const TAB_KEYS: Record<string, TabId> = {
  '1': 'overview',
  '2': 'upstreams',
  '3': 'models',
  '4': 'tenants',
  '5': 'telemetry',
  '6': 'settings',
  '7': 'playground',
};

const NATIVE_ACTIVATION_TAGS = new Set(['button', 'summary']);

// `<button>` and friends activate on Space, so swallowing the key would break the
// most basic keyboard interaction on every focused control in the dashboard.
function handlesSpaceNatively(el: HTMLElement | null): boolean {
  if (!el) return false;
  const tag = el.tagName.toLowerCase();
  if (NATIVE_ACTIVATION_TAGS.has(tag)) return true;
  if (tag === 'a' && el.hasAttribute('href')) return true;
  return el.getAttribute('role') === 'button';
}

/**
 * useKeyboardShortcuts
 * Centralized single window event listener for global keyboard navigation.
 * Adheres strictly to Vercel React Best Practices:
 * - client-event-listeners (single listener avoiding listener leaks)
 * - advanced-use-latest (stable handler refs avoiding re-subscribing)
 */
export function useKeyboardShortcuts(handlers: KeyboardShortcutHandlers) {
  const latestHandlers = useLatest(handlers);

  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      // Don't capture shortcuts when user is focused in text inputs or editable elements
      const target = e.target as HTMLElement | null;
      if (target) {
        const tagName = target.tagName.toLowerCase();
        if (
          tagName === 'input' ||
          tagName === 'textarea' ||
          tagName === 'select' ||
          target.isContentEditable
        ) {
          // Allow Escape to close modals even from inputs
          if (e.key === 'Escape') {
            latestHandlers.current.onCloseOverlays();
          }
          return;
        }
      }

      // Ignore if modifier keys (Ctrl, Meta/Cmd, Alt) are held, unless Escape
      if (e.ctrlKey || e.metaKey || e.altKey) {
        return;
      }

      // 1. Tab Navigation: '1' through '7'
      const targetTab = TAB_KEYS[e.key];
      if (targetTab) {
        e.preventDefault();
        latestHandlers.current.onTabSelect(targetTab);
        return;
      }

      // 2. Audio Toggle: 'm' or 'M'
      if (e.key === 'm' || e.key === 'M') {
        e.preventDefault();
        latestHandlers.current.onToggleAudio();
        return;
      }

      // 3. Pulse / Ping Upstream: 'p' or 'P'
      if (e.key === 'p' || e.key === 'P') {
        e.preventDefault();
        latestHandlers.current.onTriggerPulse();
        return;
      }

      // 4. Pause / Resume Canvas Particles: Space
      if (e.key === ' ' || e.code === 'Space') {
        if (handlesSpaceNatively(target)) {
          return;
        }
        e.preventDefault();
        latestHandlers.current.onTogglePauseCanvas();
        return;
      }

      // 5. Close Overlays: Escape
      if (e.key === 'Escape') {
        e.preventDefault();
        latestHandlers.current.onCloseOverlays();
        return;
      }
    };

    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [latestHandlers]);
}
