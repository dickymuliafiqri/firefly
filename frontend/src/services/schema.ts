/**
 * schema.ts — Go Backend DTO & API Schema definitions
 * Matches backend Go types in `internal/config/dto.go` and `internal/server/settings.go`.
 */

export type Protocol =
  | 'openai'
  | 'anthropic'
  | 'antigravity'
  | 'cline'
  | 'codebuddy_cn'
  | 'codebuddy_intl';
export type KeyStrategy = 'round_robin' | 'least_inflight';
export type BreakerState = 'CLOSED' | 'OPEN' | 'HALF-OPEN';
export type TenantStatus = 'active' | 'suspended' | 'revoked';

export interface ProviderInfoDTO {
  id: string;
  name: string;
  flow_type: string;
  is_sensitive?: boolean;
}

export interface AuthorizeRequestDTO {
  provider: string;
  redirect_uri?: string;
}

export interface AuthorizeResponseDTO {
  session_id: string;
  auth_url: string;
  state: string;
}

export interface CallbackRequestDTO {
  state: string;
  code: string;
}

export interface PollRequestDTO {
  state: string;
  device_code?: string;
}

export interface ConnectionDTO {
  id: string;
  provider: string;
  email?: string;
  expires_at?: string;
  is_expired: boolean;
  provider_specific_data?: Record<string, string>;
  created_at: string;
  updated_at: string;
}

export interface PollResponseDTO {
  status: string;
  pending: boolean;
  connection?: ConnectionDTO;
}

export interface CredentialKeyDTO {
  ref?: string;
  api_key?: string;
  secret?: string;
  rps?: number | null;
  max_concurrent?: number | null;
}

export interface UpstreamDTO {
  name: string;
  protocol?: Protocol | string;
  base_url?: string;
  base_urls?: string[];
  provider_id?: number | null;
  api_key?: string;
  api_keys?: string[];
  credential_ref?: string;
  key_strategy?: KeyStrategy | string;
  credential_pool?: CredentialKeyDTO[];
  timeout_ms?: number | null;
  idle_timeout_ms?: number | null;
  stream_idle_timeout_ms?: number | null;
  max_idle_conns_per_host?: number | null;
  max_conns_per_host?: number | null;
  extra_headers?: Record<string, string>;
  allow_insecure?: boolean | null;
  credential_rps?: number | null;
  credential_max_concurrent?: number | null;
  key_error_threshold?: number | null;
  key_error_action?: 'deactivate' | 'delete' | 'cooldown' | string | null;
  key_cooldown_duration_ms?: number | null;
  enabled?: boolean | null;
}

export interface CapabilitiesDTO {
  stream?: boolean;
  tools?: boolean;
  vision?: boolean;
  json_mode?: boolean;
  embeddings?: boolean;
  audio?: boolean;
}

export interface ModelDTO {
  public_name: string;
  upstream: string;
  fallback_upstreams?: string[];
  upstream_model: string;
  capabilities?: CapabilitiesDTO | null;
  max_context?: number | null;
  enabled?: boolean | null;
}

export interface RateLimitDTO {
  rps?: number | null;
  burst?: number | null;
  max_concurrent?: number | null;
}

export interface TenantDTO {
  key_hash?: string;
  api_key?: string;
  name: string;
  status?: TenantStatus | string;
  allowed_models?: string[];
  credential_ref?: string;
  rate_limit?: RateLimitDTO | null;
  metadata?: Record<string, string>;
}

export interface ComboDTO {
  name: string;
  strategy?: 'round_robin' | 'least_inflight' | 'failover' | string;
  models: string[];
  enabled?: boolean | null;
}

export interface AutoTLSDTO {
  enabled: boolean;
  domain?: string;
  email?: string;
}

export interface TursoDTO {
  database_url?: string;
  auth_token?: string;
  local_path?: string;
  sync_interval_sec?: number;
}

export interface TursoProviderDTO {
  id: number;
  name: string;
  base_url: string;
  description?: string;
  active_keys: number;
}

export interface TursoProvidersResponse {
  configured: boolean;
  providers: TursoProviderDTO[];
}

export interface TursoKeyDTO {
  id: number;
  provider_id: number;
  api_key: string;
  status: string;
  is_active: boolean;
}

