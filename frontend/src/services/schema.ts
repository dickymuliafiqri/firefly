/**
 * schema.ts — Go Backend DTO & API Schema definitions
 * Matches backend Go types in `internal/config/dto.go` and `internal/server/settings.go`.
 */

export type Protocol =
  | 'openai'
  | 'anthropic'
  | 'antigravity'
  | 'cline'
  | 'codebuddy-cn'
  | 'codebuddy-intl'
  | 'codebuddy'
  | 'codebuddy_cn'
  | 'codebuddy_intl'
  | 'grok-cli'
  | 'opencode'
  | 'opencode-go'
  | 'qoder';

/**
 * Maps protocol aliases and legacy identifiers to their canonical values.
 */
export function canonicalProtocol(proto: string | undefined | null): string {
  if (!proto) return 'openai';
  const p = proto.trim().toLowerCase();
  switch (p) {
    case 'codebuddy':
    case 'codebuddy_cn':
      return 'codebuddy-cn';
    case 'codebuddy_intl':
      return 'codebuddy-intl';
    case 'antigravity-go':
    case 'antigravity_go':
      return 'antigravity';
    case 'opencode-go':
    case 'opencode_go':
      return 'opencode';
    default:
      return p;
  }
}

/**
 * Mirrors the backend's `modelNameRe` (`internal/config/builder.go`) that gates
 * both `models[].public_name` and `combos[].name`: 1-128 characters, starting
 * with a letter or digit, then only letters, digits, `.`, `_`, or `-`.
 * The settings API rejects anything else with
 * `validation error: ...: invalid or empty name`, so the dashboard validates
 * before saving instead of surfacing the raw index-based gateway error.
 */
export const MODEL_NAME_RE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;

export const MODEL_NAME_HINT =
  'Letters, digits, dots, underscores and hyphens only; must start with a letter or digit (max 128 characters).';

export function isValidModelName(name: string): boolean {
  return MODEL_NAME_RE.test(name);
}

/**
 * Derives a backend-valid public name from an arbitrary upstream model id
 * (e.g. `anthropic/claude-3-5-sonnet`, `llama-3.3-70b:free`): invalid character
 * runs collapse to `-`, leading non-alphanumerics are stripped, and the result
 * is capped at 128 characters. Idempotent for already-valid names; returns ''
 * when nothing usable remains.
 */
export function sanitizeModelName(raw: string): string {
  return raw
    .trim()
    .replace(/[^A-Za-z0-9._-]+/g, '-')
    .replace(/^[^A-Za-z0-9]+/, '')
    .slice(0, 128);
}

/**
 * Minimal shape of a catalog model entry needed for identity checks. Kept
 * structural (not the full `ModelDTO`) so pure helpers stay usable from tests
 * and from callers that only carry a projection of the catalog.
 */
export interface CatalogModelLike {
  public_name: string;
  upstream: string;
  upstream_model?: string;
}

/**
 * Finds the catalog entry that already maps this upstream's model id.
 *
 * Identity is per-upstream: the same upstream model id (`gpt-4o`) may be
 * registered on several upstreams as distinct public names, so "is this model
 * already routed?" must be answered for *this* upstream, not for the whole
 * catalog. A legacy match on `public_name` is also accepted, because an entry
 * created before the upstream model id was tracked (or one registered by hand
 * under the id itself) is still this upstream's route.
 */
export function findRegisteredRoute(
  catalogModels: readonly CatalogModelLike[] = [],
  upstreamName: string,
  modelName: string,
): CatalogModelLike | undefined {
  return catalogModels.find(
    (m) =>
      m.upstream === upstreamName &&
      (m.upstream_model === modelName || m.public_name === modelName),
  );
}

/** Separator between the upstream namespace and the model id. `--` is valid in
 *  a public name (`MODEL_NAME_RE`) yet never appears in a real model id, so a
 *  namespaced name can never be mistaken for a raw one. */
