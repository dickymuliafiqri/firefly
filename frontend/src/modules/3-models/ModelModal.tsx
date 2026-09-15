import React, { useState, useEffect, useMemo, useCallback, useRef } from 'react';
import type { ModelDTO } from '@/services/schema';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import { Badge } from '@/components/ui/Badge';
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
  StopCircle,
  Check,
  Search,
  Zap,
  ChevronDown,
  ChevronUp,
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
}

export type ModelBatchStatus = 'pending' | 'testing' | 'success' | 'failed' | 'canceled';

export interface ModelBatchItemResult {
  model: string;
  status: ModelBatchStatus;
  healthy?: boolean;
  statusCode?: number;
  latencyMs?: number;
  message?: string;
}

export type BatchFilterTab = 'all' | 'available' | 'failed';

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
          <span className="text-[10px] opacity-75 whitespace-nowrap font-mono">
            {result.statusCode > 0 ? `HTTP ${result.statusCode} · ` : ''}{result.latencyMs}ms
          </span>
        </div>
        <p className="opacity-90 font-mono text-[10px] break-words">{result.message}</p>
      </div>
    </div>
  );
});

interface ModelBatchTestPanelProps {
  upstreamName: string;
  models: string[];
  isModelsLoading: boolean;
  results: Record<string, ModelBatchItemResult>;
  isTesting: boolean;
  safeRps: number;
  onSafeRpsChange: (rps: number) => void;
  onStartTest: () => void;
  onStopTest: () => void;
  selectedModel: string;
  onSelectModel: (model: string, result: ModelBatchItemResult) => void;
  isExpanded: boolean;
  onToggleExpanded: () => void;
}

