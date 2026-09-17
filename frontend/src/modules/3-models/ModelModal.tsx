import React, { useState, useEffect, useMemo, useCallback } from 'react';
import type { ModelDTO } from '@/services/schema';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import { cn } from '@/lib/utils';
import { useUpstreams, useStoreActions, useAdminToken, buildSettingsPayload } from '@/core/state/store';
import { useSaveSettingsMutation, useUpstreamModelsQuery, checkUpstreamHealth } from '@/services/api';
import {
  Trash2,
  Loader2,
  RotateCw,
  CheckCircle2,
  XCircle,
  Activity,
  Play,
} from 'lucide-react';

export interface ModelModalProps {
  isOpen: boolean;
  onClose: () => void;
  modelToEdit?: ModelDTO | null;
  onDelete?: (model: ModelDTO) => void;
}

export interface ModelCheckResult {
  healthy: boolean;
  statusCode: number;
  latencyMs: number;
  message: string;
  keyRef?: string;
}

interface ModelCheckBannerProps {
  result: ModelCheckResult;
}

// Module-level memoized feedback component adhering to rerender-no-inline-components
const ModelCheckBanner = React.memo(function ModelCheckBanner({ result }: ModelCheckBannerProps) {
  return (
    <div
      role="alert"
      className={
        result.healthy
          ? 'flex items-start gap-2.5 p-2.5 rounded-lg border border-emerald-500/30 bg-emerald-500/10 text-emerald-300'
          : 'flex items-start gap-2.5 p-2.5 rounded-lg border border-rose-500/30 bg-rose-500/10 text-rose-300'
      }
    >
      {result.healthy ? (
        <CheckCircle2 className="w-4 h-4 text-emerald-400 shrink-0 mt-0.5" />
      ) : (
        <XCircle className="w-4 h-4 text-rose-400 shrink-0 mt-0.5" />
      )}
      <div className="flex flex-col gap-0.5 text-[11px] leading-tight flex-1 min-w-0">
        <div className="flex items-center justify-between gap-2">
          <span className="font-semibold tracking-wide">
            {result.healthy ? 'Model Connection Active' : 'Connection Rejected'}
          </span>
          <span className="text-[10px] opacity-75 whitespace-nowrap font-mono flex items-center gap-1.5">
            {result.keyRef && (
              <span className="px-1.5 py-0.2 rounded bg-white/10 text-white/90 border border-white/10" title={`Key Slot: ${result.keyRef}`}>
                {result.keyRef}
              </span>
            )}
            {result.statusCode > 0 ? `HTTP ${result.statusCode} · ` : ''}{result.latencyMs}ms
          </span>
        </div>
        <p className="opacity-90 font-mono text-[10px] break-words">{result.message}</p>
      </div>
    </div>
  );
});

