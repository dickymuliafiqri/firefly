import type { PageDef } from "@/registry";
import type { ReactNode } from "react";
import { SkyBackground } from "@/components/shell/SkyBackground";
import { Sidebar } from "@/components/shell/Sidebar";
import { Topbar } from "@/components/shell/Topbar";
import { PendingChangesBar } from "@/components/shell/PendingChangesBar";
import { ToastHost } from "@/components/ui/ToastHost";
import { SIDEBAR_GROUPS } from "@/registry";

// Injected at build time from package.json (see vite.config.ts `define`).
// CI stamps package.json from the release tag, so this mirrors `firefly -version`.
// A semver value gets the `v` prefix; an already-tagged value (e.g. `dev-abc123`)
// is shown verbatim, matching the Go binary's string exactly.
const VERSION = /^\d/.test(__APP_VERSION__) ? `v${__APP_VERSION__}` : __APP_VERSION__;

interface AppShellProps {
  page: PageDef;
  children: ReactNode;
}

export function AppShell({ page, children }: AppShellProps) {
  const displayTitle =
    page.id === "upstream-editor" ? "Upstreams / Editor" : page.title;

  return (
    <div className="app">
      {/* Global sky — atmosphere across all pages (Decision B, DESIGN_RULES §7) */}
      <SkyBackground />

      <Sidebar groups={SIDEBAR_GROUPS} activeId={page.id} version={VERSION} />

      <div className="shell">
        <Topbar title={displayTitle} />
        <main className="content">{children}</main>
      </div>

      <ToastHost />
      <PendingChangesBar />
    </div>
  );
}
