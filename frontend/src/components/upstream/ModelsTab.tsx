import { useEffect, useRef, useState } from 'react';
import { Activity, RefreshCw, Square } from 'lucide-react';
import { Badge } from '@/components/ui/Badge';
import {
  useCheckUpstreamMutation,
  useSettingsQuery,
  useUpstreamModelsMutation,
  type UpstreamCheckRequest,
  type UpstreamCheckResponse,
  type UpstreamModelsRequest,
} from '@/services/api';
import { useUiStore } from '@/state/store';
import { resolveBaseUrl } from './GeneralTab';

interface ModelsTabProps {
  upstreamName: string;
  protocol: string;
  baseUrl: string;
  egressMode: string;
  proxyUrl?: string;
  firstKey?: string;
  discoveredModels: string[];
  latencyMs: number | null;
  catalogNames?: string[];
  catalogModels?: Array<{ public_name: string; upstream: string; upstream_model?: string }>;
  onDiscover: (models: string[], latencyMs: number) => void;
  onCreateRoute: (modelName: string) => void;
  onCreateAll: (models: string[]) => void;
}

interface ModelCheckState {
  status: 'checking' | 'ok' | 'fail';
  code?: number;
  message?: string;
  latency?: number;
  keyRef?: string;
}

export function ModelsTab({
  upstreamName,
  protocol,
  baseUrl,
  egressMode,
  proxyUrl,
  firstKey,
  discoveredModels,
  latencyMs,
  catalogNames = [],
  catalogModels = [],
  onDiscover,
  onCreateRoute,
  onCreateAll,
}: ModelsTabProps) {
  const pushToast = useUiStore((s) => s.pushToast);
  const modelsMutation = useUpstreamModelsMutation();
  const checkMutation = useCheckUpstreamMutation();
  const settings = useSettingsQuery();

  const [modelChecks, setModelChecks] = useState<Record<string, ModelCheckState>>({});
  const [checkingAll, setCheckingAll] = useState(false);
  // Bulk-run generation counter: bumping it invalidates any in-flight bulk loop
  // (stop, restart, or switching upstreams), so an old loop can never keep
  // probing in the background after a Stop/restart.
  const bulkGenRef = useRef(0);

  // Reset stale per-model health results when the editor switches upstream.
  useEffect(() => {
    bulkGenRef.current += 1;
    setCheckingAll(false);
    setModelChecks({});
  }, [upstreamName]);

  // A saved upstream resolves to a KeyRing in the gateway snapshot; probing it
  // without an explicit api_key lets the backend pick the credential through the
  // upstream's load-balancing strategy (round_robin / least_inflight) and report
  // which key_ref served the probe. Unsaved (new) upstreams have no KeyRing yet,
  // so the first pool key entered in the form is used as a fallback.
  const isSavedUpstream = (settings.data?.upstreams ?? []).some((u) => u.name === upstreamName);
  // An OAuth-managed protocol resolves to its provider endpoint; the backend pins
  // the same value, so discovery can never be aimed at another host.
  const effectiveBaseUrl = resolveBaseUrl(protocol, baseUrl);

  async function handleFetch() {
    if (!effectiveBaseUrl) {
      pushToast({ type: 'error', title: 'Empty Base URL', message: 'Specify a Base URL to fetch model list.' });
      return;
    }
    try {
      const payload: UpstreamModelsRequest = {
        name: upstreamName || undefined,
        protocol,
        base_url: effectiveBaseUrl,
        api_key: firstKey || undefined,
        egress_mode: egressMode,
        proxy_url: proxyUrl || undefined,
      };
      const res = await modelsMutation.mutateAsync(payload);
      onDiscover(res.models || [], res.latency_ms);
      setModelChecks({});
      pushToast({
        type: 'success',
        title: 'Models discovered',
        message: `${res.model_count} models available from upstream host.`,
      });
    } catch (err) {
      pushToast({
        type: 'error',
        title: 'Failed to fetch models',
        message: err instanceof Error ? err.message : 'Unknown error',
      });
    }
  }

  // Saved upstreams omit api_key so the backend picks a KeyRing credential per the
  // load-balancing strategy; brand-new upstreams fall back to the first form key.
  function buildCheckRequest(model?: string): UpstreamCheckRequest {
    return {
      name: upstreamName || undefined,
      protocol,
      base_url: effectiveBaseUrl,
      api_key: isSavedUpstream ? undefined : firstKey || undefined,
      model,
      egress_mode: egressMode,
      proxy_url: proxyUrl || undefined,
    };
  }

  async function checkModel(model: string): Promise<UpstreamCheckResponse | null> {
    if (!effectiveBaseUrl) {
      pushToast({ type: 'error', title: 'Empty Base URL', message: 'Specify a Base URL before checking health.' });
      return null;
    }
    setModelChecks((prev) => ({ ...prev, [model]: { status: 'checking' } }));
    try {
      const res = await checkMutation.mutateAsync(buildCheckRequest(model));
      setModelChecks((prev) => ({
        ...prev,
        [model]: {
          status: res.healthy ? 'ok' : 'fail',
          code: res.status_code,
          message: res.message,
          latency: res.latency_ms,
          keyRef: res.key_ref,
        },
      }));
      return res;
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Health check failed';
      setModelChecks((prev) => ({ ...prev, [model]: { status: 'fail', message } }));
      return null;
    }
  }

  async function handleCheckAll() {
    if (!effectiveBaseUrl || discoveredModels.length === 0) return;
    const gen = bulkGenRef.current + 1;
    bulkGenRef.current = gen;
    setCheckingAll(true);

    let healthy = 0;
    let failed = 0;
    for (const model of discoveredModels) {
      if (bulkGenRef.current !== gen) break;
      const res = await checkModel(model);
      if (bulkGenRef.current !== gen) break;
      if (res?.healthy) healthy += 1;
      else failed += 1;
    }

    if (bulkGenRef.current === gen) {
      setCheckingAll(false);
      pushToast({
        type: failed > 0 ? 'error' : 'success',
        title: 'Model health check finished',
        message: `${healthy} healthy · ${failed} failed (credentials picked per load-balancing strategy).`,
      });
    }
  }

  function stopChecking() {
    bulkGenRef.current += 1;
    setCheckingAll(false);
  }

  return (
    <div className="stack" style={{ marginTop: 0 }}>
      <div className="card">
        <div className="card-header">
          <h2>Upstream Model Discovery</h2>
          <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
            <button
              type="button"
              className="btn btn-primary"
              disabled={modelsMutation.isPending || checkingAll || !effectiveBaseUrl}
              onClick={() => void handleFetch()}
            >
              <RefreshCw
                style={{
                  width: 14,
                  height: 14,
                  animation: modelsMutation.isPending ? 'spin 1s linear infinite' : 'none',
                }}
              />
              {modelsMutation.isPending ? 'Fetching…' : 'Fetch Models'}
            </button>
            {checkingAll ? (
              <button type="button" className="btn btn-secondary" onClick={stopChecking}>
                <Square style={{ width: 14, height: 14 }} />
                Stop check
              </button>
            ) : (
              <button
                type="button"
                className="btn btn-secondary"
                disabled={discoveredModels.length === 0 || !effectiveBaseUrl}
                title="Probe every discovered model using a key picked by the upstream's load-balancing strategy"
                onClick={() => void handleCheckAll()}
              >
                <Activity style={{ width: 14, height: 14 }} />
                Check all health
              </button>
            )}
            {discoveredModels.length > 0 && (
              <button
                type="button"
                className="btn btn-secondary"
                onClick={() => onCreateAll(discoveredModels)}
              >
                Add all routes
              </button>
            )}
          </div>
        </div>
        <div className="card-body">
          <p className="hint" style={{ marginBottom: 14 }}>
            Discover models supported by this upstream and create gateway routes in the catalog with one click. Each
            model can be probed with a minimal inference request; saved upstreams pick the credential through the
            KeyRing&apos;s load-balancing strategy (the key used is reported with the result).
          </p>

          {latencyMs !== null && (
            <div style={{ marginBottom: 12 }}>
              <span className="mono" style={{ fontSize: 12, color: 'var(--ok)' }}>
                Response time: {latencyMs}ms
              </span>
            </div>
          )}

          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Model Name</th>
                  <th>Catalog Status</th>
                  <th style={{ width: 200 }}>Health</th>
                  <th style={{ width: 230 }}>Action</th>
                </tr>
              </thead>
              <tbody>
                {discoveredModels.length === 0 ? (
                  <tr>
                    <td colSpan={4} className="faint">
                      No models discovered yet.
                    </td>
                  </tr>
                ) : (
                  discoveredModels.map((m) => {
                    const existingMapping = catalogModels.find(
                      (cm) => cm.upstream === upstreamName && (cm.upstream_model === m || cm.public_name === m),
                    );
                    const inCatalog = Boolean(existingMapping) || catalogNames.includes(m);
                    const check = modelChecks[m];
                    return (
                      <tr key={m}>
                        <td>
                          <div className="mono font-medium text-ink">{m}</div>
                          {existingMapping && existingMapping.public_name !== m ? (
                            <div className="text-[11px] text-muted font-mono mt-0.5">
                              Mapped as: {existingMapping.public_name}
                            </div>
                          ) : null}
                        </td>
                        <td>
                          <Badge tone={inCatalog ? 'ok' : 'neutral'}>
                            {inCatalog ? 'REGISTERED' : 'UNMAPPED'}
                          </Badge>
                        </td>
                        <td>
                          {check?.status === 'checking' ? (
                            <Badge tone="warn">CHECKING…</Badge>
                          ) : check?.status === 'ok' ? (
                            <div className="flex flex-col gap-0.5">
                              <div className="flex items-center gap-1.5">
                                <Badge tone="ok">HEALTHY{check.code ? ` (${check.code})` : ''}</Badge>
                                {check.latency != null ? (
                                  <span className="mono text-[11px]" style={{ color: 'var(--ok)' }}>
                                    {check.latency}ms
                                  </span>
                                ) : null}
                              </div>
                              {check.keyRef ? (
                                <span className="text-[10px] text-faint font-mono truncate" title={check.message}>
                                  via {check.keyRef}
                                </span>
                              ) : check.message ? (
                                <span className="text-[10px] text-faint truncate max-w-[180px]" title={check.message}>
                                  {check.message}
                                </span>
                              ) : null}
                            </div>
                          ) : check?.status === 'fail' ? (
                            <div className="flex flex-col gap-0.5">
                              <Badge tone="danger">FAILED{check.code ? ` (${check.code})` : ''}</Badge>
                              {check.message ? (
                                <span className="text-[10px] text-faint truncate max-w-[180px]" title={check.message}>
                                  {check.message}
                                </span>
                              ) : null}
                            </div>
                          ) : (
                            <span className="text-faint text-xs">Not checked</span>
                          )}
                        </td>
                        <td>
                          <div className="flex items-center gap-1.5">
                            <button
                              type="button"
                              className="btn btn-secondary"
                              disabled={checkingAll || check?.status === 'checking' || !effectiveBaseUrl}
                              title="Probe this model with a key picked by the load-balancing strategy"
                              onClick={() => void checkModel(m)}
                            >
                              <Activity
                                style={{
                                  width: 13,
                                  height: 13,
                                  animation: check?.status === 'checking' ? 'spin 1s linear infinite' : 'none',
                                }}
                              />
                              Check
                            </button>
                            <button
                              type="button"
                              className="btn btn-secondary"
                              disabled={inCatalog}
                              onClick={() => onCreateRoute(m)}
                            >
                              {inCatalog ? 'Registered' : 'Add route'}
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
        </div>
      </div>
    </div>
  );
}
