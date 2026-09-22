import React, { useState, useEffect } from 'react';
import { useAudioState } from '@/core/audio/useAudioState';
import { useIsAuthenticated, useStoreActions } from '@/core/state/store';
import { Lock, Unlock, Menu, X } from 'lucide-react';
import { cn } from '@/lib/utils';
import { APP_VERSION } from '@/core/constants';

export type TabId =
  | 'overview'
  | 'upstreams'
  | 'models'
  | 'tenants'
  | 'telemetry'
  | 'settings'
  | 'playground'
  | 'providers';

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
  { id: 'providers', label: 'Providers' },
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
  const [isMobileMenuOpen, setIsMobileMenuOpen] = useState(false);

  // Close mobile menu on desktop breakpoint resize
  useEffect(() => {
    const handleResize = () => {
      if (window.innerWidth >= 768) {
        setIsMobileMenuOpen(false);
      }
    };
    window.addEventListener('resize', handleResize);
    return () => window.removeEventListener('resize', handleResize);
  }, []);

  // Close mobile menu on Escape key press
  useEffect(() => {
    if (!isMobileMenuOpen) return;
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setIsMobileMenuOpen(false);
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [isMobileMenuOpen]);

  // Lock body scroll when mobile navigation is open
  useEffect(() => {
    if (isMobileMenuOpen) {
      const originalOverflow = document.body.style.overflow;
      document.body.style.overflow = 'hidden';
      return () => {
        document.body.style.overflow = originalOverflow;
      };
    }
  }, [isMobileMenuOpen]);

  return (
    <header className="w-full pt-5 sm:pt-6 px-4 sm:px-10 lg:px-16 flex items-center justify-between relative z-40 select-none bg-transparent">
      {/* Brand: firefly bare text with version */}
      <div className="flex items-center gap-2 tracking-tight text-sm">
        <button
          onClick={() => {
            onTabChange('overview');
            setIsMobileMenuOpen(false);
          }}
          className="hover:text-neutral-300 transition-colors bg-transparent border-none p-0 cursor-pointer text-white font-semibold"
        >
          firefly
        </button>
        <span
          className="text-xs font-mono font-normal text-neutral-500 select-none tracking-normal"
          title={`Version ${APP_VERSION}`}
          aria-label={`Firefly ${APP_VERSION}`}
        >
          {APP_VERSION}
        </span>
      </div>

      {/* Desktop Navigation (visible on md and up) */}
      <nav className="hidden md:flex items-center gap-4 sm:gap-6 text-xs sm:text-sm font-normal">
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
            isPlaying ? 'music-playing' : undefined
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

      {/* Mobile Burger Button (visible below md) */}
      <div className="flex items-center gap-2 md:hidden">
        <button
          type="button"
          onClick={() => setIsMobileMenuOpen((open) => !open)}
          className={cn(
            'flex items-center justify-center w-8 h-8 rounded-full border transition-all cursor-pointer select-none',
            isMobileMenuOpen
              ? 'border-white/20 bg-white/10 text-white'
              : 'border-white/[0.08] hover:border-white/20 bg-transparent hover:bg-white/5 text-neutral-400 hover:text-white'
          )}
          aria-label={isMobileMenuOpen ? 'Close navigation menu' : 'Open navigation menu'}
          aria-expanded={isMobileMenuOpen}
        >
          {isMobileMenuOpen ? (
            <X className="w-4 h-4 text-neutral-200" />
          ) : (
            <Menu className="w-4 h-4 text-neutral-300" />
          )}
        </button>
      </div>

      {/* Mobile Navigation Dropdown & Backdrop */}
      {isMobileMenuOpen ? (
        <>
          {/* Subtle darkened backdrop overlay */}
          <div
            className="fixed inset-0 bg-black/60 backdrop-blur-sm z-40 md:hidden transition-opacity duration-200"
            onClick={() => setIsMobileMenuOpen(false)}
            aria-hidden="true"
          />

          {/* Bioluminescent Dropdown Menu Panel */}
          <div className="fixed inset-x-4 top-[64px] z-50 md:hidden animate-in fade-in slide-in-from-top-2 duration-200">
            <div className="bg-[#090b10]/95 backdrop-blur-xl border border-white/[0.08] rounded-2xl p-4 shadow-2xl shadow-black/80 space-y-3 font-mono text-xs">
              {/* Navigation Tabs List */}
              <div className="flex flex-col space-y-1">
                {TABS.map((tab) => {
                  const isActive = activeTab === tab.id;
                  return (
                    <button
                      key={tab.id}
                      onClick={() => {
                        onTabChange(tab.id);
                        setIsMobileMenuOpen(false);
                      }}
                      onMouseEnter={() => onPreloadTab?.(tab.id)}
                      className={cn(
                        'flex items-center justify-between w-full px-3 py-2 rounded-xl text-xs transition-all cursor-pointer select-none text-left',
                        isActive
                          ? 'bg-white/[0.08] text-white font-medium border border-white/[0.08]'
                          : 'text-neutral-400 hover:text-neutral-200 hover:bg-white/[0.03] border border-transparent'
                      )}
                    >
                      <span>{tab.label}</span>
                      {isActive ? (
                        <span className="w-1.5 h-1.5 rounded-full bg-emerald-400 shadow-[0_0_6px_rgba(52,211,153,0.8)]" />
                      ) : null}
                    </button>
                  );
                })}
              </div>

              {/* Minimalist Divider */}
              <div className="h-px bg-white/[0.06] my-1" />

              {/* Secondary Actions: Lofi Night & Session Lock / Sign In */}
              <div className="flex items-center justify-between pt-1 gap-2">
                <button
                  onClick={toggle}
                  className={cn(
                    'flex items-center gap-2 text-xs font-mono text-neutral-400 hover:text-neutral-200 transition-all cursor-pointer py-1.5 px-3 rounded-full hover:bg-white/5 border border-white/[0.08] group select-none',
                    isPlaying ? 'music-playing text-neutral-200 bg-white/[0.04]' : undefined
                  )}
                  title="Toggle Nocturnal Lo-Fi Ambience"
                >
                  <span className="flex items-end gap-[2px] h-3.5 w-3.5 pb-[1px] opacity-60 group-hover:opacity-100 transition-opacity">
                    <span className="lofi-bar-1 w-[2px] h-[3px] bg-neutral-400 group-hover:bg-neutral-200 rounded-full transition-all" />
                    <span className="lofi-bar-2 w-[2px] h-[8px] bg-neutral-400 group-hover:bg-neutral-200 rounded-full transition-all" />
                    <span className="lofi-bar-3 w-[2px] h-[5px] bg-neutral-400 group-hover:bg-neutral-200 rounded-full transition-all" />
                    <span className="lofi-bar-4 w-[2px] h-[3px] bg-neutral-400 group-hover:bg-neutral-200 rounded-full transition-all" />
                  </span>
                  <span className="tracking-wide text-[11px]">
                    {isPlaying ? 'Lofi (On)' : 'Music Off'}
                  </span>
                </button>

                {isAuthenticated ? (
                  <button
                    onClick={() => {
                      logout();
                      onTabChange('overview');
                      setIsMobileMenuOpen(false);
                      addToast({
                        title: 'Session Locked',
                        message: 'Dashboard locked. Returned to overview.',
                        type: 'info',
                      });
                    }}
                    className="flex items-center gap-1.5 text-xs font-mono text-neutral-400 hover:text-rose-400 transition-colors cursor-pointer py-1.5 px-3 rounded-full hover:bg-white/5 border border-white/[0.08] select-none"
                    title="Lock Dashboard Session (Active)"
                  >
                    <Unlock className="w-3 h-3 text-emerald-400" />
                    <span className="tracking-wide text-[11px] text-neutral-300 hover:text-rose-400">Lock</span>
                  </button>
                ) : (
                  <button
                    onClick={() => {
                      onTabChange('settings');
                      setIsMobileMenuOpen(false);
                    }}
                    className="flex items-center gap-1.5 text-xs font-mono text-neutral-400 hover:text-white transition-colors cursor-pointer py-1.5 px-3 rounded-full hover:bg-white/5 border border-white/[0.08] select-none"
                    title="Authenticate to Access Protected Modules"
                  >
                    <Lock className="w-3 h-3 text-neutral-500" />
                    <span className="tracking-wide text-[11px]">Sign In</span>
                  </button>
                )}
              </div>
            </div>
          </div>
        </>
      ) : null}
    </header>
  );
});
