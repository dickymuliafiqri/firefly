import React, { useState, useEffect } from 'react';
import { useAudioState } from '@/core/audio/useAudioState';
import { useIsAuthenticated, useStoreActions } from '@/core/state/store';
import { Lock, Unlock, Menu, X, Github, Heart } from 'lucide-react';
import { cn } from '@/lib/utils';
import { APP_VERSION, GITHUB_URL, SUPPORT_URL } from '@/core/constants';
import { NAV_TABS, type TabId } from '@/modules/registry';

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

  // Close mobile menu once the desktop navigation breakpoint (lg) is reached
  useEffect(() => {
    const handleResize = () => {
      if (window.innerWidth >= 1024) {
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
      <div className="flex items-center gap-2 tracking-tight text-sm shrink-0">
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

      {/* Desktop Navigation (visible on lg and up) */}
      <nav className="hidden lg:flex items-center gap-4 xl:gap-6 text-xs xl:text-sm font-normal min-w-0">
        {/* Horizontally scrollable tab strip — new registry modules extend it instead of wrapping the pills */}
        <div className="no-scrollbar flex items-center gap-4 xl:gap-6 min-w-0 overflow-x-auto">
          {NAV_TABS.map((tab) => {
            const isActive = activeTab === tab.id;
            return (
              <button
                key={tab.id}
                onClick={() => onTabChange(tab.id)}
                onMouseEnter={() => onPreloadTab?.(tab.id)}
                className={cn(
                  'nav-item whitespace-nowrap shrink-0 transition-colors cursor-pointer bg-transparent border-none p-0 select-none',
                  isActive
                    ? 'text-white font-medium'
                    : 'text-neutral-400 hover:text-neutral-200'
                )}
              >
                {tab.label}
              </button>
            );
          })}
        </div>

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
            {/* Short label while space is tight at lg, full label from xl upward */}
            <span className="xl:hidden">{isPlaying ? 'Lofi (On)' : 'Music Off'}</span>
            <span className="hidden xl:inline">{isPlaying ? 'Lofi Night (On)' : 'Music Off'}</span>
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

        {/* Project links: icon-only and xl+ only — below that the header has no spare room */}
        <a
          href={GITHUB_URL}
          target="_blank"
          rel="noopener noreferrer"
          title="Firefly on GitHub"
          aria-label="Firefly on GitHub"
          className="hidden xl:flex items-center justify-center w-7 h-7 rounded-full border border-transparent text-neutral-400 hover:text-white hover:bg-white/5 hover:border-white/10 transition-colors"
        >
          <Github className="w-3.5 h-3.5" />
        </a>

        {SUPPORT_URL ? (
          <a
            href={SUPPORT_URL}
            target="_blank"
            rel="noopener noreferrer"
            title="Support the project"
            aria-label="Support the project"
            className="hidden xl:flex items-center justify-center w-7 h-7 rounded-full border border-transparent text-neutral-400 hover:text-rose-300 hover:bg-white/5 hover:border-white/10 transition-colors"
          >
            <Heart className="w-3.5 h-3.5" />
          </a>
        ) : null}
      </nav>

      {/* Mobile Burger Button (visible below lg) */}
      <div className="flex items-center gap-2 lg:hidden">
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
            className="fixed inset-0 bg-black/60 backdrop-blur-sm z-40 lg:hidden transition-opacity duration-200"
            onClick={() => setIsMobileMenuOpen(false)}
            aria-hidden="true"
          />

          {/* Bioluminescent Dropdown Menu Panel (scrolls internally once the registry outgrows the viewport) */}
          <div className="fixed inset-x-4 top-[64px] z-50 lg:hidden max-h-[calc(100dvh-88px)] overflow-y-auto no-scrollbar animate-in fade-in slide-in-from-top-2 duration-200">
            <div className="bg-[#090b10]/95 backdrop-blur-xl border border-white/[0.08] rounded-2xl p-4 shadow-2xl shadow-black/80 space-y-3 font-mono text-xs">
              {/* Navigation Tabs List */}
              <div className="flex flex-col space-y-1">
                {NAV_TABS.map((tab) => {
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

              {/* Project links: GitHub & optional support page */}
              <div className="flex items-center justify-center gap-3 pt-1 text-[11px] text-neutral-500">
                <a
                  href={GITHUB_URL}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="flex items-center gap-1.5 hover:text-neutral-200 transition-colors"
                >
                  <Github className="w-3 h-3" />
                  <span>GitHub</span>
                </a>

                {SUPPORT_URL ? (
                  <>
                    <span aria-hidden="true" className="text-neutral-700">
                      ·
                    </span>
                    <a
                      href={SUPPORT_URL}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="flex items-center gap-1.5 hover:text-rose-300 transition-colors"
                    >
                      <Heart className="w-3 h-3" />
                      <span>Support</span>
                    </a>
                  </>
                ) : null}
              </div>
            </div>
          </div>
        </>
      ) : null}
    </header>
  );
});
