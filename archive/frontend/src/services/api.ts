import { useEffect } from 'react';
import { QueryClient, useQuery, useMutation } from '@tanstack/react-query';
import type {
  SettingsDTO,
  HealthStatus,
  TelemetryDTO,
  LiveConnectionLog,
  Protocol,
  ProviderInfoDTO,
  ConnectionDTO,
  AuthorizeRequestDTO,
  AuthorizeResponseDTO,
  PollRequestDTO,
  PollResponseDTO,
  TursoDTO,
  TursoProvidersResponse,
  TursoKeyHintsResponse,
  ProviderListResponse,
  ProviderMutationResponse,
  ProviderDeleteResponse,
  ProviderKeyListResponse,
  ProviderKeyUpsertEntry,
  ProviderKeyUpsertResponse,
  KeyPatchRequest,
  KeyPatchResponse,
  KeyDeleteResponse,
  UpstreamModelsRequest,
  UpstreamModelsResponse,
  WarpStatusDTO,
  TenantTopupRequestDTO,
  TenantTopupResponseDTO,
} from './schema';
import { useAdminToken, useAppStore } from '@/core/state/store';

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 2000, // 2 seconds
      gcTime: 60000, // 1 minute
      refetchOnWindowFocus: false,
      retry: 1,
    },
  },
});

const BASE_URL = '';

/**
 * Custom API Error class with HTTP status code and message
 */
export class ApiError extends Error {
  status: number;
  type?: string;

  constructor(status: number, message: string, type?: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.type = type;
  }
}

/**
 * Fetch settings from Go API plane: GET /api/settings
 */
export async function fetchSettings(adminToken?: string): Promise<SettingsDTO> {
  const headers: Record<string, string> = {
    'Accept': 'application/json',
  };
  if (adminToken) {
    headers['Authorization'] = `Bearer ${adminToken}`;
  }

  const res = await fetch(`${BASE_URL}/api/settings`, { headers });

  if (res.status === 401) {
    throw new ApiError(401, 'Unauthorized: Valid Admin Token required');
  }
  if (res.status === 503) {
    const body = await res.json().catch(() => ({}));
    const message = body?.error?.message || 'Service Unavailable: Gateway is draining';
    throw new ApiError(503, message, body?.error?.type);
  }
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    const message = body?.error?.message || `Failed to fetch settings: ${res.statusText}`;
    throw new ApiError(res.status, message, body?.error?.type);
  }

  return res.json();
}

/**
 * Save settings to Go API plane: PUT /api/settings
 */
export async function saveSettings(
  settings: SettingsDTO,
  adminToken?: string
): Promise<{ status: string; message?: string }> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    'Accept': 'application/json',
  };
  if (adminToken) {
    headers['Authorization'] = `Bearer ${adminToken}`;
  }

  const res = await fetch(`${BASE_URL}/api/settings`, {
    method: 'PUT',
    headers,
    body: JSON.stringify(settings),
  });

  if (res.status === 401) {
    throw new ApiError(401, 'Unauthorized: Valid Admin Token required');
  }
  if (res.status === 409) {
    const body = await res.json().catch(() => ({}));
    const message =
      body?.error?.message ||
      'Configuration conflict: Settings have been modified by another node. Refreshing latest data.';
    throw new ApiError(409, message, 'conflict');
  }
  if (res.status === 503) {
    const body = await res.json().catch(() => ({}));
    const message = body?.error?.message || 'Service Unavailable: Gateway is draining';
    throw new ApiError(503, message, body?.error?.type);
  }
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    const message = body?.error?.message || `Failed to update settings: ${res.statusText}`;
    throw new ApiError(res.status, message, body?.error?.type);
  }

  return res.json();
}

/**
 * Check backend health status: GET /healthz
 */
export async function fetchHealth(): Promise<HealthStatus> {
  const res = await fetch(`${BASE_URL}/healthz`);
  if (!res.ok) {
    return { status: 'error', timestamp: Date.now() };
  }
  const text = await res.text();
  return {
    status: text.includes('ok') ? 'ok' : 'degraded',
    timestamp: Date.now(),
  };
}

/**
 * Authenticate with backend dashboard password: POST /api/auth/login
 */
