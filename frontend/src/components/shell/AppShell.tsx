import type { PageDef } from "@/registry";
import type { ReactNode } from "react";
import { SkyBackground } from "@/components/shell/SkyBackground";
import { Sidebar } from "@/components/shell/Sidebar";
import { Topbar } from "@/components/shell/Topbar";
import { ToastHost } from "@/components/ui/ToastHost";
import { SIDEBAR_GROUPS } from "@/registry";

const VERSION = "v1.18.0";

interface AppShellProps {
  page: PageDef;
  children: ReactNode;
}

export function AppShell({ page, children }: AppShellProps) {
  const displayTitle =
    page.id === "upstream-editor" ? "Upstreams / Editor" : page.title;

  return (
    <div className="app">
      {/* Sky global — atmosfer di semua halaman (Keputusan B, DESIGN_RULES §7) */}
      <SkyBackground />

      <Sidebar groups={SIDEBAR_GROUPS} activeId={page.id} version={VERSION} />

      <div className="shell">
        <Topbar title={displayTitle} />
        <main className="content">{children}</main>
      </div>

      <ToastHost />
    </div>
  );
}
