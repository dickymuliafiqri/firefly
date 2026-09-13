import React, { useEffect } from 'react';
import { Header, type TabId } from './Header';

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
  // Shooting Star System matching index.html lines 1894-1918
  useEffect(() => {
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
  }, []);

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

      {/* Footer Spacer matching index.html line 355 */}
      <footer className="w-full mt-auto relative z-10 py-6 pointer-events-none" />
    </div>
  );
}