export async function loginApi(
  password: string
): Promise<{ status: string; token: string; expires_at: string }> {
  const res = await fetch(`${BASE_URL}/api/auth/login`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Accept': 'application/json',
    },
    body: JSON.stringify({ password }),
  });

  if (res.status === 401) {
    throw new ApiError(401, 'Incorrect dashboard access password');
  }
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    const message = body?.error?.message || `Authentication failed: ${res.statusText}`;
    throw new ApiError(res.status, message, body?.error?.type);
  }

  return res.json();
}

/**
 * Verify whether active session token is authorized: GET /api/auth/verify
 */
export async function verifyAuthApi(
  token: string
): Promise<{ status: string; authenticated: boolean }> {
  if (!token) {
    return { status: 'unauthenticated', authenticated: false };
  }

  const res = await fetch(`${BASE_URL}/api/auth/verify`, {
    headers: {
      'Accept': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
  });

  if (!res.ok) {
    return { status: 'unauthenticated', authenticated: false };
  }

  return res.json();
}

/**
 * Revoke session token: POST /api/auth/logout
 */
export async function logoutApi(token: string): Promise<{ status: string }> {
  const res = await fetch(`${BASE_URL}/api/auth/logout`, {
    method: 'POST',
    headers: {
      'Authorization': `Bearer ${token}`,
    },
  });
  return res.json().catch(() => ({ status: 'ok' }));
}

/**
 * Update master dashboard password on backend: PUT /api/auth/password
 */
export async function updatePasswordApi(
  currentPassword: string,
  newPassword: string,
  token?: string
): Promise<{ status: string; message?: string }> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    'Accept': 'application/json',
  };
  if (token) {
    headers['Authorization'] = `Bearer ${token}`;
  }

  const res = await fetch(`${BASE_URL}/api/auth/password`, {
    method: 'PUT',
    headers,
    body: JSON.stringify({
      current_password: currentPassword,
      new_password: newPassword,
    }),
  });

  if (res.status === 401) {
    throw new ApiError(401, 'Incorrect current password or session expired');
  }
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    const message = body?.error?.message || `Failed to update password: ${res.statusText}`;
    throw new ApiError(res.status, message, body?.error?.type);
  }

  return res.json();
}

/**
 * Update circuit breaker state on backend: PUT /api/breakers
 */
export async function updateBreakerApi(
  name: string,
  state: 'OPEN' | 'CLOSED' | 'HALF-OPEN',
  token?: string
): Promise<{ status: string; name: string; state: string }> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    'Accept': 'application/json',
  };
  if (token) {
    headers['Authorization'] = `Bearer ${token}`;
  }

  const res = await fetch(`${BASE_URL}/api/breakers`, {
    method: 'PUT',
    headers,
    body: JSON.stringify({ name, state }),
  });

  if (res.status === 401) {
    throw new ApiError(401, 'Unauthorized: Valid session or admin token required');
  }
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    const message = body?.error?.message || `Failed to update breaker: ${res.statusText}`;
    throw new ApiError(res.status, message, body?.error?.type);
  }

  return res.json();
}

/**
 * Clear request history on backend: DELETE /api/history
 */
export async function deleteHistoryApi(token?: string): Promise<{ status: string }> {
  const headers: Record<string, string> = {
    'Accept': 'application/json',
  };
  if (token) {
    headers['Authorization'] = `Bearer ${token}`;
  }

  const res = await fetch(`${BASE_URL}/api/history`, {
    method: 'DELETE',
    headers,
  });
  if (!res.ok) {
    throw new ApiError(res.status, 'Failed to clear history');
  }
  return res.json();
}

// ================= TANSTACK QUERY HOOKS =================

export function useSettingsQuery() {
  const adminToken = useAdminToken();

  return useQuery({
    queryKey: ['settings', adminToken],
    queryFn: () => fetchSettings(adminToken),
    refetchInterval: 5000, // Background poll every 5s
  });
}

export function useHealthQuery() {
  return useQuery({
    queryKey: ['healthz'],
    queryFn: fetchHealth,
    refetchInterval: 3000, // Background poll every 3s
  });
}

