import { useState, useRef } from 'react';
import {
  Database,
  Play,
  Square,
  Plus,
  Trash2,
  ChevronLeft,
  ChevronRight,
  Search,
  Link2,
} from 'lucide-react';
import { Badge } from '@/components/ui/Badge';
import { Field } from '@/components/ui/Controls';
import {
  useTursoProvidersQuery,
  useCheckUpstreamMutation,
  type UpstreamCheckRequest,
} from '@/services/api';
import { useUiStore } from '@/state/store';
import type { ConnectionDTO } from '@/services/schema';
import { OAuthConnectDialog, isOAuthProtocol } from './OAuthConnectDialog';

export interface KeyEntry {
  id: string;
  ref?: string;
  secret: string;
  rps: number;
  maxConcurrent: number;
}

export interface KeyCheckState {
  status: 'idle' | 'checking' | 'ok' | 'fail';
  code?: number;
  message?: string;
  latency?: number;
}

function formatKeyHint(secret: string): string {
  if (!secret) return '—';
  // Always display the prefix so user can verify key/provider type (sk-proj-, sk-ant-, env:, etc.)
  if (secret.length <= 8) return secret;
  if (secret.length <= 14) return `${secret.slice(0, 6)}••••`;
  return `${secret.slice(0, 8)}••••${secret.slice(-4)}`;
}

interface KeysTabProps {
  providerId: number | null;
  keyStrategy: 'round_robin' | 'least_inflight';
  keys: KeyEntry[];
  upstreamName: string;
  protocol: string;
  baseUrl: string;
  egressMode: string;
  proxyUrl?: string;
  probeModel?: string;
  onProviderChange: (id: number | null) => void;
  onStrategyChange: (strat: 'round_robin' | 'least_inflight') => void;
  onKeysChange: (keys: KeyEntry[]) => void;
}

