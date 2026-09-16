import { cn } from './lib/utils';
import React, { Suspense, useEffect, useTransition, useState } from 'react';
import { Shell } from './core/layout/Shell';
import type { TabId } from './core/layout/Header';
import { MODULE_REGISTRY } from './modules/registry';
import { ModuleSkeleton } from './modules/ModuleSkeleton';
import {
  useActiveTab,
  useSetActiveTab,
  useAdminToken,
  useSetAdminToken,
  useIsAuthenticated,
  useActiveModal,
  useActiveDrawer,
  useDrawerPayload,
  useUpstreamBreakers,
  useStoreActions,
} from './core/state/store';
import { useSettingsQuery, useHealthQuery, useLiveTelemetryStream, useTelemetryQuery } from './services/api';
import { Modal } from './components/ui/Modal';
import { LoginModal } from './components/ui/LoginModal';
import { Drawer } from './components/ui/Drawer';
import { Button } from './components/ui/Button';
import { RefreshCw } from 'lucide-react';

import { useKeyboardShortcuts } from './hooks/useKeyboardShortcuts';
import { AudioManager } from './core/audio/AudioManager';

const CircuitBreakerStatusBadge = React.memo(function CircuitBreakerStatusBadge({
  upstreamName,
  breakers,
}: {
  upstreamName: string;
  breakers: Record<string, 'OPEN' | 'CLOSED' | 'HALF-OPEN'>;
}) {
  const st = (upstreamName ? breakers[upstreamName] : null) || 'CLOSED';
  const isClosed = st === 'CLOSED';
  const isHalfOpen = st === 'HALF-OPEN';
  return (
    <div className="flex items-center gap-2">
      <span
        className={cn(
          'w-1.5 h-1.5 rounded-full',
          isClosed
            ? 'bg-emerald-400 shadow-[0_0_6px_rgba(52,211,153,0.8)]'
            : isHalfOpen
            ? 'bg-amber-400'
            : 'bg-neutral-600'
        )}
      />
      <span
        className={cn(
          'font-medium',
          isClosed ? 'text-emerald-400/90' : isHalfOpen ? 'text-amber-400/90' : 'text-neutral-400'
        )}
      >
        STATE: {st}
      </span>
      <span className="text-neutral-500 text-[11px]">
        {isClosed
          ? '(Circuit Healthy / Passing Traffic)'
          : isHalfOpen
          ? '(Trial Recovery / Half-Open)'
          : '(Circuit Tripped / Open)'}
      </span>
    </div>
  );
});