export function useSaveSettingsMutation() {
  const adminToken = useAdminToken();

  return useMutation({
    mutationFn: (settings: SettingsDTO) => saveSettings(settings, adminToken),
    onSuccess: (_, variables) => {
      // Optimistic cache update
      useAppStore.getState().setSettings(variables);
      queryClient.invalidateQueries({ queryKey: ['settings'] });
      useAppStore.getState().addToast({
        title: 'Settings Saved',
        message: 'Configuration successfully synced and hot-reloaded.',
        type: 'success',
      });
    },
    onError: (err: unknown) => {
      if (err instanceof ApiError && err.status === 409) {
        queryClient.invalidateQueries({ queryKey: ['settings'] });
        useAppStore.getState().addToast({
          title: 'Configuration Conflict (409)',
          message: err.message,
          type: 'error',
        });
        return;
      }
      useAppStore.getState().addToast({
        title: 'Save Failed',
        message: err instanceof Error ? err.message : 'Failed to save configuration',
        type: 'error',
      });
    },
  });
}

/**
 * Top up tenant quota tokens or extend expiry: POST /api/tenants/topup
 */
export async function topupTenant(
  req: TenantTopupRequestDTO,
  adminToken?: string
): Promise<TenantTopupResponseDTO> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    'Accept': 'application/json',
  };
  if (adminToken) {
    headers['Authorization'] = `Bearer ${adminToken}`;
  }

  const res = await fetch(`${BASE_URL}/api/tenants/topup`, {
    method: 'POST',
    headers,
    body: JSON.stringify(req),
  });

  if (res.status === 401) {
    throw new ApiError(401, 'Unauthorized: Valid Admin Token required');
  }
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    const message = body?.error?.message || `Top-up failed: ${res.statusText}`;
    throw new ApiError(res.status, message, body?.error?.type);
  }

  return res.json();
}

/**
 * Mutation hook for tenant top-up
 */
export function useTopupTenantMutation() {
  const adminToken = useAdminToken();

  return useMutation({
    mutationFn: (req: TenantTopupRequestDTO) => topupTenant(req, adminToken),
    onSuccess: (data) => {
      queryClient.invalidateQueries({ queryKey: ['settings'] });
      useAppStore.getState().addToast({
        title: 'Top-up Successful',
        message: data.message || 'Tenant balance & expiry updated successfully.',
        type: 'success',
      });
    },
    onError: (err: unknown) => {
      useAppStore.getState().addToast({
        title: 'Top-up Failed',
        message: err instanceof Error ? err.message : 'Failed to top up tenant',
        type: 'error',
      });
    },
  });
}

export interface UpstreamCheckRequest {
  name?: string;
  key_ref?: string;
  protocol?: 'openai' | 'anthropic' | string;
  base_url?: string;
  api_key?: string;
  timeout_ms?: number;
  model?: string;
  egress_mode?: string;
  proxy_url?: string;
}

export interface UpstreamCheckResponse {
  healthy: boolean;
  status_code: number;
  latency_ms: number;
  message: string;
  model_count?: number;
  models?: string[];
  key_ref?: string;
}

/**
 * Actively probe upstream connectivity and credentials: POST /api/upstreams/check
 */
export async function checkUpstreamHealth(
  req: UpstreamCheckRequest,
  adminToken?: string,
  signal?: AbortSignal
): Promise<UpstreamCheckResponse> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    'Accept': 'application/json',
  };
  if (adminToken) {
    headers['Authorization'] = `Bearer ${adminToken}`;
  }

  const res = await fetch(`${BASE_URL}/api/upstreams/check`, {
    method: 'POST',
    headers,
    body: JSON.stringify(req),
    signal,
  });

  if (res.status === 401) {
    throw new ApiError(401, 'Unauthorized: Valid Admin Token required');
  }
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    const message = body?.message || body?.error?.message || `Health check failed: ${res.statusText}`;
    throw new ApiError(res.status, message);
  }

  return res.json();
}

/**
 * Fetch available models from an upstream host without inference: POST /api/upstreams/models
 */
