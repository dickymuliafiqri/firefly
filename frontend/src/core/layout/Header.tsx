import React from 'react';
import { useAudioState } from '@/core/audio/useAudioState';
import { useIsAuthenticated, useStoreActions } from '@/core/state/store';
import { Lock, Unlock } from 'lucide-react';
import { cn } from '@/lib/utils';

export type TabId =
  | 'overview'
  | 'upstreams'
  | 'models'
  | 'tenants'
  | 'telemetry'
  | 'settings'
  | 'playground';

export interface TabItem {
  id: TabId;
  label: string;
}

export const TABS: readonly TabItem[] = [
  { id: 'overview', label: 'Overview' },
  { id: 'upstreams', label: 'Upstreams' },
  { id: 'models', label: 'Models' },
  { id: 'tenants', label: 'Tenants' },
  { id: 'telemetry', label: 'Telemetry' },
  { id: 'settings', label: 'Settings' },
  { id: 'playground', label: 'Playground' },
];

export interface HeaderProps {
  activeTab: TabId;
  onTabChange: (tab: TabId) => void;
  onPreloadTab?: (tab: TabId) => void;
  isBackendHealthy?: boolean;
}

export const Header = React.memo(function Header({
  activeTab,
  onTabChange,
  onPreloadTab,
}: HeaderProps) {
  const { isPlaying, toggle } = useAudioState();
  const isAuthenticated = useIsAuthenticated();
  const { logout, addToast } = useStoreActions();

  return (
    <header className="w-full pt-6 px-6 sm:px-10 lg:px-16 flex items-center justify-between relative z-40 select-none bg-transparent">
      {/* Brand: firefly bare text */}
      <div className="flex items-center gap-2 text-white font-semibold tracking-tight text-sm">
        <button
          onClick={() => onTabChange('overview')}
          className="hover:text-neutral-300 transition-colors bg-transparent border-none p-0 cursor-pointer text-white font-semibold"
        >
          firefly
        </button>
      </div>

      {/* Navigation */}
      <nav className="flex items-center gap-4 sm:gap-6 text-xs sm:text-sm font-normal">
        {TABS.map((tab) => {
          const isActive = activeTab === tab.id;
          return (
            <button
              key={tab.id}
              onClick={() => onTabChange(tab.id)}
              onMouseEnter={() => onPreloadTab?.(tab.id)}
              className={cn(
                'nav-item transition-colors cursor-pointer bg-transparent border-none p-0 select-none',
                isActive
                  ? 'text-white font-medium'
                  : 'text-neutral-400 hover:text-neutral-200'
              )}
            >
              {tab.label}
            </button>
          );
        })}

        {/* Relaxing Lofi Music Toggle Button matching index.html */}
        <button
          id="musicToggleBtn"
          onClick={toggle}
          className={cn(
            'flex items-center gap-2 text-xs font-mono text-neutral-400 hover:text-neutral-200 transition-all cursor-pointer py-1 px-2.5 rounded-full hover:bg-white/5 border border-transparent hover:border-white/10 group select-none',
            isPlaying && 'music-playing'
          )}
          title="Toggle Nocturnal Lo-Fi Ambience"
        >
          <span
            id="musicBars"
            className="flex items-end gap-[2px] h-3.5 w-3.5 pb-[1px] opacity-60 group-hover:opacity-100 transition-opacity"
          >
            <span className="lofi-bar-1 w-[2px] h-[3px] bg-neutral-400 group-hover:bg-neutral-200 rounded-full transition-all" />
            <span className="lofi-bar-2 w-[2px] h-[8px] bg-neutral-400 group-hover:bg-neutral-200 rounded-full transition-all" />
            <span className="lofi-bar-3 w-[2px] h-[5px] bg-neutral-400 group-hover:bg-neutral-200 rounded-full transition-all" />
            <span className="lofi-bar-4 w-[2px] h-[3px] bg-neutral-400 group-hover:bg-neutral-200 rounded-full transition-all" />
          </span>
          <span id="musicStatusText" className="tracking-wide text-[11px] sm:text-xs">
            {isPlaying ? 'Lofi Night (On)' : 'Music Off'}
          </span>
        </button>

        {/* Auth Lock / Sign In Status Button */}
        {isAuthenticated ? (
          <button
            onClick={() => {
              logout();
              onTabChange('overview');
              addToast({
                title: 'Session Locked',
                message: 'Dashboard locked. Returned to overview.',
                type: 'info',
              });
            }}
            className="flex items-center gap-1.5 text-xs font-mono text-neutral-400 hover:text-rose-400 transition-colors cursor-pointer py-1 px-2.5 rounded-full hover:bg-white/5 border border-white/[0.08] select-none"
            title="Lock Dashboard Session (Active)"
          >
            <Unlock className="w-3 h-3 text-emerald-400" />
            <span className="tracking-wide text-[11px] sm:text-xs text-neutral-300 hover:text-rose-400">Lock</span>
          </button>
        ) : (
          <button
            onClick={() => onTabChange('settings')}
            className="flex items-center gap-1.5 text-xs font-mono text-neutral-400 hover:text-white transition-colors cursor-pointer py-1 px-2.5 rounded-full hover:bg-white/5 border border-white/[0.08] select-none"
            title="Authenticate to Access Protected Modules"
          >
            <Lock className="w-3 h-3 text-neutral-500" />
            <span className="tracking-wide text-[11px] sm:text-xs">Sign In</span>
          </button>
        )}
      </nav>
    </header>
  );
});
