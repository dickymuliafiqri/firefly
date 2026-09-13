import React, { useState, useEffect, useMemo, useCallback } from 'react';
import type { ModelDTO } from '@/services/schema';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import { useUpstreams, useStoreActions, useAppStore } from '@/core/state/store';
import { useSaveSettingsMutation, useUpstreamModelsQuery } from '@/services/api';
import { Trash2, Loader2, RotateCw } from 'lucide-react';

export interface ModelModalProps {
  isOpen: boolean;
  onClose: () => void;
  modelToEdit?: ModelDTO | null;
  onDelete?: (model: ModelDTO) => void;
}

export const ModelModal = React.memo(function ModelModal({
  isOpen,
  onClose,
  modelToEdit,
  onDelete,
}: ModelModalProps) {
  const upstreams = useUpstreams();
  const { addOrUpdateModel } = useStoreActions();
  const saveMutation = useSaveSettingsMutation();

  const [publicName, setPublicName] = useState('');
  const [upstream, setUpstream] = useState('');
  const [upstreamModel, setUpstreamModel] = useState('');
  const [maxContext, setMaxContext] = useState(128000);
  const [stream, setStream] = useState(true);
  const [tools, setTools] = useState(true);
  const [vision, setVision] = useState(false);
  const [jsonMode, setJsonMode] = useState(true);
  const [embeddings, setEmbeddings] = useState(false);
  const [isManualPrivateModel, setIsManualPrivateModel] = useState(false);

  useEffect(() => {
    if (modelToEdit) {
      setPublicName(modelToEdit.public_name);
      setUpstream(modelToEdit.upstream);
      setUpstreamModel(modelToEdit.upstream_model);
      setMaxContext(modelToEdit.max_context || 128000);
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
      setMaxContext(128000);
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
        setMaxContext(1000000);
      }
    },
    []
  );

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!publicName.trim() || !upstream.trim()) return;

    const updated: ModelDTO = {
      public_name: publicName.trim(),
      upstream: upstream.trim(),
      upstream_model: upstreamModel.trim() || publicName.trim(),
      max_context: maxContext,
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
    const currentSettings = {
      upstreams: useAppStore.getState().upstreams,
      models: useAppStore.getState().models,
      tenants: useAppStore.getState().tenants,
      combos: useAppStore.getState().combos,
    };
    saveMutation.mutate(currentSettings);

    onClose();
  };

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title={modelToEdit ? `Edit Route: ${modelToEdit.public_name}` : 'Create Model Route'}
      description="Map public client model ID to an upstream provider and target model name."
      size="lg"
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
            onChange={(e) => setPublicName(e.target.value)}
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
              {fetchedModels.length > 0 ? (
                <button
                  type="button"
                  onClick={() => setIsManualPrivateModel((prev) => !prev)}
                  className="text-[10px] text-neutral-400 hover:text-white transition-colors underline underline-offset-2 cursor-pointer"
                >
                  {isManualPrivateModel ? 'Select from dropdown' : 'Enter manually'}
                </button>
              ) : null}
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
                onChange={(e) => setUpstreamModel(e.target.value)}
                placeholder={isModelsLoading ? 'Fetching upstream models...' : 'e.g. gpt-4o-2024-08-06'}
                className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
              />
            )}

            {!isManualPrivateModel && fetchedModels.length === 0 && !isModelsLoading ? (
              <span className="text-[10px] text-neutral-500">
                {modelsError
                  ? 'Could not load models automatically from upstream. Enter manually.'
                  : 'Target model ID forwarded to the upstream provider.'}
              </span>
            ) : null}
          </div>
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
              onChange={(e) => setMaxContext(parseInt(e.target.value, 10) || 128000)}
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