export async function fetchUpstreamModels(
  req: UpstreamModelsRequest,
  adminToken?: string,
  signal?: AbortSignal
): Promise<UpstreamModelsResponse> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    'Accept': 'application/json',
  };
  if (adminToken) {
    headers['Authorization'] = `Bearer ${adminToken}`;
  }

  const res = await fetch(`${BASE_URL}/api/upstreams/models`, {
    method: 'POST',
    headers,
    body: JSON.stringify(req),
    signal,
  });

  if (res.status === 401) {
    throw new ApiError(401, 'Unauthorized: Valid Admin Token required');
  }
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    const message = body?.message || body?.error?.message || `Failed to fetch models: ${res.statusText}`;
    throw new ApiError(res.status, message);
  }

  return res.json();
}

export function useCheckUpstreamMutation() {
  const adminToken = useAdminToken();

  return useMutation({
    mutationFn: (req: UpstreamCheckRequest) => checkUpstreamHealth(req, adminToken),
  });
}

/**
 * Query hook to fetch available models from an upstream using POST /api/upstreams/models
 * Adheres to Vercel React Best Practices: client-swr-dedup with 60s staleTime.
 */
export function useUpstreamModelsQuery(
  upstream: { name?: string; protocol?: string; base_url?: string; api_key?: string; key_ref?: string; egress_mode?: string; proxy_url?: string } | undefined,
  enabled = true
) {
  const adminToken = useAdminToken();

  return useQuery({
    queryKey: ['upstream-models', upstream?.name, upstream?.base_url, upstream?.egress_mode],
    queryFn: async (): Promise<string[]> => {
      if (!upstream || (!upstream.name && !upstream.base_url)) return [];
      const res = await fetchUpstreamModels(
        {
          name: upstream.name,
          key_ref: upstream.key_ref,
          protocol: upstream.protocol || 'openai',
          base_url: upstream.base_url || '',
          api_key: upstream.api_key,
          timeout_ms: 10000,
          egress_mode: upstream.egress_mode,
          proxy_url: upstream.proxy_url,
        },
        adminToken
      );
      return res.models || [];
    },
    enabled: enabled && Boolean(upstream && (upstream.name || upstream.base_url)),
    staleTime: 60000, // 1 minute deduplication cache
    retry: 1,
  });
}

/**
 * Fetch real-time gateway telemetry: GET /api/telemetry
 */
export async function fetchTelemetry(adminToken?: string): Promise<TelemetryDTO> {
  const headers: Record<string, string> = {
    Accept: 'application/json',
  };
  if (adminToken) {
    headers['Authorization'] = `Bearer ${adminToken}`;
  }

  const res = await fetch(`${BASE_URL}/api/telemetry`, { headers });
  if (res.status === 401) {
    throw new ApiError(401, 'Unauthorized: Valid Admin Token required');
  }
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    const message = body?.error?.message || `Failed to fetch telemetry: ${res.statusText}`;
    throw new ApiError(res.status, message);
  }

  return res.json();
}

/**
 * Live Telemetry query hook polling real gateway metrics every 2 seconds.
 * Adheres to Vercel React Best Practices: client-swr-dedup.
 */