export function KeysTab({
  providerId,
  keyStrategy,
  keys,
  upstreamName,
  protocol,
  baseUrl,
  egressMode,
  proxyUrl,
  probeModel,
  onProviderChange,
  onStrategyChange,
  onKeysChange,
}: KeysTabProps) {
  const pushToast = useUiStore((s) => s.pushToast);
  const tursoProviders = useTursoProvidersQuery();
  const checkMutation = useCheckUpstreamMutation();

  const [bulkInput, setBulkInput] = useState('');
  const [search, setSearch] = useState('');
  const [page, setPage] = useState(1);
  const pageSize = 15;

  const [keyChecks, setKeyChecks] = useState<Record<string, KeyCheckState>>({});
  const [checkingAll, setCheckingAll] = useState(false);
  const abortCheckRef = useRef(false);

  // OAuth connect dialog state
  const [oauthOpen, setOauthOpen] = useState(false);

  function handleOAuthSuccess(conn: ConnectionDTO) {
    const newEntry: KeyEntry = {
      id: `oauth-${conn.id}-${Date.now()}`,
      ref: `oauth:${conn.id}`,
      secret: `oauth:${conn.id}`,
      rps: 0,
      maxConcurrent: 0,
    };
    onKeysChange([...keys, newEntry]);
    pushToast({
      type: 'success',
      title: 'Account connected',
      message: `${conn.email || conn.id} added as oauth:${conn.id}`,
    });
  }

  const boundProvider = (tursoProviders.data?.providers ?? []).find((p) => p.id === providerId);

  function handleAddBulk() {
    const lines = bulkInput
      .split('\n')
      .map((l) => l.trim())
      .filter(Boolean);
    if (lines.length === 0) return;

    const newEntries: KeyEntry[] = lines.map((k, idx) => ({
      id: `k-${keys.length + idx}-${Date.now()}`,
      ref: `${upstreamName || 'key'}-cred-${keys.length + idx + 1}`,
      secret: k,
      rps: 0,
      maxConcurrent: 0,
    }));

    onKeysChange([...keys, ...newEntries]);
    setBulkInput('');
    pushToast({ type: 'success', title: 'Keys added', message: `${lines.length} key(s) added to pool.` });
  }

  async function testKey(keyId: string, secret: string, ref?: string) {
    if (!baseUrl) {
      pushToast({ type: 'error', title: 'Empty Base URL', message: 'Specify a Base URL before testing keys.' });
      return;
    }
    setKeyChecks((prev) => ({ ...prev, [keyId]: { status: 'checking' } }));

    try {
      const payload: UpstreamCheckRequest = {
        name: upstreamName || 'probe-test',
        key_ref: ref || (secret ? undefined : keyId),
        protocol,
        base_url: baseUrl,
        api_key: secret || undefined,
        egress_mode: egressMode,
        proxy_url: proxyUrl || undefined,
        model: probeModel || undefined,
      };
      const res = await checkMutation.mutateAsync(payload);
      setKeyChecks((prev) => ({
        ...prev,
        [keyId]: {
          status: res.healthy ? 'ok' : 'fail',
          code: res.status_code,
          message: res.message,
          latency: res.latency_ms,
        },
      }));
    } catch (err) {
      setKeyChecks((prev) => ({
        ...prev,
        [keyId]: {
          status: 'fail',
          message: err instanceof Error ? err.message : 'Check failed',
        },
      }));
    }
  }

  async function testAll() {
    if (!baseUrl || keys.length === 0) return;
    setCheckingAll(true);
    abortCheckRef.current = false;

    for (const k of keys) {
      if (abortCheckRef.current) break;
      await testKey(k.id, k.secret, k.ref);
    }
    setCheckingAll(false);
  }

  function stopChecking() {
    abortCheckRef.current = true;
    setCheckingAll(false);
  }

  function removeFailed() {
    const failedIds = new Set(
      Object.entries(keyChecks)
        .filter(([_, v]) => v.status === 'fail')
        .map(([id]) => id),
    );
    if (failedIds.size === 0) return;
    onKeysChange(keys.filter((k) => !failedIds.has(k.id)));
    setKeyChecks((prev) => {
      const next = { ...prev };
      for (const id of failedIds) delete next[id];
      return next;
    });
    pushToast({ type: 'success', title: 'Keys cleaned', message: `${failedIds.size} failed key(s) removed.` });
  }

  const filteredKeys = keys.filter((k) => {
    if (!search.trim()) return true;
    const q = search.toLowerCase();
    return (k.ref && k.ref.toLowerCase().includes(q)) || (k.secret && k.secret.toLowerCase().includes(q));
  });

  const totalPages = Math.max(1, Math.ceil(filteredKeys.length / pageSize));
  const safePage = Math.min(page, totalPages);
  const pagedKeys = filteredKeys.slice((safePage - 1) * pageSize, safePage * pageSize);
  const startIdx = filteredKeys.length === 0 ? 0 : (safePage - 1) * pageSize + 1;
  const endIdx = Math.min(safePage * pageSize, filteredKeys.length);

  return (
    <div className="stack" style={{ marginTop: 0 }}>
      <div className="card">
        <div className="card-header">
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <Database style={{ width: 18, height: 18, color: 'var(--biolum)' }} />
            <h2>Database Provider Binding</h2>
          </div>
          <Badge tone={providerId ? 'ok' : 'neutral'}>
            {providerId ? `BOUND (ID: ${providerId})` : 'NOT BOUND'}
          </Badge>
        </div>
        <div className="card-body">
          <p className="hint" style={{ marginBottom: 12 }}>
            Bind this upstream directly to a Turso credential pool. The gateway retrieves keys automatically without
            storing secrets in the browser.
          </p>

          {boundProvider ? (
            <div
              style={{
                padding: '10px 14px',
                borderRadius: 'var(--radius)',
                background: 'var(--surface-raised)',
                border: '1px solid var(--line)',
                marginBottom: 14,
                fontSize: 12,
              }}
            >
              <span className="text-ink font-medium">Connected to Provider #{boundProvider.id} ({boundProvider.name}): </span>
              <span className="text-muted">
                {boundProvider.active_keys} active keys registered in Turso. The gateway keyring automatically syncs and rotates these keys.
              </span>
            </div>
          ) : null}

          <div className="form-row">
            <select
              value={providerId ?? ''}
              onChange={(e) => onProviderChange(e.target.value ? Number(e.target.value) : null)}
              style={{ flex: 1 }}
            >
              <option value="">-- No Database Binding (Use Manual Keys below) --</option>
              {(tursoProviders.data?.providers ?? []).map((p) => (
                <option key={p.id} value={p.id}>
                  #{p.id} {p.name} ({p.active_keys} active keys) — {p.base_url}
                </option>
              ))}
            </select>
            {providerId && (
              <button type="button" className="btn btn-secondary" onClick={() => onProviderChange(null)}>
                Unbind
              </button>
            )}
          </div>
        </div>
      </div>

      <div className="card">
        <div className="card-header">
          <h2>Key Rotation Strategy</h2>
        </div>
        <div className="card-body">
          <Field label="Strategy" htmlFor="u-key-strat">
            <select
              id="u-key-strat"
              value={keyStrategy}
              onChange={(e) => onStrategyChange(e.target.value as 'round_robin' | 'least_inflight')}
            >
              <option value="round_robin">round_robin (balanced alternating distribution)</option>
              <option value="least_inflight">least_inflight (lowest in-flight connection load)</option>
            </select>
          </Field>
        </div>
      </div>

      <div className="card">
        <div className="card-header">
          <div>
            <h2>Manual Credential Pool</h2>
            <span className="text-xs text-faint" style={{ marginTop: 2, display: 'block' }}>
              {keys.length} {keys.length === 1 ? 'credential' : 'credentials'} registered in local pool
            </span>
          </div>
          <div style={{ display: 'flex', gap: 8 }}>
            {checkingAll ? (
              <button type="button" className="btn btn-secondary" onClick={stopChecking}>
                <Square style={{ width: 14, height: 14 }} />
                Stop
              </button>
            ) : (
              <button
                type="button"
                className="btn btn-secondary"
                disabled={keys.length === 0}
                onClick={() => void testAll()}
              >
                <Play style={{ width: 14, height: 14 }} />
                Check all
              </button>
            )}
            <button type="button" className="btn btn-ghost" disabled={keys.length === 0} onClick={removeFailed}>
              Remove failed
            </button>
          </div>
        </div>
        <div className="card-body">
          {isOAuthProtocol(protocol) && (
            <div
              style={{
                padding: '12px 14px',
                borderRadius: 6,
                border: '1px solid var(--line)',
                background: 'var(--surface-raised)',
                marginBottom: 16,
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'space-between',
                gap: 12,
              }}
            >
              <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
                <span style={{ color: 'var(--ink)', fontSize: 13, fontWeight: 500 }}>
                  OAuth Account Connection
                </span>
                <span style={{ color: 'var(--faint)', fontSize: 11 }}>
                  This protocol authenticates via OAuth. Connect an account instead of
                  pasting API keys manually.
                </span>
              </div>
              <button
                type="button"
                className="btn btn-primary"
                onClick={() => setOauthOpen(true)}
                style={{ whiteSpace: 'nowrap', display: 'inline-flex', alignItems: 'center', gap: 6 }}
              >
                <Link2 style={{ width: 14, height: 14 }} /> Connect Account
              </button>
            </div>
          )}

          <div style={{ marginBottom: 16 }}>
            <Field label="Bulk paste API keys (one key per line)" htmlFor="u-bulk-keys">
              <textarea
                id="u-bulk-keys"
                rows={3}
                placeholder="sk-..."
                value={bulkInput}
                onChange={(e) => setBulkInput(e.target.value)}
              />
            </Field>
            <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: 8 }}>
              <button type="button" className="btn btn-secondary" disabled={!bulkInput.trim()} onClick={handleAddBulk}>
                <Plus style={{ width: 14, height: 14 }} />
                Add keys
              </button>
            </div>
          </div>

          {keys.length > 5 && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12 }}>
              <div style={{ position: 'relative', flex: 1 }}>
                <Search
                  style={{
                    position: 'absolute',
                    left: 10,
                    top: '50%',
                    transform: 'translateY(-50%)',
                    width: 14,
                    height: 14,
                    color: 'var(--muted)',
                  }}
                />
                <input
                  type="search"
                  placeholder="Search ref or key hint…"
                  value={search}
                  onChange={(e) => {
                    setSearch(e.target.value);
                    setPage(1);
                  }}
                  style={{ paddingLeft: 30 }}
                />
              </div>
              <span className="text-xs text-faint shrink-0">
                {filteredKeys.length} matching
              </span>
            </div>
          )}

          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th style={{ width: 48 }}>#</th>
                  <th>Credential Ref</th>
                  <th>Key / Secret Hint</th>
                  <th>Status</th>
                  <th>Latency</th>
                  <th style={{ width: 80 }}>Action</th>
                </tr>
              </thead>
              <tbody>
                {filteredKeys.length === 0 ? (
                  <tr>
                    <td colSpan={6} className="faint">
                      {keys.length === 0 ? 'No manual keys in this pool yet.' : 'No keys match the filter.'}
                    </td>
                  </tr>
                ) : (
                  pagedKeys.map((k, idx) => {
                    const check = keyChecks[k.id];
                    const globalIdx = (safePage - 1) * pageSize + idx + 1;
                    return (
                      <tr key={k.id}>
                        <td className="mono">{globalIdx}</td>
                        <td className="mono font-semibold text-ink" title={k.ref}>
                          {k.ref || `ref-${globalIdx}`}
                        </td>
                        <td className="mono text-muted text-xs" title={k.secret}>
                          {formatKeyHint(k.secret)}
                        </td>
                        <td>
                          {check?.status === 'checking' && <Badge tone="info">Checking</Badge>}
                          {check?.status === 'ok' && (
                            <Badge tone="ok">OK ({check.code ?? 200})</Badge>
                          )}
                          {check?.status === 'fail' && (
                            <Badge tone="danger">{check.code ? `${check.code}` : 'FAIL'}</Badge>
                          )}
                          {(!check || check.status === 'idle') && <span className="faint">—</span>}
                        </td>
                        <td className="mono">{check?.latency ? `${check.latency}ms` : '—'}</td>
                        <td>
                          <div style={{ display: 'flex', gap: 4 }}>
                            <button
                              type="button"
                              className="btn btn-ghost"
                              title="Check key"
                              disabled={check?.status === 'checking'}
                              onClick={() => void testKey(k.id, k.secret, k.ref)}
                            >
                              <Play style={{ width: 12, height: 12 }} />
                            </button>
                            <button
                              type="button"
                              className="btn btn-ghost"
                              title="Delete"
                              onClick={() => onKeysChange(keys.filter((item) => item.id !== k.id))}
                            >
                              <Trash2 style={{ width: 12, height: 12 }} />
                            </button>
                          </div>
                        </td>
                      </tr>
                    );
                  })
                )}
              </tbody>
            </table>
          </div>

          {filteredKeys.length > pageSize ? (
            <div className="flex items-center justify-between pt-3 border-t border-[var(--line)] text-xs text-faint mt-2">
              <span className="tabular-nums">
                Showing <span className="text-ink font-medium">{startIdx}–{endIdx}</span> of{' '}
                <span className="text-ink font-medium">{filteredKeys.length}</span> keys
              </span>
              <div className="flex items-center gap-1.5">
                <button
                  type="button"
                  className="pagination-btn"
                  disabled={safePage <= 1}
                  onClick={() => setPage((p) => Math.max(1, p - 1))}
                  aria-label="Previous page"
                >
                  <ChevronLeft className="w-4 h-4" />
                </button>
                <span className="text-xs text-faint px-1.5 tabular-nums">
                  {safePage} / {totalPages}
                </span>
                <button
                  type="button"
                  className="pagination-btn"
                  disabled={safePage >= totalPages}
                  onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
                  aria-label="Next page"
                >
                  <ChevronRight className="w-4 h-4" />
                </button>
              </div>
            </div>
          ) : null}
        </div>
      </div>

      {/* OAuth Connect Dialog */}
      <OAuthConnectDialog
        open={oauthOpen}
        onClose={() => setOauthOpen(false)}
        provider={protocol}
        onSuccess={handleOAuthSuccess}
      />
    </div>
  );
}
