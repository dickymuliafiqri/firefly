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
  | 'codebuddy_intl'
  | 'grok-cli'
  | 'opencode'
  | 'opencode-go'
  | 'qoder';
export type KeyStrategy = 'round_robin' | 'least_inflight';
export type BreakerState = 'CLOSED' | 'OPEN' | 'HALF-OPEN';
export type TenantStatus = 'active' | 'suspended' | 'revoked' | 'exhausted' | 'expired';

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

export interface KeyErrorRuleDTO {
  status_code: number;
  threshold: number;
  action: 'deactivate' | 'delete' | 'cooldown' | string;
  cooldown_duration_s?: number;
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
  key_error_rules?: KeyErrorRuleDTO[] | null;
  probe_model?: string;
  egress_mode?: 'direct' | 'warp' | 'proxy' | string;
  proxy_url?: string;
  enabled?: boolean | null;
}

export interface WarpStatusDTO {
  enabled: boolean;
  /** Egress address reported by the Cloudflare edge; empty until probed. */
  public_ip?: string;
  /** WARP-assigned tunnel address (172.16/12), never a public IP. */
  internal_ip?: string;
  colo?: string;
  endpoint?: string;
  latency_ms?: number;
  active_connections?: number;
  draining_sessions?: number;
  last_rotated_at?: string;
  /** Periodic auto-rotation schedule in seconds (e.g. 300 for 5m). 0 = disabled. */
  auto_rotate_interval_seconds?: number;
  /** When the next scheduled auto-rotation is expected. */
  next_rotation_at?: string;
  error?: string;
}

export interface UpstreamModelsRequest {
  name?: string;
  key_ref?: string;
  protocol?: Protocol | string;
  base_url?: string;
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
  max_tokens?: number;
  used_tokens?: number;
  expires_at?: number | null;
}

export interface TenantTopupRequestDTO {
  api_key?: string;
  tenant_name?: string;
  add_tokens?: number;
  extend_days?: number;
  reset_used?: boolean;
}

/** Mirrors `server.TenantTopupResponse`, which is a flat object — there is no nested tenant. */
export interface TenantTopupResponseDTO {
  success: boolean;
  message: string;
  name: string;
  api_key?: string;
  max_tokens: number;
  used_tokens: number;
  remaining_tokens: number;
  expires_at?: number | null;
  status: string;
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

/** Provider key as exposed by the legacy /api/turso read surface: the secret is
 *  masked, so the dashboard can reconcile by id but never holds key material. */
export interface TursoKeyHintDTO {
  id: number;
  provider_id: number;
  api_key_hint: string;
  status: string;
  is_active: boolean;
}

export interface TursoKeyHintsResponse {
  ok: boolean;
  provider_id?: number;
  count: number;
  keys: TursoKeyHintDTO[];
}

/**
 * Operator provider surface (`/api/providers*`). Mirrors `turso.ProviderRecord`:
 * the row plus the count of keys routable right now.
 */
export interface ProviderRecordDTO {
  id: number;
  name: string;
  base_url: string;
  description?: string;
  is_active: boolean;
  active_keys: number;
  created_at: number;
  updated_at: number;
}

export interface ProviderListResponse {
  count: number;
  providers: ProviderRecordDTO[];
  /** Where the rows come from: the Turso catalog, or the read-only projection of
   *  the running configuration. */
  storage?: 'file' | 'turso';
  /** True when the rows are projected from the live snapshot (file-config mode).
   *  There is no row to edit — credentials are declared in upstreams.json and the
   *  environment — so the surface is shown without any mutation affordance. */
  read_only?: boolean;
}

export interface ProviderMutationResponse {
  provider: ProviderRecordDTO;
  /** Whether the routing snapshot was rebuilt before the response was written.
   *  `false` is not an error: the write committed and bumped the catalog
   *  revision, so the periodic syncer publishes it on its next tick. */
  reloaded: boolean;
}

export interface ProviderDeleteResponse {
  status: string;
  id: number;
  deleted_keys: number;
  reloaded: boolean;
}

export type ProviderKeyStatus = 'active' | 'deactivated' | 'expired' | 'revoked';

/** The statuses an operator may set. Unknown statuses are accepted by the
 *  machine sync path but stay inert, so the picker must not invent one. */
export const PROVIDER_KEY_STATUSES: ProviderKeyStatus[] = [
  'active',
  'deactivated',
  'expired',
  'revoked',
];

/**
 * One stored credential as the operator surface returns it: the row plus a
 * masked `api_key_hint`. The secret is never returned, and `account_metadata`
 * (a vault blob) is write-only.
 */
export interface ProviderKeyRecordDTO {
  id: number;
  provider_id: number;
  status: ProviderKeyStatus | string;
  is_active: boolean;
  expires_at?: number | null;
  last_used_at: number;
  total_requests: number;
  created_at: number;
  updated_at: number;
  api_key_hint: string;
}

export interface ProviderKeyListResponse {
  provider_id: number;
  count: number;
  keys: ProviderKeyRecordDTO[];
}

/** One entry of a key batch. `api_key` must carry the real secret: this surface
 *  only ever displays hints, so a hint-shaped value is refused by the server. */
export interface ProviderKeyUpsertEntry {
  api_key: string;
  status?: ProviderKeyStatus | string;
  /** Unix milliseconds. Omit to keep the stored expiry, `null` to clear it. */
  expires_at?: number | null;
  account_metadata?: Record<string, unknown>;
}

export interface ProviderKeyUpsertResponse {
  created: number;
  updated: number;
  unchanged: number;
  reassigned: number;
  key_ids: number[];
  revision: number;
  pushed: boolean;
  reloaded: boolean;
}

export interface KeyPatchRequest {
  /** Rotates the credential in place; the key keeps its id, so bound
   *  `<upstream>-key-<id>` refs survive. */
  api_key?: string;
  status?: ProviderKeyStatus | string;
  is_active?: boolean;
  /** Unix milliseconds, or `null` to clear the stored expiry. */
  expires_at?: number | null;
}

export interface KeyPatchResponse {
  key: ProviderKeyRecordDTO;
  revision: number;
  pushed: boolean;
  reloaded: boolean;
}

export interface KeyDeleteResponse {
  status: string;
  id: number;
  revision: number;
  pushed: boolean;
  reloaded: boolean;
}

export interface TokenSaverDTO {
  enabled: boolean;
  compress_tool_output: boolean;
  terse_output: boolean;
  minimal_code: boolean;
  compress_context: boolean;
  max_tool_output_chars?: number | null;
  context_threshold?: number | null;
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
  token_saver?: TokenSaverDTO;
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
  keyRef?: string;
  tenant: string;
  stream: boolean;
  tokensIn?: number;
  tokensOut?: number;
  tokens?: number;
  estimatedCost?: number;
  error?: string;
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
