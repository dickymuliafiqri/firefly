/**
 * api.ts — Port kontrak dari frontend/src/services/api.ts (app lama).
 * Kebenaran tipe: ./schema.ts (salinan verbatim dari app lama) + DTO Go backend.
 *
 * Prinsip F4: kontrak backend TIDAK diubah. Base URL '' (same-origin, dev via proxy).
 * Saat gateway tidak terjangkau (dev standalone), query hooks jatuh ke typed mock
 * dari @/data/mock agar UI tetap dapat ditinjau; ApiError asli (401/409/5xx) tetap
 * dilempar ke UI.
 */
import { QueryClient, useQuery, useMutation } from '@tanstack/react-query';
import type {
  SettingsDTO,
  HealthStatus,
  TelemetryDTO,
  LiveConnectionLog,
  ProviderInfoDTO,
  ConnectionDTO,
  AuthorizeRequestDTO,
  AuthorizeResponseDTO,
  PollRequestDTO,
  PollResponseDTO,
  WarpStatusDTO,
  TenantTopupRequestDTO,
  TenantTopupResponseDTO,
  ProviderListResponse,
  ProviderKeyListResponse,
  ProviderKeyUpsertEntry,
  ProviderKeyUpsertResponse,
  KeyPatchRequest,
  KeyPatchResponse,
  KeyDeleteResponse,
  TursoProvidersResponse,
  TursoKeyHintsResponse,
  TursoDTO,
  ProviderRecordDTO,
} from './schema';

import { getAdminToken } from '@/state/auth';
import { handleSessionInvalid } from '@/lib/session';

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 2000,
      gcTime: 60000,
      refetchOnWindowFocus: false,
      retry: 1,
    },
  },
});

const BASE_URL = '';

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

/** True hanya untuk kegagalan transport (gateway tidak ada) — bukan HTTP error. */
export function isNetworkError(err: unknown): boolean {
  return err instanceof TypeError;
}

/**
 * Probe origin sekali: apakah ada Firefly gateway di belakang origin ini?
 * Gateway selalu menjawab /healthz dengan 200 ("ok") atau 503 (draining).
 * 404/HTML = origin tanpa gateway (mis. static host demo) → hooks pakai mock.
 */
let gatewayProbe: Promise<boolean> | null = null;

export function hasGateway(): Promise<boolean> {
  if (!gatewayProbe) {
    gatewayProbe = fetch(`${BASE_URL}/healthz`, { method: 'GET' }).then(
      (res) => res.status === 200 || res.status === 503,
      () => false,
    );
  }
  return gatewayProbe;
}

async function request<T>(
  path: string,
  init?: RequestInit & { token?: string; tolerateUnauthorized?: boolean },
): Promise<T> {
  if (!(await hasGateway())) {
    throw new ApiError(503, 'Demo mode: gateway Firefly tidak terjangkau dari origin ini.');
  }
  const { token = getAdminToken(), tolerateUnauthorized = false, ...rest } = init ?? {};
  const headers: Record<string, string> = {
    Accept: 'application/json',
    ...(rest.body ? { 'Content-Type': 'application/json' } : {}),
    ...((rest.headers as Record<string, string>) ?? {}),
  };
  if (token) headers['Authorization'] = `Bearer ${token}`;

  const res = await fetch(`${BASE_URL}${path}`, { ...rest, headers });

  if (res.status === 401) {
    const body = await res.json().catch(() => ({}));
    const message =
      (body as { error?: { message?: string } })?.error?.message ??
      'Unauthorized: Valid Admin Token required';
    // 401 dari ganti password = password lama salah, bukan sesi mati.
    const credentialMismatch = /incorrect current password/i.test(message);
    if (!credentialMismatch && !tolerateUnauthorized) handleSessionInvalid();
    throw new ApiError(401, credentialMismatch ? message : 'Unauthorized: Valid Admin Token required');
  }
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    const message =
      (body as { error?: { message?: string }; message?: string })?.error?.message ??
      (body as { message?: string })?.message ??
      `Request failed: ${res.statusText}`;
    throw new ApiError(res.status, message);
  }
  return res.json() as Promise<T>;
}

