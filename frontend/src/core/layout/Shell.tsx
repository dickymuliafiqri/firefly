import React, { useEffect } from 'react';
import { Header } from './Header';
import { useReducedMotion } from '@/hooks/useReducedMotion';
import type { TabId } from '@/modules/registry';
import { GITHUB_URL, SUPPORT_URL } from '@/core/constants';
import { Github, Heart } from 'lucide-react';

export interface ShellProps {
  activeTab: TabId;
  onTabChange: (tab: TabId) => void;
  onPreloadTab?: (tab: TabId) => void;
  children: React.ReactNode;
  isBackendHealthy?: boolean;
}

export function Shell({
  activeTab,
  onTabChange,
  onPreloadTab,
  children,
  isBackendHealthy = true,
}: ShellProps) {
  const reducedMotion = useReducedMotion();

  // Shooting Star System matching index.html lines 1894-1918
  useEffect(() => {
    if (reducedMotion) return;

    const createShootingStar = () => {
      const star = document.createElement('div');
      star.className = 'shooting-star';

      const startX = Math.random() * window.innerWidth;
      const startY = Math.random() * (window.innerHeight * 0.3); // Only in upper sky

      star.style.left = `${startX}px`;
      star.style.top = `${startY}px`;
      star.style.animation = `shootingStar ${2 + Math.random() * 1.5}s ease-out forwards`;

      document.body.appendChild(star);
      setTimeout(() => star.remove(), 3500);
    };

    const interval = setInterval(() => {
      if (Math.random() < 0.15) {
        createShootingStar();
      }
    }, 8000);

    return () => clearInterval(interval);
  }, [reducedMotion]);

  return (
    <div className="min-h-[100dvh] flex flex-col font-sans transition-colors duration-200 antialiased relative selection:bg-lime-400/20 selection:text-lime-200">
      {/* Top Minimalist Transparent Header */}
      <Header
        activeTab={activeTab}
        onTabChange={onTabChange}
        onPreloadTab={onPreloadTab}
        isBackendHealthy={isBackendHealthy}
      />

      {/* Main Container matching index.html line 282 */}
      <main className="flex-1 w-full px-4 sm:px-10 lg:px-16 py-4 sm:py-6 space-y-6 sm:space-y-8 relative z-10">
        {children}
      </main>

      {/* Footer: project links (spacer matching index.html line 355) */}
      <footer className="w-full mt-auto relative z-10 py-6 flex items-center justify-center gap-4 font-mono text-[11px] text-neutral-500">
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
      </footer>
    </div>
  );
}