export function useTelemetryQuery() {
  const adminToken = useAdminToken();
  const setRecentLogs = useAppStore((state) => state.setRecentLogs);
  const updateStats = useAppStore((state) => state.updateStats);
  const setUpstreamBreakers = useAppStore((state) => state.setUpstreamBreakers);

  const query = useQuery({
    queryKey: ['telemetry', adminToken],
    queryFn: () => fetchTelemetry(adminToken),
    refetchInterval: 2000, // Background poll every 2s
    staleTime: 1000,
  });

  useEffect(() => {
    if (query.data) {
      if (query.data.recent_logs && query.data.recent_logs.length > 0) {
        setRecentLogs(query.data.recent_logs);
      }
      if (query.data.upstreams && query.data.upstreams.length > 0) {
        const breakers: Record<string, 'OPEN' | 'CLOSED' | 'HALF-OPEN'> = {};
        for (const u of query.data.upstreams) {
          const rawState = (u.breaker_state || 'CLOSED').toUpperCase();
          const state: 'OPEN' | 'CLOSED' | 'HALF-OPEN' =
            rawState === 'OPEN' || rawState === 'HALF-OPEN' ? rawState : 'CLOSED';
          breakers[u.name] = state;
        }
        setUpstreamBreakers(breakers);

        const currentStoreUpstreams = useAppStore.getState().upstreams;
        if (currentStoreUpstreams.length === 0) {
          useAppStore.getState().setUpstreams(
            query.data.upstreams.map((u) => ({
              name: u.name,
              protocol: (u.protocol as Protocol) || 'openai',
              base_url: u.base_url || '',
              enabled: breakers[u.name] !== 'OPEN',
            }))
          );
        }
      }
      if (query.data.summary) {
        const s = query.data.summary;
        updateStats({
          inputTokens: s.input_tokens ?? 0,
          outputTokens: s.output_tokens ?? 0,
          totalTokens: s.total_tokens ?? 0,
          totalRequests: s.total_requests ?? 0,
          estimatedCostUsd: s.estimated_cost_usd ?? 0,
          activeStreams: s.active_streams ?? 0,
          p95LatencyMs: Math.round(s.p95_latency_ms ?? 0),
        });
      }
    }
  }, [query.data, setRecentLogs, updateStats, setUpstreamBreakers]);

  return query;
}

/**
 * Real-time Server-Sent Events (SSE) subscriber for inbound API request logs.
 * Receives immediate push notifications (<1ms) when requests arrive at the gateway.
 * Adheres to Vercel React Best Practices:
 * - client-event-listeners: cleanly closes EventSource on unmount/reconnect
 * - rerender-defer-reads: dispatches atomic store mutations without unnecessary re-renders
 */
export function useLiveTelemetryStream() {
  const adminToken = useAdminToken();
  const addLog = useAppStore((state) => state.addLog);

  useEffect(() => {
    const url = new URL('/api/telemetry/events', window.location.origin);
    if (adminToken) {
      url.searchParams.set('token', adminToken);
    }

    let es: EventSource | null = null;
    let retryTimer: ReturnType<typeof setTimeout> | null = null;
    let isDisposed = false;

    const connect = () => {
      if (isDisposed) return;
      try {
        es = new EventSource(url.toString());

        es.addEventListener('log', (event: MessageEvent) => {
          try {
            const log = JSON.parse(event.data) as LiveConnectionLog;
            addLog(log);
          } catch {
            // Ignore malformed payloads
          }
        });

        es.onerror = () => {
          if (es) {
            es.close();
            es = null;
          }
          if (!isDisposed) {
            retryTimer = setTimeout(connect, 3000);
          }
        };
      } catch {
        if (!isDisposed) {
          retryTimer = setTimeout(connect, 3000);
        }
      }
    };

    connect();

    return () => {
      isDisposed = true;
      if (retryTimer) {
        clearTimeout(retryTimer);
      }
      if (es) {
        es.close();
      }
    };
  }, [adminToken, addLog]);
}

/**
 * Fetch available OAuth providers from Go API plane: GET /api/oauth/providers
 */
export async function fetchOAuthProviders(adminToken?: string): Promise<ProviderInfoDTO[]> {
  const headers: Record<string, string> = {
    Accept: 'application/json',
  };
  if (adminToken) {
    headers['Authorization'] = `Bearer ${adminToken}`;
  }

  const res = await fetch(`${BASE_URL}/api/oauth/providers`, { headers });
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new ApiError(res.status, body?.error?.message || 'Failed to fetch OAuth providers');
  }
  return res.json();
}

export function useOAuthProvidersQuery() {
  const adminToken = useAdminToken();
  return useQuery({
    queryKey: ['oauth', 'providers', adminToken],
    queryFn: () => fetchOAuthProviders(adminToken),
    enabled: !!adminToken,
    staleTime: 60000,
  });
}

/**
 * Fetch active OAuth connections: GET /api/oauth/connections
 */
export async function fetchOAuthConnections(adminToken?: string): Promise<ConnectionDTO[]> {
  const headers: Record<string, string> = {
    Accept: 'application/json',
  };
  if (adminToken) {
    headers['Authorization'] = `Bearer ${adminToken}`;
  }

  const res = await fetch(`${BASE_URL}/api/oauth/connections`, { headers });
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new ApiError(res.status, body?.error?.message || 'Failed to fetch OAuth connections');
  }
  return res.json();
}