// ================= RAW ENDPOINTS (kontrak identik app lama) =================

export async function fetchSettings(): Promise<SettingsDTO> {
  return request<SettingsDTO>('/api/settings');
}

export async function saveSettings(settings: SettingsDTO): Promise<{ status: string; message?: string }> {
  return request('/api/settings', { method: 'PUT', body: JSON.stringify(settings) });
}

export async function fetchHealth(): Promise<HealthStatus> {
  const res = await fetch(`${BASE_URL}/healthz`);
  if (res.status === 404) return { status: 'error', timestamp: Date.now() };
  if (!res.ok) return { status: 'error', timestamp: Date.now() };
  const text = await res.text();
  return { status: text.includes('ok') ? 'ok' : 'degraded', timestamp: Date.now() };
}

export async function loginApi(password: string): Promise<{ status: string; token: string; expires_at: string }> {
  const res = await fetch(`${BASE_URL}/api/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify({ password }),
  });
  if (res.status === 401) throw new ApiError(401, 'Incorrect dashboard access password');
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new ApiError(res.status, (body as { error?: { message?: string } })?.error?.message ?? 'Authentication failed');
  }
  return res.json();
}

export async function verifyAuthApi(token: string): Promise<{ status: string; authenticated: boolean }> {
  if (!token) return { status: 'unauthenticated', authenticated: false };
  return request('/api/auth/verify', { token });
}

export async function logoutApi(token: string): Promise<{ status: string }> {
  return request('/api/auth/logout', { method: 'POST', token, tolerateUnauthorized: true });
}

export async function updatePasswordApi(
  currentPassword: string,
  newPassword: string,
  token: string,
): Promise<{ status: string; message?: string }> {
  return request('/api/auth/password', {
    method: 'PUT',
    token,
    body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }),
  });
}

export async function updateBreakerApi(name: string, state: 'OPEN' | 'CLOSED' | 'HALF-OPEN') {
  return request('/api/breakers', { method: 'PUT', body: JSON.stringify({ name, state }) });
}

export async function deleteHistoryApi() {
  return request<{ status: string }>('/api/history', { method: 'DELETE' });
}

export async function topupTenant(req: TenantTopupRequestDTO): Promise<TenantTopupResponseDTO> {
  return request('/api/tenants/topup', { method: 'POST', body: JSON.stringify(req) });
}

export async function fetchTelemetry(): Promise<TelemetryDTO> {
  return request<TelemetryDTO>('/api/telemetry');
}

export async function fetchWarpStatus(): Promise<WarpStatusDTO> {
  return request<WarpStatusDTO>('/api/warp/status');
}

export async function rotateWarp() {
  return request<{ status: string }>('/api/warp/rotate', { method: 'POST' });
}

export async function fetchOAuthProviders(): Promise<ProviderInfoDTO[]> {
  return request<ProviderInfoDTO[]>('/api/oauth/providers');
}

export async function fetchOAuthConnections(): Promise<ConnectionDTO[]> {
  return request<ConnectionDTO[]>('/api/oauth/connections');
}

export async function initiateOAuthAuthorize(payload: AuthorizeRequestDTO): Promise<AuthorizeResponseDTO> {
  return request('/api/oauth/authorize', { method: 'POST', body: JSON.stringify(payload) });
}

export async function pollOAuthStatus(payload: PollRequestDTO): Promise<PollResponseDTO> {
  return request('/api/oauth/poll', { method: 'POST', body: JSON.stringify(payload) });
}

export async function deleteOAuthConnection(id: string) {
  return request(`/api/oauth/connections/${encodeURIComponent(id)}`, { method: 'DELETE' });
}

// ---- Operator provider CRUD (`/api/providers*`, machine surface) ----

export async function fetchProviders(): Promise<ProviderListResponse> {
  return request<ProviderListResponse>('/api/providers');
}
export async function createProvider(payload: { name: string; base_url: string; description?: string; is_active?: boolean }): Promise<ProviderRecordDTO> {
  return request<ProviderRecordDTO>('/api/providers', { method: 'POST', body: JSON.stringify(payload) });
}

export async function updateProvider(id: number, payload: { base_url?: string; description?: string; is_active?: boolean }): Promise<ProviderRecordDTO> {
  return request<ProviderRecordDTO>(`/api/providers/${id}`, { method: 'PUT', body: JSON.stringify(payload) });
}

export async function deleteProvider(id: number): Promise<{ ok: boolean }> {
  return request<{ ok: boolean }>(`/api/providers/${id}`, { method: 'DELETE' });
}


export async function fetchProviderKeys(providerId: number): Promise<ProviderKeyListResponse> {
  return request<ProviderKeyListResponse>(`/api/providers/${providerId}/keys`);
}

export async function upsertProviderKeys(providerId: number, entries: ProviderKeyUpsertEntry[]): Promise<ProviderKeyUpsertResponse> {
  return request(`/api/providers/${providerId}/keys`, { method: 'POST', body: JSON.stringify({ entries }) });
}

export async function patchProviderKey(keyId: number, patch: KeyPatchRequest): Promise<KeyPatchResponse> {
  return request(`/api/keys/${keyId}`, { method: 'PATCH', body: JSON.stringify(patch) });
}

export async function deleteProviderKey(keyId: number): Promise<KeyDeleteResponse> {
  return request(`/api/keys/${keyId}`, { method: 'DELETE' });
}

// ---- Upstream probe & model discovery ----

export interface UpstreamCheckRequest {
  name?: string;
  key_ref?: string;
  protocol: string;
  base_url: string;
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

export interface UpstreamModelsRequest {
  name?: string;
  key_ref?: string;
  protocol: string;
  base_url: string;
  api_key?: string;
  timeout_ms?: number;
  egress_mode?: string;
  proxy_url?: string;
}

export interface UpstreamModelsResponse {
  models: string[];
  model_count: number;
  latency_ms: number;
  message?: string;
  key_ref?: string;
}

export async function checkUpstreamHealth(
  req: UpstreamCheckRequest,
  signal?: AbortSignal,
): Promise<UpstreamCheckResponse> {
  return request('/api/upstreams/check', { method: 'POST', body: JSON.stringify(req), signal });
}

export async function fetchUpstreamModels(
  req: UpstreamModelsRequest,
  signal?: AbortSignal,
): Promise<UpstreamModelsResponse> {
  return request('/api/upstreams/models', { method: 'POST', body: JSON.stringify(req), signal });
}

// ---- Turso reads (hint-only, secret tidak pernah ke browser) ----

export async function fetchTursoProviders(): Promise<TursoProvidersResponse> {
  return request<TursoProvidersResponse>('/api/turso/providers');
}

export async function fetchTursoProviderKeyHints(providerId?: number): Promise<TursoKeyHintsResponse> {
  const path = providerId ? `/api/turso/providers/${providerId}/keys` : '/api/turso/keys';
  return request<TursoKeyHintsResponse>(path);
}

export async function testTursoConnection(payload: TursoDTO): Promise<{ ok: boolean; message?: string }> {
  return request('/api/turso/test', { method: 'POST', body: JSON.stringify(payload) });
}

// ---- Chat SSE tester (data plane, bukan admin) ----

export interface ChatChunk {
  text: string;
  /** Inter-arrival time in ms — untuk waterfall. */
  deltaMs: number;
}

/** Stream chat via /v1/chat/completions (SSE). onChunk per token delta. */
export async function streamChat(
  model: string,
  messages: Array<{ role: string; content: string }>,
  onChunk: (c: ChatChunk) => void,
  onEvent: (raw: string) => void,
  signal?: AbortSignal,
  opts?: { apiKey?: string; temperature?: number; maxTokens?: number },
): Promise<void> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    Accept: 'text/event-stream',
  };
  if (opts?.apiKey) headers['Authorization'] = `Bearer ${opts.apiKey}`;

  const res = await fetch(`${BASE_URL}/v1/chat/completions`, {
    method: 'POST',
    headers,
    cache: 'no-store',
    body: JSON.stringify({
      model,
      messages,
      stream: true,
      ...(opts?.temperature !== undefined ? { temperature: opts.temperature } : {}),
      ...(opts?.maxTokens !== undefined ? { max_tokens: opts.maxTokens } : {}),
    }),
    signal,
  });
  if (!res.ok || !res.body) {
    const errBody = await res.json().catch(() => null);
    const message =
      (errBody as { error?: { message?: string }; message?: string })?.error?.message ??
      (errBody as { message?: string })?.message ??
      `Chat failed: HTTP ${res.status} ${res.statusText}`;
    throw new ApiError(res.status, message);
  }

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  let last = performance.now();

  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    const lines = buffer.split('\n');
    buffer = lines.pop() ?? '';
    for (const line of lines) {
      const trimmed = line.trim();
      if (!trimmed.startsWith('data:')) continue;
      const payload = trimmed.slice(5).trim();
      onEvent(payload);
      if (payload === '[DONE]') return;
      try {
        const parsed = JSON.parse(payload) as {
          choices?: Array<{ delta?: { content?: string } }>;
        };
        const text = parsed.choices?.[0]?.delta?.content ?? '';
        if (text) {
          const now = performance.now();
          onChunk({ text, deltaMs: now - last });
          last = now;
        }
      } catch {
        // Ignore malformed payloads
      }
    }
  }
}

// ================= QUERY HOOKS (dengan mock fallback transport) =================

/**
 * Bungkus fetcher dengan fallback mock saat gateway tidak terjangkau.
 * ApiError (HTTP nyata) TIDAK difallback — tampil sebagai error state.
 */
function fallbackQuery<T extends object>(
  queryKey: unknown[],
  fetcher: () => Promise<T>,
  mock: T,
  refetchInterval?: number,
  staleTime?: number,
) {
  return useQuery({
    queryKey,
    queryFn: async (): Promise<T & { __mock?: boolean }> => {
      try {
        if (!(await hasGateway())) return { ...mock, __mock: true };
        const data = await fetcher();
        return Object.assign({}, data, { __mock: false });
      } catch (err) {
        if (isNetworkError(err)) return { ...mock, __mock: true };
        throw err;
      }
    },
    refetchInterval,
    staleTime,
  });
}

export function useHealthQuery() {
  return fallbackQuery(['healthz'], fetchHealth, { status: 'ok', timestamp: Date.now() }, 3000);
}

export function useTelemetryQuery() {
  const { telemetryMock } = mocks();
  return fallbackQuery(['telemetry'], fetchTelemetry, telemetryMock, 2000, 1000);
}

export function useSettingsQuery() {
  const { settingsMock } = mocks();
  return fallbackQuery(['settings'], fetchSettings, settingsMock, 5000);
}

export function useWarpStatusQuery() {
  const { warpMock } = mocks();
  return fallbackQuery(['warp'], fetchWarpStatus, warpMock, 5000);
}

export function useOAuthProvidersQuery() {
  return fallbackQuery(['oauth', 'providers'], fetchOAuthProviders, [], undefined, 60000);
}

export function useOAuthConnectionsQuery() {
  return fallbackQuery(['oauth', 'connections'], fetchOAuthConnections, [], 15000);
}

export function useProvidersQuery() {
  return fallbackQuery(['providers'], fetchProviders, mocks().providersMock, 5000);
}

export function useProviderKeysQuery(providerId: number | null) {
  return fallbackQuery(
    ['providers', providerId, 'keys'],
    () => fetchProviderKeys(providerId as number),
    mocks().keysMock,
    undefined,
    5000,
  );
}

// Mutations
export function useSaveSettingsMutation() {
  return useMutation({
    mutationFn: saveSettings,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['settings'] });
    },
  });
}

/**
 * Save settings dengan perilaku jujur di dua mode:
 * - Real mode: PUT /api/settings asli + invalidate.
 * - Demo mode (origin tanpa gateway): apply ke cache lokal SAJA, dilabeli
 *   `local: true` agar UI menampilkan toast "perubahan hanya lokal".
 */
export function useSaveSettingsSmart() {
  return useMutation({
    mutationFn: async (next: SettingsDTO) => {
      if (!(await hasGateway())) {
        queryClient.setQueryData(['settings'], next);
        return { local: true as const };
      }
      await saveSettings(next);
      return { local: false as const };
    },
    onSuccess: (d) => {
      if (!d.local) void queryClient.invalidateQueries({ queryKey: ['settings'] });
    },
  });
}

/** Helper untuk pages: bangun SettingsDTO berikutnya dari list yang diubah. */
export function withSettings(
  current: SettingsDTO,
  patch: Partial<SettingsDTO>,
): SettingsDTO {
  return { ...current, ...patch };
}

export function useTopupTenantMutation() {
  return useMutation({
    mutationFn: topupTenant,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['settings'] }),
  });
}

export function useRotateWarpMutation() {
  return useMutation({
    mutationFn: rotateWarp,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['warp'] }),
  });
}

export function usePatchProviderKeyMutation() {
  return useMutation({
    mutationFn: (req: { keyId: number; patch: KeyPatchRequest }) => patchProviderKey(req.keyId, req.patch),
    onSuccess: (_d, req) => {
      void req;
      queryClient.invalidateQueries({ queryKey: ['providers'] });
    },
  });
}

export function useUpsertProviderKeysMutation() {
  return useMutation({
    mutationFn: (req: { providerId: number; entries: ProviderKeyUpsertEntry[] }) =>
      upsertProviderKeys(req.providerId, req.entries),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['providers'] }),
  });
}

export function useDeleteProviderKeyMutation() {
  return useMutation({
    mutationFn: deleteProviderKey,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['providers'] }),
  });
}

export function useCreateProviderMutation() {
  return useMutation({
    mutationFn: createProvider,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['providers'] }),
  });
}

export function useUpdateProviderMutation() {
  return useMutation({
    mutationFn: (req: { id: number; payload: Parameters<typeof updateProvider>[1] }) =>
      updateProvider(req.id, req.payload),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['providers'] }),
  });
}

export function useDeleteProviderMutation() {
  return useMutation({
    mutationFn: deleteProvider,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['providers'] }),
  });
}
export function useCheckUpstreamMutation() {
  return useMutation({
    mutationFn: (req: UpstreamCheckRequest) => checkUpstreamHealth(req),
  });
}

export function useUpstreamModelsMutation() {
  return useMutation({
    mutationFn: (req: UpstreamModelsRequest) => fetchUpstreamModels(req),
  });
}

export function useTursoProvidersQuery() {
  return useQuery({
    queryKey: ['turso', 'providers'],
    queryFn: fetchTursoProviders,
    staleTime: 30000,
  });
}

export function useTursoProviderKeyHintsQuery(providerId?: number) {
  return useQuery({
    queryKey: ['turso', 'keys', providerId],
    queryFn: () => fetchTursoProviderKeyHints(providerId),
    enabled: providerId !== undefined && providerId > 0,
    staleTime: 10000,
  });
}

export function useTestTursoMutation() {
  return useMutation({
    mutationFn: (payload: TursoDTO) => testTursoConnection(payload),
  });
}


// ================= MOCK PAYLOADS (bentuk DTO asli) =================

function mocks() {
  const now = Date.now();

  const settingsMock: SettingsDTO = {
    upstreams: [
      {
        name: 'openai-main',
        protocol: 'openai',
        base_url: 'https://api.openai.com',
        key_strategy: 'least_inflight',
        credential_pool: Array.from({ length: 12 }, (_, i) => ({ ref: `openai-cred-${i + 1}` })),
        enabled: true,
      },
      {
        name: 'anthropic-prod',
        protocol: 'anthropic',
        base_url: 'https://api.anthropic.com',
        key_strategy: 'least_inflight',
        credential_pool: Array.from({ length: 4 }, (_, i) => ({ ref: `anthropic-cred-${i + 1}` })),
        enabled: true,
      },
      {
        name: 'grok-build',
        protocol: 'grok-cli',
        base_url: 'https://cli-chat-proxy.grok.com',
        key_strategy: 'round_robin',
        credential_pool: [{ ref: 'grok-cred-1' }, { ref: 'grok-cred-2' }],
        enabled: true,
      },
    ],
    models: [
      { public_name: 'gpt-4o', upstream: 'openai-main', upstream_model: 'gpt-4o-2024-11-20', enabled: true },
      { public_name: 'gpt-4o-mini', upstream: 'openai-main', upstream_model: 'gpt-4o-mini-2024-07-18', enabled: true },
      { public_name: 'claude-sonnet-4-5', upstream: 'antigravity-prod', upstream_model: 'claude-sonnet-4-5', enabled: true },
      { public_name: 'claude-opus-4-1', upstream: 'anthropic-prod', upstream_model: 'claude-opus-4-1-20250805', enabled: true },
      { public_name: 'grok-4', upstream: 'grok-build', upstream_model: 'grok-4-latest', enabled: true },
      { public_name: 'text-embedding-3-small', upstream: 'openai-main', upstream_model: 'text-embedding-3-small', enabled: true },
    ],
    combos: [
      { name: 'coding-stack', strategy: 'failover', models: ['claude-sonnet-4-5', 'gpt-4o', 'gemini-2.5-pro'], enabled: true },
      { name: 'cheap-batch', strategy: 'least_inflight', models: ['gpt-4o-mini', 'gemini-2.0-flash'], enabled: true },
      { name: 'reasoner', strategy: 'round_robin', models: ['claude-opus-4-1', 'grok-4'], enabled: true },
    ],
    tenants: [
      {
        name: 'personal',
        api_key: 'sk-gw-000000000000000000000000000000000000e410',
        status: 'active',
        allowed_models: Array.from({ length: 12 }, (_, i) => `m${i}`),
        rate_limit: { rps: 10, max_concurrent: 8 },
      },
      {
        name: 'work',
        api_key: 'sk-gw-00000000000000000000000000000000000077ba',
        status: 'active',
        allowed_models: Array.from({ length: 21 }, (_, i) => `m${i}`),
        rate_limit: { rps: 30, max_concurrent: 24 },
      },
      {
        name: 'agent-ops',
        api_key: 'sk-gw-000000000000000000000000000000000000192c',
        status: 'active',
        allowed_models: Array.from({ length: 21 }, (_, i) => `m${i}`),
        rate_limit: { rps: 60, max_concurrent: 48 },
      },
      {
        name: 'guest-demo',
        api_key: 'sk-gw-00000000000000000000000000000000000002af',
        status: 'suspended',
        allowed_models: ['gpt-4o-mini', 'text-embedding-3-small'],
        rate_limit: { rps: 2, max_concurrent: 2 },
      },
    ],
    manage_models: true,
    manage_upstreams: true,
    manage_combos: true,
    manage_tenants: true,
    token_saver: {
      enabled: true,
      compress_tool_output: true,
      terse_output: false,
      minimal_code: false,
      compress_context: true,
    },
  };

  const telemetryMock: TelemetryDTO = {
    timestamp: now,
    generation: 1,
    global_admission: { inflight: 42, capacity: 1500, queue_depth: 0 },
    summary: {
      active_streams: 42,
      total_requests: 16996,
      total_errors: 68,
      circuit_trips: 1,
      error_rate_pct: 0.4,
      p50_latency_ms: 182,
      p90_latency_ms: 330,
      p95_latency_ms: 412,
      p99_latency_ms: 687,
      input_tokens: 38_700_000,
      output_tokens: 10_600_000,
      total_tokens: 49_300_000,
      estimated_cost_usd: 61.25,
    },
    models: [
      { model: 'claude-sonnet-4-5', upstream: 'antigravity-prod', enabled: true, requests: 9625, errors: 12, p50_ms: 204, p90_ms: 388, p99_ms: 712 },
      { model: 'gpt-4o', upstream: 'openai-main', enabled: true, requests: 3872, errors: 22, p50_ms: 168, p90_ms: 340, p99_ms: 620 },
      { model: 'grok-4', upstream: 'grok-build', enabled: true, requests: 912, errors: 30, p50_ms: 312, p90_ms: 610, p99_ms: 924 },
      { model: 'gpt-4o-mini', upstream: 'openai-main', enabled: true, requests: 587, errors: 2, p50_ms: 122, p90_ms: 200, p99_ms: 380 },
      { model: 'text-embedding-3-small', upstream: 'openai-main', enabled: true, requests: 2140, errors: 2, p50_ms: 38, p90_ms: 60, p99_ms: 96 },
    ],
    upstreams: [
      {
        name: 'openai-main',
        protocol: 'openai',
        base_url: 'https://api.openai.com',
        breaker_state: 'CLOSED',
        total_requests: 6599,
        slots: [
          { ref: 'openai-key-1', inflight: 2, is_cooldown: false, cooldown_remaining_sec: 0, is_revoked: false, total_cooldown_events: 3, requests_total: 2100 },
          { ref: 'openai-key-2', inflight: 0, is_cooldown: true, cooldown_remaining_sec: 22, is_revoked: false, total_cooldown_events: 11, requests_total: 1840 },
        ],
      },
      {
        name: 'anthropic-prod',
        protocol: 'anthropic',
        base_url: 'https://api.anthropic.com',
        breaker_state: 'CLOSED',
        total_requests: 10829,
        slots: [
          { ref: 'anthropic-key-1', inflight: 4, is_cooldown: false, cooldown_remaining_sec: 0, is_revoked: false, total_cooldown_events: 1, requests_total: 6100 },
        ],
      },
      {
        name: 'grok-build',
        protocol: 'grok-cli',
        base_url: 'https://cli-chat-proxy.grok.com',
        breaker_state: 'HALF-OPEN',
        total_requests: 912,
        slots: [
          { ref: 'grok-key-1', inflight: 0, is_cooldown: true, cooldown_remaining_sec: 47, is_revoked: false, total_cooldown_events: 8, requests_total: 700 },
          { ref: 'grok-key-2', inflight: 0, is_cooldown: false, cooldown_remaining_sec: 0, is_revoked: true, total_cooldown_events: 0, requests_total: 0 },
        ],
      },
    ],
    tenants_usage: [
      { tenant: 'agent-ops', model: 'claude-sonnet-4-5', credential_ref: 'openai-key-1', total_requests: 8421 },
      { tenant: 'work', model: 'gpt-4o', credential_ref: 'openai-key-1', total_requests: 3872 },
      { tenant: 'personal', model: 'claude-sonnet-4-5', credential_ref: 'anthropic-key-1', total_requests: 1204 },
      { tenant: 'agent-ops', model: 'grok-4', credential_ref: 'grok-key-1', total_requests: 912 },
      { tenant: 'guest-demo', model: 'gpt-4o-mini', credential_ref: 'openai-key-2', total_requests: 87 },
    ],
    recent_logs: [
      { id: 'a1', timestamp: now - 12_000, method: 'POST', path: '/v1/chat/completions', status: 200, durationMs: 412, model: 'claude-sonnet-4-5', upstream: 'antigravity-prod', tenant: 'agent-ops', stream: true, tokensIn: 1204, tokensOut: 890 },
      { id: 'a2', timestamp: now - 15_000, method: 'POST', path: '/v1/chat/completions', status: 200, durationMs: 287, model: 'gpt-4o', upstream: 'openai-main', tenant: 'work', stream: true, tokensIn: 512, tokensOut: 340 },
      { id: 'a3', timestamp: now - 26_000, method: 'POST', path: '/v1/messages', status: 200, durationMs: 1200, model: 'claude-opus-4-1', upstream: 'anthropic-prod', tenant: 'personal', stream: true, tokensIn: 2210, tokensOut: 1120 },
      { id: 'a4', timestamp: now - 40_000, method: 'POST', path: '/v1/embeddings', status: 200, durationMs: 43, model: 'text-embedding-3-small', upstream: 'openai-main', tenant: 'work', stream: false, tokensIn: 88 },
      { id: 'a5', timestamp: now - 53_000, method: 'POST', path: '/v1/chat/completions', status: 200, durationMs: 640, model: 'grok-4', upstream: 'grok-build', tenant: 'agent-ops', stream: true, tokensIn: 640, tokensOut: 410 },
      { id: 'a6', timestamp: now - 61_000, method: 'POST', path: '/v1/chat/completions', status: 429, durationMs: 38, model: 'gpt-4o', upstream: 'openai-main', tenant: 'work', stream: false, error: 'rate limited' },
      { id: 'a7', timestamp: now - 68_000, method: 'POST', path: '/v1/chat/completions', status: 200, durationMs: 508, model: 'claude-sonnet-4-5', upstream: 'antigravity-prod', tenant: 'personal', stream: true, tokensIn: 320, tokensOut: 260 },
      { id: 'a8', timestamp: now - 84_000, method: 'POST', path: '/v1/chat/completions', status: 200, durationMs: 210, model: 'gpt-4o-mini', upstream: 'openai-main', tenant: 'guest-demo', stream: true, tokensIn: 90, tokensOut: 60 },
    ],
  };

  const warpMock: WarpStatusDTO = {
    enabled: true,
    public_ip: '',
    internal_ip: '172.16.0.2',
    colo: 'SIN',
    endpoint: 'engage.cloudflareclient.com:2408',
    latency_ms: 8.4,
    active_connections: 3,
    draining_sessions: 0,
    auto_rotate_interval_seconds: 1800,
    next_rotation_at: new Date(now + 22 * 60_000).toISOString(),
  };

  const providersMock: ProviderListResponse = {
    count: 4,
    storage: 'turso',
    read_only: false,
    providers: [
      { id: 1, name: 'openai', base_url: 'https://api.openai.com', is_active: true, active_keys: 12, created_at: 0, updated_at: 0 },
      { id: 2, name: 'anthropic', base_url: 'https://api.anthropic.com', is_active: true, active_keys: 4, created_at: 0, updated_at: 0 },
      { id: 3, name: 'grok', base_url: 'https://cli-chat-proxy.grok.com', is_active: true, active_keys: 1, created_at: 0, updated_at: 0 },
      { id: 4, name: 'opencode', base_url: 'https://opencode.ai/zen', is_active: true, active_keys: 2, created_at: 0, updated_at: 0 },
    ],
  };

  const keysMock: ProviderKeyListResponse = {
    provider_id: 1,
    count: 2,
    keys: [
      { id: 1, provider_id: 1, status: 'active', is_active: true, last_used_at: now - 120_000, total_requests: 2100, created_at: 0, updated_at: 0, api_key_hint: 'sk-••••7f2a' },
      { id: 2, provider_id: 1, status: 'deactivated', is_active: false, last_used_at: now - 300_000, total_requests: 940, created_at: 0, updated_at: 0, api_key_hint: 'sk-••••a91c' },
    ],
  };

  return { settingsMock, telemetryMock, warpMock, providersMock, keysMock };
}

export type { LiveConnectionLog };