export default function App() {
  useLiveTelemetryStream();
  useTelemetryQuery();

  const activeTab = useActiveTab();
  const rawSetActiveTab = useSetActiveTab();
  const [, startTransition] = useTransition();

  const adminToken = useAdminToken();
  const setAdminToken = useSetAdminToken();
  const isAuthenticated = useIsAuthenticated();
  const [isLoginModalOpen, setIsLoginModalOpen] = useState(false);
  const [pendingTab, setPendingTab] = useState<TabId | null>(null);

  const activeModal = useActiveModal();
  const activeDrawer = useActiveDrawer();
  const drawerPayload = useDrawerPayload() as Record<string, unknown> | null;
  const upstreamBreakers = useUpstreamBreakers();
  const { setSettings, closeModal, closeDrawer, addToast, login, verifySession } = useStoreActions();

  // Verify existing session against backend on mount
  useEffect(() => {
    verifySession();
  }, [verifySession]);

  // Tab transition wrapper with Auth Guard for non-overview routes
  const handleTabChange = (tab: TabId) => {
    if (tab !== 'overview' && !isAuthenticated) {
      setPendingTab(tab);
      setIsLoginModalOpen(true);
      return;
    }
    startTransition(() => {
      rawSetActiveTab(tab);
    });
  };

  // Safe redirect to overview if session is not authenticated while on protected tab
  useEffect(() => {
    if (!isAuthenticated && activeTab !== 'overview') {
      rawSetActiveTab('overview');
    }
  }, [isAuthenticated, activeTab, rawSetActiveTab]);

  // Centralized global keyboard navigation (Vercel Best Practice: client-event-listeners)
  useKeyboardShortcuts({
    onTabSelect: handleTabChange,
    onToggleAudio: () => {
      AudioManager.getInstance().toggle();
    },
    onTriggerPulse: () => {
      window.dispatchEvent(new CustomEvent('firefly:pulse'));
      addToast({
        title: 'Photon Pulse Triggered',
        message: 'Broadcasting test pulse across upstream nodes (Hotkey: P)',
        type: 'info',
      });
    },
    onTogglePauseCanvas: () => {
      window.dispatchEvent(new CustomEvent('firefly:toggle-pause'));
      addToast({
        title: 'Canvas Simulation Toggled',
        message: 'Particle physics pause/resume triggered (Hotkey: Space)',
        type: 'info',
      });
    },
    onCloseOverlays: () => {
      closeModal();
      closeDrawer();
    },
  });

  // TanStack Query for Health & Settings
  const { data: healthData } = useHealthQuery();
  const { data: settingsData, isLoading: isSettingsLoading, refetch: refetchSettings } =
    useSettingsQuery();

  const isBackendHealthy = healthData?.status === 'ok';

  // Sync settings into Zustand when loaded
  useEffect(() => {
    if (settingsData) {
      setSettings(settingsData);
    }
  }, [settingsData, setSettings]);

  // Resolve active module definition from registry
  const activeModule = MODULE_REGISTRY[activeTab] || MODULE_REGISTRY.overview;
  const ActiveComponent = activeModule.component;

  return (
    <>
      <Shell
        activeTab={activeTab}
        onTabChange={handleTabChange}
        onPreloadTab={(tab) => MODULE_REGISTRY[tab]?.preload()}
        isBackendHealthy={isBackendHealthy}
      >
        {/* Dynamic Module Outlet with Suspense boundary (Vercel Best Practice: async-suspense-boundaries) */}
        <Suspense fallback={<ModuleSkeleton />}>
          <ActiveComponent />
        </Suspense>
      </Shell>

      {/* Navigation Login Authentication Modal */}
      <LoginModal
        isOpen={isLoginModalOpen || activeModal === 'login'}
        onClose={() => {
          setIsLoginModalOpen(false);
          closeModal();
          setPendingTab(null);
        }}
        targetTab={pendingTab}
        onLogin={login}
        onSuccess={(target) => {
          setIsLoginModalOpen(false);
          closeModal();
          setPendingTab(null);
          if (target) {
            startTransition(() => {
              rawSetActiveTab(target);
            });
          }
          addToast({
            title: 'Authentication Successful',
            message: 'Dashboard access unlocked.',
            type: 'success',
          });
        }}
      />

      {/* Admin Token Modal */}
      <Modal
        isOpen={activeModal === 'admin-token'}
        onClose={closeModal}
        title="Admin Token Authentication"
        description="Provide the FIREFLY_ADMIN_TOKEN required to manage configuration on the Go API plane."
        size="md"
      >
        <div className="flex flex-col gap-4 font-mono text-xs">
          <div className="flex flex-col gap-1.5">
            <label className="text-neutral-400 font-medium">Admin Bearer Token</label>
            <input
              type="password"
              placeholder="e.g. sk-admin-secret..."
              defaultValue={adminToken}
              onChange={(e) => setAdminToken(e.target.value)}
              className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
            />
          </div>

          <div className="flex items-center justify-between gap-3 pt-2">
            <Button
              variant="minimal"
              size="sm"
              onClick={() => {
                refetchSettings();
                addToast({
                  title: 'Refetching Settings',
                  message: 'Synchronizing configuration from /api/settings...',
                  type: 'info',
                });
              }}
              isLoading={isSettingsLoading}
              leftIcon={<RefreshCw className="w-3.5 h-3.5 text-neutral-400 group-hover:text-white transition-colors" />}
            >
              Sync
            </Button>

            <Button
              variant="minimal"
              size="sm"
              onClick={() => {
                closeModal();
                addToast({
                  title: 'Token Stored',
                  message: 'Admin token saved for authenticated requests.',
                  type: 'success',
                });
              }}
            >
              Save Token
            </Button>
          </div>
        </div>
      </Modal>

      {/* Upstream Inspector Drawer */}
      <Drawer
        isOpen={activeDrawer === 'upstream-detail'}
        onClose={closeDrawer}
        title={`Upstream: ${drawerPayload?.name || 'Inspector'}`}
        description="Detailed telemetry, circuit breaker status, and credential health."
        width="md"
      >
        <div className="flex flex-col gap-3 font-mono text-xs">
          <div className="p-3.5 rounded-xl bg-transparent border border-white/[0.06] flex flex-col gap-1.5">
            <span className="text-[10px] text-neutral-500 uppercase tracking-wider">Protocol</span>
            <span className="text-neutral-200">OpenAI Wire Compatible</span>
          </div>

          <div className="p-3.5 rounded-xl bg-transparent border border-white/[0.06] flex flex-col gap-1.5">
            <span className="text-[10px] text-neutral-500 uppercase tracking-wider">Circuit Breaker</span>
            <CircuitBreakerStatusBadge
              upstreamName={String(drawerPayload?.name || '')}
              breakers={upstreamBreakers}
            />
          </div>

          <div className="p-3.5 rounded-xl bg-transparent border border-white/[0.06] flex flex-col gap-1.5">
            <span className="text-[10px] text-neutral-500 uppercase tracking-wider">Latency</span>
            <span className="text-neutral-200 tabular-nums">~178 ms (Healthy)</span>
          </div>
        </div>
      </Drawer>

      {/* Log Inspector Drawer */}
      <Drawer
        isOpen={activeDrawer === 'log-detail'}
        onClose={closeDrawer}
        title="Request Telemetry Inspector"
        description="Detailed trace metrics of outbound LLM inference request."
        width="md"
      >
        <div className="flex flex-col gap-3 font-mono text-xs">
          <div className="p-3.5 rounded-xl bg-transparent border border-white/[0.06] flex flex-col gap-1.5">
            <span className="text-[10px] text-neutral-500 uppercase">Endpoint & Method</span>
            <div className="text-white font-medium flex items-center gap-2">
              <span className="text-neutral-400 font-semibold">{String(drawerPayload?.method || 'POST')}</span>
              <span>{String(drawerPayload?.path || '/v1/chat/completions')}</span>
            </div>
          </div>

          <div className="p-3.5 rounded-xl bg-transparent border border-white/[0.06] flex flex-col gap-1.5">
            <span className="text-[10px] text-neutral-500 uppercase">Model & Upstream</span>
            <div className="text-neutral-300">
              Model: <span className="text-white">{String(drawerPayload?.model || '')}</span>
            </div>
            <div className="text-neutral-400">
              Host: <span className="text-neutral-200">{String(drawerPayload?.upstream || '')}</span>
            </div>
            {drawerPayload?.keyRef ? (
              <div className="text-neutral-400">
                Credential: <span className="text-neutral-200">{String(drawerPayload.keyRef)}</span>
              </div>
            ) : null}
          </div>

          <div className="p-3.5 rounded-xl bg-transparent border border-white/[0.06] flex flex-col gap-1.5">
            <span className="text-[10px] text-neutral-500 uppercase">Status & Latency</span>
            <div className="flex items-center gap-3">
              <span className={cn('tabular-nums font-medium', Number(drawerPayload?.status) < 400 ? 'text-emerald-400/90' : 'text-rose-400/90')}>
                HTTP {String(drawerPayload?.status || '200')}
              </span>
              <span className="text-neutral-400 tabular-nums">
                {String(drawerPayload?.durationMs || 0)}ms
              </span>
            </div>
          </div>
        </div>
      </Drawer>
    </>
  );
}
