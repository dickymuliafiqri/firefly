import { useState, useEffect } from 'react';
import { ArrowLeft, Server, KeyRound, Layers, Shield, Trash2 } from 'lucide-react';
import { PageHeader } from '@/components/ui/PageHeader';
import { useSettingsQuery, useSaveSettingsSmart } from '@/services/api';
import type { UpstreamDTO, CredentialKeyDTO } from '@/services/schema';
import { useHashRest, navigate } from '@/lib/router';
import { useUiStore } from '@/state/store';

import { GeneralTab, type GeneralState } from '@/components/upstream/GeneralTab';
import { KeysTab, type KeyEntry } from '@/components/upstream/KeysTab';
import { ModelsTab } from '@/components/upstream/ModelsTab';
import { ResilienceTab } from '@/components/upstream/ResilienceTab';

type Tab = 'general' | 'keys' | 'models' | 'resilience';

export function UpstreamEditorPage() {
  const rest = useHashRest(); // 'new' or 'edit/<name>'
  const settings = useSettingsQuery();
  const saveSmart = useSaveSettingsSmart();
  const pushToast = useUiStore((s) => s.pushToast);

  const isEdit = rest.startsWith('edit/');
  const editingName = isEdit ? decodeURIComponent(rest.replace('edit/', '')) : '';

  const [activeTab, setActiveTab] = useState<Tab>('general');

  // Tab 1 state
  const [general, setGeneral] = useState<GeneralState>({
    name: '',
    protocol: 'openai',
    baseUrl: '',
    fallbackUrls: [],
    egressMode: 'direct',
    proxyUrl: '',
    allowInsecure: false,
    timeoutMs: 30000,
    idleTimeoutMs: 60000,
    probeModel: '',
    extraHeaders: [],
  });

  // Tab 2 state
  const [providerId, setProviderId] = useState<number | null>(null);
  const [keyStrategy, setKeyStrategy] = useState<'round_robin' | 'least_inflight'>('round_robin');
  const [keys, setKeys] = useState<KeyEntry[]>([]);

  // Tab 3 state
  const [discoveredModels, setDiscoveredModels] = useState<string[]>([]);
  const [modelsLatency, setModelsLatency] = useState<number | null>(null);

  // Tab 4 state
  const [keyErrorAction, setKeyErrorAction] = useState<'cooldown' | 'deactivate' | 'delete'>('cooldown');
  const [keyErrorThreshold, setKeyErrorThreshold] = useState(3);
  const [keyCooldownDurationMs, setKeyCooldownDurationMs] = useState(30000);
  const [keyErrorRules, setKeyErrorRules] = useState<UpstreamDTO['key_error_rules']>([]);

  // Populate data when editing
  useEffect(() => {
    if (!settings.data || !isEdit || !editingName) return;
    const target = settings.data.upstreams.find((u) => u.name === editingName);
    if (!target) return;

    setGeneral({
      name: target.name,
      protocol: target.protocol ?? 'openai',
      baseUrl: target.base_url ?? '',
      fallbackUrls: target.base_urls ?? [],
      egressMode: (target.egress_mode as GeneralState['egressMode']) ?? 'direct',
      proxyUrl: target.proxy_url ?? '',
      allowInsecure: target.allow_insecure ?? false,
      timeoutMs: target.timeout_ms ?? 30000,
      idleTimeoutMs: target.idle_timeout_ms ?? 60000,
      probeModel: target.probe_model ?? '',
      extraHeaders: target.extra_headers
        ? Object.entries(target.extra_headers).map(([k, v]) => ({ key: k, value: v }))
        : [],
    });

    setProviderId(target.provider_id ?? null);
    setKeyStrategy((target.key_strategy as 'round_robin' | 'least_inflight') ?? 'round_robin');
    setKeyErrorAction((target.key_error_action as 'cooldown' | 'deactivate' | 'delete') ?? 'cooldown');
    setKeyErrorThreshold(target.key_error_threshold ?? 3);
    setKeyCooldownDurationMs(target.key_cooldown_duration_ms ?? 30000);
    setKeyErrorRules(target.key_error_rules ?? []);

    const pool = target.credential_pool ?? [];
    if (pool.length > 0) {
      setKeys(
        pool.map((p, idx) => ({
          id: p.ref || `k-${idx}`,
          ref: p.ref,
          secret: p.secret || p.api_key || '',
          rps: p.rps || 0,
          maxConcurrent: p.max_concurrent || 0,
        })),
      );
    } else if (target.api_keys && target.api_keys.length > 0) {
      setKeys(
        target.api_keys.map((k, idx) => ({
          id: `k-${idx}`,
          ref: `${target.name}-key-${idx + 1}`,
          secret: k,
          rps: 0,
          maxConcurrent: 0,
        })),
      );
    } else if (target.api_key) {
      setKeys([{ id: 'k-0', ref: `${target.name}-key-1`, secret: target.api_key, rps: 0, maxConcurrent: 0 }]);
    }
  }, [settings.data, isEdit, editingName]);

  // Model actions
  const catalogModelNames = (settings.data?.models ?? []).map((m) => m.public_name);

  function handleCreateRoute(modelName: string) {
    if (!settings.data) return;
    const nextModels = [
      ...settings.data.models,
      {
        public_name: modelName,
        upstream: general.name,
        upstream_model: modelName,
        enabled: true,
      },
    ];
    saveSmart.mutate(
      { ...settings.data, models: nextModels },
      {
        onSuccess: () => pushToast({ type: 'success', title: 'Route created', message: `Model ${modelName} registered.` }),
        onError: (e) => pushToast({ type: 'error', title: 'Failed', message: e instanceof Error ? e.message : 'Error' }),
      },
    );
  }

  function handleCreateAllRoutes(modelsToAdd: string[]) {
    if (!settings.data) return;
    const currentSet = new Set(catalogModelNames);
    const newModels = modelsToAdd
      .filter((m) => !currentSet.has(m))
      .map((m) => ({
        public_name: m,
        upstream: general.name,
        upstream_model: m,
        enabled: true,
      }));

    if (newModels.length === 0) return;

    saveSmart.mutate(
      { ...settings.data, models: [...settings.data.models, ...newModels] },
      {
        onSuccess: () => pushToast({ type: 'success', title: 'Routes created', message: `${newModels.length} models registered.` }),
        onError: (e) => pushToast({ type: 'error', title: 'Failed', message: e instanceof Error ? e.message : 'Error' }),
      },
    );
  }

  // Delete Upstream
  function handleDeleteUpstream() {
    if (!isEdit || !editingName || !settings.data) return;
    const referencing = (settings.data.models ?? []).filter((m) => m.upstream === editingName);
    if (referencing.length > 0) {
      pushToast({
        type: 'error',
        title: 'Cannot delete',
        message: `This upstream is still referenced by ${referencing.length} model(s): ${referencing.map((m) => m.public_name).join(', ')}. Please update or remove those model routes first.`,
      });
      return;
    }

    if (!window.confirm(`Are you sure you want to delete upstream "${editingName}"?`)) return;

    const nextUpstreams = (settings.data.upstreams ?? []).filter((u) => u.name !== editingName);
    saveSmart.mutate(
      { ...settings.data, upstreams: nextUpstreams },
      {
        onSuccess: () => {
          pushToast({
            type: 'success',
            title: 'Upstream deleted',
            message: `Upstream '${editingName}' successfully deleted.`,
          });
          navigate('upstreams');
        },
        onError: (e) => {
          pushToast({
            type: 'error',
            title: 'Failed to delete',
            message: e instanceof Error ? e.message : 'Unknown error',
          });
        },
      },
    );
  }

  // Save Upstream
  function handleSave() {
    if (!general.name.trim()) {
      pushToast({ type: 'error', title: 'Empty name', message: 'Upstream name is required.' });
      return;
    }
    if (!general.baseUrl.trim()) {
      pushToast({ type: 'error', title: 'Empty Base URL', message: 'Base URL is required.' });
      return;
    }
    if (!settings.data) return;

    const existingTarget = isEdit ? settings.data.upstreams.find((u) => u.name === editingName) : undefined;

    const pool: CredentialKeyDTO[] = keys.map((k, idx) => ({
      ref: k.ref || `${general.name}-cred-${idx + 1}`,
      api_key: k.secret,
      secret: k.secret,
      rps: k.rps > 0 ? k.rps : undefined,
      max_concurrent: k.maxConcurrent > 0 ? k.maxConcurrent : undefined,
    }));

    const extraHeadersObj: Record<string, string> = {};
    for (const h of general.extraHeaders) {
      if (h.key.trim() && h.value.trim()) {
        extraHeadersObj[h.key.trim()] = h.value.trim();
      }
    }

    const payload: UpstreamDTO = {
      ...(existingTarget || {}),
      name: general.name.trim(),
      protocol: general.protocol,
      base_url: general.baseUrl.trim(),
      base_urls: general.fallbackUrls.length > 0 ? general.fallbackUrls : undefined,
      provider_id: providerId,
      key_strategy: keyStrategy,
      credential_pool: pool.length > 0 ? pool : undefined,
      egress_mode: general.egressMode,
      proxy_url: general.egressMode === 'proxy' ? general.proxyUrl.trim() || undefined : undefined,
      allow_insecure: general.allowInsecure,
      timeout_ms: general.timeoutMs > 0 ? general.timeoutMs : undefined,
      idle_timeout_ms: general.idleTimeoutMs > 0 ? general.idleTimeoutMs : undefined,
      stream_idle_timeout_ms: existingTarget?.stream_idle_timeout_ms,
      max_idle_conns_per_host: existingTarget?.max_idle_conns_per_host,
      max_conns_per_host: existingTarget?.max_conns_per_host,
      probe_model: general.probeModel.trim() || undefined,
      extra_headers: Object.keys(extraHeadersObj).length > 0 ? extraHeadersObj : undefined,
      key_error_action: keyErrorAction,
      key_error_threshold: keyErrorThreshold,
      key_cooldown_duration_ms: keyCooldownDurationMs,
      key_error_rules: keyErrorRules,
      enabled: existingTarget ? existingTarget.enabled : true,
    };

    const currentList = settings.data.upstreams ?? [];
    let nextList: UpstreamDTO[];

    if (isEdit) {
      nextList = currentList.map((u) => (u.name === editingName ? payload : u));
    } else {
      if (currentList.some((u) => u.name === payload.name)) {
        pushToast({ type: 'error', title: 'Duplicate name', message: `Upstream '${payload.name}' already exists.` });
        return;
      }
      nextList = [...currentList, payload];
    }

    saveSmart.mutate(
      { ...settings.data, upstreams: nextList },
      {
        onSuccess: () => {
          pushToast({
            type: 'success',
            title: isEdit ? 'Upstream updated' : 'Upstream created',
            message: `Upstream '${payload.name}' saved.`,
          });
          navigate('upstreams');
        },
        onError: (e) => {
          pushToast({
            type: 'error',
            title: 'Save failed',
            message: e instanceof Error ? e.message : 'Unknown error',
          });
        },
      },
    );
  }

  return (
    <div className="page-col" style={{ paddingBottom: '90px' }}>
      <PageHeader
        title={isEdit ? `Edit Upstream: ${editingName}` : 'Add Upstream'}
        description="Configure upstream host, credential pool, model auto-routing, and error policy."
        actions={
          <div style={{ display: 'flex', gap: 8 }}>
            {isEdit && (
              <button
                type="button"
                className="btn btn-ghost"
                style={{ color: 'var(--danger)' }}
                onClick={handleDeleteUpstream}
                disabled={saveSmart.isPending}
              >
                <Trash2 style={{ width: 14, height: 14 }} />
                Delete
              </button>
            )}
            <button className="btn btn-secondary" onClick={() => navigate('upstreams')}>
              <ArrowLeft style={{ width: 14, height: 14 }} />
              Back
            </button>
          </div>
        }
      />

      <div style={{ display: 'flex', gap: 8, borderBottom: '1px solid var(--line)', marginBottom: 20 }}>
        <button
          type="button"
          className={`btn ${activeTab === 'general' ? 'btn-primary' : 'btn-ghost'}`}
          onClick={() => setActiveTab('general')}
        >
          <Server style={{ width: 15, height: 15 }} />
          General & Network
        </button>
        <button
          type="button"
          className={`btn ${activeTab === 'keys' ? 'btn-primary' : 'btn-ghost'}`}
          onClick={() => setActiveTab('keys')}
        >
          <KeyRound style={{ width: 15, height: 15 }} />
          Keys ({keys.length})
        </button>
        <button
          type="button"
          className={`btn ${activeTab === 'models' ? 'btn-primary' : 'btn-ghost'}`}
          onClick={() => setActiveTab('models')}
        >
          <Layers style={{ width: 15, height: 15 }} />
          Models ({discoveredModels.length})
        </button>
        <button
          type="button"
          className={`btn ${activeTab === 'resilience' ? 'btn-primary' : 'btn-ghost'}`}
          onClick={() => setActiveTab('resilience')}
        >
          <Shield style={{ width: 15, height: 15 }} />
          Resilience
        </button>
      </div>

      {activeTab === 'general' && (
        <GeneralTab
          state={general}
          isEdit={isEdit}
          onChange={(patch) => setGeneral((prev) => ({ ...prev, ...patch }))}
        />
      )}

      {activeTab === 'keys' && (
        <KeysTab
          providerId={providerId}
          keyStrategy={keyStrategy}
          keys={keys}
          upstreamName={general.name}
          protocol={general.protocol}
          baseUrl={general.baseUrl}
          egressMode={general.egressMode}
          proxyUrl={general.proxyUrl}
          probeModel={general.probeModel}
          onProviderChange={setProviderId}
          onStrategyChange={setKeyStrategy}
          onKeysChange={setKeys}
        />
      )}

      {activeTab === 'models' && (
        <ModelsTab
          upstreamName={general.name}
          protocol={general.protocol}
          baseUrl={general.baseUrl}
          egressMode={general.egressMode}
          proxyUrl={general.proxyUrl}
          firstKey={keys[0]?.secret}
          discoveredModels={discoveredModels}
          latencyMs={modelsLatency}
          catalogNames={catalogModelNames}
          catalogModels={settings.data?.models ?? []}
          onDiscover={(mods, lat) => {
            setDiscoveredModels(mods);
            setModelsLatency(lat);
          }}
          onCreateRoute={handleCreateRoute}
          onCreateAll={handleCreateAllRoutes}
        />
      )}

      {activeTab === 'resilience' && (
        <ResilienceTab
          keyErrorAction={keyErrorAction}
          keyErrorThreshold={keyErrorThreshold}
          keyCooldownDurationMs={keyCooldownDurationMs}
          keyErrorRules={keyErrorRules ?? []}
          onActionChange={setKeyErrorAction}
          onThresholdChange={setKeyErrorThreshold}
          onCooldownChange={setKeyCooldownDurationMs}
          onRulesChange={setKeyErrorRules}
        />
      )}

      <div
        style={{
          position: 'fixed',
          bottom: 0,
          left: 0,
          right: 0,
          background: 'var(--surface-overlay)',
          borderTop: '1px solid var(--line-strong)',
          padding: '12px 24px',
          display: 'flex',
          justifyContent: isEdit ? 'space-between' : 'flex-end',
          alignItems: 'center',
          gap: 12,
          zIndex: 40,
        }}
      >
        {isEdit ? (
          <button
            type="button"
            className="btn btn-ghost"
            style={{ color: 'var(--danger)' }}
            onClick={handleDeleteUpstream}
            disabled={saveSmart.isPending}
          >
            <Trash2 style={{ width: 14, height: 14 }} />
            Delete Upstream
          </button>
        ) : null}
        <div style={{ display: 'flex', gap: 12 }}>
          <button type="button" className="btn btn-secondary" onClick={() => navigate('upstreams')}>
            Cancel
          </button>
          <button
            type="button"
            className="btn btn-primary"
            disabled={saveSmart.isPending || !general.name.trim() || !general.baseUrl.trim()}
            onClick={handleSave}
          >
            {saveSmart.isPending ? 'Saving…' : 'Save Upstream'}
          </button>
        </div>
      </div>
    </div>
  );
}