// Module-level memoized batch testing panel
const ModelBatchTestPanel = React.memo(function ModelBatchTestPanel({
  upstreamName,
  models,
  isModelsLoading,
  results,
  isTesting,
  safeRps,
  onSafeRpsChange,
  onStartTest,
  onStopTest,
  selectedModel,
  onSelectModel,
  isExpanded,
  onToggleExpanded,
}: ModelBatchTestPanelProps) {
  const [filterTab, setFilterTab] = useState<BatchFilterTab>('all');
  const [searchFilter, setSearchFilter] = useState<string>('');

  const totalCount = models.length;
  const testedCount = useMemo(
    () => Object.values(results).filter((r) => r.status === 'success' || r.status === 'failed').length,
    [results]
  );
  const availableCount = useMemo(
    () => Object.values(results).filter((r) => r.status === 'success').length,
    [results]
  );
  const failedCount = useMemo(
    () => Object.values(results).filter((r) => r.status === 'failed').length,
    [results]
  );
  const pendingCount = useMemo(
    () => Object.values(results).filter((r) => r.status === 'pending').length,
    [results]
  );
  const percent = totalCount > 0 ? Math.round((testedCount / totalCount) * 100) : 0;
  const hasResults = Object.keys(results).length > 0;

  const filteredModels = useMemo(() => {
    return models.filter((m) => {
      const res = results[m];
      if (filterTab === 'available') {
        if (!res || !res.healthy) return false;
      } else if (filterTab === 'failed') {
        if (!res || res.status !== 'failed') return false;
      }

      if (searchFilter.trim()) {
        const q = searchFilter.trim().toLowerCase();
        const matchesName = m.toLowerCase().includes(q);
        const matchesMsg = res?.message ? res.message.toLowerCase().includes(q) : false;
        if (!matchesName && !matchesMsg) return false;
      }
      return true;
    });
  }, [models, results, filterTab, searchFilter]);

  return (
    <div className="flex flex-col gap-3 p-3 rounded-lg border border-white/[0.08] bg-white/[0.015]">
      {/* Top Controls Row */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-col gap-0.5 min-w-0">
          <span className="text-neutral-300 font-medium text-[11px] flex items-center gap-1.5">
            <Zap className="w-3.5 h-3.5 text-cyan-400" />
            Batch Model Verification
          </span>
          <span className="text-[10px] text-neutral-500 truncate">
            {isModelsLoading
              ? `Fetching models from ${upstreamName || 'upstream'}...`
              : models.length > 0
              ? `Probe all ${models.length} models on ${upstreamName || 'upstream'} with safe RPS throttling`
              : `No models detected from ${upstreamName || 'upstream'} to batch test`}
          </span>
        </div>

        <div className="flex items-center gap-2 shrink-0">
          {/* Safe Rate Limiting Selector */}
          <div className="flex items-center gap-1.5 bg-black/40 border border-white/[0.08] px-2 py-1 rounded-md text-[10px] text-neutral-400">
            <span className="text-neutral-500 hidden sm:inline">Safe Rate:</span>
            <select
              value={safeRps}
              disabled={isTesting}
              onChange={(e) => onSafeRpsChange(Number(e.target.value))}
              className="bg-transparent text-neutral-300 font-mono text-[10px] focus:outline-none cursor-pointer disabled:opacity-50"
              title="Safe request rate throttle between health checks to prevent quota exhaustion"
            >
              <option value={1} className="bg-[#090b10] text-neutral-200">1 req/s (Ultra Safe)</option>
              <option value={2} className="bg-[#090b10] text-neutral-200">2 req/s (Recommended)</option>
              <option value={5} className="bg-[#090b10] text-neutral-200">5 req/s (Fast)</option>
            </select>
          </div>

          {/* Action Button */}
          {isTesting ? (
            <Button
              type="button"
              variant="minimal"
              size="sm"
              onClick={onStopTest}
              className="text-xs px-2.5 py-1 text-rose-400 hover:text-rose-300 border-rose-500/30 bg-rose-500/10 hover:bg-rose-500/20"
              leftIcon={<StopCircle className="w-3.5 h-3.5 text-rose-400" />}
            >
              Stop Testing
            </Button>
          ) : (
            <Button
              type="button"
              variant="minimal"
              size="sm"
              onClick={onStartTest}
              disabled={models.length === 0 || isModelsLoading}
              className="text-xs px-2.5 py-1 text-neutral-300 hover:text-white border-white/10 hover:border-white/20"
              leftIcon={<Play className="w-3 h-3 text-neutral-300 fill-neutral-300/20" />}
            >
              {hasResults ? `Re-test All (${models.length})` : `Test All Models (${models.length})`}
            </Button>
          )}
        </div>
      </div>

      {/* Progress & Summary Bar */}
      {(isTesting || hasResults) && (
        <div className="flex flex-col gap-2 pt-2 border-t border-white/[0.04]">
          {/* Progress Bar */}
          <div className="w-full bg-white/[0.05] h-1.5 rounded-full overflow-hidden">
            <div
              className={cn(
                'h-full transition-all duration-300 rounded-full',
                isTesting
                  ? 'bg-cyan-400'
                  : failedCount === 0 && testedCount > 0
                  ? 'bg-emerald-400'
                  : 'bg-neutral-300'
              )}
              style={{ width: `${percent}%` }}
            />
          </div>

          <div className="flex flex-wrap items-center justify-between gap-2 text-[10px]">
            <div className="flex items-center gap-1.5 text-neutral-400 font-mono">
              {isTesting ? (
                <>
                  <Loader2 className="w-2.5 h-2.5 animate-spin text-cyan-400" />
                  <span>Testing: {testedCount}/{totalCount} ({percent}%)</span>
                </>
              ) : (
                <span>Completed {testedCount} of {totalCount} models</span>
              )}
            </div>

            <div className="flex items-center gap-2">
              <Badge variant="emerald" dot>
                {availableCount} Available
              </Badge>
              <Badge variant="rose" dot>
                {failedCount} Unavailable
              </Badge>
              {pendingCount > 0 ? (
                <Badge variant="neutral">
                  {pendingCount} Pending
                </Badge>
              ) : null}

              <button
                type="button"
                onClick={onToggleExpanded}
                className="flex items-center gap-1 text-[10px] text-neutral-400 hover:text-white transition-colors cursor-pointer ml-1 underline underline-offset-2"
              >
                {isExpanded ? (
                  <>
                    <span>Hide Details</span>
                    <ChevronUp className="w-3 h-3" />
                  </>
                ) : (
                  <>
                    <span>Show Details</span>
                    <ChevronDown className="w-3 h-3" />
                  </>
                )}
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Expanded Results Section */}
      {(isTesting || hasResults) && isExpanded && (
        <div className="flex flex-col gap-2 pt-2 border-t border-white/[0.04]">
          {/* Tabs and Search Bar */}
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="flex items-center gap-1 bg-black/40 border border-white/[0.06] p-0.5 rounded-lg text-[10px]">
              <button
                type="button"
                onClick={() => setFilterTab('all')}
                className={cn(
                  'px-2 py-0.5 rounded-md transition-colors cursor-pointer',
                  filterTab === 'all'
                    ? 'bg-white/[0.1] text-white font-medium'
                    : 'text-neutral-400 hover:text-white'
                )}
              >
                All ({totalCount})
              </button>
              <button
                type="button"
                onClick={() => setFilterTab('available')}
                className={cn(
                  'px-2 py-0.5 rounded-md transition-colors cursor-pointer',
                  filterTab === 'available'
                    ? 'bg-emerald-500/20 text-emerald-300 font-medium'
                    : 'text-neutral-400 hover:text-emerald-300'
                )}
              >
                Available ({availableCount})
              </button>
              <button
                type="button"
                onClick={() => setFilterTab('failed')}
                className={cn(
                  'px-2 py-0.5 rounded-md transition-colors cursor-pointer',
                  filterTab === 'failed'
                    ? 'bg-rose-500/20 text-rose-300 font-medium'
                    : 'text-neutral-400 hover:text-rose-300'
                )}
              >
                Unavailable ({failedCount})
              </button>
            </div>

            {/* Search Input */}
            <div className="relative flex-1 max-w-[220px]">
              <Search className="w-3 h-3 text-neutral-500 absolute left-2 top-1/2 -translate-y-1/2 pointer-events-none" />
              <input
                type="text"
                value={searchFilter}
                onChange={(e) => setSearchFilter(e.target.value)}
                placeholder="Search models..."
                className="w-full pl-6 pr-2 py-1 rounded bg-black/40 border border-white/[0.06] text-white font-mono text-[10px] focus:outline-none focus:border-white/20 placeholder:text-neutral-600"
              />
            </div>
          </div>

          {/* Model Items List */}
          <div className="max-h-56 overflow-y-auto rounded-lg border border-white/[0.06] bg-[#07090e] divide-y divide-white/[0.04]">
            {filteredModels.length === 0 ? (
              <div className="p-4 text-center text-neutral-500 text-[11px] font-mono">
                No models matching current filter
              </div>
            ) : (
              filteredModels.map((m) => {
                const res = results[m];
                const status = res?.status || 'pending';
                const isSelected = selectedModel === m;

                return (
                  <div
                    key={m}
                    className={cn(
                      'flex items-center justify-between gap-2 p-2 hover:bg-white/[0.02] transition-colors',
                      isSelected && 'bg-white/[0.04]'
                    )}
                  >
                    <div className="flex items-center gap-2 min-w-0 flex-1">
                      {status === 'success' ? (
                        <CheckCircle2 className="w-3.5 h-3.5 text-emerald-400 shrink-0" />
                      ) : status === 'failed' ? (
                        <XCircle className="w-3.5 h-3.5 text-rose-400 shrink-0" />
                      ) : status === 'testing' ? (
                        <Loader2 className="w-3.5 h-3.5 animate-spin text-cyan-400 shrink-0" />
                      ) : (
                        <span className="w-3.5 h-3.5 rounded-full border border-neutral-700 flex items-center justify-center text-[8px] text-neutral-500 shrink-0 font-mono">
                          -
                        </span>
                      )}

                      <div className="flex flex-col min-w-0 flex-1">
                        <div className="flex items-center gap-2">
                          <span className="text-[11px] font-mono text-neutral-200 truncate font-medium">
                            {m}
                          </span>
                          {res?.latencyMs ? (
                            <span className="text-[9px] font-mono text-neutral-500 whitespace-nowrap">
                              {res.latencyMs}ms
                            </span>
                          ) : null}
                        </div>
                        {res?.message && res.status === 'failed' ? (
                          <span className="text-[10px] font-mono text-rose-400/80 truncate block" title={res.message}>
                            {res.statusCode && res.statusCode > 0 ? `HTTP ${res.statusCode}: ` : ''}{res.message}
                          </span>
                        ) : null}
                      </div>
                    </div>

                    <div className="flex items-center gap-1.5 shrink-0">
                      {status === 'success' ? (
                        <span className="text-[9px] font-mono text-emerald-400 bg-emerald-500/10 px-1.5 py-0.5 rounded border border-emerald-500/20">
                          Active
                        </span>
                      ) : status === 'failed' ? (
                        <span className="text-[9px] font-mono text-rose-400 bg-rose-500/10 px-1.5 py-0.5 rounded border border-rose-500/20">
                          {res?.statusCode ? `HTTP ${res.statusCode}` : 'Failed'}
                        </span>
                      ) : null}

                      {isSelected ? (
                        <span className="flex items-center gap-1 text-[10px] font-mono text-emerald-300 bg-emerald-500/20 px-2 py-0.5 rounded border border-emerald-500/30">
                          <Check className="w-2.5 h-2.5" />
                          <span>Selected</span>
                        </span>
                      ) : (
                        <button
                          type="button"
                          onClick={() => onSelectModel(m, res || { model: m, status: 'pending' })}
                          className="text-[10px] font-mono px-2 py-0.5 rounded bg-white/[0.04] hover:bg-white/[0.1] text-neutral-300 hover:text-white border border-white/[0.08] transition-colors cursor-pointer"
                        >
                          Select
                        </button>
                      )}
                    </div>
                  </div>
                );
              })
            )}
          </div>
        </div>
      )}
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

  // Batch model testing state with safe RPS throttle
  const [safeRps, setSafeRps] = useState<number>(2);
  const [isBatchTesting, setIsBatchTesting] = useState<boolean>(false);
  const [batchResults, setBatchResults] = useState<Record<string, ModelBatchItemResult>>({});
  const [isBatchExpanded, setIsBatchExpanded] = useState<boolean>(false);
  const batchAbortControllerRef = useRef<AbortController | null>(null);

  // Abort and stop batch testing
  const handleStopBatchTest = useCallback(() => {
    if (batchAbortControllerRef.current) {
      batchAbortControllerRef.current.abort();
      batchAbortControllerRef.current = null;
    }
    setIsBatchTesting(false);
    setBatchResults((prev) => {
      const updated: Record<string, ModelBatchItemResult> = { ...prev };
      for (const [m, res] of Object.entries(updated)) {
        if (res.status === 'pending' || res.status === 'testing') {
          updated[m] = { ...res, status: 'canceled', message: 'Test stopped by user' };
        }
      }
      return updated;
    });
  }, []);

  // Cleanup abort controller on unmount or when modal closes
  useEffect(() => {
    if (!isOpen && batchAbortControllerRef.current) {
      batchAbortControllerRef.current.abort();
      batchAbortControllerRef.current = null;
      setIsBatchTesting(false);
    }
    return () => {
      if (batchAbortControllerRef.current) {
        batchAbortControllerRef.current.abort();
        batchAbortControllerRef.current = null;
      }
    };
  }, [isOpen]);

  // Reset batch testing results when selected upstream host changes
  useEffect(() => {
    handleStopBatchTest();
    setBatchResults({});
    setIsBatchExpanded(false);
  }, [upstream, handleStopBatchTest]);

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

    try {
      const res = await checkUpstreamHealth(
        {
          name: selectedUpstreamObj.name,
          protocol: selectedUpstreamObj.protocol,
          base_url: selectedUpstreamObj.base_url,
          key_ref: selectedUpstreamObj.credential_ref || selectedUpstreamObj.credential_pool?.[0]?.ref,
          api_key: selectedUpstreamObj.api_key,
          model: targetModel,
          timeout_ms: 10000,
        },
        adminToken
      );

      setCheckResult({
        healthy: res.healthy,
        statusCode: res.status_code,
        latencyMs: res.latency_ms,
        message: res.message,
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

  // Batch model testing runner with safe rate limiting
  const handleStartBatchTest = useCallback(async () => {
    if (!selectedUpstreamObj || fetchedModels.length === 0 || isBatchTesting) return;

    // Abort any active batch run
    if (batchAbortControllerRef.current) {
      batchAbortControllerRef.current.abort();
    }
    const controller = new AbortController();
    batchAbortControllerRef.current = controller;
    const signal = controller.signal;

    // Initialize all models to pending
    const initialResults: Record<string, ModelBatchItemResult> = {};
    for (const m of fetchedModels) {
      initialResults[m] = {
        model: m,
        status: 'pending',
      };
    }
    setBatchResults(initialResults);
    setIsBatchTesting(true);
    setIsBatchExpanded(true);

    const intervalMs = Math.max(100, Math.round(1000 / safeRps));
    const targetUpstream = selectedUpstreamObj;
    const credentialRef = targetUpstream.credential_ref || targetUpstream.credential_pool?.[0]?.ref;

    for (let i = 0; i < fetchedModels.length; i++) {
      if (signal.aborted) break;
      const modelName = fetchedModels[i];

      setBatchResults((prev) => ({
        ...prev,
        [modelName]: {
          model: modelName,
          status: 'testing',
        },
      }));

      const startTime = Date.now();
      try {
        const res = await checkUpstreamHealth(
          {
            name: targetUpstream.name,
            protocol: targetUpstream.protocol,
            base_url: targetUpstream.base_url,
            key_ref: credentialRef,
            api_key: targetUpstream.api_key,
            model: modelName,
            timeout_ms: 8000,
          },
          adminToken,
          signal
        );

        if (signal.aborted) break;

        const isHealthy = res.healthy || res.status_code === 200;
        setBatchResults((prev) => ({
          ...prev,
          [modelName]: {
            model: modelName,
            status: isHealthy ? 'success' : 'failed',
            healthy: isHealthy,
            statusCode: res.status_code,
            latencyMs: res.latency_ms,
            message: res.message,
          },
        }));
      } catch (err: unknown) {
        if (signal.aborted) break;
        const latencyMs = Date.now() - startTime;
        const msg = err instanceof Error ? err.message : 'Connection test failed';
        setBatchResults((prev) => ({
          ...prev,
          [modelName]: {
            model: modelName,
            status: 'failed',
            healthy: false,
            statusCode: 0,
            latencyMs,
            message: msg,
          },
        }));
      }

      // Safe throttle delay between consecutive model requests
      if (i < fetchedModels.length - 1 && !signal.aborted) {
        await new Promise<void>((resolve) => {
          const timer = setTimeout(resolve, intervalMs);
          signal.addEventListener(
            'abort',
            () => {
              clearTimeout(timer);
              resolve();
            },
            { once: true }
          );
        });
      }
    }

    if (batchAbortControllerRef.current === controller) {
      batchAbortControllerRef.current = null;
    }
    setIsBatchTesting(false);
  }, [selectedUpstreamObj, fetchedModels, isBatchTesting, safeRps, adminToken]);

  // Select tested model and populate form & verification feedback
  const handleSelectBatchModel = useCallback(
    (model: string, result: ModelBatchItemResult) => {
      setUpstreamModel(model);
      setIsManualPrivateModel(false);
      setPublicName((prev) => (prev ? prev : model));

      if (result.healthy != null) {
        setCheckResult({
          healthy: !!result.healthy,
          statusCode: result.statusCode ?? 0,
          latencyMs: result.latencyMs ?? 0,
          message: result.message ?? '',
        });
      }

      // Auto-tune capabilities based on model name hints
      const lower = model.toLowerCase();
      if (lower.includes('embed')) {
        setEmbeddings(true);
        setStream(false);
        setTools(false);
      } else if (
        lower.includes('vision') ||
        lower.includes('gpt-4o') ||
        lower.includes('claude-3') ||
        lower.includes('gemini')
      ) {
        setVision(true);
      }
      if (lower.includes('gemini-3') || lower.includes('claude-opus-4')) {
        setMaxContext('1000000');
      }
    },
    []
  );

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
                onChange={(e) => {
                  setUpstreamModel(e.target.value);
                  setCheckResult(null);
                }}
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

        {/* Batch Model Verification with Safe RPS Throttle */}
        <ModelBatchTestPanel
          upstreamName={selectedUpstreamObj?.name || upstream}
          models={fetchedModels}
          isModelsLoading={isModelsLoading || isModelsFetching}
          results={batchResults}
          isTesting={isBatchTesting}
          safeRps={safeRps}
          onSafeRpsChange={setSafeRps}
          onStartTest={handleStartBatchTest}
          onStopTest={handleStopBatchTest}
          selectedModel={upstreamModel}
          onSelectModel={handleSelectBatchModel}
          isExpanded={isBatchExpanded}
          onToggleExpanded={() => setIsBatchExpanded((prev) => !prev)}
        />

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
