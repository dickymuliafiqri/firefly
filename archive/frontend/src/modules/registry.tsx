import { lazy } from 'react';
import type { FireflyModuleDefinition } from './types';
import {
  Activity,
  Server,
  Layers,
  Users,
  BarChart3,
  Sliders,
  Wrench,
  Database,
} from 'lucide-react';

// Dynamic code-split imports for all 8 modules (Vercel Best Practice: bundle-dynamic-imports)
const OverviewView = lazy(() => import('./1-overview/OverviewView'));
const UpstreamsView = lazy(() => import('./2-upstreams/UpstreamsView'));
const ModelsView = lazy(() => import('./3-models/ModelsView'));
const TenantsView = lazy(() => import('./4-tenants/TenantsView'));
const TelemetryView = lazy(() => import('./5-telemetry/TelemetryView'));
const SettingsView = lazy(() => import('./6-settings/SettingsView'));
const ToolsView = lazy(() => import('./7-tools/ToolsView'));
const ProvidersView = lazy(() => import('./8-providers/ProvidersView'));

/**
 * Pluggable Module Registry for Firefly
 * Every module provides an isolated chunk, lazy component, and proactive preload handler.
 *
 * This is the single source of truth for navigation: the header tabs, the keyboard
 * hotkeys, and the router outlet all derive from these keys and `order` values, so
 * adding a module requires touching exactly one entry here.
 */
export const MODULE_REGISTRY = {
  overview: {
    title: 'Overview',
    description: 'Living network canvas and real-time inference telemetry',
    order: 1,
    icon: Activity,
    component: OverviewView,
    preload: () => import('./1-overview/OverviewView'),
  },
  upstreams: {
    title: 'Upstreams',
    description: 'Upstream provider fleet, circuit breakers, and KeyRing rotation',
    order: 2,
    icon: Server,
    component: UpstreamsView,
    preload: () => import('./2-upstreams/UpstreamsView'),
  },
  models: {
    title: 'Models',
    description: 'Virtual model routing catalog and intelligent fallback chains',
    order: 3,
    icon: Layers,
    component: ModelsView,
    preload: () => import('./3-models/ModelsView'),
  },
  tenants: {
    title: 'Tenants',
    description: 'Tenant authentication, token-bucket rate limits, and model access',
    order: 4,
    icon: Users,
    component: TenantsView,
    preload: () => import('./4-tenants/TenantsView'),
  },
  telemetry: {
    title: 'Telemetry',
    description: 'Historical token throughput, latency percentiles, and error gauges',
    order: 5,
    icon: BarChart3,
    component: TelemetryView,
    preload: () => import('./5-telemetry/TelemetryView'),
  },
  settings: {
    title: 'Settings',
    description: 'Global gateway configuration and declarative raw JSON editor',
    order: 6,
    icon: Sliders,
    component: SettingsView,
    preload: () => import('./6-settings/SettingsView'),
  },
  tools: {
    title: 'Tools',
    description: 'Interactive tooling: SSE chat testing and gateway benchmarking',
    order: 7,
    icon: Wrench,
    component: ToolsView,
    preload: () => import('./7-tools/ToolsView'),
  },
  providers: {
    title: 'Providers',
    description: 'Stored provider credential pools and key lifecycle management',
    order: 8,
    icon: Database,
    component: ProvidersView,
    preload: () => import('./8-providers/ProvidersView'),
  },
} satisfies Record<string, FireflyModuleDefinition>;

/** Every registry key is a valid route/tab identity. */
export type TabId = keyof typeof MODULE_REGISTRY;

/** A module definition joined with its registry key. */
export type RegisteredModule = FireflyModuleDefinition & { id: TabId };

/** Registry entries flattened into sorted modules, each carrying its own id. */
export const ORDERED_MODULES: readonly RegisteredModule[] = (Object.keys(MODULE_REGISTRY) as TabId[])
  .map((id) => ({ ...MODULE_REGISTRY[id], id }))
  .toSorted((a, b) => a.order - b.order);

/** Header navigation item — label and order are projected from the registry. */
export interface NavTab {
  id: TabId;
  label: string;
}

export const NAV_TABS: readonly NavTab[] = ORDERED_MODULES.map(({ id, title }) => ({
  id,
  label: title,
}));
