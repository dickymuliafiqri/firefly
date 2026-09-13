import React, { useState, useEffect, useCallback, useMemo, useDeferredValue, useRef } from 'react';
import type { UpstreamDTO, CredentialKeyDTO } from '@/services/schema';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import { useStoreActions, useAppStore } from '@/core/state/store';
import {
  useSaveSettingsMutation,
  checkUpstreamHealth,
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
} from 'lucide-react';
import { cn } from '@/lib/utils';

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

type TabType = 'general' | 'keys' | 'models' | 'load_balancing' | 'network';

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

  const handleCopy = useCallback(() => {
    navigator.clipboard.writeText(item.secret);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
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
            title={item.message || 'Key valid'}
          >
            valid · {item.latencyMs ?? 0}ms
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
 * ModelRowItem — Memoized model item
 */
const ModelRowItem = React.memo(function ModelRowItem({
  modelId,
  isInCatalog,
}: {
  modelId: string;
  isInCatalog: boolean;
}) {
  const [copied, setCopied] = useState(false);

  const handleCopy = useCallback(() => {
    navigator.clipboard.writeText(modelId);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  }, [modelId]);

  return (
    <div
      style={{ contentVisibility: 'auto', containIntrinsicSize: '0 36px' }}
      className="flex items-center justify-between p-2 rounded-lg border border-white/[0.04] bg-white/[0.01] hover:bg-white/[0.025] transition-colors font-mono text-xs"
    >
      <div className="flex items-center gap-2 truncate">
        <span className="text-neutral-200 font-medium text-[11px] truncate">
          {modelId}
        </span>
      </div>

      <div className="flex items-center gap-3 shrink-0">
        {isInCatalog ? (
          <span className="text-[10px] text-emerald-400 font-mono">
            mapped
          </span>
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
  const { addOrUpdateUpstream, addToast } = useStoreActions();
  const saveMutation = useSaveSettingsMutation();
  const adminToken = useAppStore((s) => s.adminToken);
  const catalogModels = useAppStore((s) => s.models);

  const [activeTab, setActiveTab] = useState<TabType>('keys');

  // General tab state
  const [name, setName] = useState('');
  const [protocol, setProtocol] = useState<'openai' | 'anthropic'>('openai');
  const [baseUrl, setBaseUrl] = useState('');
  const [baseUrls, setBaseUrls] = useState<string[]>([]);
  const [newBaseUrlInput, setNewBaseUrlInput] = useState('');

  // Keys tab state
  const [keys, setKeys] = useState<KeyItem[]>([]);
  const [bulkInputOpen, setBulkInputOpen] = useState(false);
  const [bulkText, setBulkText] = useState('');
  const [singleKeyInput, setSingleKeyInput] = useState('');
  const [keySearch, setKeySearch] = useState('');
  const deferredKeySearch = useDeferredValue(keySearch);
  const [keyFilter, setKeyFilter] = useState<'all' | 'valid' | 'invalid' | 'rate_limited' | 'unchecked'>('all');

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

  // Load balancing tab state
  const [keyStrategy, setKeyStrategy] = useState<'round_robin' | 'least_inflight'>('round_robin');
  const [credentialRps, setCredentialRps] = useState<number>(0);
  const [credentialMaxConcurrent, setCredentialMaxConcurrent] = useState<number>(0);

  // Network & Timeouts tab state
  const [timeoutSec, setTimeoutSec] = useState(30);
  const [streamTimeoutSec, setStreamTimeoutSec] = useState(120);
  const [idleTimeoutSec, setIdleTimeoutSec] = useState(90);
  const [maxConns, setMaxConns] = useState(1500);
  const [maxIdleConns, setMaxIdleConns] = useState(1000);
  const [allowInsecure, setAllowInsecure] = useState(false);
  const [extraHeaders, setExtraHeaders] = useState<Array<{ key: string; value: string }>>([]);

  // Reset or initialize state
  useEffect(() => {
    if (isOpen) {
      if (upstreamToEdit) {
        setName(upstreamToEdit.name);
        setProtocol((upstreamToEdit.protocol as 'openai' | 'anthropic') || 'openai');
        setBaseUrl(upstreamToEdit.base_url || (upstreamToEdit.base_urls && upstreamToEdit.base_urls[0]) || '');
        setBaseUrls(upstreamToEdit.base_urls || []);
        setKeyStrategy((upstreamToEdit.key_strategy as 'round_robin' | 'least_inflight') || 'round_robin');
        setCredentialRps(upstreamToEdit.credential_rps || 0);
        setCredentialMaxConcurrent(upstreamToEdit.credential_max_concurrent || 0);
        setTimeoutSec(Math.round((upstreamToEdit.timeout_ms || 30000) / 1000));
        setStreamTimeoutSec(Math.round((upstreamToEdit.stream_idle_timeout_ms || 120000) / 1000));
        setIdleTimeoutSec(Math.round((upstreamToEdit.idle_timeout_ms || 90000) / 1000));
        setMaxConns(upstreamToEdit.max_conns_per_host || 1500);
        setMaxIdleConns(upstreamToEdit.max_idle_conns_per_host || 1000);
        setAllowInsecure(Boolean(upstreamToEdit.allow_insecure));

        // Format extra headers
        if (upstreamToEdit.extra_headers) {
          setExtraHeaders(
            Object.entries(upstreamToEdit.extra_headers).map(([k, v]) => ({ key: k, value: v }))
          );
        } else {
          setExtraHeaders([]);
        }

        // Initialize Keys
        let initialKeys: KeyItem[] = [];
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
        setKeys(initialKeys);
        setActiveTab('keys');
      } else {
        setName('');
        setProtocol('openai');
        setBaseUrl('https://api.openai.com/v1');
        setBaseUrls([]);
        setKeyStrategy('round_robin');
        setCredentialRps(0);
        setCredentialMaxConcurrent(0);
        setTimeoutSec(30);
        setStreamTimeoutSec(120);
        setIdleTimeoutSec(90);
        setMaxConns(1500);
        setMaxIdleConns(1000);
        setAllowInsecure(false);
        setExtraHeaders([]);
        setKeys([]);
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
    }
  }, [upstreamToEdit, isOpen]);

  // Clean up worker pool on unmount
  useEffect(() => {
    return () => {
      stopCheckRef.current = true;
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
        },
        adminToken
      );
    },
    [name, protocol, baseUrl, adminToken]
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

    // Pick first valid key or any key
    const validKey = keys.find((k) => k.status === 'valid')?.secret || (keys[0]?.secret || '');

    setIsLoadingModels(true);
    try {
      const res = await checkUpstreamHealth(
        {
          name: name.trim(),
          protocol,
          base_url: baseUrl.trim(),
          api_key: validKey,
          timeout_ms: 10000,
        },
        adminToken
      );

      if (res.models && res.models.length > 0) {
        setUpstreamModels(res.models);
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
  }, [baseUrl, keys, name, protocol, adminToken, addToast]);

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
  // Form Submission
  // -------------------------------------------------------------
  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim() || !baseUrl.trim()) {
      addToast({
        title: 'Validation Error',
        message: 'Upstream Name and Base URL are required.',
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

    const poolDTO: CredentialKeyDTO[] = keys.map((k, idx) => ({
      ref: k.ref || `${name.trim()}-key-${idx + 1}`,
      secret: k.secret,
      api_key: k.secret,
      rps: k.rps ?? (credentialRps > 0 ? credentialRps : null),
      max_concurrent: k.max_concurrent ?? (credentialMaxConcurrent > 0 ? credentialMaxConcurrent : null),
    }));

    const apiKeysList = keys.map((k) => k.secret).filter(Boolean);

    const updated: UpstreamDTO = {
      name: name.trim(),
      protocol,
      base_url: baseUrl.trim(),
      base_urls: baseUrls.filter(Boolean),
      key_strategy: keyStrategy,
      api_key: apiKeysList[0] || '',
      api_keys: apiKeysList,
      credential_pool: poolDTO,
      credential_rps: credentialRps > 0 ? credentialRps : null,
      credential_max_concurrent: credentialMaxConcurrent > 0 ? credentialMaxConcurrent : null,
      timeout_ms: timeoutSec * 1000,
      stream_idle_timeout_ms: streamTimeoutSec * 1000,
      idle_timeout_ms: idleTimeoutSec * 1000,
      max_conns_per_host: maxConns,
      max_idle_conns_per_host: maxIdleConns,
      allow_insecure: allowInsecure,
      extra_headers: Object.keys(headersMap).length > 0 ? headersMap : undefined,
      enabled: upstreamToEdit?.enabled ?? true,
    };

    addOrUpdateUpstream(updated);

    // Synchronize to Go backend
    const currentSettings = {
      upstreams: useAppStore.getState().upstreams,
      models: useAppStore.getState().models,
      tenants: useAppStore.getState().tenants,
      combos: useAppStore.getState().combos,
    };
    saveMutation.mutate(currentSettings);

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
            <span>Credentials</span>
            <span className="text-[10px] px-1.5 py-0.2 rounded bg-white/[0.06] tabular-nums">
              {keys.length}
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
                    placeholder="e.g. openai-prod"
                    className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20 disabled:opacity-50"
                  />
                  <span className="text-[10px] text-neutral-500">
                    Unique identifier referenced by model route definitions.
                  </span>
                </div>

                {/* Wire Protocol */}
                <div className="flex flex-col gap-1.5">
                  <label className="text-neutral-400 font-medium">Wire Protocol</label>
                  <select
                    value={protocol}
                    onChange={(e) => setProtocol(e.target.value as 'openai' | 'anthropic')}
                    className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-neutral-200 font-mono text-xs focus:outline-none focus:border-white/20"
                  >
                    <option value="openai" className="bg-[#090b10]">OpenAI (Wire Compatible SSE / Chat)</option>
                    <option value="anthropic" className="bg-[#090b10]">Anthropic (/v1/messages Translation)</option>
                  </select>
                  <span className="text-[10px] text-neutral-500">
                    Firefly transparently converts request/response schemas if needed.
                  </span>
                </div>
              </div>

              {/* Primary Base URL */}
              <div className="flex flex-col gap-1.5">
                <label className="text-neutral-400 font-medium">Primary Base URL</label>
                <input
                  type="url"
                  required
                  value={baseUrl}
                  onChange={(e) => setBaseUrl(e.target.value)}
                  placeholder="https://api.openai.com/v1"
                  className="w-full px-3 py-1.5 rounded-lg bg-transparent border border-white/[0.08] text-white font-mono text-xs focus:outline-none focus:border-white/20"
                />
                <span className="text-[10px] text-neutral-500">
                  Target API endpoint (e.g. Azure OpenAI, OpenAI, Ollama, Anthropic API).
                </span>
              </div>

              {/* Informational failover hint */}
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
            </div>
          ) : null}

          {/* ============================================================== */}
          {/* TAB 2: MASS API KEYS & CONCURRENT HEALTH CHECK */}
          {/* ============================================================== */}
          {activeTab === 'keys' ? (
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
                  </span>
                </div>

                {/* Batch Action Buttons */}
                <div className="flex items-center gap-2 flex-wrap">
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
              <div className="p-3 rounded-xl border border-white/[0.06] bg-white/[0.015] flex flex-col sm:flex-row sm:items-center justify-between gap-3">
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
                  <span className="text-[11px] text-neutral-500 tabular-nums shrink-0">
                    {filteredModels.length} of {upstreamModels.length}
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
                      onChange={(e) => setCredentialMaxConcurrent(parseInt(e.target.value, 10) || 0)}
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
                      onChange={(e) => setCredentialRps(parseFloat(e.target.value) || 0)}
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
                              placeholder={credentialMaxConcurrent > 0 ? `${credentialMaxConcurrent} (def)` : 'unlimited'}
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
                              placeholder={credentialRps > 0 ? `${credentialRps} (def)` : 'unlimited'}
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

              {/* 4. 2-Layer Resilience Architecture */}
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
                    onChange={(e) => setTimeoutSec(parseInt(e.target.value, 10) || 30)}
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
                    onChange={(e) => setStreamTimeoutSec(parseInt(e.target.value, 10) || 120)}
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
                    onChange={(e) => setMaxConns(parseInt(e.target.value, 10) || 1500)}
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
                    onChange={(e) => setMaxIdleConns(parseInt(e.target.value, 10) || 1000)}
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
    </Modal>
  );
});