const NAMESPACE_SEP = "--";

/**
 * Derives a globally-unique public name for `modelName` on `upstreamName`.
 *
 * The gateway keys the catalog by `public_name` (globally unique), while the
 * same provider model id legitimately exists on several upstreams. So the plain
 * sanitized id is preferred when free, and on collision the upstream name is
 * prefixed (`<upstream>--<model>`), with a numeric suffix (`-2`, `-3`, …) as a
 * last resort when even the namespaced form is taken. Deterministic: repeated
 * calls with the same catalog always yield the same name, so "Add all routes"
 * run twice cannot invent drifting suffixes.
 *
 * Returns '' when nothing nameable remains (mirrors `sanitizeModelName`).
 * Callers that must distinguish "already routed on this upstream" from "name
 * collision" should check `findRegisteredRoute` first.
 */
export function uniquePublicName(
  modelName: string,
  upstreamName: string,
  catalogModels: readonly CatalogModelLike[] = [],
): string {
  const taken = new Set(catalogModels.map((m) => m.public_name));
  const base = sanitizeModelName(modelName);
  if (!base) return "";
  if (!taken.has(base)) return base;

  const namespaced = namespaceName(upstreamName, modelName);
  if (namespaced && namespaced !== base && !taken.has(namespaced)) {
    return namespaced;
  }

  // Last resort: a numeric suffix on the best candidate we have. Bounded so a
  // pathological catalog can never spin forever.
  const stem = namespaced || base;
  for (let n = 2; n < 10_000; n++) {
    const suffix = `-${n}`;
    const candidate = `${stem.slice(0, 128 - suffix.length)}${suffix}`;
    if (!taken.has(candidate) && isValidModelName(candidate)) return candidate;
  }
  return stem;
}

/**
 * Builds `<upstream>--<model>` within the 128-char public-name budget. The
 * model part is truncated (not the upstream prefix) so a long provider id can
 * never swallow the namespace that makes the name unique.
 */
function namespaceName(upstreamName: string, modelName: string): string {
  const prefix = sanitizeModelName(upstreamName);
  if (!prefix) return "";
  const budget = 128 - prefix.length - NAMESPACE_SEP.length;
  if (budget <= 0) return prefix.slice(0, 128);
  const model = sanitizeModelName(modelName).slice(0, budget);
  if (!model) return prefix;
  return `${prefix}${NAMESPACE_SEP}${model}`;
}

/**
 * Scans a save payload for the first catalog entry the backend's name rules
 * would reject. Gives the dashboard a precise, named complaint instead of the
 * backend's index-based `models[0]` / `combos[0]` messages — even when the
 * offender is an untouched pre-existing entry rather than the one being edited.
 */
export function findInvalidCatalogName(
  models: Array<{ public_name: string }> = [],
  combos: Array<{ name: string }> = [],
): { kind: 'model' | 'combo'; name: string } | undefined {
  const badModel = models.find((m) => !isValidModelName(m.public_name));
  if (badModel) return { kind: 'model', name: badModel.public_name };
  const badCombo = combos.find((c) => !isValidModelName(c.name));
  if (badCombo) return { kind: 'combo', name: badCombo.name };
  return undefined;
}

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
  user_code?: string;
  verification_uri?: string;
}

export interface CallbackRequestDTO {
  state: string;
  code: string;
}

export interface CallbackResponseDTO {
  status: string;
  connection: ConnectionDTO;
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
  /**
   * Pins the upstream open: no key cooldown, no key revocation, no
   * consecutive-error action, and no circuit breaker for it. Absent means
   * "let the server decide" (its per-protocol default, on for the keyless
   * OpenCode free tier); GET /api/settings always reports the effective value.
   */
  always_available?: boolean | null;
  egress_mode?: 'direct' | 'warp' | 'proxy' | 'singbox' | string;
  proxy_url?: string;
  /** sing-box outbound tag this upstream dials (egress_mode 'singbox'). Empty = the manager's default (URL-test group `auto`). */
  singbox_outbound_tag?: string;
  enabled?: boolean | null;
}