export function useOAuthConnectionsQuery() {
  const adminToken = useAdminToken();
  return useQuery({
    queryKey: ['oauth', 'connections', adminToken],
    queryFn: () => fetchOAuthConnections(adminToken),
    enabled: !!adminToken,
    staleTime: 5000,
    refetchInterval: 15000,
  });
}

/**
 * Initiate an OAuth authorization flow: POST /api/oauth/authorize
 */
export async function initiateOAuthAuthorize(
  payload: AuthorizeRequestDTO,
  adminToken?: string
): Promise<AuthorizeResponseDTO> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    Accept: 'application/json',
  };
  if (adminToken) {
    headers['Authorization'] = `Bearer ${adminToken}`;
  }

  const res = await fetch(`${BASE_URL}/api/oauth/authorize`, {
    method: 'POST',
    headers,
    body: JSON.stringify(payload),
  });

  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new ApiError(res.status, body?.error?.message || 'Failed to initiate OAuth authorization');
  }
  return res.json();
}

export function useOAuthAuthorizeMutation() {
  const adminToken = useAdminToken();
  return useMutation({
    mutationFn: (payload: AuthorizeRequestDTO) => initiateOAuthAuthorize(payload, adminToken),
  });
}

/**
 * Poll OAuth session status: POST /api/oauth/poll
 */
export async function pollOAuthStatus(
  payload: PollRequestDTO,
  adminToken?: string
): Promise<PollResponseDTO> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    Accept: 'application/json',
  };
  if (adminToken) {
    headers['Authorization'] = `Bearer ${adminToken}`;
  }

  const res = await fetch(`${BASE_URL}/api/oauth/poll`, {
    method: 'POST',
    headers,
    body: JSON.stringify(payload),
  });

  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new ApiError(res.status, body?.error?.message || 'Failed to poll OAuth status');
  }
  return res.json();
}

export function useOAuthPollMutation() {
  const adminToken = useAdminToken();
  return useMutation({
    mutationFn: (payload: PollRequestDTO) => pollOAuthStatus(payload, adminToken),
  });
}

/**
 * Delete an OAuth connection: DELETE /api/oauth/connections/{id}
 */
export async function deleteOAuthConnection(id: string, adminToken?: string): Promise<void> {
  const headers: Record<string, string> = {
    Accept: 'application/json',
  };
  if (adminToken) {
    headers['Authorization'] = `Bearer ${adminToken}`;
  }

  const res = await fetch(`${BASE_URL}/api/oauth/connections/${encodeURIComponent(id)}`, {
    method: 'DELETE',
    headers,
  });

  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new ApiError(res.status, body?.error?.message || 'Failed to delete OAuth connection');
  }
}

export function useDeleteOAuthConnectionMutation() {
  const adminToken = useAdminToken();
  return useMutation({
    mutationFn: (id: string) => deleteOAuthConnection(id, adminToken),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['oauth', 'connections'] });
    },
  });
}

/**
 * Fetch available Harvester providers from Turso centralized database: GET /api/turso/providers
 */
export async function fetchTursoProviders(adminToken?: string): Promise<TursoProvidersResponse> {
  const headers: Record<string, string> = {
    Accept: 'application/json',
  };
  if (adminToken) {
    headers['Authorization'] = `Bearer ${adminToken}`;
  }

  const res = await fetch(`${BASE_URL}/api/turso/providers`, { headers });
  if (!res.ok) {
    return { configured: false, providers: [] };
  }
  return res.json().catch(() => ({ configured: false, providers: [] }));
}

export function useTursoProvidersQuery() {
  const adminToken = useAdminToken();
  return useQuery({
    queryKey: ['turso', 'providers', adminToken],
    queryFn: () => fetchTursoProviders(adminToken),
    staleTime: 30000,
    refetchInterval: 15000,
  });
}

/**
 * Test connectivity and authorization with Turso database: POST /api/turso/test
 */
