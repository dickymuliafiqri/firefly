import React, { useState, useEffect, useCallback, useMemo, useDeferredValue, useRef } from 'react';
import type { UpstreamDTO, CredentialKeyDTO, Protocol, ConnectionDTO } from '@/services/schema';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import { useStoreActions, useAppStore, buildSettingsPayload } from '@/core/state/store';
import {
  useSaveSettingsMutation,
  useOAuthConnectionsQuery,
  useTursoProvidersQuery,
  useSettingsQuery,
  checkUpstreamHealth,
  fetchUpstreamModels,
  fetchTursoProviderKeys,
  type UpstreamCheckResponse,
} from '@/services/api';
import {
  Check,
  Loader2,
  Trash2,
  Plus,
  Search,
  Copy,
  RotateCw,
  StopCircle,
  UserCheck,
  Database,
  Play,
} from 'lucide-react';
import { cn, copyToClipboard } from '@/lib/utils';
import { OAuthConnectDialog } from './OAuthConnectDialog';

export function getOAuthDefaultUrl(proto: string): string {
  switch (proto) {
    case 'antigravity':
      return 'https://cloudsandbox-pa.googleapis.com';
    case 'cline':
      return 'https://api.cline.bot/api/v1';
    case 'codebuddy_cn':
    case 'codebuddy-cn':
      return 'https://copilot.tencent.com';
    case 'codebuddy_intl':
    case 'codebuddy-intl':
      return 'https://www.codebuddy.ai';
    default:
      return '';
  }
}

export function isOAuthProtocol(proto?: string): boolean {
  return ['antigravity', 'cline', 'codebuddy_cn', 'codebuddy_intl', 'codebuddy-cn', 'codebuddy-intl'].includes(proto || '');
}

export interface BoundAccountItem {
  id: string;
  ref: string;
  connId: string;
  email?: string;
  projectId?: string;
  isExpired?: boolean;
  maxConcurrent?: number | null;
  rps?: number | null;
}

export interface UpstreamModalProps {
  isOpen: boolean;
  onClose: () => void;
  upstreamToEdit?: UpstreamDTO | null;
  onDelete?: (upstream: UpstreamDTO) => void;
}

export interface KeyItem {
  id: string;
  ref: string;
  secret: string;
  rps?: number | null;
  max_concurrent?: number | null;
  status: 'idle' | 'checking' | 'valid' | 'invalid' | 'rate_limited' | 'error';
  statusCode?: number;
  latencyMs?: number;
  message?: string;
}

export type ModelBatchStatus = 'idle' | 'testing' | 'success' | 'failed';

export interface ModelBatchItemResult {
  model: string;
  status: ModelBatchStatus;
  statusCode?: number;
  latencyMs?: number;
  errorMessage?: string;
  testedAt?: number;
}

type TabType = 'general' | 'keys' | 'models' | 'load_balancing' | 'network';

function parseDraftNumber(value: string): number | null {
  if (value.trim() === '') return null;
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : null;
}

function numericDraft(value: number | null | undefined, fallback: number): string {
  return String(value ?? fallback);
}

/**
 * Helper to mask secrets cleanly for display
 */
function maskKeyForDisplay(s: string): string {
  if (!s) return '';
  if (s.includes('...') || s === '[REDACTED]') return s;
  if (s.length <= 8) return 'sk-***';
  return `${s.slice(0, 4)}...${s.slice(-4)}`;
}

/**
 * KeyRowItem — Memoized key slot row for high performance rendering of 100+ keys
 * (Adheres to Vercel React Best Practice: rerender-memo & rendering-content-visibility)
 */
const KeyRowItem = React.memo(function KeyRowItem({
  item,
  index,
  onTest,
  onRemove,
}: {
  item: KeyItem;
  index: number;
  onTest: (id: string) => void;
  onRemove: (id: string) => void;
}) {
  const [copied, setCopied] = useState(false);

  const handleCopy = useCallback(async () => {
    const success = await copyToClipboard(item.secret);
    if (success) {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    }
  }, [item.secret]);

  const displaySecret = maskKeyForDisplay(item.secret);

  return (
    <div
      style={{ contentVisibility: 'auto', containIntrinsicSize: '0 40px' }}
      className="flex items-center justify-between p-2 rounded-lg border border-white/[0.04] bg-white/[0.01] hover:bg-white/[0.03] transition-colors font-mono text-xs gap-3"
    >
      {/* Left: Index & Ref */}
      <div className="flex items-center gap-2.5 min-w-0 flex-1">
        <span className="text-[10px] text-neutral-500 tabular-nums w-7 shrink-0">
          #{index + 1}
        </span>
        <span className="text-[11px] font-medium text-neutral-300 shrink-0">
          {item.ref}
        </span>
        <span
          className="text-[11px] text-neutral-400 font-mono truncate max-w-[200px] sm:max-w-[280px]"
          title={item.secret}
        >
          {displaySecret}
        </span>
        {item.secret && !item.secret.includes('...') ? (
          <button
            type="button"
            onClick={handleCopy}
            className="text-neutral-500 hover:text-white transition-colors p-0.5 cursor-pointer"
            title="Copy API Key"
          >
            {copied ? (
              <Check className="w-2.5 h-2.5 text-emerald-400" />
            ) : (
              <Copy className="w-2.5 h-2.5" />
            )}
          </button>
        ) : null}
      </div>

      {/* Right: Status text & Actions */}
      <div className="flex items-center gap-2.5 shrink-0">
        {item.status === 'checking' ? (
          <span className="inline-flex items-center gap-1 text-[10px] text-blue-400 font-mono">
            <Loader2 className="w-2.5 h-2.5 animate-spin" />
            <span>testing...</span>
          </span>
        ) : item.status === 'valid' ? (
          <span
            className="text-[10px] text-emerald-400 font-mono tabular-nums"
            title={item.message || 'Key active and verified via inference'}
          >
            Active · {item.latencyMs ?? 0}ms
          </span>
        ) : item.status === 'invalid' ? (
          <span
            className="text-[10px] text-rose-400 font-mono tabular-nums"
            title={item.message || 'Invalid or revoked key'}
          >
            invalid {item.statusCode ? `(${item.statusCode})` : ''}
          </span>
        ) : item.status === 'rate_limited' ? (
          <span
            className="text-[10px] text-amber-400 font-mono tabular-nums"
            title={item.message || 'Rate limit (429)'}
          >
            429 cooldown
          </span>
        ) : item.status === 'error' ? (
          <span
            className="text-[10px] text-rose-400 font-mono truncate max-w-[120px]"
            title={item.message || 'Error checking key'}
          >
            {item.message || 'error'}
          </span>
        ) : (
          <span className="text-[10px] text-neutral-500 font-mono">
            ready
          </span>
        )}

        {/* Single Test Button */}
        <button
          type="button"
          onClick={() => onTest(item.id)}
          disabled={item.status === 'checking'}
          className="text-neutral-400 hover:text-white transition-colors p-1 rounded hover:bg-white/[0.06] disabled:opacity-40 cursor-pointer"
          title="Test this API Key"
        >
          <RotateCw className="w-3.5 h-3.5" />
        </button>

        {/* Remove Button */}
        <button
          type="button"
          onClick={() => onRemove(item.id)}
          className="text-neutral-500 hover:text-rose-400 transition-colors p-1 rounded hover:bg-white/[0.06] cursor-pointer"
          title="Remove Key"
        >
          <Trash2 className="w-3.5 h-3.5" />
        </button>
      </div>
    </div>
  );
});

/**
 * Pick optimal free/lightweight probe model from a list of discovered models.
 * Prioritizes:
 * 1. Contains 'free' (case-insensitive, e.g. 'meta-llama/llama-3-8b-instruct:free')
 * 2. Contains 'flash' (case-insensitive, e.g. 'gemini-2.0-flash', 'gemini-1.5-flash')
 * 3. Fallback: contains 'mini', 'haiku', 'chat' (excluding embedding/moderation models)
 * 4. Fallback: first non-embedding model
 */
export function pickOptimalProbeModel(models: string[]): string | undefined {
  if (!models || models.length === 0) return undefined;

  // Filter out non-chat models (embeddings, moderation, rerank)
  const chatCandidates = models.filter((m) => {
    const lower = m.toLowerCase();
    return !lower.includes('embed') && !lower.includes('moderation') && !lower.includes('rerank');
  });
  const candidates = chatCandidates.length > 0 ? chatCandidates : models;

  // 1. Primary choice: keyword 'free'
  const freeModel = candidates.find((m) => m.toLowerCase().includes('free'));
  if (freeModel) return freeModel;

  // 2. Secondary choice: keyword 'flash'
  const flashModel = candidates.find((m) => m.toLowerCase().includes('flash'));
  if (flashModel) return flashModel;

  // 3. Fallbacks: lightweight standard models
  const miniModel = candidates.find((m) => m.toLowerCase().includes('mini'));
  if (miniModel) return miniModel;
  const haikuModel = candidates.find((m) => m.toLowerCase().includes('haiku'));
  if (haikuModel) return haikuModel;
  const chatModel = candidates.find((m) => m.toLowerCase().includes('chat'));
  if (chatModel) return chatModel;

  return candidates[0];
}

/**
 * ModelRowItem — Memoized model item
 */
const ModelRowItem = React.memo(function ModelRowItem({
  modelId,
  isInCatalog,
  isProbeModel,
  batchResult,
  onSetProbeModel,
  onCreateRoute,
  onTestModel,
}: {
  modelId: string;
  isInCatalog: boolean;
  isProbeModel?: boolean;
  batchResult?: ModelBatchItemResult;
  onSetProbeModel?: (modelId: string) => void;
  onCreateRoute?: (modelId: string) => void;
  onTestModel?: (modelId: string) => void;
}) {
  const [copied, setCopied] = useState(false);

  const handleCopy = useCallback(async () => {
    const success = await copyToClipboard(modelId);
    if (success) {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    }
  }, [modelId]);

  return (
    <div
      style={{ contentVisibility: 'auto', containIntrinsicSize: '0 36px' }}
      className="flex items-center justify-between p-2 rounded-lg border border-white/[0.04] bg-white/[0.01] hover:bg-white/[0.025] transition-colors font-mono text-xs gap-2"
    >
      <div className="flex items-center gap-2 truncate min-w-0 flex-1">
        <span className="text-neutral-200 font-medium text-[11px] truncate">
          {modelId}
        </span>
      </div>

      <div className="flex items-center gap-2 shrink-0">
        {/* Verification Status Badge */}
        {batchResult && batchResult.status === 'testing' ? (
          <span className="text-[10px] px-1.5 py-0.5 rounded bg-cyan-500/10 border border-cyan-500/20 text-cyan-300 font-mono flex items-center gap-1">
            <Loader2 className="w-2.5 h-2.5 animate-spin text-cyan-400" />
            <span>testing...</span>
          </span>
        ) : batchResult && batchResult.status === 'success' ? (
          <span className="text-[10px] px-1.5 py-0.5 rounded bg-emerald-500/10 border border-emerald-500/20 text-emerald-400 font-mono flex items-center gap-1">
            <span className="w-1.5 h-1.5 rounded-full bg-emerald-400" />
            <span>Active{batchResult.latencyMs != null ? ` · ${batchResult.latencyMs}ms` : ''}</span>
          </span>
        ) : batchResult && batchResult.status === 'failed' ? (
          <span
            className="text-[10px] px-1.5 py-0.5 rounded bg-rose-500/10 border border-rose-500/20 text-rose-400 font-mono flex items-center gap-1"
            title={batchResult.errorMessage || 'Model verification failed'}
          >
            <span className="w-1.5 h-1.5 rounded-full bg-rose-400" />
            <span>Failed{batchResult.statusCode ? ` (${batchResult.statusCode})` : ''}</span>
          </span>
        ) : null}

        {/* Single Model Test Button */}
        {onTestModel ? (
          <button
            type="button"
            onClick={() => onTestModel(modelId)}
            disabled={batchResult?.status === 'testing'}
            className="text-neutral-400 hover:text-white transition-colors p-1 rounded hover:bg-white/[0.06] disabled:opacity-40 cursor-pointer"
            title="Test model inference"
          >
            <RotateCw className="w-3 h-3" />
          </button>
        ) : null}

        {/* Probe Model Designation */}
        {isProbeModel ? (
          <span className="text-[10px] px-1.5 py-0.5 rounded bg-amber-500/10 border border-amber-500/20 text-amber-300 font-mono font-medium">
            PROBE MODEL
          </span>
        ) : onSetProbeModel ? (
          <button
            type="button"
            onClick={() => onSetProbeModel(modelId)}
            className="text-[10px] px-1.5 py-0.5 rounded text-neutral-400 hover:text-white hover:bg-white/[0.06] border border-white/[0.04] transition-colors cursor-pointer"
            title="Designate as health check probe model"
          >
            Set as Probe
          </button>
        ) : null}

        {/* Route Status in Catalog */}
        {isInCatalog ? (
          <span className="text-[10px] px-1.5 py-0.5 rounded bg-emerald-500/10 border border-emerald-500/20 text-emerald-400 font-mono flex items-center gap-1">
            <Check className="w-2.5 h-2.5" />
            <span>Route Active</span>
          </span>
        ) : onCreateRoute ? (
          <button
            type="button"
            onClick={() => onCreateRoute(modelId)}
            className="text-[10px] px-2 py-0.5 rounded bg-cyan-500/10 hover:bg-cyan-500/20 text-cyan-300 border border-cyan-500/30 hover:border-cyan-500/50 transition-colors font-mono cursor-pointer flex items-center gap-1"
            title="Add this model as a routing entry in Models catalog"
          >
            <Plus className="w-2.5 h-2.5" />
            <span>Add Route</span>
          </button>
        ) : (
          <span className="text-[10px] text-neutral-500 font-mono">
            unmapped
          </span>
        )}

        <button
          type="button"
          onClick={handleCopy}
          className="p-1 rounded text-neutral-400 hover:text-white hover:bg-white/[0.05] transition-colors cursor-pointer"
          title="Copy Model ID"
        >
          {copied ? (
            <Check className="w-3 h-3 text-emerald-400" />
          ) : (
            <Copy className="w-3 h-3" />
          )}
        </button>
      </div>
    </div>
  );
});