export interface SingboxNodeDTO {
  tag: string;
  type: string;
  server: string;
  port: number;
  /** True while the health gate keeps this node out of the rotation. */
  cooling?: boolean;
  /** RFC3339 expiry of the current cooldown (present when cooling). */
  cooldown_until?: string;
  /** Current consecutive-failure count behind the cooldown. */
  failures?: number;
  /** Round-trip latency in milliseconds from the last health check. */
  latency_ms?: number | null;
  /** RFC3339 timestamp of the last health check probe. */
  last_check_at?: string | null;
  /** Node health status: 'active' | 'cooling' | 'unreachable' | 'idle'. */
  status?: 'active' | 'cooling' | 'unreachable' | 'idle' | string;
  /** Error message from the last failed health check or dial. */
  last_error?: string;
}

export interface SingboxStatusDTO {
  running: boolean;
  box_created: boolean;
  ready: boolean;
  node_count: number;
  default_tag?: string;
  /**
   * Only present for an authenticated caller: the subscription URL is a
   * token-bearing credential, so the public status response omits it and
   * reports `subscription_configured` instead.
   */
  subscription_url?: string;
  subscription_configured?: boolean;
  /** Refresh interval in nanoseconds (Go time.Duration JSON encoding). */
  refresh_interval?: number;
  last_refresh_at?: string;
  last_refresh_error?: string;
  nodes?: SingboxNodeDTO[];
  /** Node-selection mode: "urltest" (fastest) or "round_robin" (rotating). */
  strategy?: string;
  error?: string;
}

export interface WarpSessionDTO {
  id: number;
  public_ip: string;
  internal_ip?: string;
  colo?: string;
  endpoint?: string;
  latency_ms: number;
  active_conns: number;
  status: 'active' | 'cooling' | 'unreachable' | string;
  created_at?: string;
  last_check_at?: string;
  primary?: boolean;
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
  /** Configured number of concurrent WARP slots (0 until a pool exists). */
  pool_size?: number;
  /** Lowest pool size the dashboard may set (engine-enforced window). */
  pool_size_min?: number;
  /** Highest pool size the dashboard may set (engine-enforced window). */
  pool_size_max?: number;
  /** Slots currently usable for new dials. */
  active_sessions?: number;
  /** Public egress address of each active slot, free of duplicates. */
  egress_ips?: string[];
  /** Detailed real-time status and latency for each session in the pool. */
  sessions?: WarpSessionDTO[];
  /** Whether a Cloudflare WARP+ / Zero Trust license key is configured. */
  license_configured?: boolean;
  /** Preferred Cloudflare data center (COLO) code (e.g. "SIN", "CGK") or empty for Any/Auto. */
  preferred_colo?: string;
  /** Available COLO codes detected across the active and cached session pool. */
  available_colos?: string[];
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
  singbox_outbound_tag?: string;
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
  /** Optional per-model directive appended to the system block of every chat
   *  completion routed to this model. Injected before the global Token Saver
   *  guard, so the operator's global directive keeps the last word. Capped at
   *  ~32,000 tokens (128,000 chars) and forwarded to the provider verbatim. */
  system_prompt?: string | null;
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
  /** Operator directive injected into every chat completion's system block. Empty = disabled. */
  system_prompt?: string | null;
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
  pricing?: PricingFile;
  notifications?: NotificationsFile;
}

// ================= PRICING =================

/** One price-sheet entry. Prices are integer micro-USD per 1M tokens. */
export interface PricingEntryDTO {
  model: string;
  input_micros_per_m: number;
  output_micros_per_m: number;
  cache_read_micros_per_m?: number;
  cache_write_micros_per_m?: number;
  source?: 'models.dev' | 'manual';
  canonical_model_id?: string;
}

