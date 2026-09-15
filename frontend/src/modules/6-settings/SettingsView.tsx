import React, { useState, useEffect, useMemo, useCallback, useTransition } from 'react';
import { Button } from '@/components/ui/Button';
import {
  Sliders,
  Code,
  Shield,
  KeyRound,
  Database,
  Cpu,
  Eye,
  EyeOff,
  GitCommit,
  AlertOctagon,
  RefreshCw,
} from 'lucide-react';
import {
  useAdminToken,
  useSetAdminToken,
  useUpstreams,
  useModels,
  useCombos,
  useTenants,
  useAppStore,
  useStoreActions,
} from '@/core/state/store';
import { useSaveSettingsMutation, useSettingsQuery } from '@/services/api';
import { RawJsonEditor } from './RawJsonEditor';
import { DiffModal } from './DiffModal';
import type { AutoTLSDTO, SettingsDTO } from '@/services/schema';
import { cn } from '@/lib/utils';

/**
 * SettingsView
 * Modul 6: Global Settings & Configuration Dual-Editor.
 * Supports structured visual forms and raw JSON schema editing with line diff preview
 * before triggering atomic hot-swap on Go server.
 *
 * Vercel React Best Practices:
 * - rerender-memo
 * - rerender-defer-reads
 * - rendering-conditional-render
 */