export async function testTursoConnection(
  payload: TursoDTO,
  adminToken?: string
): Promise<{ ok: boolean; message: string; latency_ms?: number }> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    Accept: 'application/json',
  };
  if (adminToken) {
    headers['Authorization'] = `Bearer ${adminToken}`;
  }

  const res = await fetch(`${BASE_URL}/api/turso/test`, {
    method: 'POST',
    headers,
    body: JSON.stringify(payload),
  });

  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new ApiError(res.status, body?.message || 'Turso test request failed');
  }
  return res.json();
}

export function useTestTursoMutation() {
  const adminToken = useAdminToken();
  return useMutation({
    mutationFn: (payload: TursoDTO) => testTursoConnection(payload, adminToken),
  });
}

/**
 * Fetch active provider keys from Turso: GET /api/turso/providers/{id}/keys or /api/turso/keys.
 * Secret material stays on the server — every entry carries a masked `api_key_hint`
 * plus the id/status the dashboard needs to reconcile its local list.
 */
export async function fetchTursoProviderKeyHints(
  providerId?: number,
  adminToken?: string
): Promise<TursoKeyHintsResponse> {
  const headers: Record<string, string> = {
    Accept: 'application/json',
  };
  if (adminToken) {
    headers['Authorization'] = `Bearer ${adminToken}`;
  }

  const endpoint = providerId && providerId > 0
    ? `${BASE_URL}/api/turso/providers/${providerId}/keys`
    : `${BASE_URL}/api/turso/keys`;

  const res = await fetch(endpoint, { headers });
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new ApiError(res.status, body?.error?.message || body?.message || 'Failed to fetch keys from Turso database');
  }
  return res.json().catch(() => ({ ok: false, count: 0, keys: [] }));
}

/**
 * Fetch Cloudflare WARP tunnel status: GET /api/warp/status
 */
export async function fetchWarpStatus(adminToken?: string | null): Promise<WarpStatusDTO> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  if (adminToken) {
    headers['Authorization'] = `Bearer ${adminToken}`;
  }
  const res = await fetch(`${BASE_URL}/api/warp/status`, { headers });
  if (!res.ok) {
    throw new ApiError(res.status, 'Failed to fetch WARP status');
  }
  return res.json();
}

export function useWarpStatusQuery() {
  const adminToken = useAdminToken();
  return useQuery({
    queryKey: ['warp_status', adminToken],
    queryFn: () => fetchWarpStatus(adminToken),
    refetchInterval: 10000,
  });
}

/**
 * Trigger on-demand Cloudflare WARP session rotation: POST /api/warp/rotate
 */
export async function rotateWarp(adminToken?: string | null): Promise<WarpStatusDTO> {
  const headers: Record<string, string> = {
    Accept: 'application/json',
    'Content-Type': 'application/json',
  };
  if (adminToken) {
    headers['Authorization'] = `Bearer ${adminToken}`;
  }
  const res = await fetch(`${BASE_URL}/api/warp/rotate`, {
    method: 'POST',
    headers,
  });
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new ApiError(res.status, body?.error || 'Failed to rotate WARP session');
  }
  return res.json();
}

export function useRotateWarpMutation() {
  const adminToken = useAdminToken();
  return useMutation({
    mutationFn: () => rotateWarp(adminToken),
    onSuccess: (status) => {
      queryClient.invalidateQueries({ queryKey: ['warp_status'] });
      useAppStore.getState().addToast({
        title: 'WARP Session Rotated',
        message: `New egress public IP assigned: ${status.public_ip || 'Unknown'} (${status.colo || 'Cloudflare Edge'})`,
        type: 'success',
      });
    },
    onError: (err: unknown) => {
      useAppStore.getState().addToast({
        title: 'Rotation Failed',
        message: err instanceof Error ? err.message : 'Could not negotiate new WARP session',
        type: 'error',
      });
    },
  });
}

/**
 * Operator provider administration: `/api/providers*` and `/api/keys/{id}`.
 * These routes write the shared `providers`/`api_keys` tables the harvester also
 * feeds, so every mutation reports `reloaded`/`revision` and reads back hints
 * only — no endpoint here returns a secret.
 */