export interface TursoKeysResponse {
  ok: boolean;
  provider_id?: number;
  count: number;
  keys: TursoKeyDTO[];
}

export interface SettingsDTO {
  upstreams: UpstreamDTO[];
  models: ModelDTO[];
  tenants: TenantDTO[];
  combos?: ComboDTO[];
  /** Marks the models list as authoritative so the backend may delete absent
   *  models, including deleting the last one (empty list). */
  manage_models?: boolean;
  /** Marks the upstreams list as authoritative so the backend may delete absent
   *  upstreams, including the last one (empty list). */
  manage_upstreams?: boolean;
  /** Marks the combos list as authoritative so the backend may delete absent
   *  combos, including the last one (empty list). */
  manage_combos?: boolean;
  /** Marks the tenants list as authoritative so the backend may delete absent
   *  tenants, including the last one (empty list). */
  manage_tenants?: boolean;
  auto_tls?: AutoTLSDTO;
  storage_engine?: string;
  turso?: TursoDTO;
}

export interface HealthStatus {
  status: 'ok' | 'degraded' | 'error';
  version?: string;
  uptime?: string;
  timestamp?: number;
}

export interface LiveConnectionLog {
  id: string;
  timestamp: number;
  method: string;
  path: string;
  status: number;
  durationMs: number;
  model: string;
  upstream: string;
  tenant: string;
  stream: boolean;
  tokensIn?: number;
  tokensOut?: number;
  tokens?: number;
  estimatedCost?: number;
  error?: string;
}

export interface TelemetrySnapshot {
  activeStreams: number;
  totalRequests: number;
  requestsPerSecond: number;
  p95LatencyMs: number;
  errorRate5xx: number;
  breakerStates: Record<string, BreakerState>;
}

export interface GlobalAdmissionDTO {
  inflight: number;
  capacity: number;
  queue_depth: number;
}

export interface TelemetrySummaryDTO {
  active_streams: number;
  total_requests: number;
  total_errors: number;
  circuit_trips: number;
  error_rate_pct: number;
  p50_latency_ms: number;
  p90_latency_ms: number;
  p95_latency_ms: number;
  p99_latency_ms: number;
  input_tokens?: number;
  output_tokens?: number;
  total_tokens?: number;
  estimated_cost_usd?: number;
}

export interface ModelTelemetryDTO {
  model: string;
  upstream: string;
  enabled: boolean;
  requests: number;
  errors: number;
  p50_ms: number;
  p90_ms: number;
  p99_ms: number;
}

export interface KeySlotTelemetryDTO {
  ref: string;
  inflight: number;
  is_cooldown: boolean;
  cooldown_remaining_sec: number;
  is_revoked: boolean;
  total_cooldown_events: number;
  requests_total: number;
}

export interface UpstreamTelemetryDTO {
  name: string;
  protocol: string;
  base_url: string;
  breaker_state: BreakerState | string;
  total_requests: number;
  slots: KeySlotTelemetryDTO[];
}

export interface TenantUsageDTO {
  tenant: string;
  model: string;
  credential_ref: string;
  total_requests: number;
}

export interface TelemetryDTO {
  timestamp: number;
  global_admission: GlobalAdmissionDTO;
  summary: TelemetrySummaryDTO;
  models: ModelTelemetryDTO[];
  upstreams: UpstreamTelemetryDTO[];
  tenants_usage: TenantUsageDTO[];
  generation: number;
  recent_logs?: LiveConnectionLog[];
}

export interface OpenAIErrorResponse {
  error: {
    message: string;
    type: string;
    param?: string | null;
    code?: string | number | null;
  };
}

/**
 * Safely parses any API error into a human-readable message.
 */
export function parseApiError(err: unknown): string {
  if (!err) return 'Unknown error';
  if (typeof err === 'string') return err;
  if (err instanceof Error) return err.message;
  
  if (typeof err === 'object' && err !== null) {
    const errorObj = err as Record<string, unknown>;
    if ('error' in errorObj && typeof errorObj.error === 'object' && errorObj.error !== null) {
      const nested = errorObj.error as Record<string, unknown>;
      if (typeof nested.message === 'string') {
        return nested.message;
      }
    }
    if ('message' in errorObj && typeof errorObj.message === 'string') {
      return errorObj.message;
    }
  }

  return 'Request failed';
}