export const ModelModal = React.memo(function ModelModal({
  isOpen,
  onClose,
  modelToEdit,
  onDelete,
}: ModelModalProps) {
  const upstreams = useUpstreams();
  const adminToken = useAdminToken();
  const { addOrUpdateModel } = useStoreActions();
  const saveMutation = useSaveSettingsMutation();

  const [publicName, setPublicName] = useState('');
  const [upstream, setUpstream] = useState('');
  const [upstreamModel, setUpstreamModel] = useState('');
  const [maxContext, setMaxContext] = useState('128000');
  const [stream, setStream] = useState(true);
  const [tools, setTools] = useState(true);
  const [vision, setVision] = useState(false);
  const [jsonMode, setJsonMode] = useState(true);
  const [embeddings, setEmbeddings] = useState(false);
  const [isManualPrivateModel, setIsManualPrivateModel] = useState(false);

  // Model connection testing state
  const [isCheckingModel, setIsCheckingModel] = useState(false);
  const [checkResult, setCheckResult] = useState<ModelCheckResult | null>(null);



  useEffect(() => {
    setIsCheckingModel(false);
    setCheckResult(null);
    if (modelToEdit) {
      setPublicName(modelToEdit.public_name);
      setUpstream(modelToEdit.upstream);
      setUpstreamModel(modelToEdit.upstream_model);
      setMaxContext(modelToEdit.max_context != null ? String(modelToEdit.max_context) : '128000');
      setStream(modelToEdit.capabilities?.stream ?? true);
      setTools(modelToEdit.capabilities?.tools ?? true);
      setVision(modelToEdit.capabilities?.vision ?? false);
      setJsonMode(modelToEdit.capabilities?.json_mode ?? true);
      setEmbeddings(modelToEdit.capabilities?.embeddings ?? false);
      setIsManualPrivateModel(true);
    } else {
      setPublicName('');
      const defaultUp = upstreams[0]?.name || 'openai-main';
      setUpstream(defaultUp);
      setUpstreamModel('');
      setMaxContext('128000');
      setStream(true);
      setTools(true);
      setVision(false);
      setJsonMode(true);
      setEmbeddings(false);
      setIsManualPrivateModel(false);
    }
  }, [modelToEdit, isOpen, upstreams]);

  // Derive selected primary upstream object for querying models
  const selectedUpstreamObj = useMemo(() => {
    return upstreams.find((u) => u.name === upstream) || upstreams[0];
  }, [upstreams, upstream]);

  // Fetch available models automatically from the selected Upstream Host
  const {
    data: fetchedModels = [],
    isLoading: isModelsLoading,
    isFetching: isModelsFetching,
    error: modelsError,
    refetch: refetchModels,
  } = useUpstreamModelsQuery(selectedUpstreamObj, isOpen);

  // Handle selection of a private model from the dropdown
  const handlePrivateModelSelect = useCallback(
    (e: React.ChangeEvent<HTMLSelectElement>) => {
      const selected = e.target.value;
      if (selected === '__manual__') {
        setIsManualPrivateModel(true);
        return;
      }
      setUpstreamModel(selected);

      // Auto-suggest public model ID if user hasn't entered one yet
      setPublicName((prev) => (prev ? prev : selected));
      setCheckResult(null);

      // Auto-tune capabilities based on model name hints
      const lower = selected.toLowerCase();
      if (lower.includes('embed')) {
        setEmbeddings(true);
        setStream(false);
        setTools(false);
      } else if (lower.includes('vision') || lower.includes('gpt-4o') || lower.includes('claude-3') || lower.includes('gemini')) {
        setVision(true);
      }
      if (lower.includes('gemini-3') || lower.includes('claude-opus-4')) {
        setMaxContext('1000000');
      }
    },
    []
  );

  // Derive target model name (private upstream model if provided, otherwise public name)
  const targetModel = upstreamModel.trim() || publicName.trim();

  // Test connection to the selected model on the upstream host
  const handleTestModel = useCallback(async () => {
    // js-early-exit: ensure model and upstream host are selected
    if (!targetModel || !selectedUpstreamObj) return;

    setIsCheckingModel(true);
    setCheckResult(null);

    const isSavedUpstream = Boolean(selectedUpstreamObj.name);

    try {
      const res = await checkUpstreamHealth(
        {
          name: selectedUpstreamObj.name,
          protocol: selectedUpstreamObj.protocol,
          base_url: selectedUpstreamObj.base_url,
          key_ref: isSavedUpstream ? undefined : (selectedUpstreamObj.credential_ref || selectedUpstreamObj.credential_pool?.[0]?.ref),
          api_key: isSavedUpstream ? undefined : selectedUpstreamObj.api_key,
          model: targetModel,
          timeout_ms: 10000,
          egress_mode: selectedUpstreamObj.egress_mode,
          proxy_url: selectedUpstreamObj.proxy_url,
        },
        adminToken
      );

      setCheckResult({
        healthy: res.healthy,
        statusCode: res.status_code,
        latencyMs: res.latency_ms,
        message: res.message,
        keyRef: res.key_ref,
      });
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : 'Unknown connection error';
      setCheckResult({
        healthy: false,
        statusCode: 0,
        latencyMs: 0,
        message,
      });
    } finally {
      setIsCheckingModel(false);
    }
  }, [targetModel, selectedUpstreamObj, adminToken]);



  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!publicName.trim() || !upstream.trim()) return;

    const parsedMaxContext = Number(maxContext);
    const normalizedMaxContext = Number.isFinite(parsedMaxContext) && parsedMaxContext > 0
      ? Math.trunc(parsedMaxContext)
      : undefined;

    const updated: ModelDTO = {
      public_name: publicName.trim(),
      upstream: upstream.trim(),
      upstream_model: upstreamModel.trim() || publicName.trim(),
      max_context: normalizedMaxContext,
      enabled: modelToEdit?.enabled ?? true,
      capabilities: {
        stream,
        tools,
        vision,
        json_mode: jsonMode,
        embeddings,
      },
    };

    addOrUpdateModel(updated);

    // Sync to Go backend
    saveMutation.mutate(buildSettingsPayload());

    onClose();
  };

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title={modelToEdit ? `Edit Route: ${modelToEdit.public_name}` : 'Create Model Route'}
      description="Map public client model ID to an upstream provider and target model name."
      size="xl"
    >
      <form onSubmit={handleSubmit} className="flex flex-col gap-4 font-mono text-xs">
        {/* Row 1: Public Model ID (Client-facing ID, manual input) */}
        <div className="flex flex-col gap-1.5">
          <label className="text-neutral-400 font-medium">Public Model ID</label>
          <input
            type="text"
            required
            disabled={!!modelToEdit}
            value={publicName}
            onChange={(e) => {
              setPublicName(e.target.value);
              setCheckResult(null);
            }}
            placeholder="e.g. gpt-4o or gemini-pro (ID exposed to clients)"
            className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20 disabled:opacity-50"
          />
          <span className="text-[10px] text-neutral-500">
            The virtual model name that clients send in API requests (e.g. in /v1/chat/completions).
          </span>
        </div>

        {/* Row 2: Primary Upstream Host & Private Upstream Model Name (Dropdown) */}
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          {/* Primary Upstream Host */}
          <div className="flex flex-col gap-1.5">
            <div className="flex items-center justify-between">
              <label className="text-neutral-400 font-medium">Primary Upstream Host</label>
              {isModelsLoading || isModelsFetching ? (
                <span className="flex items-center gap-1 text-[10px] text-neutral-400 font-normal">
                  <Loader2 className="w-2.5 h-2.5 animate-spin text-neutral-400" />
                  <span>Fetching models...</span>
                </span>
              ) : fetchedModels.length > 0 ? (
                <div className="flex items-center gap-1 text-[10px] text-neutral-500 font-normal">
                  <span>· {fetchedModels.length} models</span>
                  <button
                    type="button"
                    onClick={() => refetchModels()}
                    title="Refresh model list from upstream"
                    className="text-neutral-500 hover:text-white transition-colors cursor-pointer p-0.5"
                  >
                    <RotateCw className="w-2.5 h-2.5" />
                  </button>
                </div>
              ) : null}
            </div>
            <select
              value={upstream}
              onChange={(e) => {
                const nextUpstream = e.target.value;
                setUpstream(nextUpstream);
                setCheckResult(null);
                if (!modelToEdit && !isManualPrivateModel) {
                  setUpstreamModel('');
                }
              }}
              className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-neutral-200 font-mono text-xs focus:outline-none focus:border-white/20"
            >
              {upstreams.map((u) => (
                <option key={u.name} value={u.name} className="bg-[#090b10]">
                  {u.name} ({u.protocol})
                </option>
              ))}
            </select>
          </div>

          {/* Private Upstream Model Name (Dropdown from Upstream Host) */}
          <div className="flex flex-col gap-1.5">
            <div className="flex items-center justify-between">
              <label className="text-neutral-400 font-medium">Private Upstream Model Name</label>
              <div className="flex items-center gap-2">
                {fetchedModels.length > 0 ? (
                  <button
                    type="button"
                    onClick={() => setIsManualPrivateModel((prev) => !prev)}
                    className="text-[10px] text-neutral-400 hover:text-white transition-colors underline underline-offset-2 cursor-pointer"
                  >
                    {isManualPrivateModel ? 'Select from dropdown' : 'Enter manually'}
                  </button>
                ) : null}
                <button
                  type="button"
                  onClick={() => refetchModels()}
                  disabled={isModelsLoading || isModelsFetching}
                  className="flex items-center gap-1 text-[10px] text-neutral-400 hover:text-white transition-colors cursor-pointer disabled:opacity-50"
                  title="Retry fetching models from upstream"
                >
                  <RotateCw className={cn('w-3 h-3', (isModelsLoading || isModelsFetching) && 'animate-spin')} />
                  <span>{isModelsLoading || isModelsFetching ? 'Fetching...' : 'Retry Fetch'}</span>
                </button>
              </div>
            </div>

            {/* Dropdown or manual text input */}
            {!isManualPrivateModel && fetchedModels.length > 0 ? (
              <select
                required
                value={upstreamModel}
                onChange={handlePrivateModelSelect}
                className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-neutral-200 font-mono text-xs focus:outline-none focus:border-white/20"
              >
                <option value="" className="bg-[#090b10] text-neutral-400">
                  {isModelsLoading || isModelsFetching
                    ? `Fetching models from ${selectedUpstreamObj?.name || 'upstream'}...`
                    : `-- Select Upstream Model (${fetchedModels.length} available) --`}
                </option>
                {fetchedModels.map((m) => (
                  <option key={m} value={m} className="bg-[#090b10]">
                    {m}
                  </option>
                ))}
                <option value="__manual__" className="bg-[#090b10] text-neutral-400">
                  ✏️ Custom / Enter manually...
                </option>
              </select>
            ) : (
              <input
                type="text"
                required
                value={upstreamModel}
                onChange={(e) => {
                  setUpstreamModel(e.target.value);
                  setCheckResult(null);
                }}
                placeholder={isModelsLoading ? 'Fetching upstream models...' : 'e.g. gpt-4o-2024-08-06'}
                className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
              />
            )}

            {!isManualPrivateModel && fetchedModels.length === 0 && !isModelsLoading ? (
              <div className="flex items-center justify-between gap-2 text-[10px] text-neutral-500">
                <span>
                  {modelsError
                    ? 'Could not load models automatically from upstream. Enter manually or retry.'
                    : 'Target model ID forwarded to the upstream provider.'}
                </span>
                <button
                  type="button"
                  onClick={() => refetchModels()}
                  className="text-cyan-400 hover:text-cyan-300 underline shrink-0 cursor-pointer"
                >
                  Retry Fetch
                </button>
              </div>
            ) : null}
          </div>
        </div>

        {/* Model Connectivity & Verification */}
        <div className="flex flex-col gap-2.5 p-3 rounded-lg border border-white/[0.08] bg-white/[0.015]">
          <div className="flex items-center justify-between gap-3">
            <div className="flex flex-col gap-0.5 min-w-0">
              <span className="text-neutral-300 font-medium text-[11px] flex items-center gap-1.5">
                <Activity className="w-3.5 h-3.5 text-neutral-400" />
                Model Verification
              </span>
              <span className="text-[10px] text-neutral-500 truncate">
                {targetModel
                  ? `Probe upstream host (${selectedUpstreamObj?.name || 'upstream'}) for "${targetModel}"`
                  : 'Select or specify a model to test connection before saving'}
              </span>
            </div>
            <Button
              type="button"
              variant="minimal"
              size="sm"
              onClick={handleTestModel}
              disabled={isCheckingModel || !targetModel || !selectedUpstreamObj}
              className="shrink-0 text-xs px-2.5 py-1 text-neutral-300 hover:text-white border-white/10 hover:border-white/20"
              leftIcon={
                isCheckingModel ? (
                  <Loader2 className="w-3.5 h-3.5 animate-spin text-neutral-400" />
                ) : (
                  <Play className="w-3 h-3 text-neutral-300 fill-neutral-300/20" />
                )
              }
            >
              {isCheckingModel ? 'Testing...' : 'Test Connection'}
            </Button>
          </div>

          {/* Verification Feedback Banner adhering to rendering-conditional-render */}
          {checkResult ? <ModelCheckBanner result={checkResult} /> : null}
        </div>



        {/* Row 3: Max Context Window & Combo Info Note */}
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          {/* Max Context Window */}
          <div className="flex flex-col gap-1.5">
            <label className="text-neutral-400 font-medium">Max Context Window (Tokens)</label>
            <input
              type="number"
              step={1000}
              value={maxContext}
              onChange={(e) => setMaxContext(e.target.value)}
              className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
            />
            <span className="text-[10px] text-neutral-500">
              Maximum token budget allowed per request session.
            </span>
          </div>

          {/* Model Combo Note */}
          <div className="flex flex-col justify-center p-3 rounded-lg border border-white/[0.04] bg-white/[0.01]">
            <span className="text-neutral-300 font-medium text-[11px]">Multi-Upstream Routing & Failover</span>
            <span className="text-[10px] text-neutral-500 mt-1">
              To group this model with other models for load balancing (least in-flight, round robin, or failover), create a <strong className="text-cyan-400">Virtual Combo</strong> under the Combos tab.
            </span>
          </div>
        </div>

        {/* Capabilities Checkboxes */}
        <div className="flex flex-col gap-2 pt-2 border-t border-white/[0.04]">
          <span className="text-neutral-400 font-medium">Capabilities</span>
          <div className="grid grid-cols-2 sm:grid-cols-3 gap-2">
            <label className="flex items-center gap-2 cursor-pointer text-neutral-300 hover:text-white">
              <input
                type="checkbox"
                checked={stream}
                onChange={(e) => setStream(e.target.checked)}
                className="rounded border-white/20 bg-transparent text-white focus:ring-0"
              />
              <span>Stream (SSE)</span>
            </label>
            <label className="flex items-center gap-2 cursor-pointer text-neutral-300 hover:text-white">
              <input
                type="checkbox"
                checked={tools}
                onChange={(e) => setTools(e.target.checked)}
                className="rounded border-white/20 bg-transparent text-white focus:ring-0"
              />
              <span>Tools / Functions</span>
            </label>
            <label className="flex items-center gap-2 cursor-pointer text-neutral-300 hover:text-white">
              <input
                type="checkbox"
                checked={vision}
                onChange={(e) => setVision(e.target.checked)}
                className="rounded border-white/20 bg-transparent text-white focus:ring-0"
              />
              <span>Vision</span>
            </label>
            <label className="flex items-center gap-2 cursor-pointer text-neutral-300 hover:text-white">
              <input
                type="checkbox"
                checked={jsonMode}
                onChange={(e) => setJsonMode(e.target.checked)}
                className="rounded border-white/20 bg-transparent text-white focus:ring-0"
              />
              <span>JSON Mode</span>
            </label>
            <label className="flex items-center gap-2 cursor-pointer text-neutral-300 hover:text-white">
              <input
                type="checkbox"
                checked={embeddings}
                onChange={(e) => setEmbeddings(e.target.checked)}
                className="rounded border-white/20 bg-transparent text-white focus:ring-0"
              />
              <span>Embeddings</span>
            </label>
          </div>
        </div>

        {/* Modal Actions */}
        <div className="pt-3 border-t border-white/[0.04] flex items-center justify-end gap-3">
          {modelToEdit && onDelete ? (
            <Button
              variant="minimal"
              size="sm"
              type="button"
              onClick={() => {
                onDelete(modelToEdit);
                onClose();
              }}
              disabled={saveMutation.isPending}
              className="mr-auto text-rose-400 hover:text-rose-300 hover:bg-rose-500/10 border-rose-500/20 hover:border-rose-500/40"
              leftIcon={<Trash2 className="w-3.5 h-3.5 text-rose-400" />}
            >
              Delete
            </Button>
          ) : null}

          <Button variant="minimal" size="sm" type="button" onClick={onClose} disabled={saveMutation.isPending}>
            Cancel
          </Button>
          <Button variant="minimal" size="sm" type="submit" isLoading={saveMutation.isPending}>
            {modelToEdit ? 'Save Changes' : 'Add Route'}
          </Button>
        </div>
      </form>
    </Modal>
  );
});