async function adminProviderRequest<T>(
  path: string,
  method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE',
  adminToken: string | undefined,
  body?: unknown
): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json';
  }
  if (adminToken) {
    headers['Authorization'] = `Bearer ${adminToken}`;
  }

  const res = await fetch(`${BASE_URL}${path}`, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });

  if (!res.ok) {
    const payload = await res.json().catch(() => ({}));
    const message =
      payload?.error?.message || `Provider request failed: ${res.statusText}`;
    throw new ApiError(res.status, message, payload?.error?.type);
  }

  return res.json();
}

function invalidateProviders() {
  queryClient.invalidateQueries({ queryKey: ['providers'] });
  queryClient.invalidateQueries({ queryKey: ['turso', 'providers'] });
}

export async function fetchProviders(adminToken?: string): Promise<ProviderListResponse> {
  return adminProviderRequest<ProviderListResponse>('/api/providers', 'GET', adminToken);
}

export function useProvidersQuery() {
  const adminToken = useAdminToken();
  return useQuery({
    queryKey: ['providers', adminToken],
    queryFn: () => fetchProviders(adminToken),
    staleTime: 15000,
  });
}

export async function fetchProviderKeys(
  providerId: number,
  adminToken?: string
): Promise<ProviderKeyListResponse> {
  return adminProviderRequest<ProviderKeyListResponse>(
    `/api/providers/${providerId}/keys`,
    'GET',
    adminToken
  );
}

export function useProviderKeysQuery(providerId: number | null) {
  const adminToken = useAdminToken();
  return useQuery({
    queryKey: ['providers', providerId, 'keys', adminToken],
    queryFn: () => fetchProviderKeys(providerId as number, adminToken),
    enabled: providerId !== null && providerId > 0,
    staleTime: 10000,
  });
}

export interface CreateProviderPayload {
  name: string;
  base_url: string;
  description?: string;
  is_active?: boolean;
}

export function useCreateProviderMutation() {
  const adminToken = useAdminToken();
  return useMutation({
    mutationFn: (payload: CreateProviderPayload) =>
      adminProviderRequest<ProviderMutationResponse>('/api/providers', 'POST', adminToken, payload),
    onSuccess: invalidateProviders,
  });
}

export interface UpdateProviderPayload {
  base_url?: string;
  description?: string;
  is_active?: boolean;
}

export function useUpdateProviderMutation() {
  const adminToken = useAdminToken();
  return useMutation({
    mutationFn: ({ id, patch }: { id: number; patch: UpdateProviderPayload }) =>
      adminProviderRequest<ProviderMutationResponse>(
        `/api/providers/${id}`,
        'PUT',
        adminToken,
        patch
      ),
    onSuccess: invalidateProviders,
  });
}

export function useDeleteProviderMutation() {
  const adminToken = useAdminToken();
  return useMutation({
    mutationFn: (id: number) =>
      adminProviderRequest<ProviderDeleteResponse>(
        `/api/providers/${id}`,
        'DELETE',
        adminToken
      ),
    onSuccess: invalidateProviders,
  });
}

export function useUpsertProviderKeysMutation() {
  const adminToken = useAdminToken();
  return useMutation({
    mutationFn: ({
      providerId,
      keys,
      reassign,
    }: {
      providerId: number;
      keys: ProviderKeyUpsertEntry[];
      reassign?: boolean;
    }) =>
      adminProviderRequest<ProviderKeyUpsertResponse>(
        `/api/providers/${providerId}/keys`,
        'POST',
        adminToken,
        reassign ? { keys, reassign } : { keys }
      ),
    onSuccess: invalidateProviders,
  });
}

export function usePatchProviderKeyMutation() {
  const adminToken = useAdminToken();
  return useMutation({
    mutationFn: ({ id, patch }: { id: number; patch: KeyPatchRequest }) =>
      adminProviderRequest<KeyPatchResponse>(`/api/keys/${id}`, 'PATCH', adminToken, patch),
    onSuccess: invalidateProviders,
  });
}

export function useDeleteProviderKeyMutation() {
  const adminToken = useAdminToken();
  return useMutation({
    mutationFn: (id: number) =>
      adminProviderRequest<KeyDeleteResponse>(`/api/keys/${id}`, 'DELETE', adminToken),
    onSuccess: invalidateProviders,
  });
}