export const UpstreamModal = React.memo(function UpstreamModal({
  isOpen,
  onClose,
  upstreamToEdit,
  onDelete,
}: UpstreamModalProps) {
  const { addOrUpdateUpstream, addOrUpdateModel, addToast } = useStoreActions();
  const saveMutation = useSaveSettingsMutation();
  const adminToken = useAppStore((s) => s.adminToken);
  const catalogModels = useAppStore((s) => s.models);

  const [activeTab, setActiveTab] = useState<TabType>('keys');

  // General tab state
  const [name, setName] = useState('');
  const [authType, setAuthType] = useState<'direct' | 'oauth'>('direct');
  const [protocol, setProtocol] = useState<Protocol>('openai');
  const [baseUrl, setBaseUrl] = useState('');
  const [baseUrls, setBaseUrls] = useState<string[]>([]);
  const [newBaseUrlInput, setNewBaseUrlInput] = useState('');
  const [providerId, setProviderId] = useState('');
  const [probeModel, setProbeModel] = useState('');

  // OAuth tab state
  const [boundAccounts, setBoundAccounts] = useState<BoundAccountItem[]>([]);
  const [connectDialogOpen, setConnectDialogOpen] = useState(false);
  const [selectedExistingConnId, setSelectedExistingConnId] = useState('');
  const { data: oauthConnections = [] } = useOAuthConnectionsQuery();

  // Turso Harvester Providers
  const { data: serverSettings } = useSettingsQuery();
  const {
    data: tursoProvidersData,
    isLoading: isLoadingProviders,
    refetch: refetchTursoProviders,
  } = useTursoProvidersQuery();

  const isTursoConfigured = Boolean(
    tursoProvidersData?.configured ||
    Boolean(serverSettings?.turso?.database_url) ||
    serverSettings?.storage_engine === 'turso'
  );
  const tursoProviders = useMemo(() => tursoProvidersData?.providers ?? [], [tursoProvidersData]);

  // Refetch Turso providers whenever modal opens
  useEffect(() => {
    if (isOpen) {
      void refetchTursoProviders();
    }
  }, [isOpen, refetchTursoProviders]);

  const handleProviderSelect = useCallback((e: React.ChangeEvent<HTMLSelectElement>) => {
    const selectedId = e.target.value;
    setProviderId(selectedId);
    if (selectedId) {
      const p = tursoProviders.find((prov) => String(prov.id) === selectedId);
      if (p && p.base_url && (!baseUrl || baseUrl === 'https://api.openai.com/v1')) {
        setBaseUrl(p.base_url);
      }
    }
  }, [tursoProviders, baseUrl]);

  const isOAuth = isOAuthProtocol(protocol);

  // Keys tab state
  const [keys, setKeys] = useState<KeyItem[]>([]);
  const [bulkInputOpen, setBulkInputOpen] = useState(false);
  const [bulkText, setBulkText] = useState('');
  const [singleKeyInput, setSingleKeyInput] = useState('');
  const [keySearch, setKeySearch] = useState('');
  const deferredKeySearch = useDeferredValue(keySearch);
  const [keyFilter, setKeyFilter] = useState<'all' | 'valid' | 'invalid' | 'rate_limited' | 'unchecked'>('all');
  const [isImportingDbKeys, setIsImportingDbKeys] = useState(false);
  const [isDbPickerOpen, setIsDbPickerOpen] = useState(false);
  const [selectedImportProviderId, setSelectedImportProviderId] = useState('');

  // Concurrent health check state
  const [isCheckingAll, setIsCheckingAll] = useState(false);
  const [checkProgress, setCheckProgress] = useState({
    completed: 0,
    total: 0,
    valid: 0,
    invalid: 0,
    rateLimited: 0,
  });
  const stopCheckRef = useRef(false);

  // Models tab state
  const [upstreamModels, setUpstreamModels] = useState<string[]>([]);
  const [isLoadingModels, setIsLoadingModels] = useState(false);
  const [modelSearch, setModelSearch] = useState('');
  const deferredModelSearch = useDeferredValue(modelSearch);
  const [isManualProbeInput, setIsManualProbeInput] = useState(false);
  const [safeRps, setSafeRps] = useState<number>(2);
  const [isBatchTesting, setIsBatchTesting] = useState<boolean>(false);
  const [batchResults, setBatchResults] = useState<Record<string, ModelBatchItemResult>>({});
  const batchAbortControllerRef = useRef<AbortController | null>(null);

  // Load balancing tab state
  const [keyStrategy, setKeyStrategy] = useState<'round_robin' | 'least_inflight'>('round_robin');
  const [credentialRps, setCredentialRps] = useState('0');
  const [credentialMaxConcurrent, setCredentialMaxConcurrent] = useState('0');
  const [keyErrorThreshold, setKeyErrorThreshold] = useState('0');
  const [keyErrorAction, setKeyErrorAction] = useState<'deactivate' | 'delete' | 'cooldown'>('deactivate');
  const [keyCooldownSec, setKeyCooldownSec] = useState('300');

  // Network & Timeouts tab state
  const [timeoutSec, setTimeoutSec] = useState('30');
  const [streamTimeoutSec, setStreamTimeoutSec] = useState('120');
  const [idleTimeoutSec, setIdleTimeoutSec] = useState(90);
  const [maxConns, setMaxConns] = useState('1500');
  const [maxIdleConns, setMaxIdleConns] = useState('1000');
  const credentialRpsValue = parseDraftNumber(credentialRps);
  const credentialMaxConcurrentValue = parseDraftNumber(credentialMaxConcurrent);
  const [allowInsecure, setAllowInsecure] = useState(false);
  const [extraHeaders, setExtraHeaders] = useState<Array<{ key: string; value: string }>>([]);

  // Reset or initialize state
  useEffect(() => {
    if (isOpen) {
      if (upstreamToEdit) {
        const proto = (upstreamToEdit.protocol as Protocol) || 'openai';
        const isUpstreamOAuth = isOAuthProtocol(proto);
        setName(upstreamToEdit.name);
        setProtocol(proto);
        setAuthType(isUpstreamOAuth ? 'oauth' : 'direct');
        setBaseUrl(
          upstreamToEdit.base_url ||
          (upstreamToEdit.base_urls && upstreamToEdit.base_urls[0]) ||
          (isUpstreamOAuth ? getOAuthDefaultUrl(proto) : '')
        );
        setBaseUrls(upstreamToEdit.base_urls || []);
        setKeyStrategy(
          (upstreamToEdit.key_strategy as 'round_robin' | 'least_inflight') ||
          (isUpstreamOAuth ? 'least_inflight' : 'round_robin')
        );
        setCredentialRps(numericDraft(upstreamToEdit.credential_rps, 0));
        setCredentialMaxConcurrent(numericDraft(upstreamToEdit.credential_max_concurrent, 0));
        setTimeoutSec(numericDraft(upstreamToEdit.timeout_ms != null ? Math.round(upstreamToEdit.timeout_ms / 1000) : null, 30));
        setStreamTimeoutSec(numericDraft(upstreamToEdit.stream_idle_timeout_ms != null ? Math.round(upstreamToEdit.stream_idle_timeout_ms / 1000) : null, 120));
        setIdleTimeoutSec(Math.round((upstreamToEdit.idle_timeout_ms || 90000) / 1000));
        setMaxConns(numericDraft(upstreamToEdit.max_conns_per_host, 1500));
        setMaxIdleConns(numericDraft(upstreamToEdit.max_idle_conns_per_host, 1000));
        setAllowInsecure(Boolean(upstreamToEdit.allow_insecure));
        setProviderId(upstreamToEdit.provider_id != null ? String(upstreamToEdit.provider_id) : '');
        setKeyErrorThreshold(numericDraft(upstreamToEdit.key_error_threshold, 0));
        setKeyErrorAction((upstreamToEdit.key_error_action as 'deactivate' | 'delete' | 'cooldown') || 'deactivate');
        setKeyCooldownSec(numericDraft(upstreamToEdit.key_cooldown_duration_ms != null ? Math.round(upstreamToEdit.key_cooldown_duration_ms / 1000) : null, 300));
        setProbeModel(upstreamToEdit.probe_model || '');

        // Format extra headers
        if (upstreamToEdit.extra_headers) {
          setExtraHeaders(
            Object.entries(upstreamToEdit.extra_headers).map(([k, v]) => ({ key: k, value: v }))
          );
        } else {
          setExtraHeaders([]);
        }

        // Initialize Keys & Bound Accounts
        let initialKeys: KeyItem[] = [];
        let initialBoundAccounts: BoundAccountItem[] = [];

        if (isUpstreamOAuth) {
          const pool = upstreamToEdit.credential_pool && upstreamToEdit.credential_pool.length > 0
            ? upstreamToEdit.credential_pool
            : upstreamToEdit.credential_ref
            ? [{ ref: upstreamToEdit.credential_ref }]
            : [];
          initialBoundAccounts = pool.map((slot, idx) => {
            const rawRef = slot.ref || '';
            const cleanRef = rawRef.startsWith('oauth:') ? rawRef.slice(6) : rawRef;
            const match = oauthConnections.find((c) => c.id === cleanRef || c.email === cleanRef);
            return {
              id: `acc-${idx}-${cleanRef}`,
              ref: rawRef.startsWith('oauth:') ? rawRef : `oauth:${rawRef}`,
              connId: cleanRef,
              email: match?.email || cleanRef,
              projectId: match?.provider_specific_data?.project_id || match?.provider_specific_data?.region,
              isExpired: match?.is_expired,
              maxConcurrent: slot.max_concurrent,
              rps: slot.rps,
            };
          });
        } else {
          if (upstreamToEdit.credential_pool && upstreamToEdit.credential_pool.length > 0) {
            initialKeys = upstreamToEdit.credential_pool.map((slot, idx) => ({
              id: `k-${idx}-${slot.ref || idx}`,
              ref: slot.ref || `${upstreamToEdit.name}-key-${idx + 1}`,
              secret: slot.secret || slot.api_key || '',
              rps: slot.rps,
              max_concurrent: slot.max_concurrent,
              status: 'idle',
            }));
          } else if (upstreamToEdit.api_keys && upstreamToEdit.api_keys.length > 0) {
            initialKeys = upstreamToEdit.api_keys.map((k, idx) => ({
              id: `k-${idx}`,
              ref: `${upstreamToEdit.name}-key-${idx + 1}`,
              secret: k,
              status: 'idle',
            }));
          } else if (upstreamToEdit.api_key) {
            initialKeys = [
              {
                id: 'k-0',
                ref: `${upstreamToEdit.name}-key-1`,
                secret: upstreamToEdit.api_key,
                status: 'idle',
              },
            ];
          }
        }
        setKeys(initialKeys);
        setBoundAccounts(initialBoundAccounts);
        setActiveTab('keys');
      } else {
        setName('');
        setAuthType('direct');
        setProtocol('openai');
        setBaseUrl('https://api.openai.com/v1');
        setBaseUrls([]);
        setKeyStrategy('round_robin');
        setCredentialRps('0');
        setCredentialMaxConcurrent('0');
        setTimeoutSec('30');
        setStreamTimeoutSec('120');
        setIdleTimeoutSec(90);
        setMaxConns('1500');
        setMaxIdleConns('1000');
        setAllowInsecure(false);
        setExtraHeaders([]);
        setKeys([]);
        setBoundAccounts([]);
        setProviderId('');
        setKeyErrorThreshold('0');
        setKeyErrorAction('deactivate');
        setKeyCooldownSec('300');
        setProbeModel('');
        setIsManualProbeInput(false);
        setActiveTab('general');
      }

      // Reset runtime check states
      setBulkInputOpen(false);
      setBulkText('');
      setSingleKeyInput('');
      setKeySearch('');
      setKeyFilter('all');
      setIsCheckingAll(false);
      setUpstreamModels([]);
      setIsLoadingModels(false);
      setModelSearch('');
      setIsManualProbeInput(false);
      if (batchAbortControllerRef.current) {
        batchAbortControllerRef.current.abort();
        batchAbortControllerRef.current = null;
      }
      setIsBatchTesting(false);
      setBatchResults({});
    }
  }, [upstreamToEdit, isOpen]);

  // Clean up worker pool & batch testing on unmount
  useEffect(() => {
    return () => {
      stopCheckRef.current = true;
      if (batchAbortControllerRef.current) {
        batchAbortControllerRef.current.abort();
        batchAbortControllerRef.current = null;
      }
    };
  }, []);

  // -------------------------------------------------------------
  // Bulk Import Handler
  // -------------------------------------------------------------
  const parsedBulkKeys = useMemo(() => {
    if (!bulkText.trim()) return [];
    return bulkText
      .split(/[\n,;]+/)
      .map((s) => s.trim())
      .filter((s) => s.length > 0 && !s.startsWith('#'));
  }, [bulkText]);

  const uniqueBulkKeys = useMemo(() => {
    return Array.from(new Set(parsedBulkKeys));
  }, [parsedBulkKeys]);

  const handleApplyBulkKeys = useCallback(
    (mode: 'append' | 'replace') => {
      if (uniqueBulkKeys.length === 0) return;

      const prefix = name.trim() || 'upstream';
      const startIndex = mode === 'replace' ? 0 : keys.length;

      const newKeyItems: KeyItem[] = uniqueBulkKeys.map((secret, idx) => ({
        id: `bulk-${Date.now()}-${idx}`,
        ref: `${prefix}-key-${startIndex + idx + 1}`,
        secret,
        status: 'idle',
      }));

      setKeys((prev) => (mode === 'replace' ? newKeyItems : [...prev, ...newKeyItems]));
      setBulkText('');
      setBulkInputOpen(false);

      addToast({
        title: 'API Keys Loaded',
        message: `${newKeyItems.length} keys ${mode === 'replace' ? 'replaced' : 'appended'}.`,
        type: 'info',
      });
    },
    [uniqueBulkKeys, name, keys.length, addToast]
  );

  // -------------------------------------------------------------
  // Import Keys from Turso Database Handler
  // -------------------------------------------------------------
  const handleImportKeysFromDatabase = useCallback(
    async (targetProviderId?: number) => {
      if (!isTursoConfigured) {
        addToast({
          title: 'Database Not Configured',
          message: 'Turso centralized database credentials have not been configured in Settings.',
          type: 'error',
        });
        return;
      }

      // If no target provider ID specified:
      let effectiveProviderId: number | undefined = targetProviderId;
      if (!effectiveProviderId) {
        if (providerId.trim() !== '') {
          const parsed = parseInt(providerId, 10);
          if (!isNaN(parsed) && parsed > 0) {
            effectiveProviderId = parsed;
          }
        }
      }

      // If still not determined:
      if (!effectiveProviderId) {
        if (tursoProviders.length === 0) {
          addToast({
            title: 'No Providers Found',
            message: 'No Harvester providers were found in the Turso database.',
            type: 'info',
          });
          return;
        }
        if (tursoProviders.length === 1) {
          effectiveProviderId = tursoProviders[0].id;
          setProviderId(String(tursoProviders[0].id));
          if (tursoProviders[0].base_url && (!baseUrl || baseUrl === 'https://api.openai.com/v1')) {
            setBaseUrl(tursoProviders[0].base_url);
          }
        } else {
          // Open picker modal
          setSelectedImportProviderId(tursoProviders[0] ? String(tursoProviders[0].id) : '');
          setIsDbPickerOpen(true);
          return;
        }
      }

      setIsImportingDbKeys(true);
      try {
        const res = await fetchTursoProviderKeys(effectiveProviderId, adminToken);
        const incoming = res.keys || [];
        if (incoming.length === 0) {
          addToast({
            title: 'No Active Keys',
            message: 'No active keys found for this provider in the Turso database.',
            type: 'info',
          });
          setIsDbPickerOpen(false);
          return;
        }

        // Deduplicate against existing keys in form state
        const existingSecrets = new Set(keys.map((k) => k.secret.trim()));
        const newValidKeys = incoming.filter(
          (k) => k.api_key && k.api_key.trim() && !existingSecrets.has(k.api_key.trim())
        );
        const duplicatesCount = incoming.length - newValidKeys.length;

        if (newValidKeys.length === 0) {
          addToast({
            title: 'Keys Already Imported',
            message: `All ${incoming.length} key(s) from this provider are already present in the credential pool.`,
            type: 'info',
          });
          setIsDbPickerOpen(false);
          return;
        }

        const prefix = name.trim() || 'upstream';
        const startIndex = keys.length;

        const newItems: KeyItem[] = newValidKeys.map((item, idx) => ({
          id: `turso-${item.id}-${Date.now()}-${idx}`,
          ref: `${prefix}-key-${startIndex + idx + 1}`,
          secret: item.api_key.trim(),
          status: 'idle',
        }));

        setKeys((prev) => [...prev, ...newItems]);
        setIsDbPickerOpen(false);

        // Update providerId and baseUrl if not yet set
        if (!providerId && effectiveProviderId) {
          setProviderId(String(effectiveProviderId));
          const prov = tursoProviders.find((p) => p.id === effectiveProviderId);
          if (prov && prov.base_url && (!baseUrl || baseUrl === 'https://api.openai.com/v1')) {
            setBaseUrl(prov.base_url);
          }
        }

        addToast({
          title: 'Keys Imported from Database',
          message: `Imported ${newItems.length} active key(s) from Turso database${
            duplicatesCount > 0 ? ` (${duplicatesCount} duplicate(s) skipped)` : ''
          }.`,
          type: 'success',
        });
      } catch (err) {
        addToast({
          title: 'Database Import Failed',
          message: err instanceof Error ? err.message : 'Failed to retrieve keys from database',
          type: 'error',
        });
      } finally {
        setIsImportingDbKeys(false);
      }
    },
    [
      isTursoConfigured,
      providerId,
      tursoProviders,
      adminToken,
      keys,
      name,
      baseUrl,
      addToast,
    ]
  );

  // -------------------------------------------------------------
  // Single Key Handler
  // -------------------------------------------------------------
  const handleAddSingleKey = useCallback(() => {
    const trimmed = singleKeyInput.trim();
    if (!trimmed) return;

    const prefix = name.trim() || 'upstream';
    const newSlot: KeyItem = {
      id: `single-${Date.now()}`,
      ref: `${prefix}-key-${keys.length + 1}`,
      secret: trimmed,
      status: 'idle',
    };

    setKeys((prev) => [...prev, newSlot]);
    setSingleKeyInput('');
  }, [singleKeyInput, name, keys.length]);

  const handleRemoveKey = useCallback((id: string) => {
    setKeys((prev) => prev.filter((k) => k.id !== id));
  }, []);

  const handleRemoveInvalidKeys = useCallback(() => {
    setKeys((prev) => {
      const remaining = prev.filter((k) => k.status !== 'invalid');
      const removedCount = prev.length - remaining.length;
      if (removedCount > 0) {
        addToast({
          title: 'Removed Invalid Keys',
          message: `Removed ${removedCount} invalid/expired key${removedCount > 1 ? 's' : ''}.`,
          type: 'info',
        });
      }
      return remaining;
    });
  }, [addToast]);

  const handleUpdateKeyLimit = useCallback(
    (id: string, field: 'rps' | 'max_concurrent', value: number | null) => {
      setKeys((prev) =>
        prev.map((k) => (k.id === id ? { ...k, [field]: value } : k))
      );
    },
    []
  );

  // -------------------------------------------------------------
  // Concurrent Key Health Checker (Vercel Best Practice: async-parallel)
  // -------------------------------------------------------------
  const testKeySlot = useCallback(
    async (item: KeyItem): Promise<UpstreamCheckResponse> => {
      return checkUpstreamHealth(
        {
          name: name.trim(),
          key_ref: item.ref,
          protocol,
          base_url: baseUrl.trim(),
          api_key: item.secret,
          timeout_ms: 8000,
          model: probeModel.trim() || undefined,
        },
        adminToken
      );
    },
    [name, protocol, baseUrl, adminToken, probeModel]
  );

  const handleTestSingleKey = useCallback(
    async (id: string) => {
      const keyItem = keys.find((k) => k.id === id);
      if (!keyItem || !baseUrl.trim()) return;

      setKeys((prev) =>
        prev.map((k) => (k.id === id ? { ...k, status: 'checking', message: undefined } : k))
      );

      try {
        const res = await testKeySlot(keyItem);

        let status: KeyItem['status'] = 'error';
        if (res.healthy || res.status_code === 200) {
          status = 'valid';
          if (res.models && res.models.length > 0) {
            setUpstreamModels(res.models);
          }
        } else if (res.status_code === 401 || res.status_code === 403) {
          status = 'invalid';
        } else if (res.status_code === 429) {
          status = 'rate_limited';
        }

        setKeys((prev) =>
          prev.map((k) =>
            k.id === id
              ? {
                  ...k,
                  status,
                  statusCode: res.status_code,
                  latencyMs: res.latency_ms,
                  message: res.message,
                }
              : k
          )
        );
      } catch (err) {
        setKeys((prev) =>
          prev.map((k) =>
            k.id === id
              ? {
                  ...k,
                  status: 'error',
                  statusCode: 0,
                  message: err instanceof Error ? err.message : 'Network error',
                }
              : k
          )
        );
      }
    },
    [keys, baseUrl, testKeySlot]
  );

  const handleCheckAllKeysConcurrently = useCallback(async () => {
    if (keys.length === 0 || !baseUrl.trim()) {
      addToast({
        title: 'Cannot Check Keys',
        message: 'Please provide Base URL and at least 1 API key.',
        type: 'error',
      });
      return;
    }

    stopCheckRef.current = false;
    setIsCheckingAll(true);

    // Reset status to idle before starting
    setKeys((prev) =>
      prev.map((k) => ({
        ...k,
        status: 'idle',
        statusCode: undefined,
        latencyMs: undefined,
        message: undefined,
      }))
    );

    setCheckProgress({
      completed: 0,
      total: keys.length,
      valid: 0,
      invalid: 0,
      rateLimited: 0,
    });

    const CONCURRENCY = 5; // Balanced concurrent pool to avoid browser socket exhaustion
    const currentKeys = [...keys];
    let currentIndex = 0;
    let completed = 0;
    let validCount = 0;
    let invalidCount = 0;
    let rateLimitedCount = 0;

    const worker = async () => {
      while (currentIndex < currentKeys.length) {
        if (stopCheckRef.current) break;
        const index = currentIndex++;
        const keyItem = currentKeys[index];
        if (!keyItem) continue;

        // Mark checking
        setKeys((prev) =>
          prev.map((k) => (k.id === keyItem.id ? { ...k, status: 'checking' } : k))
        );

        try {
          const res = await testKeySlot(keyItem);

          let status: KeyItem['status'] = 'error';
          if (res.healthy || res.status_code === 200) {
            status = 'valid';
            validCount++;
            if (res.models && res.models.length > 0) {
              setUpstreamModels((existing) =>
                Array.from(new Set([...existing, ...(res.models || [])])).sort()
              );
            }
          } else if (res.status_code === 401 || res.status_code === 403) {
            status = 'invalid';
            invalidCount++;
          } else if (res.status_code === 429) {
            status = 'rate_limited';
            rateLimitedCount++;
          } else {
            status = 'error';
          }

          completed++;
          setCheckProgress({
            completed,
            total: currentKeys.length,
            valid: validCount,
            invalid: invalidCount,
            rateLimited: rateLimitedCount,
          });

          setKeys((prev) =>
            prev.map((k) =>
              k.id === keyItem.id
                ? {
                    ...k,
                    status,
                    statusCode: res.status_code,
                    latencyMs: res.latency_ms,
                    message: res.message,
                  }
                : k
            )
          );
        } catch (err) {
          completed++;
          setCheckProgress({
            completed,
            total: currentKeys.length,
            valid: validCount,
            invalid: invalidCount,
            rateLimited: rateLimitedCount,
          });

          setKeys((prev) =>
            prev.map((k) =>
              k.id === keyItem.id
                ? {
                    ...k,
                    status: 'error',
                    statusCode: 0,
                    message: err instanceof Error ? err.message : 'Network error',
                  }
                : k
            )
          );
        }
      }
    };

    const pool = Array.from({ length: Math.min(CONCURRENCY, currentKeys.length) }, () => worker());
    await Promise.all(pool);

    setIsCheckingAll(false);
  }, [keys, baseUrl, testKeySlot, addToast]);

  const handleStopCheck = useCallback(() => {
    stopCheckRef.current = true;
    setIsCheckingAll(false);
    addToast({
      title: 'Check Stopped',
      message: 'Concurrent health check was halted by operator.',
      type: 'info',
    });
  }, [addToast]);

  // -------------------------------------------------------------
  // Filtered Keys calculation with useDeferredValue
  // -------------------------------------------------------------
  const filteredKeys = useMemo(() => {
    return keys.filter((k) => {
      if (keyFilter === 'valid' && k.status !== 'valid') return false;
      if (keyFilter === 'invalid' && k.status !== 'invalid') return false;
      if (keyFilter === 'rate_limited' && k.status !== 'rate_limited') return false;
      if (keyFilter === 'unchecked' && k.status !== 'idle') return false;

      if (!deferredKeySearch) return true;
      const q = deferredKeySearch.toLowerCase();
      return (
        k.ref.toLowerCase().includes(q) ||
        k.secret.toLowerCase().includes(q) ||
        (k.message && k.message.toLowerCase().includes(q))
      );
    });
  }, [keys, keyFilter, deferredKeySearch]);

  // -------------------------------------------------------------
  // Fetch Models from Upstream
  // -------------------------------------------------------------
  const handleFetchUpstreamModels = useCallback(async () => {
    if (!baseUrl.trim()) return;

    // Pick first valid key or any key (or OAuth bound account ref)
    const oauthRef = isOAuth ? (boundAccounts[0]?.ref || '') : '';
    const validKey = isOAuth
      ? oauthRef
      : (keys.find((k) => k.status === 'valid')?.secret || (keys[0]?.secret || ''));

    if (batchAbortControllerRef.current) {
      batchAbortControllerRef.current.abort();
      batchAbortControllerRef.current = null;
    }
    setIsBatchTesting(false);
    setBatchResults({});

    setIsLoadingModels(true);
    try {
      const res = await fetchUpstreamModels(
        {
          name: name.trim(),
          key_ref: oauthRef || undefined,
          protocol,
          base_url: baseUrl.trim(),
          api_key: validKey,
          timeout_ms: 10000,
        },
        adminToken
      );

      if (res.models && res.models.length > 0) {
        setUpstreamModels(res.models);

        // Auto-select probe model based on 'free' (priority 1) or 'flash' (priority 2)
        if (!probeModel.trim()) {
          const optimal = pickOptimalProbeModel(res.models);
          if (optimal) {
            setProbeModel(optimal);
            addToast({
              title: 'Probe Model Selected',
              message: `Auto-selected "${optimal}" as designated health check model.`,
              type: 'info',
            });
          }
        }

        addToast({
          title: 'Models Loaded',
          message: `Discovered ${res.models.length} models from upstream host.`,
          type: 'success',
        });
      } else {
        addToast({
          title: 'No Models Found',
          message: res.message || 'Upstream host returned empty models list.',
          type: 'info',
        });
      }
    } catch (err) {
      addToast({
        title: 'Failed to Fetch Models',
        message: err instanceof Error ? err.message : 'Connection failed',
        type: 'error',
      });
    } finally {
      setIsLoadingModels(false);
    }
  }, [baseUrl, keys, boundAccounts, isOAuth, name, protocol, adminToken, addToast, probeModel]);

  const filteredModels = useMemo(() => {
    if (!deferredModelSearch) return upstreamModels;
    const q = deferredModelSearch.toLowerCase();
    return upstreamModels.filter((m) => m.toLowerCase().includes(q));
  }, [upstreamModels, deferredModelSearch]);

  const isModelInCatalog = useCallback(
    (modelId: string) => {
      return catalogModels.some(
        (m) =>
          m.upstream === name &&
          (m.upstream_model === modelId || m.public_name === modelId)
      );
    },
    [catalogModels, name]
  );

  const unmappedCount = useMemo(() => {
    return filteredModels.filter((m) => !isModelInCatalog(m)).length;
  }, [filteredModels, isModelInCatalog]);

  const ensureUpstreamInStore = useCallback(() => {
    const upstreamName = name.trim();
    if (!upstreamName) return false;

    const existing = useAppStore.getState().upstreams.find((u) => u.name === upstreamName);
    if (!existing) {
      const currentUpstreamDto: UpstreamDTO = {
        name: upstreamName,
        base_url: baseUrl.trim(),
        protocol,
        enabled: true,
        api_key: keys[0]?.secret || undefined,
        credential_ref: keys[0]?.ref || undefined,
        credential_pool: keys.map((k) => ({
          ref: k.ref,
          secret: k.secret,
          max_concurrent: k.max_concurrent ?? undefined,
          rps: k.rps ?? undefined,
        })),
        probe_model: probeModel.trim() || undefined,
      };
      addOrUpdateUpstream(currentUpstreamDto);
    }
    return true;
  }, [name, baseUrl, protocol, keys, probeModel, addOrUpdateUpstream]);

  const handleCreateRoute = useCallback(
    (modelId: string) => {
      const upstreamName = name.trim();
      if (!upstreamName) {
        addToast({
          title: 'Upstream Name Required',
          message: 'Please provide an Upstream Name in the General tab first before adding routes.',
          type: 'warning',
        });
        return;
      }

      ensureUpstreamInStore();

      addOrUpdateModel({
        public_name: modelId,
        upstream: upstreamName,
        upstream_model: modelId,
        enabled: true,
        max_context: 128000,
        capabilities: {
          stream: true,
          tools: true,
          json_mode: true,
          vision: false,
          embeddings: modelId.toLowerCase().includes('embed'),
        },
      });

      saveMutation.mutate(buildSettingsPayload());

      addToast({
        title: 'Route Created',
        message: `Model "${modelId}" added to active routes.`,
        type: 'success',
      });
    },
    [name, ensureUpstreamInStore, addOrUpdateModel, saveMutation, addToast]
  );

  const handleCreateAllUnmappedRoutes = useCallback(() => {
    const upstreamName = name.trim();
    if (!upstreamName) {
      addToast({
        title: 'Upstream Name Required',
        message: 'Please provide an Upstream Name in the General tab first.',
        type: 'warning',
      });
      return;
    }

    const unmapped = filteredModels.filter((m) => !isModelInCatalog(m));
    if (unmapped.length === 0) return;

    ensureUpstreamInStore();

    for (const modelId of unmapped) {
      addOrUpdateModel({
        public_name: modelId,
        upstream: upstreamName,
        upstream_model: modelId,
        enabled: true,
        max_context: 128000,
        capabilities: {
          stream: true,
          tools: true,
          json_mode: true,
          vision: false,
          embeddings: modelId.toLowerCase().includes('embed'),
        },
      });
    }

    saveMutation.mutate(buildSettingsPayload());

    addToast({
      title: 'Batch Routes Created',
      message: `Added ${unmapped.length} models as routes for ${upstreamName}.`,
      type: 'success',
    });
  }, [name, filteredModels, isModelInCatalog, ensureUpstreamInStore, addOrUpdateModel, saveMutation, addToast]);

  // -------------------------------------------------------------
  // Rate-Paced Batch Model Verification Runner
  // -------------------------------------------------------------
  const handleStopBatchTest = useCallback(() => {
    if (batchAbortControllerRef.current) {
      batchAbortControllerRef.current.abort();
      batchAbortControllerRef.current = null;
    }
    setIsBatchTesting(false);
  }, []);

  const handleStartBatchTest = useCallback(async () => {
    if (upstreamModels.length === 0 || !baseUrl.trim()) return;

    const oauthRef = isOAuth ? (boundAccounts[0]?.ref || '') : '';
    const validKey = isOAuth
      ? oauthRef
      : (keys.find((k) => k.status === 'valid')?.secret || (keys[0]?.secret || ''));

    if (batchAbortControllerRef.current) {
      batchAbortControllerRef.current.abort();
    }
    const abortController = new AbortController();
    batchAbortControllerRef.current = abortController;
    const signal = abortController.signal;

    setIsBatchTesting(true);

    const initialMap: Record<string, ModelBatchItemResult> = {};
    for (const m of upstreamModels) {
      initialMap[m] = {
        model: m,
        status: 'idle',
      };
    }
    setBatchResults(initialMap);

    const intervalMs = Math.max(50, Math.round(1000 / safeRps));
    const maxConcurrency = Math.max(safeRps * 2, 2);

    let activeCount = 0;
    let nextIndex = 0;

    const runModelTest = async (modelName: string) => {
      if (signal.aborted) {
        activeCount--;
        return;
      }

      setBatchResults((prev) => ({
        ...prev,
        [modelName]: {
          model: modelName,
          status: 'testing',
        },
      }));

      try {
        const res = await checkUpstreamHealth(
          {
            name: name.trim(),
            key_ref: oauthRef || (keys.find((k) => k.status === 'valid')?.ref || (keys[0]?.ref || undefined)),
            protocol,
            base_url: baseUrl.trim(),
            api_key: validKey,
            timeout_ms: 8000,
            model: modelName,
          },
          adminToken,
          signal
        );

        if (signal.aborted) return;

        if (res.healthy || res.status_code === 200) {
          setBatchResults((prev) => ({
            ...prev,
            [modelName]: {
              model: modelName,
              status: 'success',
              statusCode: res.status_code || 200,
              latencyMs: res.latency_ms,
              testedAt: Date.now(),
            },
          }));
        } else {
          setBatchResults((prev) => ({
            ...prev,
            [modelName]: {
              model: modelName,
              status: 'failed',
              statusCode: res.status_code,
              latencyMs: res.latency_ms,
              errorMessage: res.message || 'Verification failed',
              testedAt: Date.now(),
            },
          }));
        }
      } catch (err) {
        if (signal.aborted) return;
        setBatchResults((prev) => ({
          ...prev,
          [modelName]: {
            model: modelName,
            status: 'failed',
            errorMessage: err instanceof Error ? err.message : 'Network error',
            testedAt: Date.now(),
          },
        }));
      } finally {
        activeCount--;
      }
    };

    try {
      while (nextIndex < upstreamModels.length) {
        if (signal.aborted) break;

        while (activeCount >= maxConcurrency && !signal.aborted) {
          await new Promise((resolve) => setTimeout(resolve, 50));
        }
        if (signal.aborted) break;

        const currentModel = upstreamModels[nextIndex++];
        activeCount++;

        runModelTest(currentModel);

        if (nextIndex < upstreamModels.length && !signal.aborted) {
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

      while (activeCount > 0 && !signal.aborted) {
        await new Promise((resolve) => setTimeout(resolve, 50));
      }
    } finally {
      if (!signal.aborted) {
        setIsBatchTesting(false);
      }
    }
  }, [upstreamModels, baseUrl, isOAuth, boundAccounts, keys, safeRps, name, protocol, adminToken]);

  const handleTestSingleModel = useCallback(
    async (modelId: string) => {
      if (!baseUrl.trim()) return;

      const oauthRef = isOAuth ? (boundAccounts[0]?.ref || '') : '';
      const validKey = isOAuth
        ? oauthRef
        : (keys.find((k) => k.status === 'valid')?.secret || (keys[0]?.secret || ''));

      setBatchResults((prev) => ({
        ...prev,
        [modelId]: {
          model: modelId,
          status: 'testing',
        },
      }));

      try {
        const res = await checkUpstreamHealth(
          {
            name: name.trim(),
            key_ref: oauthRef || (keys.find((k) => k.status === 'valid')?.ref || (keys[0]?.ref || undefined)),
            protocol,
            base_url: baseUrl.trim(),
            api_key: validKey,
            timeout_ms: 8000,
            model: modelId,
          },
          adminToken
        );

        if (res.healthy || res.status_code === 200) {
          setBatchResults((prev) => ({
            ...prev,
            [modelId]: {
              model: modelId,
              status: 'success',
              statusCode: res.status_code || 200,
              latencyMs: res.latency_ms,
              testedAt: Date.now(),
            },
          }));
        } else {
          setBatchResults((prev) => ({
            ...prev,
            [modelId]: {
              model: modelId,
              status: 'failed',
              statusCode: res.status_code,
              latencyMs: res.latency_ms,
              errorMessage: res.message || 'Verification failed',
              testedAt: Date.now(),
            },
          }));
        }
      } catch (err) {
        setBatchResults((prev) => ({
          ...prev,
          [modelId]: {
            model: modelId,
            status: 'failed',
            errorMessage: err instanceof Error ? err.message : 'Network error',
            testedAt: Date.now(),
          },
        }));
      }
    },
    [baseUrl, isOAuth, boundAccounts, keys, name, protocol, adminToken]
  );

  const testedModelsCount = useMemo(() => {
    return Object.values(batchResults).filter((r) => r.status === 'success' || r.status === 'failed').length;
  }, [batchResults]);

  const availableModelsCount = useMemo(() => {
    return Object.values(batchResults).filter((r) => r.status === 'success').length;
  }, [batchResults]);

  const failedModelsCount = useMemo(() => {
    return Object.values(batchResults).filter((r) => r.status === 'failed').length;
  }, [batchResults]);

  const inFlightModelsCount = useMemo(() => {
    return Object.values(batchResults).filter((r) => r.status === 'testing').length;
  }, [batchResults]);

  const activeUnmappedModels = useMemo(() => {
    const uName = name.trim();
    return upstreamModels.filter(
      (m) =>
        batchResults[m]?.status === 'success' &&
        !catalogModels.some(
          (cm) => cm.upstream === uName && (cm.upstream_model === m || cm.public_name === m)
        )
    );
  }, [upstreamModels, batchResults, catalogModels, name]);

  const handleCreateAllActiveRoutes = useCallback(() => {
    const upstreamName = name.trim();
    if (!upstreamName) {
      addToast({
        title: 'Upstream Name Required',
        message: 'Please provide an Upstream Name in the General tab first.',
        type: 'warning',
      });
      return;
    }

    if (activeUnmappedModels.length === 0) return;

    ensureUpstreamInStore();

    for (const modelId of activeUnmappedModels) {
      addOrUpdateModel({
        public_name: modelId,
        upstream: upstreamName,
        upstream_model: modelId,
        enabled: true,
        max_context: 128000,
        capabilities: {
          stream: true,
          tools: true,
          json_mode: true,
          vision: false,
          embeddings: modelId.toLowerCase().includes('embed'),
        },
      });
    }

    saveMutation.mutate(buildSettingsPayload());

    addToast({
      title: 'Routes Created',
      message: `${activeUnmappedModels.length} active models added as routes in catalog.`,
      type: 'success',
    });
  }, [name, activeUnmappedModels, ensureUpstreamInStore, addOrUpdateModel, saveMutation, addToast]);

  const probeModelSuggestions = useMemo(() => {
    const set = new Set<string>();
    for (const m of upstreamModels) {
      if (m.trim()) set.add(m.trim());
    }
    for (const m of catalogModels) {
      if (m.upstream === name) {
        if (m.upstream_model?.trim()) set.add(m.upstream_model.trim());
        if (m.public_name?.trim()) set.add(m.public_name.trim());
      }
    }
    return Array.from(set);
  }, [upstreamModels, catalogModels, name]);

  const hasModelList = probeModelSuggestions.length > 0;

  const handleSetProbeModel = useCallback((modelId: string) => {
    setProbeModel(modelId);
    setIsManualProbeInput(false);
  }, []);

  const handleSelectProbeModel = useCallback((e: React.ChangeEvent<HTMLSelectElement>) => {
    const val = e.target.value;
    if (val === '__custom__') {
      setIsManualProbeInput(true);
    } else {
      setProbeModel(val);
    }
  }, []);

  // -------------------------------------------------------------
  // Header and URL helpers
  // -------------------------------------------------------------
  const handleAddBaseUrl = useCallback(() => {
    const trimmed = newBaseUrlInput.trim();
    if (!trimmed) return;
    setBaseUrls((prev) => Array.from(new Set([...prev, trimmed])));
    setNewBaseUrlInput('');
  }, [newBaseUrlInput]);

  const handleRemoveBaseUrl = useCallback((idx: number) => {
    setBaseUrls((prev) => prev.filter((_, i) => i !== idx));
  }, []);

  const handleAddHeader = useCallback(() => {
    setExtraHeaders((prev) => [...prev, { key: '', value: '' }]);
  }, []);

  const handleHeaderChange = useCallback((index: number, field: 'key' | 'value', val: string) => {
    setExtraHeaders((prev) =>
      prev.map((h, i) => (i === index ? { ...h, [field]: val } : h))
    );
  }, []);

  const handleRemoveHeader = useCallback((index: number) => {
    setExtraHeaders((prev) => prev.filter((_, i) => i !== index));
  }, []);

  // -------------------------------------------------------------
  // OAuth Account Pool Handlers
  // -------------------------------------------------------------
  const handleRemoveAccount = useCallback((id: string) => {
    setBoundAccounts((prev) => prev.filter((a) => a.id !== id));
  }, []);

  const handleAccountLimitChange = useCallback((id: string, val: number | null) => {
    setBoundAccounts((prev) =>
      prev.map((a) => (a.id === id ? { ...a, maxConcurrent: val } : a))
    );
  }, []);

  const handleAttachExistingAccount = useCallback(() => {
    if (!selectedExistingConnId) return;
    const conn = oauthConnections.find((c) => c.id === selectedExistingConnId);
    if (!conn) return;
    const ref = `oauth:${conn.id}`;
    if (boundAccounts.some((a) => a.ref === ref || a.connId === conn.id)) {
      addToast({
        title: 'Account Already Attached',
        message: `${conn.email || conn.id} is already in the pool.`,
        type: 'info',
      });
      return;
    }
    setBoundAccounts((prev) => [
      ...prev,
      {
        id: `acc-${Date.now()}-${conn.id}`,
        ref,
        connId: conn.id,
        email: conn.email || conn.id,
        projectId: conn.provider_specific_data?.project_id || conn.provider_specific_data?.region,
        isExpired: conn.is_expired,
      },
    ]);
    setSelectedExistingConnId('');
  }, [selectedExistingConnId, oauthConnections, boundAccounts, addToast]);

  const handleNewAccountConnected = useCallback((conn: ConnectionDTO) => {
    const ref = `oauth:${conn.id}`;
    setBoundAccounts((prev) => {
      if (prev.some((a) => a.ref === ref || a.connId === conn.id)) return prev;
      return [
        ...prev,
        {
          id: `acc-${Date.now()}-${conn.id}`,
          ref,
          connId: conn.id,
          email: conn.email || conn.id,
          projectId: conn.provider_specific_data?.project_id || conn.provider_specific_data?.region,
          isExpired: conn.is_expired,
        },
      ];
    });
    setConnectDialogOpen(false);
    addToast({
      title: 'Account Attached',
      message: `${conn.email || conn.id} added to pool.`,
      type: 'success',
    });
  }, [addToast]);

  const availableExistingConnections = useMemo(() => {
    return oauthConnections.filter((c) => {
      if (c.provider !== protocol) return false;
      const ref = `oauth:${c.id}`;
      return !boundAccounts.some((a) => a.ref === ref || a.connId === c.id);
    });
  }, [oauthConnections, protocol, boundAccounts]);

  const handleAuthTypeChange = (type: 'direct' | 'oauth') => {
    setAuthType(type);
    if (type === 'oauth') {
      setProtocol('antigravity');
      setBaseUrl(getOAuthDefaultUrl('antigravity'));
      setKeyStrategy('least_inflight');
    } else {
      setProtocol('openai');
      setBaseUrl('https://api.openai.com/v1');
      setKeyStrategy('round_robin');
    }
  };

  const handleProtocolChange = (newProto: Protocol) => {
    setProtocol(newProto);
    if (isOAuthProtocol(newProto)) {
      setBaseUrl(getOAuthDefaultUrl(newProto));
    }
  };

  // -------------------------------------------------------------
  // Form Submission
  // -------------------------------------------------------------
  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    const finalBaseUrl = isOAuth
      ? (baseUrl.trim() || getOAuthDefaultUrl(protocol))
      : baseUrl.trim();

    if (!name.trim() || !finalBaseUrl) {
      addToast({
        title: 'Validation Error',
        message: 'Upstream Name and Base URL are required.',
        type: 'error',
      });
      return;
    }

    if (isOAuth && boundAccounts.length === 0) {
      addToast({
        title: 'Validation Error',
        message: 'Please bind at least one OAuth account to this upstream pool.',
        type: 'error',
      });
      return;
    }

    // Build headers map
    const headersMap: Record<string, string> = {};
    for (const h of extraHeaders) {
      if (h.key.trim()) {
        headersMap[h.key.trim()] = h.value.trim();
      }
    }

    const credentialRpsLimit = credentialRpsValue !== null && credentialRpsValue > 0 ? credentialRpsValue : null;
    const credentialMaxConcurrentLimit = credentialMaxConcurrentValue !== null && credentialMaxConcurrentValue > 0
      ? Math.trunc(credentialMaxConcurrentValue)
      : null;
    const timeoutMs = parseDraftNumber(timeoutSec);
    const streamTimeoutMs = parseDraftNumber(streamTimeoutSec);
    const maxConnsValue = parseDraftNumber(maxConns);
    const maxIdleConnsValue = parseDraftNumber(maxIdleConns);
    const parsedProviderId = parseDraftNumber(providerId);
    const parsedKeyErrorThreshold = parseDraftNumber(keyErrorThreshold);
    const parsedKeyCooldownSec = parseDraftNumber(keyCooldownSec);

    let poolDTO: CredentialKeyDTO[] = [];
    let apiKeysList: string[] = [];

    if (isOAuth) {
      poolDTO = boundAccounts.map((acc, idx) => ({
        ref: acc.ref || `oauth:${acc.connId || idx}`,
        rps: acc.rps ?? credentialRpsLimit,
        max_concurrent: acc.maxConcurrent ?? credentialMaxConcurrentLimit,
      }));
    } else {
      poolDTO = keys.map((k, idx) => ({
        ref: k.ref || `${name.trim()}-key-${idx + 1}`,
        secret: k.secret,
        api_key: k.secret,
        rps: k.rps ?? credentialRpsLimit,
        max_concurrent: k.max_concurrent ?? credentialMaxConcurrentLimit,
      }));
      apiKeysList = keys.map((k) => k.secret).filter(Boolean);
    }

    const updated: UpstreamDTO = {
      name: name.trim(),
      protocol,
      base_url: finalBaseUrl,
      base_urls: isOAuth ? [] : baseUrls.filter(Boolean),
      provider_id: parsedProviderId === null ? null : Math.trunc(parsedProviderId),
      key_strategy: keyStrategy,
      api_key: apiKeysList[0] || '',
      api_keys: apiKeysList,
      credential_ref: isOAuth && poolDTO.length > 0 ? poolDTO[0].ref : undefined,
      credential_pool: poolDTO,
      credential_rps: credentialRpsLimit,
      credential_max_concurrent: credentialMaxConcurrentLimit,
      key_error_threshold: parsedKeyErrorThreshold === null || parsedKeyErrorThreshold <= 0 ? 0 : Math.trunc(parsedKeyErrorThreshold),
      key_error_action: keyErrorAction,
      key_cooldown_duration_ms: keyErrorAction === 'cooldown' && parsedKeyCooldownSec !== null && parsedKeyCooldownSec > 0
        ? Math.round(parsedKeyCooldownSec * 1000)
        : 300000,
      timeout_ms: timeoutMs === null ? null : Math.round(timeoutMs * 1000),
      stream_idle_timeout_ms: streamTimeoutMs === null ? null : Math.round(streamTimeoutMs * 1000),
      idle_timeout_ms: idleTimeoutSec * 1000,
      max_conns_per_host: maxConnsValue === null ? null : Math.trunc(maxConnsValue),
      max_idle_conns_per_host: maxIdleConnsValue === null ? null : Math.trunc(maxIdleConnsValue),
      allow_insecure: allowInsecure,
      extra_headers: Object.keys(headersMap).length > 0 ? headersMap : undefined,
      probe_model: probeModel.trim() || undefined,
      enabled: upstreamToEdit?.enabled ?? true,
    };

    addOrUpdateUpstream(updated);

    // Synchronize to Go backend
    saveMutation.mutate(buildSettingsPayload());

    onClose();
  };

  const isSaving = saveMutation.isPending;

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title={
        <div className="flex items-center gap-3">
          <span>{upstreamToEdit ? `Manage Upstream: ${upstreamToEdit.name}` : 'Add New Upstream'}</span>
          <span className="text-[11px] font-mono px-2 py-0.5 rounded bg-white/[0.04] border border-white/[0.08] text-neutral-400 uppercase">
            {protocol}
          </span>
        </div>
      }
      description="Configure endpoints, manage credentials, discover live models, and configure load balancing policies."
      size="2xl"
    >
      <form onSubmit={handleSubmit} className="flex flex-col gap-4 font-mono text-xs max-h-[82vh]">
        {/* Navigation Tabs Bar */}
        <div className="flex items-center gap-1 border-b border-white/[0.06] pb-2 overflow-x-auto no-scrollbar shrink-0">
          <button
            type="button"
            onClick={() => setActiveTab('general')}
            className={cn(
              'px-3 py-1.5 rounded-lg text-xs font-mono transition-colors cursor-pointer',
              activeTab === 'general'
                ? 'bg-white/[0.08] text-white border border-white/[0.1]'
                : 'text-neutral-400 hover:text-white hover:bg-white/[0.02]'
            )}
          >
            General
          </button>

          <button
            type="button"
            onClick={() => setActiveTab('keys')}
            className={cn(
              'px-3 py-1.5 rounded-lg text-xs font-mono transition-colors flex items-center gap-1.5 cursor-pointer',
              activeTab === 'keys'
                ? 'bg-white/[0.08] text-white border border-white/[0.1]'
                : 'text-neutral-400 hover:text-white hover:bg-white/[0.02]'
            )}
          >
            <span>{isOAuth ? 'Accounts' : 'Credentials'}</span>
            <span className="text-[10px] px-1.5 py-0.2 rounded bg-white/[0.06] tabular-nums">
              {isOAuth ? boundAccounts.length : keys.length}
            </span>
          </button>

          <button
            type="button"
            onClick={() => setActiveTab('models')}
            className={cn(
              'px-3 py-1.5 rounded-lg text-xs font-mono transition-colors flex items-center gap-1.5 cursor-pointer',
              activeTab === 'models'
                ? 'bg-white/[0.08] text-white border border-white/[0.1]'
                : 'text-neutral-400 hover:text-white hover:bg-white/[0.02]'
            )}
          >
            <span>Live Models</span>
            {upstreamModels.length > 0 ? (
              <span className="text-[10px] px-1.5 py-0.2 rounded bg-white/[0.06] text-neutral-300 tabular-nums">
                {upstreamModels.length}
              </span>
            ) : null}
          </button>

          <button
            type="button"
            onClick={() => setActiveTab('load_balancing')}
            className={cn(
              'px-3 py-1.5 rounded-lg text-xs font-mono transition-colors cursor-pointer',
              activeTab === 'load_balancing'
                ? 'bg-white/[0.08] text-white border border-white/[0.1]'
                : 'text-neutral-400 hover:text-white hover:bg-white/[0.02]'
            )}
          >
            Load Balancing
          </button>

          <button
            type="button"
            onClick={() => setActiveTab('network')}
            className={cn(
              'px-3 py-1.5 rounded-lg text-xs font-mono transition-colors cursor-pointer',
              activeTab === 'network'
                ? 'bg-white/[0.08] text-white border border-white/[0.1]'
                : 'text-neutral-400 hover:text-white hover:bg-white/[0.02]'
            )}
          >
            Timeouts & Sockets
          </button>
        </div>

        {/* Tab Content Container (Scrollable) */}
        <div className="overflow-y-auto pr-1 space-y-4 min-h-[360px] max-h-[52vh]">
          {/* ============================================================== */}
          {/* TAB 1: GENERAL & ENDPOINTS */}
          {/* ============================================================== */}
          {activeTab === 'general' ? (
            <div className="space-y-4">
              {!upstreamToEdit && (
                <div className="flex flex-col gap-1.5 p-3 rounded-xl border border-white/[0.06] bg-white/[0.015]">
                  <label className="text-neutral-400 font-medium text-[11px]">Upstream Authentication Type</label>
                  <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
                    <button
                      type="button"
                      onClick={() => handleAuthTypeChange('direct')}
                      className={cn(
                        'p-2.5 rounded-lg border text-left flex flex-col gap-0.5 transition-all cursor-pointer font-mono',
                        authType === 'direct'
                          ? 'border-white/[0.2] bg-white/[0.06] text-white'
                          : 'border-white/[0.04] bg-transparent text-neutral-400 hover:text-neutral-200 hover:bg-white/[0.02]'
                      )}
                    >
                      <span className="font-medium text-xs">Standard API Key</span>
                      <span className="text-[10px] text-neutral-500">OpenAI, Anthropic, DeepSeek, vLLM</span>
                    </button>

                    <button
                      type="button"
                      onClick={() => handleAuthTypeChange('oauth')}
                      className={cn(
                        'p-2.5 rounded-lg border text-left flex flex-col gap-0.5 transition-all cursor-pointer font-mono',
                        authType === 'oauth'
                          ? 'border-white/[0.2] bg-white/[0.06] text-white'
                          : 'border-white/[0.04] bg-transparent text-neutral-400 hover:text-neutral-200 hover:bg-white/[0.02]'
                      )}
                    >
                      <span className="font-medium text-xs">OAuth Provider Fleet</span>
                      <span className="text-[10px] text-neutral-500">Google Antigravity, Cline, CodeBuddy</span>
                    </button>
                  </div>
                </div>
              )}

              <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                {/* Name */}
                <div className="flex flex-col gap-1.5">
                  <label className="text-neutral-400 font-medium">Upstream Name</label>
                  <input
                    type="text"
                    required
                    disabled={!!upstreamToEdit}
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    placeholder={isOAuth ? 'e.g. antigravity-prod' : 'e.g. openai-prod'}
                    className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20 disabled:opacity-50"
                  />
                  <span className="text-[10px] text-neutral-500">
                    Unique identifier referenced by model route definitions.
                  </span>
                </div>

                {/* Wire Protocol / Provider */}
                <div className="flex flex-col gap-1.5">
                  <label className="text-neutral-400 font-medium">
                    {isOAuth ? 'OAuth Provider' : 'Wire Protocol'}
                  </label>
                  {isOAuth ? (
                    <select
                      value={protocol}
                      onChange={(e) => handleProtocolChange(e.target.value as Protocol)}
                      className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-neutral-200 font-mono text-xs focus:outline-none focus:border-white/20"
                    >
                      <option value="antigravity" className="bg-[#090b10]">Google Antigravity (Gemini & Claude Sandbox)</option>
                      <option value="cline" className="bg-[#090b10]">Cline (Claude Gateway)</option>
                      <option value="codebuddy_cn" className="bg-[#090b10]">Tencent Cloud CodeBuddy (CN)</option>
                      <option value="codebuddy_intl" className="bg-[#090b10]">CodeBuddy International</option>
                    </select>
                  ) : (
                    <select
                      value={protocol}
                      onChange={(e) => {
                        const p = e.target.value as Protocol;
                        setProtocol(p);
                        if (p === 'grok-cli') {
                          setBaseUrl('https://cli-chat-proxy.grok.com/v1');
                        } else if (baseUrl === 'https://cli-chat-proxy.grok.com/v1') {
                          setBaseUrl('https://api.openai.com/v1');
                        }
                      }}
                      className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-neutral-200 font-mono text-xs focus:outline-none focus:border-white/20"
                    >
                      <option value="openai" className="bg-[#090b10]">OpenAI (Wire Compatible SSE / Chat)</option>
                      <option value="anthropic" className="bg-[#090b10]">Anthropic (/v1/messages Translation)</option>
                      <option value="grok-cli" className="bg-[#090b10]">Grok CLI / Grok Build (xAI OAuth)</option>
                    </select>
                  )}
                  <span className="text-[10px] text-neutral-500">
                    {isOAuth
                      ? 'Selects official upstream authentication and gateway dispatch handler.'
                      : 'Firefly transparently converts request/response schemas if needed.'}
                  </span>
                </div>
              </div>

              {/* Primary Base URL */}
              <div className="flex flex-col gap-1.5">
                <div className="flex items-center justify-between">
                  <label className="text-neutral-400 font-medium">Primary Base URL</label>
                  {isOAuth && (
                    <span className="text-[10px] text-neutral-400 bg-white/[0.04] px-1.5 py-0.5 rounded border border-white/[0.06] font-mono">
                      MANAGED PROVIDER ENDPOINT
                    </span>
                  )}
                </div>
                <input
                  type="url"
                  required
                  disabled={isOAuth}
                  value={baseUrl}
                  onChange={(e) => setBaseUrl(e.target.value)}
                  placeholder={isOAuth ? getOAuthDefaultUrl(protocol) : 'https://api.openai.com/v1'}
                  className={cn(
                    'w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] font-mono text-xs focus:outline-none focus:border-white/20',
                    isOAuth ? 'text-neutral-400 opacity-80 cursor-not-allowed' : 'text-white'
                  )}
                />
                <span className="text-[10px] text-neutral-500">
                  {isOAuth
                    ? 'Preconfigured official endpoint handled automatically by the gateway.'
                    : 'Target API endpoint (e.g. Azure OpenAI, OpenAI, Ollama, Anthropic API).'}
                </span>
              </div>

              {/* Turso Harvester Provider Dropdown */}
              {!isOAuth && (
                <div className="flex flex-col gap-1.5">
                  <div className="flex items-center justify-between">
                    <label className="text-neutral-400 font-medium">Turso Harvester Provider</label>
                    <span
                      className={cn(
                        'text-[10px] font-mono',
                        isTursoConfigured ? 'text-emerald-400' : 'text-neutral-500'
                      )}
                    >
                      {isLoadingProviders
                        ? 'SYNCING...'
                        : isTursoConfigured
                        ? 'CENTRALIZED DB'
                        : 'NOT CONFIGURED'}
                    </span>
                  </div>
                  {isTursoConfigured ? (
                    <select
                      value={providerId || ''}
                      onChange={handleProviderSelect}
                      disabled={isLoadingProviders}
                      className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-neutral-200 font-mono text-xs focus:outline-none focus:border-white/20"
                    >
                      <option value="" className="bg-[#090b10]">
                        {isLoadingProviders
                          ? 'Loading providers from Turso...'
                          : tursoProviders.length === 0
                          ? '-- Standalone Upstream (0 providers in Turso) --'
                          : '-- Not connected (Standalone Upstream) --'}
                      </option>
                      {tursoProviders.map((p) => (
                        <option key={p.id} value={String(p.id)} className="bg-[#090b10]">
                          {p.name} ({p.active_keys} active keys) — {p.base_url || 'no url'}
                        </option>
                      ))}
                    </select>
                  ) : (
                    <select
                      disabled
                      className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-neutral-500 font-mono text-xs cursor-not-allowed opacity-60"
                    >
                      <option className="bg-[#090b10]">
                        Turso database is not configured (set it up in the Settings menu)
                      </option>
                    </select>
                  )}
                  <span className="text-[10px] text-neutral-500">
                    {isTursoConfigured
                      ? 'Select a Turso provider to load all its active API keys automatically, without manual copy-paste.'
                      : 'Configure the Turso database credentials on the Settings page to enable automatic provider loading.'}
                  </span>
                </div>
              )}

              {/* Informational failover hint */}
              {!isOAuth && (
                <div className="pt-2 border-t border-white/[0.04] text-[11px] text-neutral-500">
                  Multi-host failover endpoints ({baseUrls.length}) can be managed under the{' '}
                  <button
                    type="button"
                    onClick={() => setActiveTab('load_balancing')}
                    className="text-neutral-400 hover:text-white underline cursor-pointer"
                  >
                    Load Balancing
                  </button>{' '}
                  tab.
                </div>
              )}
            </div>
          ) : null}

          {/* ============================================================== */}
          {/* TAB 2: ACCOUNTS (OAUTH) OR KEYS (DIRECT API)                 */}
          {/* ============================================================== */}
          {activeTab === 'keys' && isOAuth ? (
            <div className="space-y-4">
              {/* Top Controls Bar */}
              <div className="p-3 rounded-xl border border-white/[0.06] bg-white/[0.015] flex flex-col sm:flex-row sm:items-center justify-between gap-3">
                <div>
                  <div className="flex items-center gap-2">
                    <span className="text-white font-medium text-xs">Account Pool</span>
                    <span className="px-2 py-0.5 rounded text-[10px] bg-white/[0.06] text-neutral-300 font-mono tabular-nums">
                      {boundAccounts.length} {boundAccounts.length === 1 ? 'account' : 'accounts'}
                    </span>
                  </div>
                  <span className="text-[10px] text-neutral-500">
                    Strategy: <strong className="font-medium text-neutral-400">{keyStrategy}</strong> · Automatic 429 quota cooldown & failover
                  </span>
                </div>

                <div className="flex items-center gap-2">
                  <Button
                    type="button"
                    variant="minimal"
                    size="sm"
                    onClick={() => setConnectDialogOpen(true)}
                    leftIcon={<Plus className="w-3.5 h-3.5 text-neutral-400 group-hover:text-white transition-colors" />}
                  >
                    Connect New Account
                  </Button>
                </div>
              </div>

              {/* Pool Rotation Strategy */}
              <div className="p-3 rounded-lg border border-white/[0.04] bg-white/[0.01] flex flex-col gap-2">
                <label className="text-[11px] text-neutral-400 font-medium">Pool Rotation Strategy</label>
                <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
                  <label
                    className={cn(
                      'flex items-start gap-2 p-2 rounded-lg border cursor-pointer transition-colors',
                      keyStrategy === 'least_inflight'
                        ? 'border-white/[0.2] bg-white/[0.04] text-white'
                        : 'border-white/[0.04] text-neutral-400 hover:text-neutral-200'
                    )}
                  >
                    <input
                      type="radio"
                      name="keyStrategy"
                      value="least_inflight"
                      checked={keyStrategy === 'least_inflight'}
                      onChange={() => setKeyStrategy('least_inflight')}
                      className="mt-0.5"
                    />
                    <div className="flex flex-col">
                      <span className="text-xs font-medium">Least In-Flight (Recommended)</span>
                      <span className="text-[10px] text-neutral-500">Routes traffic to the account with lowest in-flight load.</span>
                    </div>
                  </label>

                  <label
                    className={cn(
                      'flex items-start gap-2 p-2 rounded-lg border cursor-pointer transition-colors',
                      keyStrategy === 'round_robin'
                        ? 'border-white/[0.2] bg-white/[0.04] text-white'
                        : 'border-white/[0.04] text-neutral-400 hover:text-neutral-200'
                    )}
                  >
                    <input
                      type="radio"
                      name="keyStrategy"
                      value="round_robin"
                      checked={keyStrategy === 'round_robin'}
                      onChange={() => setKeyStrategy('round_robin')}
                      className="mt-0.5"
                    />
                    <div className="flex flex-col">
                      <span className="text-xs font-medium">Round Robin</span>
                      <span className="text-[10px] text-neutral-500">Cycles evenly through each account in the pool.</span>
                    </div>
                  </label>
                </div>
              </div>

              {/* Bound Accounts List */}
              <div className="flex flex-col gap-2">
                <div className="flex items-center justify-between text-[11px] text-neutral-400 font-medium">
                  <span>Bound Accounts in Pool</span>
                  <span className="text-[10px] text-neutral-500">Active credentials rotated by gateway</span>
                </div>

                {boundAccounts.length > 0 ? (
                  <div className="space-y-1.5 max-h-[220px] overflow-y-auto pr-1">
                    {boundAccounts.map((acc, index) => (
                      <div
                        key={acc.id}
                        className="flex items-center justify-between p-2.5 rounded-lg border border-white/[0.04] bg-white/[0.01] hover:bg-white/[0.03] transition-colors font-mono text-xs gap-3"
                      >
                        <div className="flex items-center gap-2.5 min-w-0 flex-1">
                          <span className="text-[10px] text-neutral-500 tabular-nums w-6 shrink-0">
                            #{index + 1}
                          </span>
                          <UserCheck className="w-3.5 h-3.5 text-neutral-400 shrink-0" />
                          <span className="text-neutral-200 font-medium text-xs truncate max-w-[200px]" title={acc.email || acc.connId}>
                            {acc.email || acc.connId}
                          </span>
                          {acc.projectId && (
                            <span className="text-[10px] text-neutral-500 bg-white/[0.04] px-1.5 py-0.5 rounded border border-white/[0.04] shrink-0" title={`Project: ${acc.projectId}`}>
                              {acc.projectId}
                            </span>
                          )}
                          {acc.isExpired ? (
                            <span className="text-[10px] text-rose-400 flex items-center gap-1 shrink-0">
                              <span className="w-1.5 h-1.5 rounded-full bg-rose-400" /> Expired
                            </span>
                          ) : (
                            <span className="text-[10px] text-emerald-400 flex items-center gap-1 shrink-0">
                              <span className="w-1.5 h-1.5 rounded-full bg-emerald-400" /> Active
                            </span>
                          )}
                        </div>

                        <div className="flex items-center gap-3 shrink-0">
                          <div className="flex items-center gap-1 text-[11px] text-neutral-400">
                            <span className="text-[10px] text-neutral-500">Max Inflight:</span>
                            <input
                              type="number"
                              min="0"
                              placeholder="10"
                              value={acc.maxConcurrent ?? ''}
                              onChange={(e) => handleAccountLimitChange(acc.id, parseDraftNumber(e.target.value))}
                              className="w-12 px-1.5 py-0.5 rounded bg-transparent border border-white/[0.08] text-white text-[11px] text-center font-mono focus:outline-none focus:border-white/20"
                            />
                          </div>

                          <button
                            type="button"
                            onClick={() => handleRemoveAccount(acc.id)}
                            className="text-neutral-500 hover:text-rose-400 p-1 rounded hover:bg-white/[0.04] transition-colors cursor-pointer"
                            title="Remove account from pool"
                          >
                            <Trash2 className="w-3.5 h-3.5" />
                          </button>
                        </div>
                      </div>
                    ))}
                  </div>
                ) : (
                  <div className="p-6 text-center rounded-lg border border-dashed border-white/[0.08] text-neutral-500 font-mono text-xs flex flex-col items-center gap-2">
                    <span>No accounts bound to this upstream yet.</span>
                    <span className="text-[10px]">Select an existing connected account or connect a new one below.</span>
                  </div>
                )}
              </div>

              {/* Attach Existing Account Row */}
              <div className="p-3 rounded-lg border border-white/[0.04] bg-white/[0.01] flex flex-col sm:flex-row sm:items-center justify-between gap-3">
                <div className="flex flex-col gap-1 min-w-0 flex-1">
                  <label className="text-[11px] text-neutral-400 font-medium">Attach Saved Account</label>
                  <div className="flex items-center gap-2">
                    <select
                      value={selectedExistingConnId}
                      onChange={(e) => setSelectedExistingConnId(e.target.value)}
                      className="flex-1 px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-neutral-200 font-mono text-xs focus:outline-none focus:border-white/20"
                    >
                      <option value="" className="bg-[#090b10]">-- Select an authorized account --</option>
                      {availableExistingConnections.map((c) => (
                        <option key={c.id} value={c.id} className="bg-[#090b10]">
                          {c.email || c.id} {c.provider_specific_data?.project_id ? `(${c.provider_specific_data.project_id})` : ''}
                        </option>
                      ))}
                    </select>
                    <Button
                      type="button"
                      variant="minimal"
                      size="sm"
                      disabled={!selectedExistingConnId}
                      onClick={handleAttachExistingAccount}
                    >
                      Attach
                    </Button>
                  </div>
                </div>
              </div>
            </div>
          ) : activeTab === 'keys' && !isOAuth ? (
            <div className="space-y-4">
              {/* Top Controls Bar */}
              <div className="p-3 rounded-xl border border-white/[0.06] bg-white/[0.015] flex flex-col sm:flex-row sm:items-center justify-between gap-3">
                <div>
                  <div className="flex items-center gap-2">
                    <span className="text-white font-medium text-xs">Credential Pool</span>
                    <span className="px-2 py-0.5 rounded text-[10px] bg-white/[0.06] text-neutral-300 font-mono tabular-nums">
                      {keys.length} {keys.length === 1 ? 'key' : 'keys'}
                    </span>
                  </div>
                  <span className="text-[10px] text-neutral-500">
                    Strategy: <strong className="font-medium text-neutral-400">{keyStrategy}</strong> · Dynamic 429 cooldown & 401 revocation
                    {parseDraftNumber(keyErrorThreshold) ? (
                      <> · Policy: <span className="text-amber-400 font-medium">{keyErrorAction} after {keyErrorThreshold} fails</span></>
                    ) : null}
                  </span>
                </div>

                {/* Batch Action Buttons */}
                <div className="flex items-center gap-2 flex-wrap">
                  <Button
                    type="button"
                    variant="minimal"
                    size="sm"
                    onClick={() => handleImportKeysFromDatabase()}
                    disabled={isImportingDbKeys || !isTursoConfigured}
                    title={!isTursoConfigured ? 'Turso database is not configured in Settings' : undefined}
                    leftIcon={
                      isImportingDbKeys ? (
                        <Loader2 className="w-3.5 h-3.5 animate-spin text-neutral-400" />
                      ) : (
                        <Database className="w-3.5 h-3.5 text-neutral-400" />
                      )
                    }
                  >
                    {isImportingDbKeys ? 'Importing...' : 'Import from Database'}
                  </Button>

                  <Button
                    type="button"
                    variant="minimal"
                    size="sm"
                    onClick={() => setBulkInputOpen((prev) => !prev)}
                    leftIcon={<Plus className="w-3.5 h-3.5" />}
                  >
                    {bulkInputOpen ? 'Close Bulk Input' : 'Bulk Import'}
                  </Button>

                  {isCheckingAll ? (
                    <Button
                      type="button"
                      variant="minimal"
                      size="sm"
                      onClick={handleStopCheck}
                      className="text-rose-400 border-rose-500/20 hover:border-rose-500/40"
                      leftIcon={<StopCircle className="w-3.5 h-3.5 text-rose-400" />}
                    >
                      Stop Check
                    </Button>
                  ) : (
                    <Button
                      type="button"
                      variant="minimal"
                      size="sm"
                      onClick={handleCheckAllKeysConcurrently}
                      disabled={keys.length === 0 || !baseUrl.trim()}
                    >
                      Check All Keys
                    </Button>
                  )}
                </div>
              </div>

              {/* Progress Indicator for Concurrent Health Check */}
              {isCheckingAll || checkProgress.completed > 0 ? (
                <div className="p-3 rounded-lg border border-white/[0.08] bg-white/[0.02] space-y-2 animate-in fade-in duration-200">
                  <div className="flex items-center justify-between text-[11px]">
                    <span className="text-neutral-300 font-medium flex items-center gap-1.5">
                      {isCheckingAll ? (
                        <Loader2 className="w-3 h-3 animate-spin text-blue-400" />
                      ) : null}
                      <span>
                        {isCheckingAll ? 'Testing Keys Concurrently...' : 'Health Check Completed'}
                      </span>
                    </span>
                    <span className="text-neutral-400 tabular-nums text-[10px]">
                      {checkProgress.completed} / {checkProgress.total} checked (
                      {checkProgress.total > 0
                        ? Math.round((checkProgress.completed / checkProgress.total) * 100)
                        : 0}
                      %)
                    </span>
                  </div>

                  {/* Progress bar */}
                  <div className="w-full h-1.5 rounded-full bg-white/[0.06] overflow-hidden">
                    <div
                      className="h-full bg-emerald-500 transition-all duration-300"
                      style={{
                        width: `${
                          checkProgress.total > 0
                            ? (checkProgress.completed / checkProgress.total) * 100
                            : 0
                        }%`,
                      }}
                    />
                  </div>

                  {/* Summary Counters */}
                  <div className="flex items-center gap-3 text-[10px] tabular-nums pt-1">
                    <span className="text-emerald-400 flex items-center gap-1">
                      <span className="w-1.5 h-1.5 rounded-full bg-emerald-400" />
                      {checkProgress.valid} Valid
                    </span>
                    <span className="text-rose-400 flex items-center gap-1">
                      <span className="w-1.5 h-1.5 rounded-full bg-rose-400" />
                      {checkProgress.invalid} Invalid (401)
                    </span>
                    <span className="text-amber-400 flex items-center gap-1">
                      <span className="w-1.5 h-1.5 rounded-full bg-amber-400" />
                      {checkProgress.rateLimited} Rate-Limited (429)
                    </span>

                    {checkProgress.invalid > 0 ? (
                      <button
                        type="button"
                        onClick={handleRemoveInvalidKeys}
                        className="ml-auto text-rose-400 hover:text-rose-300 underline cursor-pointer"
                      >
                        Remove Invalid Keys
                      </button>
                    ) : null}
                  </div>
                </div>
              ) : null}

              {/* Bulk Textarea Input (Collapsible) */}
              {bulkInputOpen ? (
                <div className="p-3 rounded-lg border border-white/[0.08] bg-black/40 space-y-3 animate-in fade-in zoom-in-95 duration-150">
                  <div className="flex items-center justify-between">
                    <span className="text-neutral-300 font-medium text-[11px]">
                      Paste Multiple API Keys (e.g. 100 keys)
                    </span>
                    <span className="text-[10px] text-neutral-500">
                      Delimited by newlines, commas, or semicolons
                    </span>
                  </div>

                  <textarea
                    rows={6}
                    value={bulkText}
                    onChange={(e) => setBulkText(e.target.value)}
                    placeholder="sk-key-1&#10;sk-key-2&#10;sk-key-3..."
                    className="w-full px-3 py-2 rounded-lg bg-black/60 border border-white/[0.1] text-neutral-200 font-mono text-xs focus:outline-none focus:border-white/30 resize-y"
                  />

                  <div className="flex items-center justify-between flex-wrap gap-2 pt-1">
                    <span className="text-[11px] text-neutral-400 tabular-nums">
                      {uniqueBulkKeys.length} unique keys detected
                      {parsedBulkKeys.length > uniqueBulkKeys.length ? (
                        <span className="text-neutral-500 ml-1">
                          ({parsedBulkKeys.length - uniqueBulkKeys.length} duplicates filtered)
                        </span>
                      ) : null}
                    </span>

                    <div className="flex items-center gap-2">
                      <Button
                        type="button"
                        variant="minimal"
                        size="sm"
                        onClick={() => handleApplyBulkKeys('append')}
                        disabled={uniqueBulkKeys.length === 0}
                      >
                        Append ({uniqueBulkKeys.length})
                      </Button>
                      <Button
                        type="button"
                        variant="minimal"
                        size="sm"
                        onClick={() => handleApplyBulkKeys('replace')}
                        disabled={uniqueBulkKeys.length === 0}
                        className="text-amber-400 border-amber-500/20 hover:border-amber-500/40"
                      >
                        Replace All Keys
                      </Button>
                    </div>
                  </div>
                </div>
              ) : null}

              {/* Single Key Input Row */}
              <div className="flex items-center gap-2">
                <input
                  type="text"
                  value={singleKeyInput}
                  onChange={(e) => setSingleKeyInput(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') {
                      e.preventDefault();
                      handleAddSingleKey();
                    }
                  }}
                  placeholder="Paste a single API key to add (sk-...)"
                  className="flex-1 px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
                />
                <Button
                  type="button"
                  variant="minimal"
                  size="sm"
                  onClick={handleAddSingleKey}
                  disabled={!singleKeyInput.trim()}
                  leftIcon={<Plus className="w-3.5 h-3.5" />}
                >
                  Add Key
                </Button>
              </div>

              {/* Filter & Search Bar */}
              <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-2 pt-1 border-t border-white/[0.04]">
                {/* Filter Pills */}
                <div className="flex items-center gap-1.5 overflow-x-auto no-scrollbar">
                  <button
                    type="button"
                    onClick={() => setKeyFilter('all')}
                    className={cn(
                      'px-2 py-0.5 rounded text-[10px] font-mono transition-colors cursor-pointer',
                      keyFilter === 'all'
                        ? 'bg-white/[0.08] text-white border border-white/[0.1]'
                        : 'text-neutral-500 hover:text-neutral-300'
                    )}
                  >
                    All ({keys.length})
                  </button>
                  <button
                    type="button"
                    onClick={() => setKeyFilter('valid')}
                    className={cn(
                      'px-2 py-0.5 rounded text-[10px] font-mono transition-colors cursor-pointer',
                      keyFilter === 'valid'
                        ? 'bg-emerald-500/10 text-emerald-400 border border-emerald-500/20'
                        : 'text-neutral-500 hover:text-emerald-400'
                    )}
                  >
                    Valid ({keys.filter((k) => k.status === 'valid').length})
                  </button>
                  <button
                    type="button"
                    onClick={() => setKeyFilter('invalid')}
                    className={cn(
                      'px-2 py-0.5 rounded text-[10px] font-mono transition-colors cursor-pointer',
                      keyFilter === 'invalid'
                        ? 'bg-rose-500/10 text-rose-400 border border-rose-500/20'
                        : 'text-neutral-500 hover:text-rose-400'
                    )}
                  >
                    Invalid ({keys.filter((k) => k.status === 'invalid').length})
                  </button>
                  <button
                    type="button"
                    onClick={() => setKeyFilter('rate_limited')}
                    className={cn(
                      'px-2 py-0.5 rounded text-[10px] font-mono transition-colors cursor-pointer',
                      keyFilter === 'rate_limited'
                        ? 'bg-amber-500/10 text-amber-400 border border-amber-500/20'
                        : 'text-neutral-500 hover:text-amber-400'
                    )}
                  >
                    429 ({keys.filter((k) => k.status === 'rate_limited').length})
                  </button>
                  <button
                    type="button"
                    onClick={() => setKeyFilter('unchecked')}
                    className={cn(
                      'px-2 py-0.5 rounded text-[10px] font-mono transition-colors cursor-pointer',
                      keyFilter === 'unchecked'
                        ? 'bg-white/[0.08] text-white border border-white/[0.1]'
                        : 'text-neutral-500 hover:text-neutral-300'
                    )}
                  >
                    Ready ({keys.filter((k) => k.status === 'idle').length})
                  </button>
                </div>

                {/* Quick Search */}
                <div className="relative w-full sm:w-44">
                  <Search className="w-3 h-3 text-neutral-500 absolute left-2.5 top-1/2 -translate-y-1/2" />
                  <input
                    type="text"
                    value={keySearch}
                    onChange={(e) => setKeySearch(e.target.value)}
                    placeholder="Search keys..."
                    className="w-full pl-7 pr-2.5 py-1 rounded bg-transparent border border-white/[0.06] text-neutral-200 font-mono text-[11px] focus:outline-none focus:border-white/20"
                  />
                </div>
              </div>

              {/* Scrollable Key Slot List (Efficient Virtualized Rendering) */}
              <div className="space-y-1.5 max-h-[300px] overflow-y-auto pr-1">
                {filteredKeys.length > 0 ? (
                  filteredKeys.map((k, idx) => (
                    <KeyRowItem
                      key={k.id}
                      item={k}
                      index={idx}
                      onTest={handleTestSingleKey}
                      onRemove={handleRemoveKey}
                    />
                  ))
                ) : (
                  <div className="p-8 text-center text-neutral-500 font-mono text-xs border border-white/[0.04] rounded-lg">
                    {keys.length === 0
                      ? 'No API keys configured yet. Paste bulk keys or add a single key above.'
                      : 'No keys match the selected filter or search.'}
                  </div>
                )}
              </div>
            </div>
          ) : null}

          {/* ============================================================== */}
          {/* TAB 3: UPSTREAM MODELS VIEWER */}
          {/* ============================================================== */}
          {activeTab === 'models' ? (
            <div className="space-y-4">
              <div className="p-3 rounded-xl border border-white/[0.06] bg-white/[0.015] flex flex-col gap-2.5">
                <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
                  <div>
                    <div className="flex items-center gap-2">
                      <span className="text-white font-medium text-xs">Live Upstream Models</span>
                      <span className="px-2 py-0.5 rounded text-[10px] bg-white/[0.06] text-neutral-300 font-mono tabular-nums">
                        {upstreamModels.length} models
                      </span>
                    </div>
                    <span className="text-[10px] text-neutral-500">
                      Discovered directly from {baseUrl || 'upstream host'} via /v1/models
                    </span>
                  </div>

                  <Button
                    type="button"
                    variant="minimal"
                    size="sm"
                    onClick={handleFetchUpstreamModels}
                    isLoading={isLoadingModels}
                    disabled={!baseUrl.trim()}
                    leftIcon={<RotateCw className="w-3.5 h-3.5 text-neutral-400" />}
                  >
                    {isLoadingModels ? 'Fetching...' : 'Fetch Models'}
                  </Button>
                </div>

                {/* Probe Model (Health & Quota Check) Selector */}
                <div className="pt-3 border-t border-white/[0.06] flex flex-col gap-2">
                  <div className="flex items-center justify-between">
                    <div className="flex items-center gap-2">
                      <label className="text-neutral-300 font-medium text-xs">
                        Probe Model (Health & Quota Check)
                      </label>
                      {probeModel ? (
                        <span className="text-[10px] px-1.5 py-0.5 rounded bg-amber-500/10 border border-amber-500/20 text-amber-300 font-mono">
                          ACTIVE: {probeModel}
                        </span>
                      ) : (
                        <span className="text-[10px] px-1.5 py-0.5 rounded bg-white/[0.04] text-neutral-500 font-mono">
                          NOT CONFIGURED
                        </span>
                      )}
                    </div>

                    <div className="flex items-center gap-2.5">
                      {hasModelList && (
                        <button
                          type="button"
                          onClick={() => setIsManualProbeInput((prev) => !prev)}
                          className="text-[10px] text-neutral-400 hover:text-white underline cursor-pointer"
                        >
                          {isManualProbeInput ? 'Select from dropdown' : 'Manual input'}
                        </button>
                      )}
                      {probeModel && (
                        <button
                          type="button"
                          onClick={() => setProbeModel('')}
                          className="text-[10px] text-neutral-500 hover:text-neutral-300 underline cursor-pointer"
                        >
                          Clear
                        </button>
                      )}
                    </div>
                  </div>

                  {hasModelList && !isManualProbeInput ? (
                    <select
                      value={probeModel}
                      onChange={handleSelectProbeModel}
                      className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-neutral-200 font-mono text-xs focus:outline-none focus:border-white/20"
                    >
                      <option value="" className="bg-[#090b10]">
                        -- None (falls back to reachability / catalog) --
                      </option>
                      {probeModel && !probeModelSuggestions.includes(probeModel) && (
                        <option value={probeModel} className="bg-[#090b10]">
                          {probeModel} (Custom)
                        </option>
                      )}
                      {probeModelSuggestions.map((m) => (
                        <option key={m} value={m} className="bg-[#090b10]">
                          {m}
                        </option>
                      ))}
                      <option value="__custom__" className="bg-[#090b10]">
                        ✏️ Type custom model ID manually...
                      </option>
                    </select>
                  ) : (
                    <input
                      type="text"
                      list="upstream-probe-model-suggestions"
                      value={probeModel}
                      onChange={(e) => setProbeModel(e.target.value)}
                      placeholder="e.g. gpt-4o-mini, claude-3-haiku-20240307, deepseek-chat..."
                      className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
                    />
                  )}

                  <datalist id="upstream-probe-model-suggestions">
                    {probeModelSuggestions.map((m) => (
                      <option key={m} value={m} />
                    ))}
                  </datalist>

                  <span className="text-[10px] text-neutral-500">
                    Designated lightweight model used for active 1-token health checks and key validation. Prevents false positives from $0-balance or expired trial keys.
                    {!hasModelList && ' Fetch models above to choose from a dropdown, or type any model ID manually.'}
                  </span>
                </div>
              </div>

              {/* Batch Verification Toolbar */}
              {upstreamModels.length > 0 && (
                <div className="p-3 rounded-xl border border-white/[0.06] bg-white/[0.015] space-y-2.5">
                  <div className="flex flex-wrap items-center justify-between gap-2.5">
                    <div className="flex items-center gap-2">
                      {isBatchTesting ? (
                        <Button
                          type="button"
                          variant="danger"
                          size="sm"
                          onClick={handleStopBatchTest}
                          leftIcon={<StopCircle className="w-3.5 h-3.5" />}
                          className="text-xs"
                        >
                          Stop Testing
                        </Button>
                      ) : (
                        <Button
                          type="button"
                          variant="secondary"
                          size="sm"
                          onClick={handleStartBatchTest}
                          leftIcon={<Play className="w-3.5 h-3.5 text-cyan-400" />}
                          className="text-xs border-cyan-500/30 hover:border-cyan-500/50 text-cyan-200"
                        >
                          Test All Models
                        </Button>
                      )}

                      <div className="flex items-center gap-1.5">
                        <span className="text-[11px] text-neutral-400">Safe Rate:</span>
                        <select
                          value={safeRps}
                          disabled={isBatchTesting}
                          onChange={(e) => setSafeRps(Number(e.target.value))}
                          className="px-2 py-1 rounded bg-black/40 border border-white/[0.08] text-neutral-200 font-mono text-xs focus:outline-none focus:border-white/20 disabled:opacity-50"
                        >
                          <option value={1}>1 req/s (Gentle)</option>
                          <option value={2}>2 req/s (Standard)</option>
                          <option value={5}>5 req/s (Fast)</option>
                          <option value={10}>10 req/s (High Throughput)</option>
                        </select>
                      </div>
                    </div>

                    <div className="flex items-center gap-2">
                      {availableModelsCount > 0 ? (
                        <Button
                          type="button"
                          variant="minimal"
                          size="sm"
                          onClick={handleCreateAllActiveRoutes}
                          disabled={activeUnmappedModels.length === 0}
                          className="text-[11px] px-2.5 py-1 text-emerald-300 border-emerald-500/30 bg-emerald-500/10 hover:bg-emerald-500/20 disabled:opacity-40"
                          leftIcon={<Plus className="w-3 h-3 text-emerald-400" />}
                          title="Add all verified active models to catalog routes"
                        >
                          Add All Active as Routes ({activeUnmappedModels.length})
                        </Button>
                      ) : unmappedCount > 0 ? (
                        <Button
                          type="button"
                          variant="minimal"
                          size="sm"
                          onClick={handleCreateAllUnmappedRoutes}
                          className="text-[11px] px-2.5 py-1 text-cyan-300 border-cyan-500/30 bg-cyan-500/10 hover:bg-cyan-500/20"
                          leftIcon={<Plus className="w-3 h-3 text-cyan-400" />}
                          title="Add all unmapped models as routes in catalog"
                        >
                          Add All as Routes ({unmappedCount})
                        </Button>
                      ) : null}
                    </div>
                  </div>

                  {/* Real-time Progress and Stats */}
                  {(isBatchTesting || testedModelsCount > 0) && (
                    <div className="space-y-1.5 pt-1.5 border-t border-white/[0.04]">
                      <div className="flex items-center justify-between text-[11px] font-mono text-neutral-400">
                        <div className="flex items-center gap-2">
                          <span>
                            {isBatchTesting
                              ? `Testing: ${testedModelsCount}/${upstreamModels.length} (${Math.round((testedModelsCount / upstreamModels.length) * 100)}%)`
                              : `Completed: ${testedModelsCount}/${upstreamModels.length} tested`}
                          </span>
                          {inFlightModelsCount > 0 && (
                            <span className="px-1.5 py-0.5 rounded bg-cyan-500/20 text-cyan-300 border border-cyan-500/30 animate-pulse text-[10px]">
                              {inFlightModelsCount} in-flight
                            </span>
                          )}
                        </div>
                        <div className="flex items-center gap-2">
                          {availableModelsCount > 0 && (
                            <span className="px-1.5 py-0.5 rounded bg-emerald-500/15 text-emerald-400 border border-emerald-500/25 text-[10px]">
                              {availableModelsCount} Active
                            </span>
                          )}
                          {failedModelsCount > 0 && (
                            <span className="px-1.5 py-0.5 rounded bg-rose-500/15 text-rose-400 border border-rose-500/25 text-[10px]">
                              {failedModelsCount} Failed
                            </span>
                          )}
                        </div>
                      </div>

                      {/* Progress Bar */}
                      <div className="w-full h-1.5 rounded-full bg-white/[0.06] overflow-hidden">
                        <div
                          className={cn(
                            "h-full transition-all duration-300 rounded-full",
                            isBatchTesting ? "bg-gradient-to-r from-cyan-500 to-emerald-400" : "bg-emerald-500"
                          )}
                          style={{
                            width: `${Math.min(100, Math.round((testedModelsCount / upstreamModels.length) * 100))}%`,
                          }}
                        />
                      </div>
                    </div>
                  )}
                </div>
              )}

              {/* Model Search & Stats */}
              {upstreamModels.length > 0 ? (
                <div className="flex items-center justify-between gap-2">
                  <div className="relative flex-1">
                    <Search className="w-3 h-3 text-neutral-500 absolute left-2.5 top-1/2 -translate-y-1/2" />
                    <input
                      type="text"
                      value={modelSearch}
                      onChange={(e) => setModelSearch(e.target.value)}
                      placeholder="Filter models (e.g. gpt-4, claude, deepseek)..."
                      className="w-full pl-7 pr-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
                    />
                  </div>
                  <span className="text-[11px] text-neutral-500 tabular-nums shrink-0 font-mono">
                    {filteredModels.length} of {upstreamModels.length} models
                  </span>
                </div>
              ) : null}

              {/* Models List */}
              <div className="space-y-1.5 max-h-[300px] overflow-y-auto pr-1">
                {isLoadingModels ? (
                  <div className="p-8 text-center flex flex-col items-center justify-center gap-2 font-mono text-xs text-neutral-400">
                    <Loader2 className="w-4 h-4 animate-spin text-neutral-300" />
                    <span>Querying models from upstream host...</span>
                  </div>
                ) : filteredModels.length > 0 ? (
                  filteredModels.map((modelId) => (
                    <ModelRowItem
                      key={modelId}
                      modelId={modelId}
                      isInCatalog={isModelInCatalog(modelId)}
                      isProbeModel={modelId === probeModel}
                      batchResult={batchResults[modelId]}
                      onSetProbeModel={handleSetProbeModel}
                      onCreateRoute={handleCreateRoute}
                      onTestModel={handleTestSingleModel}
                    />
                  ))
                ) : (
                  <div className="p-8 text-center text-neutral-500 font-mono text-xs border border-white/[0.04] rounded-lg">
                    {upstreamModels.length === 0
                      ? 'No models loaded yet. Click "Fetch Models" above to query this upstream host.'
                      : 'No models match the search filter.'}
                  </div>
                )}
              </div>
            </div>
          ) : null}

          {/* ============================================================== */}
          {/* TAB 4: LOAD BALANCING & RESILIENCE */}
          {/* ============================================================== */}
          {activeTab === 'load_balancing' ? (
            <div className="space-y-6">
              {/* 1. Multi-Host Endpoint Failover */}
              <div className="space-y-3">
                <div>
                  <h4 className="text-white font-medium text-xs tracking-tight">
                    Host-Level Routing & Multi-URL Failover
                  </h4>
                  <p className="text-[10px] text-neutral-500">
                    Cycles requests across candidate base URLs upon host network errors, TCP resets, or HTTP 5xx responses.
                  </p>
                </div>

                {/* Primary Host URL */}
                <div className="flex items-center justify-between p-2.5 rounded-lg border border-white/[0.06] bg-white/[0.015]">
                  <div className="flex items-center gap-2 truncate">
                    <span className="text-[10px] px-1.5 py-0.5 rounded bg-emerald-500/10 text-emerald-400 font-mono">
                      PRIMARY
                    </span>
                    <span className="text-neutral-200 font-mono text-xs truncate">
                      {baseUrl || 'No base URL configured'}
                    </span>
                  </div>
                  <span className="text-[10px] text-neutral-500 shrink-0">Configured in General</span>
                </div>

                {/* Backup / Failover URLs */}
                <div className="space-y-2">
                  <div className="flex items-center justify-between">
                    <span className="text-neutral-400 text-[11px] font-medium">
                      Failover & Mirror Endpoints ({baseUrls.length})
                    </span>
                    <span className="text-[10px] text-neutral-500">Deterministic fallback on circuit breaker</span>
                  </div>

                  {baseUrls.map((url, idx) => (
                    <div key={idx} className="flex items-center gap-2">
                      <span className="text-[10px] text-neutral-500 font-mono w-6 text-right shrink-0">
                        #{idx + 1}
                      </span>
                      <input
                        type="text"
                        readOnly
                        value={url}
                        className="flex-1 px-3 py-1.5 rounded-lg bg-white/[0.02] border border-white/[0.06] text-neutral-300 font-mono text-xs"
                      />
                      <button
                        type="button"
                        onClick={() => handleRemoveBaseUrl(idx)}
                        className="p-1 rounded text-neutral-500 hover:text-rose-400 transition-colors cursor-pointer"
                        title="Remove Endpoint"
                      >
                        <Trash2 className="w-3.5 h-3.5" />
                      </button>
                    </div>
                  ))}

                  <div className="flex items-center gap-2">
                    <input
                      type="url"
                      value={newBaseUrlInput}
                      onChange={(e) => setNewBaseUrlInput(e.target.value)}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter') {
                          e.preventDefault();
                          handleAddBaseUrl();
                        }
                      }}
                      placeholder="https://backup-endpoint.provider.com/v1"
                      className="flex-1 px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
                    />
                    <Button
                      type="button"
                      variant="minimal"
                      size="sm"
                      onClick={handleAddBaseUrl}
                      disabled={!newBaseUrlInput.trim()}
                      leftIcon={<Plus className="w-3.5 h-3.5" />}
                    >
                      Add URL
                    </Button>
                  </div>
                </div>
              </div>

              {/* 2. KeyRing Load Balancing Strategy */}
              <div className="space-y-3 pt-4 border-t border-white/[0.06]">
                <div>
                  <h4 className="text-white font-medium text-xs tracking-tight">
                    Credential KeyRing Load Balancing
                  </h4>
                  <p className="text-[10px] text-neutral-500">
                    Governs how traffic is distributed across the {keys.length} API keys in this upstream's credential pool.
                  </p>
                </div>

                <div className="flex flex-col gap-1.5">
                  <label className="text-neutral-400 font-medium">Rotation Strategy</label>
                  <select
                    value={keyStrategy}
                    onChange={(e) => setKeyStrategy(e.target.value as 'round_robin' | 'least_inflight')}
                    className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-neutral-200 font-mono text-xs focus:outline-none focus:border-white/20"
                  >
                    <option value="least_inflight" className="bg-[#090b10]">
                      Least-Inflight (Routes to key with minimum active requests — recommended)
                    </option>
                    <option value="round_robin" className="bg-[#090b10]">
                      Round-Robin (Sequential fair rotation across all healthy keys)
                    </option>
                  </select>
                  <span className="text-[10px] text-neutral-500">
                    {keyStrategy === 'least_inflight'
                      ? 'Optimal for streaming SSE: prevents queuing on accounts that are processing slow, long-running generations.'
                      : 'Standard sequential rotation across all non-cooldown keys in the keyring.'}
                  </span>
                </div>

                {/* Global Credential Default Limits */}
                <div className="grid grid-cols-1 md:grid-cols-2 gap-4 pt-2">
                  <div className="flex flex-col gap-1.5">
                    <label className="text-neutral-400 font-medium">Default Max Concurrent Inflight</label>
                    <input
                      type="number"
                      min={0}
                      value={credentialMaxConcurrent}
                      onChange={(e) => setCredentialMaxConcurrent(e.target.value)}
                      placeholder="0 = unlimited"
                      className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
                    />
                    <span className="text-[10px] text-neutral-500">
                      Atomic CAS ticket ceiling per key slot (0 = unlimited). Saturated keys are skipped in favor of idle keys.
                    </span>
                  </div>

                  <div className="flex flex-col gap-1.5">
                    <label className="text-neutral-400 font-medium">Default RPS Rate Limit</label>
                    <input
                      type="number"
                      min={0}
                      step={0.5}
                      value={credentialRps}
                      onChange={(e) => setCredentialRps(e.target.value)}
                      placeholder="0 = unlimited"
                      className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
                    />
                    <span className="text-[10px] text-neutral-500">
                      Token-bucket requests-per-second ceiling per key slot (0 = unlimited) to protect against provider rate limits.
                    </span>
                  </div>
                </div>
              </div>

              {/* 3. Per-Key Slot Limits & Allocation (Overrides) */}
              {keys.length > 0 ? (
                <div className="space-y-3 pt-4 border-t border-white/[0.06]">
                  <div className="flex items-center justify-between">
                    <div>
                      <h4 className="text-white font-medium text-xs tracking-tight">
                        Key-Level Quota & Concurrency Overrides
                      </h4>
                      <p className="text-[10px] text-neutral-500">
                        Customize concurrency and RPS limits for individual keys (useful when mixing different account tiers).
                      </p>
                    </div>
                    <span className="text-[10px] text-neutral-500">{keys.length} keys in pool</span>
                  </div>

                  <div className="space-y-1.5 max-h-[220px] overflow-y-auto pr-1">
                    {keys.map((k, idx) => (
                      <div
                        key={k.id}
                        className="flex flex-col sm:flex-row sm:items-center justify-between p-2 rounded-lg border border-white/[0.04] bg-white/[0.01] hover:bg-white/[0.02] transition-colors gap-2 text-xs font-mono"
                      >
                        <div className="flex items-center gap-2 truncate min-w-0 flex-1">
                          <span className="text-[10px] text-neutral-500 tabular-nums w-6">#{idx + 1}</span>
                          <span className="text-neutral-300 font-medium text-[11px] truncate">{k.ref}</span>
                          <span className="text-neutral-500 text-[10px] truncate">{maskKeyForDisplay(k.secret)}</span>
                        </div>

                        <div className="flex items-center gap-3 shrink-0">
                          <div className="flex items-center gap-1.5">
                            <span className="text-[10px] text-neutral-500">Max Inflight:</span>
                            <input
                              type="number"
                              min={0}
                              value={k.max_concurrent ?? ''}
                              placeholder={credentialMaxConcurrentValue !== null && credentialMaxConcurrentValue > 0 ? `${credentialMaxConcurrentValue} (def)` : 'unlimited'}
                              onChange={(e) => {
                                const val = e.target.value === '' ? null : parseInt(e.target.value, 10);
                                handleUpdateKeyLimit(k.id, 'max_concurrent', val);
                              }}
                              className="w-20 px-2 py-0.5 rounded bg-transparent border border-white/[0.08] text-neutral-200 text-[11px] text-right focus:outline-none focus:border-white/20"
                            />
                          </div>

                          <div className="flex items-center gap-1.5">
                            <span className="text-[10px] text-neutral-500">RPS:</span>
                            <input
                              type="number"
                              min={0}
                              step={0.5}
                              value={k.rps ?? ''}
                              placeholder={credentialRpsValue !== null && credentialRpsValue > 0 ? `${credentialRpsValue} (def)` : 'unlimited'}
                              onChange={(e) => {
                                const val = e.target.value === '' ? null : parseFloat(e.target.value);
                                handleUpdateKeyLimit(k.id, 'rps', val);
                              }}
                              className="w-20 px-2 py-0.5 rounded bg-transparent border border-white/[0.08] text-neutral-200 text-[11px] text-right focus:outline-none focus:border-white/20"
                            />
                          </div>
                        </div>
                      </div>
                    ))}
                  </div>
                </div>
              ) : null}

              {/* 4. Key Error Resilience Policy */}
              <div className="space-y-3 pt-4 border-t border-white/[0.06]">
                <div className="flex items-center justify-between">
                  <div>
                    <h4 className="text-white font-medium text-xs tracking-tight flex items-center gap-2">
                      <span>Consecutive Key Error Resilience Policy</span>
                      {parseDraftNumber(keyErrorThreshold) ? (
                        <span className="text-[10px] px-1.5 py-0.5 rounded bg-amber-500/10 text-amber-400 border border-amber-500/20">
                          Active ({keyErrorAction} after {keyErrorThreshold} fails)
                        </span>
                      ) : (
                        <span className="text-[10px] px-1.5 py-0.5 rounded bg-white/[0.04] text-neutral-500 border border-white/[0.06]">
                          Disabled
                        </span>
                      )}
                    </h4>
                    <p className="text-[10px] text-neutral-500">
                      Protects the upstream pool by automatically taking action when an individual key encounters N consecutive credential or quota errors (429, 401, 402, 403). Healthy 200 OK responses automatically reset the counter.
                    </p>
                  </div>
                </div>

                <div className="grid grid-cols-1 md:grid-cols-2 gap-4 pt-1">
                  {/* Error Threshold */}
                  <div className="flex flex-col gap-1.5">
                    <label className="text-neutral-400 font-medium">Consecutive Error Threshold</label>
                    <input
                      type="number"
                      min={0}
                      value={keyErrorThreshold}
                      onChange={(e) => setKeyErrorThreshold(e.target.value)}
                      placeholder="0 = disabled (e.g. 3 or 5)"
                      className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
                    />
                    <span className="text-[10px] text-neutral-500">
                      Consecutive failures before action triggers (0 = disabled). Recommended: 3 to 5.
                    </span>
                  </div>

                  {/* Action on Threshold Reached */}
                  <div className="flex flex-col gap-1.5">
                    <label className="text-neutral-400 font-medium">Action on Threshold Reached</label>
                    <select
                      value={keyErrorAction}
                      onChange={(e) => setKeyErrorAction(e.target.value as 'deactivate' | 'delete' | 'cooldown')}
                      className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-neutral-200 font-mono text-xs focus:outline-none focus:border-white/20"
                    >
                      <option value="deactivate" className="bg-[#090b10]">
                        Deactivate Key (safe for subscription accounts)
                      </option>
                      <option value="delete" className="bg-[#090b10]">
                        Delete Permanently (best for trial/disposable accounts)
                      </option>
                      <option value="cooldown" className="bg-[#090b10]">
                        Temporary Cooldown
                      </option>
                    </select>
                    <span className="text-[10px] text-neutral-500">
                      {keyErrorAction === 'deactivate' &&
                        'Key is deactivated in memory and marked inactive in database (is_active = 0). Preserves credentials for subscription renewals.'}
                      {keyErrorAction === 'delete' &&
                        'Key is permanently purged from memory and deleted from database (DELETE FROM api_keys). Ideal for free trial accounts.'}
                      {keyErrorAction === 'cooldown' &&
                        'Key is placed on temporary cooldown for the set duration without modifying database state.'}
                    </span>
                  </div>
                </div>

                {/* Cooldown duration input if action is cooldown */}
                {keyErrorAction === 'cooldown' && (
                  <div className="flex flex-col gap-1.5 pt-1">
                    <label className="text-neutral-400 font-medium">Cooldown Duration (Seconds)</label>
                    <input
                      type="number"
                      min={10}
                      step={10}
                      value={keyCooldownSec}
                      onChange={(e) => setKeyCooldownSec(e.target.value)}
                      placeholder="300"
                      className="w-full md:w-1/2 px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
                    />
                    <span className="text-[10px] text-neutral-500">
                      Seconds to pause traffic to the key before resetting the counter (default 300s = 5 minutes).
                    </span>
                  </div>
                )}

                {isTursoConfigured ? (
                  <div className="p-2.5 rounded-lg bg-emerald-500/5 border border-emerald-500/20 text-[10px] text-emerald-400/90 flex items-center gap-2">
                    <Database className="w-3.5 h-3.5 text-emerald-400 shrink-0" />
                    <span>
                      <strong>Turso Centralized DB Connected:</strong> Key state changes will automatically propagate to local SQLite replica and push to cloud asynchronously.
                    </span>
                  </div>
                ) : (
                  <div className="p-2.5 rounded-lg bg-white/[0.02] border border-white/[0.06] text-[10px] text-neutral-400 flex items-center gap-2">
                    <span>
                      Turso DB is not configured; key actions will apply locally to active memory and runtime configuration.
                    </span>
                  </div>
                )}
              </div>

              {/* 5. 2-Layer Resilience Architecture */}
              <div className="p-3.5 rounded-lg border border-white/[0.06] bg-white/[0.015] text-xs font-mono space-y-2 pt-4 border-t border-white/[0.06]">
                <div className="text-neutral-300 font-medium text-[11px] tracking-tight">
                  2-Layer Resilience & Fault Isolation Architecture
                </div>
                <div className="grid grid-cols-1 md:grid-cols-2 gap-3 text-[10px] text-neutral-400 leading-relaxed">
                  <div className="p-2.5 rounded bg-white/[0.01] border border-white/[0.03] space-y-1">
                    <div className="text-neutral-200 font-medium">Layer 1: KeyRing Fault Handling (429 / 401)</div>
                    <p className="text-neutral-500">
                      HTTP 429 parses <code className="text-neutral-300">Retry-After</code> dynamically and sets temporary cooldown on the key, immediately failing over to the next healthy key. HTTP 401 revokes the credential until reload.
                    </p>
                  </div>
                  <div className="p-2.5 rounded bg-white/[0.01] border border-white/[0.03] space-y-1">
                    <div className="text-neutral-200 font-medium">Layer 2: Host Circuit Breaker (5xx / Net)</div>
                    <p className="text-neutral-500">
                      Protects against upstream outages. 5 consecutive host network/5xx failures trip the breaker to OPEN. Client 4xx or Key 429 quota exhaustion never count as host failures.
                    </p>
                  </div>
                </div>
              </div>
            </div>
          ) : null}

          {/* ============================================================== */}
          {/* TAB 5: ADVANCED NETWORK & TIMEOUTS */}
          {/* ============================================================== */}
          {activeTab === 'network' ? (
            <div className="space-y-4">
              <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                {/* Response Timeout */}
                <div className="flex flex-col gap-1.5">
                  <label className="text-neutral-400 font-medium">Response Header Timeout (Seconds)</label>
                  <input
                    type="number"
                    min={1}
                    max={300}
                    value={timeoutSec}
                    onChange={(e) => setTimeoutSec(e.target.value)}
                    className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
                  />
                  <span className="text-[10px] text-neutral-500">
                    Max wait time until the first byte of response (TTFB) from the AI provider.
                  </span>
                </div>

                {/* Stream Watchdog Idle Timeout */}
                <div className="flex flex-col gap-1.5">
                  <label className="text-neutral-400 font-medium">Stream Watchdog Idle Timeout (Seconds)</label>
                  <input
                    type="number"
                    min={10}
                    max={600}
                    value={streamTimeoutSec}
                    onChange={(e) => setStreamTimeoutSec(e.target.value)}
                    className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
                  />
                  <span className="text-[10px] text-neutral-500">
                    SSE stream watchdog: closes hung connections if idle gap between tokens exceeds this.
                  </span>
                </div>

                {/* Connection Pool Limits */}
                <div className="flex flex-col gap-1.5">
                  <label className="text-neutral-400 font-medium">Max Outbound Conns Per Host</label>
                  <input
                    type="number"
                    min={10}
                    max={5000}
                    value={maxConns}
                    onChange={(e) => setMaxConns(e.target.value)}
                    className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
                  />
                  <span className="text-[10px] text-neutral-500">
                    Upper bound on TCP sockets to prevent exhausting OS file descriptors.
                  </span>
                </div>

                <div className="flex flex-col gap-1.5">
                  <label className="text-neutral-400 font-medium">Max Idle Keep-Alive Conns Per Host</label>
                  <input
                    type="number"
                    min={10}
                    max={2000}
                    value={maxIdleConns}
                    onChange={(e) => setMaxIdleConns(e.target.value)}
                    className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
                  />
                  <span className="text-[10px] text-neutral-500">
                    Keeps pre-warmed HTTP/2 TLS sockets for zero-latency consecutive calls.
                  </span>
                </div>
              </div>

              {/* Allow Insecure HTTP */}
              <div className="pt-2 border-t border-white/[0.04] flex items-center justify-between">
                <div>
                  <span className="text-neutral-300 font-medium text-xs">Allow Insecure (HTTP)</span>
                  <p className="text-[10px] text-neutral-500">
                    Enable non-TLS connections (useful for local testing with Ollama or vLLM).
                  </p>
                </div>
                <input
                  type="checkbox"
                  checked={allowInsecure}
                  onChange={(e) => setAllowInsecure(e.target.checked)}
                  className="rounded border-white/20 bg-transparent text-emerald-500 focus:ring-0 cursor-pointer"
                />
              </div>

              {/* Custom Extra Headers */}
              <div className="pt-2 border-t border-white/[0.04] space-y-2">
                <div className="flex items-center justify-between">
                  <div>
                    <span className="text-neutral-300 font-medium text-xs">Custom Outbound Headers</span>
                    <p className="text-[10px] text-neutral-500">
                      Forwarded with every outbound request to this upstream.
                    </p>
                  </div>
                  <Button
                    type="button"
                    variant="minimal"
                    size="sm"
                    onClick={handleAddHeader}
                    leftIcon={<Plus className="w-3.5 h-3.5" />}
                  >
                    Add Header
                  </Button>
                </div>

                {extraHeaders.map((hdr, idx) => (
                  <div key={idx} className="flex items-center gap-2">
                    <input
                      type="text"
                      value={hdr.key}
                      onChange={(e) => handleHeaderChange(idx, 'key', e.target.value)}
                      placeholder="Header Name (e.g. anthropic-version)"
                      className="flex-1 px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
                    />
                    <input
                      type="text"
                      value={hdr.value}
                      onChange={(e) => handleHeaderChange(idx, 'value', e.target.value)}
                      placeholder="Header Value"
                      className="flex-1 px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
                    />
                    <button
                      type="button"
                      onClick={() => handleRemoveHeader(idx)}
                      className="p-1 rounded text-neutral-500 hover:text-rose-400 transition-colors"
                      title="Remove Header"
                    >
                      <Trash2 className="w-3.5 h-3.5" />
                    </button>
                  </div>
                ))}
              </div>
            </div>
          ) : null}
        </div>

        {/* Modal Bottom Actions Bar */}
        <div className="pt-3 border-t border-white/[0.06] flex items-center justify-between shrink-0">
          {upstreamToEdit && onDelete ? (
            <Button
              variant="minimal"
              size="sm"
              type="button"
              onClick={() => {
                onDelete(upstreamToEdit);
                onClose();
              }}
              disabled={isSaving}
              className="text-rose-400 hover:text-rose-300 hover:bg-rose-500/10 border-rose-500/20 hover:border-rose-500/40"
              leftIcon={<Trash2 className="w-3.5 h-3.5 text-rose-400" />}
            >
              Delete Upstream
            </Button>
          ) : (
            <div />
          )}

          <div className="flex items-center gap-3">
            <Button variant="minimal" size="sm" type="button" onClick={onClose} disabled={isSaving}>
              Cancel
            </Button>
            <Button
              variant="minimal"
              size="sm"
              type="submit"
              isLoading={isSaving}
              leftIcon={<Check className="w-3.5 h-3.5 text-neutral-400 group-hover:text-white transition-colors" />}
            >
              {upstreamToEdit ? 'Save Upstream' : 'Create Upstream'}
            </Button>
          </div>
        </div>
      </form>

      <OAuthConnectDialog
        isOpen={connectDialogOpen}
        onClose={() => setConnectDialogOpen(false)}
        provider={protocol}
        onSuccess={handleNewAccountConnected}
      />

      <Modal
        isOpen={isDbPickerOpen}
        onClose={() => setIsDbPickerOpen(false)}
        title={
          <div className="flex items-center gap-2">
            <Database className="w-4 h-4 text-emerald-400" />
            <span>Import Keys from Database</span>
          </div>
        }
        description="Select a Harvester provider to import active keys from Turso database."
        size="sm"
      >
        <div className="space-y-4 pt-1 font-mono text-xs">
          <div className="flex flex-col gap-1.5">
            <label className="text-neutral-400 font-medium">Turso Provider</label>
            <select
              value={selectedImportProviderId}
              onChange={(e) => setSelectedImportProviderId(e.target.value)}
              className="w-full px-3 py-2 rounded-lg bg-transparent border border-white/[0.08] text-neutral-200 font-mono text-xs focus:outline-none focus:border-white/20"
            >
              <option value="" className="bg-[#090b10]">-- Select Provider --</option>
              {tursoProviders.map((p) => (
                <option key={p.id} value={String(p.id)} className="bg-[#090b10]">
                  {p.name} ({p.active_keys} active keys)
                </option>
              ))}
            </select>
          </div>

          <div className="flex items-center justify-end gap-2 pt-3 border-t border-white/[0.04]">
            <Button
              type="button"
              variant="minimal"
              size="sm"
              onClick={() => setIsDbPickerOpen(false)}
            >
              Cancel
            </Button>
            <Button
              type="button"
              variant="minimal"
              size="sm"
              disabled={!selectedImportProviderId || isImportingDbKeys}
              isLoading={isImportingDbKeys}
              onClick={() => {
                const pid = parseInt(selectedImportProviderId, 10);
                if (!isNaN(pid) && pid > 0) {
                  void handleImportKeysFromDatabase(pid);
                }
              }}
              leftIcon={<Database className="w-3.5 h-3.5 text-neutral-400" />}
            >
              Import Keys
            </Button>
          </div>
        </div>
      </Modal>
    </Modal>
  );
});