export interface PricingFile {
  entries: PricingEntryDTO[];
}

/** A price entry plus the backend's "is this model routed?" flag. */
export interface PricingEntryView extends PricingEntryDTO {
  used: boolean;
}

export interface PricingListResponse {
  status: string;
  entries: PricingEntryView[];
  total: number;
}

/** What a model would actually be billed at, or null when unpriced. */
export interface PricingResolveEntry {
  public_name: string;
  matched_key?: string;
  entry: PricingEntryDTO | null;
}

export interface PricingResolveResponse {
  status: string;
  entries: PricingResolveEntry[];
}

export interface PricingResolveModel {
  public_name: string;
  upstream?: string;
  upstream_model?: string;
}

// ================= NOTIFICATIONS =================

export interface NotificationChannelDTO {
  url: string;
  format: string;
  /** Write-only: the API returns "***" and never the real value. */
  bearer?: string;
  /** Write-only: the API returns "***" and never the real value. */
  secret?: string;
  enabled: boolean;
  events?: string[];
  min_severity?: string;
}

export interface NotificationsFile {
  channels: NotificationChannelDTO[];
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
  /**
   * Key slots of the upstream's KeyRing. An upstream created without any
   * credentials (name + base_url only) has zero slots, and the gateway used to
   * serialize that as `null` instead of `[]` — which crashed the Upstreams page
   * ("can't access property \"filter\", slots is null"). The type stays nullable
   * so every consumer is forced to normalize with `?? []` before iterating.
   */
  slots: KeySlotTelemetryDTO[] | null;
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
  /**
   * Host/process resource monitor (htop-style). Present only for authorized
   * admin callers — the backend omits it for anonymous public telemetry.
   */
  resource_monitor?: ResourceMonitorDTO | null;
  /** Webhook dispatcher counters. Admin only — absent for unauthenticated callers. */
  notifications?: {
    queued: number;
    dropped: number;
    delivered: number;
    failed: number;
  } | null;
}

export interface ResourceMonitorDTO {
  timestamp: number;
  /** Aggregate host CPU utilization, 0..100. */
  cpu_percent: number;
  /** Per logical core CPU utilization, 0..100 each. */
  cpu_per_core: number[];
  cpu_cores: number;
  mem_total_bytes: number;
  mem_used_bytes: number;
  mem_percent: number;
  swap_total_bytes: number;
  swap_used_bytes: number;
  /** Network throughput in bytes per second. */
  net_rx_bps: number;
  net_tx_bps: number;
  net_rx_total_bytes: number;
  net_tx_total_bytes: number;
  goroutines: number;
  go_heap_bytes: number;
  go_sys_bytes: number;
  proc_rss_bytes: number;
  /** Firefly process CPU, 0..100 across all cores. */
  proc_cpu_percent: number;
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

/* ---------- Provider-side quota (upstream allowance, not the tenant ledger) ---------- */

export interface ProviderQuotaModelDTO {
  model: string;
  display_name?: string;
  remaining_percent: number;
  used: number;
  total: number;
  reset_at?: string;
  exhausted: boolean;
  unlimited?: boolean;
}

export interface ProviderQuotaWindowDTO {
  family: string;
  window: string;
  display_name?: string;
  remaining_percent: number;
  used: number;
  total: number;
  reset_at?: string;
}

export interface ProviderQuotaDTO {
  connection_id: string;
  provider: string;
  email?: string;
  plan: string;
  paid_tier_id?: string;
  free_tier: boolean;
  project_id?: string;
  token_expired: boolean;
  fetched_at: string;
  age_seconds: number;
  from_cache: boolean;
  message?: string;
  models: ProviderQuotaModelDTO[];
  windows: ProviderQuotaWindowDTO[];
}

export interface ProviderQuotaResponseDTO {
  providers: ProviderQuotaDTO[];
  fetched_at: string;
  supported: string[];
}
