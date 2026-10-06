import type { ReactNode } from 'react';
import type { LucideIcon } from 'lucide-react';
import {
  Activity,
  Coins,
  Database,
  Gauge,
  Globe,
  Layers,
  Lock,
  MessageCircle,
  Plug,
  Radar,
  Server,
  Sliders,
  Terminal,
  Users,
  Zap,
} from 'lucide-react';
import { OverviewPage } from '@/pages/OverviewPage';
import { TelemetryPage } from '@/pages/TelemetryPage';
import { UsagePage } from '@/pages/UsagePage';
import { UpstreamsPage } from '@/pages/UpstreamsPage';
import { ProvidersPage } from '@/pages/ProvidersPage';
import { ModelsPage } from '@/pages/ModelsPage';
import { TenantsPage } from '@/pages/TenantsPage';
import { ConnectPage } from '@/pages/ConnectPage';
import { SettingsPage } from '@/pages/SettingsPage';
import { ChatPage } from '@/pages/ChatPage';
import { BenchmarkPage } from '@/pages/BenchmarkPage';
import { QuotaPage } from '@/pages/QuotaPage';
import { ConsolePage } from '@/pages/ConsolePage';
import { VisualizerPage } from '@/pages/VisualizerPage';
import { WarpPage } from '@/pages/WarpPage';

import { LoginPage } from '@/pages/LoginPage';
import { UpstreamEditorPage } from '@/pages/UpstreamEditorPage';

export type PageId =
  | 'overview'
  | 'telemetry'
  | 'usage'
  | 'upstreams'
  | 'providers'
  | 'models'
  | 'tenants'
  | 'connect'
  | 'settings'
  | 'chat'
  | 'benchmark'
  | 'quota'
  | 'console'
  | 'visualizer'
  | 'warp'
  | 'login'
  | 'upstream-editor';

export interface PageDef {
  id: PageId;
  title: string;
  description: string;
  group: 'MONITORING' | 'SERVICES' | 'PROXIES' | 'CONFIGURATION' | 'TOOLS' | 'AUTH' | 'EDITOR';
  icon: LucideIcon;
  element: () => ReactNode;
}

export const PAGES: Record<PageId, PageDef> = {
  overview:   { id: 'overview',   title: 'Overview',   description: 'Gateway health at a glance.',                     group: 'MONITORING',    icon: Gauge,         element: () => <OverviewPage /> },
  telemetry:  { id: 'telemetry',  title: 'Telemetry',  description: 'Historical throughput & latency.',                group: 'MONITORING',    icon: Activity,      element: () => <TelemetryPage /> },
  usage:      { id: 'usage',      title: 'Usage',      description: 'Token ledger per tenant, model, and credential.', group: 'MONITORING',    icon: Coins,         element: () => <UsagePage /> },
  upstreams:  { id: 'upstreams',  title: 'Upstreams',  description: 'Provider fleet and circuit breakers.',            group: 'SERVICES',      icon: Server,        element: () => <UpstreamsPage /> },
  providers:  { id: 'providers',  title: 'Providers',  description: 'Stored credential pools and key lifecycle.',      group: 'SERVICES',      icon: Database,      element: () => <ProvidersPage /> },
  models:     { id: 'models',     title: 'Models',     description: 'Direct models and virtual combos.',               group: 'SERVICES',      icon: Layers,        element: () => <ModelsPage /> },
  tenants:    { id: 'tenants',    title: 'Tenants',    description: 'Tenant auth, rate limits, and access.',           group: 'SERVICES',      icon: Users,         element: () => <TenantsPage /> },
  connect:    { id: 'connect',    title: 'Connect',    description: 'Wire AI agents to this gateway.',                 group: 'SERVICES',      icon: Plug,          element: () => <ConnectPage /> },
  settings:   { id: 'settings',   title: 'Settings',   description: 'Global gateway configuration.',                   group: 'CONFIGURATION', icon: Sliders,       element: () => <SettingsPage /> },
  warp:      { id: 'warp',      title: 'Warp',      description: 'Cloudflare WARP egress pool & rotation.',           group: 'PROXIES',      icon: Globe,         element: () => <WarpPage /> },
  chat:       { id: 'chat',       title: 'Chat',       description: 'SSE chat tester with stream inspector.',          group: 'TOOLS',         icon: MessageCircle, element: () => <ChatPage /> },
  benchmark:  { id: 'benchmark',  title: 'Benchmark',  description: 'Gateway throughput benchmarking.',                group: 'TOOLS',         icon: Zap,           element: () => <BenchmarkPage /> },
  quota:      { id: 'quota',      title: 'Quota',      description: 'Tenant quota tracker with top-up.',               group: 'MONITORING',    icon: Coins,         element: () => <QuotaPage /> },
  console:    { id: 'console',    title: 'Console',    description: 'Structured request execution log.',               group: 'MONITORING',    icon: Terminal,      element: () => <ConsolePage /> },
  visualizer: { id: 'visualizer', title: 'Visualizer', description: 'Live routing traces as pluggable diagrams.',      group: 'MONITORING',    icon: Radar,         element: () => <VisualizerPage /> },
  login:      { id: 'login',      title: 'Sign in',    description: 'Dashboard master password.',                      group: 'AUTH',          icon: Lock,          element: () => <LoginPage /> },
  'upstream-editor': { id: 'upstream-editor', title: 'Upstream Editor', description: 'Full-page upstream configuration.', group: 'EDITOR', icon: Server, element: () => <UpstreamEditorPage /> },
};

export const GROUP_ORDER: Array<PageDef['group']> = ['MONITORING', 'SERVICES', 'PROXIES', 'CONFIGURATION', 'TOOLS'];

/** Sidebar groups — MONITORING/SERVICES/PROXIES/CONFIGURATION/TOOLS. AUTH & EDITOR are routable but intentionally unlisted. */
export const SIDEBAR_GROUPS: Array<{ label: PageDef['group']; pages: PageDef[] }> = GROUP_ORDER.map(
  (label) => ({
    label,
    pages: Object.values(PAGES).filter((p) => p.group === label),
  }),
);

export const PAGE_IDS = Object.keys(PAGES) as PageId[];