export default React.memo(function SettingsView() {
  const adminToken = useAdminToken();
  const setAdminToken = useSetAdminToken();
  const upstreams = useUpstreams();
  const models = useModels();
  const combos = useCombos();
  const tenants = useTenants();
  const { logout, addToast, updatePassword } = useStoreActions();

  const { data: serverSettings, refetch, isFetching } = useSettingsQuery();
  const saveMutation = useSaveSettingsMutation();

  const [activeTab, setActiveTab] = useState<'visual' | 'raw'>('visual');
  const [tokenInput, setTokenInput] = useState(adminToken || '');
  const [isTokenMasked, setIsTokenMasked] = useState(true);

  const [currentPasswordInput, setCurrentPasswordInput] = useState('');
  const [newPasswordInput, setNewPasswordInput] = useState('');
  const [isCurrentPasswordMasked, setIsCurrentPasswordMasked] = useState(true);
  const [isNewPasswordMasked, setIsNewPasswordMasked] = useState(true);
  const [isUpdatingPassword, startPasswordTransition] = useTransition();
  const [tlsDraft, setTlsDraft] = useState<AutoTLSDTO>({ enabled: false, domain: '', email: '' });

  // Generate baseline JSON from current state
  const serverJson = useMemo(() => {
    const config: SettingsDTO = {
      upstreams: serverSettings?.upstreams || upstreams,
      models: serverSettings?.models || models,
      tenants: serverSettings?.tenants || tenants,
      combos: serverSettings?.combos || combos,
      auto_tls: serverSettings?.auto_tls,
    };
    return JSON.stringify(config, null, 2);
  }, [serverSettings, upstreams, models, tenants, combos]);

  // Draft raw JSON state
  const [rawJsonText, setRawJsonText] = useState(serverJson);
  const [isDiffModalOpen, setIsDiffModalOpen] = useState(false);

  // Sync draft JSON when server JSON changes if unmodified
  useEffect(() => {
    setRawJsonText(serverJson);
  }, [serverJson]);

  useEffect(() => {
    const tls = serverSettings?.auto_tls;
    setTlsDraft({
      enabled: tls?.enabled ?? false,
      domain: tls?.domain ?? '',
      email: tls?.email ?? '',
    });
  }, [serverSettings?.auto_tls]);

  // Save admin token to store & session storage
  const handleSaveToken = useCallback(() => {
    setAdminToken(tokenInput.trim());
    addToast({
      title: 'Admin Token Updated',
      message: 'Token saved to session storage and attached to management requests.',
      type: 'success',
    });
  }, [tokenInput, setAdminToken, addToast]);

  const handleSaveAutoTLS = useCallback(() => {
    const autoTLS: AutoTLSDTO = {
      enabled: tlsDraft.enabled,
      domain: tlsDraft.domain?.trim(),
      email: tlsDraft.email?.trim(),
    };
    if (autoTLS.enabled && (!autoTLS.domain || !autoTLS.email)) {
      addToast({
        title: 'Auto-TLS Needs Domain and Email',
        message: 'Enter a public hostname and ACME notification email before enabling HTTPS.',
        type: 'error',
      });
      return;
    }
    saveMutation.mutate({
      upstreams: serverSettings?.upstreams || upstreams,
      models: serverSettings?.models || models,
      tenants: serverSettings?.tenants || tenants,
      combos: serverSettings?.combos || combos,
      auto_tls: autoTLS,
    }, {
      onSuccess: () => { void refetch(); },
    });
  }, [tlsDraft, addToast, serverSettings, upstreams, models, tenants, combos, saveMutation, refetch]);

  // Update dashboard access password on backend
  const handleSavePassword = useCallback(() => {
    const trimmedNew = newPasswordInput.trim();
    if (!trimmedNew) {
      addToast({
        title: 'Validation Error',
        message: 'New password cannot be empty.',
        type: 'error',
      });
      return;
    }
    if (trimmedNew.length < 4) {
      addToast({
        title: 'Validation Error',
        message: 'New password must be at least 4 characters long.',
        type: 'error',
      });
      return;
    }

    startPasswordTransition(async () => {
      const res = await updatePassword(currentPasswordInput.trim(), trimmedNew);
      if (res.ok) {
        addToast({
          title: 'Password Updated',
          message: res.message || 'Dashboard access password successfully updated on backend.',
          type: 'success',
        });
        setCurrentPasswordInput('');
        setNewPasswordInput('');
      } else {
        addToast({
          title: 'Update Failed',
          message: res.message || 'Failed to update dashboard password.',
          type: 'error',
        });
      }
    });
  }, [currentPasswordInput, newPasswordInput, updatePassword, addToast, startPasswordTransition]);

  // Reset dashboard password to default on backend
  const handleResetDefaultPassword = useCallback(() => {
    startPasswordTransition(async () => {
      const res = await updatePassword(currentPasswordInput.trim(), '12345678');
      if (res.ok) {
        addToast({
          title: 'Password Reset',
          message: 'Dashboard password reset to default: 12345678',
          type: 'info',
        });
        setCurrentPasswordInput('');
        setNewPasswordInput('');
      } else {
        addToast({
          title: 'Reset Failed',
          message: res.message || 'Incorrect current password or server error.',
          type: 'error',
        });
      }
    });
  }, [currentPasswordInput, updatePassword, addToast, startPasswordTransition]);

  // Lock session immediately
  const handleLockSession = useCallback(() => {
    logout();
    addToast({
      title: 'Session Locked',
      message: 'Dashboard session ended. Returning to overview.',
      type: 'info',
    });
  }, [logout, addToast]);

  // Open diff review modal
  const handleReviewDiff = useCallback(() => {
    setIsDiffModalOpen(true);
  }, []);

  // Execute hot swap mutation
  const handleConfirmHotSwap = useCallback(() => {
    try {
      const parsed = JSON.parse(rawJsonText) as SettingsDTO;
      saveMutation.mutate(parsed, {
        onSuccess: () => {
          setIsDiffModalOpen(false);
        },
      });
    } catch (err) {
      useAppStore.getState().addToast({
        title: 'Validation Error',
        message: 'Cannot apply invalid JSON configuration.',
        type: 'error',
      });
    }
  }, [rawJsonText, saveMutation]);

  // Reset draft to server state
  const handleResetToCurrent = useCallback(() => {
    setRawJsonText(serverJson);
    useAppStore.getState().addToast({
      title: 'Draft Reverted',
      message: 'Reset local changes to match server configuration.',
      type: 'info',
    });
  }, [serverJson]);

  const hasPendingChanges = rawJsonText.trim() !== serverJson.trim();

  return (
    <div className="space-y-6 animate-in fade-in duration-300">
      {/* Top Action Bar */}
      <div className="flex flex-wrap items-center justify-between sm:justify-end gap-2.5">
          <div className="inline-flex p-0.5 rounded-lg border border-white/[0.08] bg-transparent font-mono text-xs">
            <button
              onClick={() => setActiveTab('visual')}
              className={cn(
                'px-2.5 py-1 rounded flex items-center gap-1.5 transition-all',
                activeTab === 'visual'
                  ? 'bg-white/[0.08] text-white font-medium'
                  : 'text-neutral-500 hover:text-neutral-300'
              )}
            >
              <Sliders className="w-3.5 h-3.5" />
              Visual Form
            </button>
            <button
              onClick={() => setActiveTab('raw')}
              className={cn(
                'px-2.5 py-1 rounded flex items-center gap-1.5 transition-all',
                activeTab === 'raw'
                  ? 'bg-white/[0.08] text-white font-medium'
                  : 'text-neutral-500 hover:text-neutral-300'
              )}
            >
              <Code className="w-3.5 h-3.5" />
              Raw JSON Editor
            </button>
          </div>

          <Button
            variant="minimal"
            size="sm"
            onClick={() => refetch()}
            isLoading={isFetching}
            leftIcon={<RefreshCw className="w-3 h-3 text-neutral-400 group-hover:text-white transition-colors" />}
          >
            Sync
          </Button>

          <Button
            variant="minimal"
            size="sm"
            onClick={handleReviewDiff}
            disabled={!hasPendingChanges}
            leftIcon={<GitCommit className="w-3 h-3 text-neutral-400 group-hover:text-white transition-colors" />}
          >
            Review Diff & Apply
          </Button>
      </div>

      {/* Main Content Area */}
      {activeTab === 'raw' ? (
        <RawJsonEditor
          value={rawJsonText}
          onChange={setRawJsonText}
          onReset={handleResetToCurrent}
          serverValue={serverJson}
        />
      ) : (
        <div className="grid grid-cols-1 lg:grid-cols-12 gap-6">
          {/* Left Column (7 cols): Gateway & Admin Configuration */}
          <div className="lg:col-span-7 space-y-6">
            {/* Dashboard Access Password Configuration Card */}
            <div className="p-5 rounded-xl bg-transparent border border-white/[0.06] space-y-4 font-mono text-xs select-none">
              <div className="flex items-center justify-between pb-3 border-b border-white/[0.04]">
                <div className="flex items-center gap-2">
                  <KeyRound className="w-4 h-4 text-neutral-400" />
                  <h3 className="text-xs font-mono uppercase tracking-wider text-neutral-300 font-medium">
                    Dashboard Access Security
                  </h3>
                </div>
                <div className="flex items-center gap-2">
                  <span className="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full border border-emerald-500/20 bg-emerald-500/10 text-emerald-400 text-[10px]">
                    <span className="w-1 h-1 rounded-full bg-emerald-400" />
                    Unlocked
                  </span>
                  <button
                    type="button"
                    onClick={handleLockSession}
                    className="text-[11px] text-neutral-400 hover:text-rose-400 transition-colors cursor-pointer px-2 py-0.5 rounded border border-white/[0.06] hover:border-rose-400/30"
                    title="Lock dashboard session immediately"
                  >
                    Lock Session
                  </button>
                </div>
              </div>

              <div className="space-y-3">
                <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                  <div className="space-y-1.5">
                    <label className="text-xs font-mono text-neutral-400 block">
                      Current Password
                    </label>
                    <div className="relative">
                      <input
                        type={isCurrentPasswordMasked ? 'password' : 'text'}
                        value={currentPasswordInput}
                        onChange={(e) => setCurrentPasswordInput(e.target.value)}
                        placeholder="Current password (default: 12345678)"
                        className="w-full px-3 py-1.5 pr-9 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
                      />
                      <button
                        type="button"
                        onClick={() => setIsCurrentPasswordMasked((m) => !m)}
                        className="absolute right-2.5 top-2 text-neutral-500 hover:text-white transition-colors cursor-pointer"
                        title={isCurrentPasswordMasked ? 'Show password' : 'Hide password'}
                      >
                        {isCurrentPasswordMasked ? <Eye className="w-3.5 h-3.5" /> : <EyeOff className="w-3.5 h-3.5" />}
                      </button>
                    </div>
                  </div>

                  <div className="space-y-1.5">
                    <label className="text-xs font-mono text-neutral-400 block">
                      New Password
                    </label>
                    <div className="relative">
                      <input
                        type={isNewPasswordMasked ? 'password' : 'text'}
                        value={newPasswordInput}
                        onChange={(e) => setNewPasswordInput(e.target.value)}
                        placeholder="Enter new master password..."
                        className="w-full px-3 py-1.5 pr-9 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
                      />
                      <button
                        type="button"
                        onClick={() => setIsNewPasswordMasked((m) => !m)}
                        className="absolute right-2.5 top-2 text-neutral-500 hover:text-white transition-colors cursor-pointer"
                        title={isNewPasswordMasked ? 'Show password' : 'Hide password'}
                      >
                        {isNewPasswordMasked ? <Eye className="w-3.5 h-3.5" /> : <EyeOff className="w-3.5 h-3.5" />}
                      </button>
                    </div>
                  </div>
                </div>

                <div className="flex items-center gap-2 pt-1">
                  <Button
                    variant="minimal"
                    size="sm"
                    onClick={handleSavePassword}
                    isLoading={isUpdatingPassword}
                    disabled={isUpdatingPassword}
                  >
                    Update Password
                  </Button>
                  <Button
                    variant="minimal"
                    size="sm"
                    onClick={handleResetDefaultPassword}
                    disabled={isUpdatingPassword}
                  >
                    Reset (12345678)
                  </Button>
                </div>

                <div className="p-3 rounded-lg bg-transparent border border-white/[0.04] space-y-1 text-[11px] text-neutral-400">
                  <p className="flex items-center gap-1.5 text-neutral-300 font-medium">
                    <span className="w-1 h-1 rounded-full bg-neutral-400" />
                    Route Access Policy:
                  </p>
                  <p className="text-neutral-500 pl-2.5">
                    • <span className="text-neutral-300">Overview</span> tab is always public and does not require authentication.
                  </p>
                  <p className="text-neutral-500 pl-2.5">
                    • Navigation to <span className="text-neutral-300">Upstreams</span>, <span className="text-neutral-300">Models</span>, <span className="text-neutral-300">Tenants</span>, <span className="text-neutral-300">Telemetry</span>, <span className="text-neutral-300">Settings</span>, and <span className="text-neutral-300">Playground</span> requires this password.
                  </p>
                </div>
              </div>
            </div>

            {/* Admin Authentication Card */}
            <div className="p-5 rounded-xl bg-transparent border border-white/[0.06] space-y-4 font-mono text-xs select-none">
              <div className="flex items-center gap-2 pb-3 border-b border-white/[0.04]">
                <Shield className="w-4 h-4 text-neutral-400" />
                <h3 className="text-xs font-mono uppercase tracking-wider text-neutral-300 font-medium">
                  Management Authentication (Admin Token)
                </h3>
              </div>

              <div className="space-y-2">
                <label className="text-xs font-mono text-neutral-400 block">
                  Admin Bearer Token
                </label>
                <div className="flex gap-2">
                  <div className="relative flex-1">
                    <input
                      type={isTokenMasked ? 'password' : 'text'}
                      value={tokenInput}
                      onChange={(e) => setTokenInput(e.target.value)}
                      placeholder="Enter FIREFLY_ADMIN_TOKEN or sk-admin-..."
                      className="w-full px-3 py-1.5 pr-9 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
                    />
                    <button
                      type="button"
                      onClick={() => setIsTokenMasked((m) => !m)}
                      className="absolute right-2.5 top-2 text-neutral-500 hover:text-white transition-colors"
                    >
                      {isTokenMasked ? <Eye className="w-3.5 h-3.5" /> : <EyeOff className="w-3.5 h-3.5" />}
                    </button>
                  </div>

                  <Button variant="minimal" size="sm" onClick={handleSaveToken}>
                    Save Token
                  </Button>
                </div>
                <p className="text-[11px] font-mono text-neutral-500">
                  Required when Firefly is launched with <code className="text-neutral-400">-admin-token</code> or <code className="text-neutral-400">FIREFLY_ADMIN_TOKEN</code>.
                </p>
              </div>
            </div>

            {/* Native Auto-TLS Card */}
            <div className="p-5 rounded-xl bg-transparent border border-white/[0.06] space-y-4 font-mono text-xs select-none">
              <div className="flex items-center gap-2 pb-3 border-b border-white/[0.04]">
                <Shield className="w-4 h-4 text-neutral-400" />
                <h3 className="text-xs font-mono uppercase tracking-wider text-neutral-300 font-medium">
                  Native Let&apos;s Encrypt Auto-TLS
                </h3>
              </div>

              <label className="flex items-center gap-2 text-neutral-300 cursor-pointer">
                <input
                  type="checkbox"
                  checked={tlsDraft.enabled}
                  onChange={(e) => setTlsDraft((current) => ({ ...current, enabled: e.target.checked }))}
                  className="h-3.5 w-3.5 rounded border-white/20 bg-transparent accent-neutral-300"
                />
                Enable automatic HTTPS certificates and renewal
              </label>

              <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                <div className="space-y-1.5">
                  <label className="text-xs font-mono text-neutral-400 block">Public Domain</label>
                  <input
                    type="text"
                    inputMode="url"
                    autoCapitalize="none"
                    spellCheck={false}
                    disabled={!tlsDraft.enabled}
                    value={tlsDraft.domain || ''}
                    onChange={(e) => setTlsDraft((current) => ({ ...current, domain: e.target.value }))}
                    placeholder="ai.example.com"
                    className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs disabled:opacity-50 focus:outline-none focus:border-white/20"
                  />
                </div>
                <div className="space-y-1.5">
                  <label className="text-xs font-mono text-neutral-400 block">ACME Notification Email</label>
                  <input
                    type="email"
                    autoCapitalize="none"
                    spellCheck={false}
                    disabled={!tlsDraft.enabled}
                    value={tlsDraft.email || ''}
                    onChange={(e) => setTlsDraft((current) => ({ ...current, email: e.target.value }))}
                    placeholder="ops@example.com"
                    className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs disabled:opacity-50 focus:outline-none focus:border-white/20"
                  />
                </div>
              </div>

              <div className="p-3 rounded-lg bg-transparent border border-white/[0.04] space-y-1 text-[11px] text-neutral-400">
                <p className="text-neutral-300 font-medium">HTTP-01 validation requirements</p>
                <p>Point DNS for this exact hostname to the server and make TCP ports 80 and 443 public and unused. Firefly handles certificate issuance, renewal, and HTTP-to-HTTPS redirect.</p>
                <p>IP addresses, localhost, wildcards, and URL values are not supported by this native mode.</p>
              </div>

              <Button
                variant="minimal"
                size="sm"
                onClick={handleSaveAutoTLS}
                isLoading={saveMutation.isPending}
                disabled={saveMutation.isPending}
              >
                Save Auto-TLS
              </Button>
            </div>

            {/* Admission Semaphore & Rate Limit Settings */}
            <div className="p-5 rounded-xl bg-transparent border border-white/[0.06] space-y-4 font-mono text-xs select-none">
              <div className="flex items-center gap-2 pb-3 border-b border-white/[0.04]">
                <Cpu className="w-4 h-4 text-neutral-400" />
                <h3 className="text-xs font-mono uppercase tracking-wider text-neutral-300 font-medium">
                  Admission Gate & Transport Constants
                </h3>
              </div>

              <div className="grid grid-cols-1 sm:grid-cols-2 gap-3 font-mono text-xs">
                <div className="p-3 rounded-lg bg-transparent border border-white/[0.04]">
                  <span className="text-[10px] text-neutral-500 block">Server-Wide Concurrency</span>
                  <span className="text-neutral-200 font-medium text-sm mt-0.5 block tabular-nums">1,500 slots</span>
                  <span className="text-[10px] text-neutral-500 block mt-1">httpx.GlobalLimiter channel semaphore</span>
                </div>

                <div className="p-3 rounded-lg bg-transparent border border-white/[0.04]">
                  <span className="text-[10px] text-neutral-500 block">Admission Queue Timeout</span>
                  <span className="text-neutral-200 font-medium text-sm mt-0.5 block tabular-nums">1,500 ms</span>
                  <span className="text-[10px] text-neutral-500 block mt-1">Returns 429 Retry-After: 2 on expire</span>
                </div>

                <div className="p-3 rounded-lg bg-transparent border border-white/[0.04]">
                  <span className="text-[10px] text-neutral-500 block">ResponseHeaderTimeout</span>
                  <span className="text-neutral-200 font-medium text-sm mt-0.5 block tabular-nums">30.0 s</span>
                  <span className="text-[10px] text-neutral-500 block mt-1">Time to first response byte from AI</span>
                </div>

                <div className="p-3 rounded-lg bg-transparent border border-white/[0.04]">
                  <span className="text-[10px] text-neutral-500 block">Stream Idle Watchdog</span>
                  <span className="text-neutral-200 font-medium text-sm mt-0.5 block tabular-nums">45.0 s</span>
                  <span className="text-[10px] text-neutral-500 block mt-1">Aborts socket if token gap exceeds</span>
                </div>
              </div>
            </div>
          </div>

          {/* Right Column (5 cols): Invariants & Catalog Health */}
          <div className="lg:col-span-5 space-y-6">
            {/* Non-Negotiable Invariants Card */}
            <div className="p-5 rounded-xl bg-transparent border border-white/[0.06] space-y-3 font-mono text-xs select-none">
              <div className="flex items-center gap-2 pb-2 border-b border-white/[0.04]">
                <AlertOctagon className="w-4 h-4 text-neutral-400" />
                <h3 className="text-xs font-mono uppercase tracking-wider text-neutral-300 font-medium">
                  Architecture Invariants
                </h3>
              </div>

              <div className="space-y-2 font-mono text-[11px] text-neutral-400 leading-relaxed">
                <div className="p-2.5 rounded-lg bg-transparent border border-white/[0.04]">
                  <span className="font-medium text-neutral-200 block mb-0.5">1. Zero WriteTimeout on Data Plane:</span>
                  LLM SSE streams can last minutes. Setting WriteTimeout kills active streams prematurely.
                </div>

                <div className="p-2.5 rounded-lg bg-transparent border border-white/[0.04]">
                  <span className="font-medium text-neutral-200 block mb-0.5">2. Layer 1 (429/401) vs Layer 2 (5xx):</span>
                  429 quota and 401 key errors never trip host Circuit Breakers. They trigger KeyRing rotation.
                </div>

                <div className="p-2.5 rounded-lg bg-transparent border border-white/[0.04]">
                  <span className="font-medium text-neutral-200 block mb-0.5">3. Decoupled Stream Context:</span>
                  Graceful shutdown drains HTTP listeners while active streams flow for up to 30s.
                </div>
              </div>
            </div>

            {/* Quick Catalog Stats */}
            <div className="p-5 rounded-xl bg-transparent border border-white/[0.06] space-y-3 font-mono text-xs select-none">
              <div className="flex items-center gap-2 pb-2 border-b border-white/[0.04]">
                <Database className="w-4 h-4 text-neutral-400" />
                <h3 className="text-xs font-mono uppercase tracking-wider text-neutral-300 font-medium">
                  Catalog Snapshot Status
                </h3>
              </div>

              <div className="space-y-2">
                <div className="flex items-center justify-between py-1 border-b border-white/[0.04]">
                  <span className="text-neutral-500">Upstream Hosts:</span>
                  <span className="text-neutral-200 font-medium">{upstreams.length} configured</span>
                </div>
                <div className="flex items-center justify-between py-1 border-b border-white/[0.04]">
                  <span className="text-neutral-500">Model Routes:</span>
                  <span className="text-neutral-200 font-medium">{models.length} active</span>
                </div>
                <div className="flex items-center justify-between py-1 border-b border-white/[0.04]">
                  <span className="text-neutral-500">Virtual Combos:</span>
                  <span className="text-cyan-400 font-medium">{combos.length} active</span>
                </div>
                <div className="flex items-center justify-between py-1">
                  <span className="text-neutral-500">Registered Tenants:</span>
                  <span className="text-neutral-200 font-medium">{tenants.length} tenants</span>
                </div>
              </div>
            </div>
          </div>
        </div>
      )}

      {/* Line Diff Confirmation Modal */}
      <DiffModal
        isOpen={isDiffModalOpen}
        onClose={() => setIsDiffModalOpen(false)}
        onConfirm={handleConfirmHotSwap}
        isSaving={saveMutation.isPending}
        originalJson={serverJson}
        modifiedJson={rawJsonText}
      />
    </div>
  );
});
