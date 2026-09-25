import { useEffect, useState } from 'react';
import { Menu } from 'lucide-react';
import type { PageDef } from '@/registry';
import { navigate } from '@/lib/router';
import { useUiStore } from '@/state/store';

interface SidebarProps {
  groups: Array<{ label: string; pages: PageDef[] }>;
  activeId: string;
  version: string;
}

export function Sidebar({ groups, activeId, version }: SidebarProps) {
  const sidebarOpen = useUiStore((s) => s.sidebarOpen);
  const sidebarCollapsed = useUiStore((s) => s.sidebarCollapsed);
  const closeSidebar = useUiStore((s) => s.closeSidebar);

  return (
    <>
      <aside
        className={
          'sidebar' + (sidebarOpen ? ' open' : '') + (sidebarCollapsed ? ' collapsed' : '')
        }
        aria-label="Main navigation"
      >
        <div className="sidebar-brand">
          <a className="wordmark" href="#/overview" onClick={closeSidebar}>
            Firefly
          </a>
        </div>

        <nav className="sidebar-nav">
          {groups.map((group) => (
            <div className="nav-group" key={group.label}>
              <span className="nav-group-label">{group.label}</span>
              {group.pages.map((page) => {
                const isItemActive =
                  page.id === activeId ||
                  (activeId === 'upstream-editor' && page.id === 'upstreams');

                return (
                  <button
                    key={page.id}
                    className={'nav-item' + (isItemActive ? ' active' : '')}
                    aria-label={page.title}
                    aria-current={isItemActive ? 'page' : undefined}
                    onClick={() => {
                      navigate(page.id);
                      closeSidebar();
                    }}
                  >
                    <page.icon aria-hidden="true" />
                    <span className="nav-text">{page.title}</span>
                  </button>
                );
              })}
            </div>
          ))}
        </nav>

        <div className="sidebar-foot">
          {version} &middot; new
        </div>
      </aside>

      {/* Backdrop solid untuk drawer sidebar mobile */}
      {sidebarOpen ? (
        <div className="nav-backdrop" onClick={closeSidebar} aria-hidden="true" />
      ) : null}
    </>
  );
}

/** Tombol-tombol sidebar di topbar (dipisah agar toggle dekat tombol menu). */
export function SidebarToggles() {
  const sidebarOpen = useUiStore((s) => s.sidebarOpen);
  const toggleOpen = () => useUiStore.setState({ sidebarOpen: !sidebarOpen });
  const toggleCollapsed = useUiStore((s) => s.toggleCollapsed);

  // Window sempit: collapse desktop tidak berlaku (collapse hanya >=1024px)
  const [isNarrow, setIsNarrow] = useState(() => window.innerWidth < 1024);
  useEffect(() => {
    const onResize = () => setIsNarrow(window.innerWidth < 1024);
    window.addEventListener('resize', onResize);
    return () => window.removeEventListener('resize', onResize);
  }, []);

  return (
    <>
      <button
        className="icon-btn nav-toggle-mobile"
        onClick={toggleOpen}
        aria-label="Toggle navigation menu"
        aria-expanded={sidebarOpen}
      >
        <Menu aria-hidden="true" />
      </button>
      {isNarrow ? null : (
        <button className="icon-btn" onClick={toggleCollapsed} aria-label="Collapse sidebar">
          <Menu aria-hidden="true" />
        </button>
      )}
    </>
  );
}
