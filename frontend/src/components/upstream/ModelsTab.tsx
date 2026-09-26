import { RefreshCw } from 'lucide-react';
import { Badge } from '@/components/ui/Badge';
import { useUpstreamModelsMutation, type UpstreamModelsRequest } from '@/services/api';
import { useUiStore } from '@/state/store';

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

  async function handleFetch() {
    if (!baseUrl) {
      pushToast({ type: 'error', title: 'Empty Base URL', message: 'Specify a Base URL to fetch model list.' });
      return;
    }
    try {
      const payload: UpstreamModelsRequest = {
        name: upstreamName || undefined,
        protocol,
        base_url: baseUrl,
        api_key: firstKey || undefined,
        egress_mode: egressMode,
        proxy_url: proxyUrl || undefined,
      };
      const res = await modelsMutation.mutateAsync(payload);
      onDiscover(res.models || [], res.latency_ms);
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

  return (
    <div className="stack" style={{ marginTop: 0 }}>
      <div className="card">
        <div className="card-header">
          <h2>Upstream Model Discovery</h2>
          <div style={{ display: 'flex', gap: 8 }}>
            <button
              type="button"
              className="btn btn-primary"
              disabled={modelsMutation.isPending || !baseUrl}
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
            Discover models supported by this upstream and create gateway routes in the catalog with one click.
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
                  <th style={{ width: 140 }}>Action</th>
                </tr>
              </thead>
              <tbody>
                {discoveredModels.length === 0 ? (
                  <tr>
                    <td colSpan={3} className="faint">
                      No models discovered yet.
                    </td>
                  </tr>
                ) : (
                  discoveredModels.map((m) => {
                    const existingMapping = catalogModels.find(
                      (cm) => cm.upstream === upstreamName && (cm.upstream_model === m || cm.public_name === m),
                    );
                    const inCatalog = Boolean(existingMapping) || catalogNames.includes(m);
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
                          <button
                            type="button"
                            className="btn btn-secondary"
                            disabled={inCatalog}
                            onClick={() => onCreateRoute(m)}
                          >
                            {inCatalog ? 'Registered' : 'Add route'}
                          </button>
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
