// Package config loads and validates the JSON configuration set and translates
// it into immutable domain snapshots. It owns all JSON tags; the domain types
// stay transport-agnostic.
package config

// FileSet groups the raw bytes of the logical config files.
type FileSet struct {
	Upstreams  []byte
	Models     []byte
	Tenants    []byte
	Combos     []byte
	TokenSaver []byte
}

// --- DTOs mirroring the JSON schema. All fields are pointers or have explicit
// presence handling where the zero value is meaningful. ---

// CredentialKeyDTO is one key entry in an upstream's credential_pool.
type CredentialKeyDTO struct {
	Ref           string   `json:"ref,omitempty"`
	APIKey        string   `json:"api_key,omitempty"`
	Secret        string   `json:"secret,omitempty"`
	RPS           *float64 `json:"rps,omitempty"`
	MaxConcurrent *int     `json:"max_concurrent,omitempty"`
}

// UpstreamsFile is the top-level shape of upstreams.json.
type UpstreamsFile struct {
	Upstreams []UpstreamDTO `json:"upstreams"`
}

// UpstreamDTO is one entry in upstreams.json.
type UpstreamDTO struct {
	Name                string             `json:"name"`
	Protocol            string             `json:"protocol,omitempty"`
	BaseURL             string             `json:"base_url,omitempty"`
	BaseURLs            []string           `json:"base_urls,omitempty"`
	APIKey              string             `json:"api_key,omitempty"`
	APIKeys             []string           `json:"api_keys,omitempty"`
	CredentialRef       string             `json:"credential_ref,omitempty"`
	KeyStrategy         string             `json:"key_strategy,omitempty"`
	CredentialPool      []CredentialKeyDTO `json:"credential_pool,omitempty"`
	TimeoutMs           *int               `json:"timeout_ms,omitempty"`
	IdleTimeoutMs       *int               `json:"idle_timeout_ms,omitempty"`
	StreamIdleTimeoutMs *int               `json:"stream_idle_timeout_ms,omitempty"`
	MaxIdleConnsPerHost *int               `json:"max_idle_conns_per_host,omitempty"`
	MaxConnsPerHost     *int               `json:"max_conns_per_host,omitempty"`
	ExtraHeaders        map[string]string  `json:"extra_headers,omitempty"`
	AllowInsecure       *bool              `json:"allow_insecure,omitempty"`
	Enabled             *bool              `json:"enabled,omitempty"`

	ProviderID              *int64   `json:"provider_id,omitempty"`
	CredentialRPS           *float64 `json:"credential_rps,omitempty"`
	CredentialMaxConcurrent *int     `json:"credential_max_concurrent,omitempty"`

	KeyErrorThreshold     *int   `json:"key_error_threshold,omitempty"`
	KeyErrorAction        string `json:"key_error_action,omitempty"`
	KeyCooldownDurationMs *int   `json:"key_cooldown_duration_ms,omitempty"`
	ProbeModel            string `json:"probe_model,omitempty"`
}

// ModelsFile is the top-level shape of models.json.
type ModelsFile struct {
	Models []ModelDTO `json:"models"`
}

// ModelDTO is one entry in models.json.
type ModelDTO struct {
	PublicName        string            `json:"public_name"`
	Upstream          string            `json:"upstream"`
	FallbackUpstreams []string          `json:"fallback_upstreams,omitempty"`
	UpstreamModel     string            `json:"upstream_model"`
	UpstreamModels    map[string]string `json:"upstream_models,omitempty"`
	RoutingStrategy   string            `json:"routing_strategy,omitempty"`
	Capabilities      *CapabilitiesDTO  `json:"capabilities,omitempty"`
	MaxContext        *int              `json:"max_context,omitempty"`
	Enabled           *bool             `json:"enabled,omitempty"`
}

// CapabilitiesDTO mirrors the capabilities object.
type CapabilitiesDTO struct {
	Stream     bool `json:"stream,omitempty"`
	Tools      bool `json:"tools,omitempty"`
	Vision     bool `json:"vision,omitempty"`
	JSONMode   bool `json:"json_mode,omitempty"`
	Embeddings bool `json:"embeddings,omitempty"`
	Audio      bool `json:"audio,omitempty"`
}

// TenantsFile is the top-level shape of tenants.json.
type TenantsFile struct {
	Tenants []TenantDTO `json:"tenants"`
}

// TenantDTO is one entry in tenants.json.
type TenantDTO struct {
	KeyHash       string            `json:"key_hash,omitempty"`
	APIKey        string            `json:"api_key,omitempty"`
	Name          string            `json:"name"`
	Status        string            `json:"status,omitempty"`
	AllowedModels []string          `json:"allowed_models,omitempty"`
	CredentialRef string            `json:"credential_ref,omitempty"`
	RateLimit     *RateLimitDTO     `json:"rate_limit,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

// CombosFile is the top-level shape of combos.json.
type CombosFile struct {
	Combos []ComboDTO `json:"combos"`
}

// ComboDTO is one entry in combos.json.
type ComboDTO struct {
	Name     string   `json:"name"`
	Strategy string   `json:"strategy,omitempty"` // round_robin, least_inflight, failover
	Models   []string `json:"models"`
	Enabled  *bool    `json:"enabled,omitempty"`
}

// SettingsDTO aggregates the entire system configuration for frontend management.
type SettingsDTO struct {
	Upstreams []UpstreamDTO `json:"upstreams"`
	Models    []ModelDTO    `json:"models"`
	Tenants   []TenantDTO   `json:"tenants"`
	Combos    []ComboDTO    `json:"combos,omitempty"`
	// ManageModels signals that the Models list in this payload is authoritative,
	// so the backend may delete models absent from it — even when the list is
	// empty (deleting the last model). When false/absent, an empty Models list is
	// treated as a partial/stale payload and no models are deleted.
	ManageModels bool `json:"manage_models,omitempty"`
	// ManageUpstreams signals that the Upstreams list is authoritative, allowing
	// deletion of upstreams absent from it (including the last one). When
	// false/absent, an empty Upstreams list never deletes existing upstreams.
	ManageUpstreams bool `json:"manage_upstreams,omitempty"`
	// ManageCombos signals that the Combos list is authoritative, allowing
	// deletion of combos absent from it (including the last one). When
	// false/absent, an empty Combos list never deletes existing combos.
	ManageCombos bool `json:"manage_combos,omitempty"`
	// ManageTenants signals that the Tenants list is authoritative, allowing
	// deletion of tenants absent from it (including the last one). When
	// false/absent, an empty Tenants list never deletes existing tenants.
	ManageTenants bool `json:"manage_tenants,omitempty"`
	// AutoTLS is persisted separately in tls.json because it controls network
	// listeners rather than the hot-swappable routing catalog. A nil value on
	// update means "preserve the existing TLS configuration".
	AutoTLS *AutoTLSDTO `json:"auto_tls,omitempty"`
	// StorageEngine indicates whether configuration is backed by Turso or local JSON.
	StorageEngine string `json:"storage_engine,omitempty"`
	// Turso credentials and replica options.
	Turso *TursoDTO `json:"turso,omitempty"`
	// TokenSaver configures prompt and tool output optimization.
	TokenSaver *TokenSaverDTO `json:"token_saver,omitempty"`
}

// TokenSaverDTO mirrors tokensaver.json and configures optimization features.
type TokenSaverDTO struct {
	Enabled            bool `json:"enabled"`
	CompressToolOutput bool `json:"compress_tool_output"`
	TerseOutput        bool `json:"terse_output"`
	MinimalCode        bool `json:"minimal_code"`
	CompressContext    bool `json:"compress_context"`
	MaxToolOutputChars *int `json:"max_tool_output_chars,omitempty"`
	ContextThreshold   *int `json:"context_threshold,omitempty"`
}

// RateLimitDTO mirrors the rate_limit object.
type RateLimitDTO struct {
	RPS           *float64 `json:"rps,omitempty"`
	Burst         *int     `json:"burst,omitempty"`
	MaxConcurrent *int     `json:"max_concurrent,omitempty"`
}

// --- Defaults applied during translation (documented in config_schema.md). ---

const (
	DefaultProtocol      = "openai"
	DefaultKeyStrategy   = "round_robin"
	DefaultTimeoutMs     = 30000
	DefaultIdleTimeoutMs = 90000
	// DefaultStreamIdleTimeoutMs bounds a stalled SSE stream. 120s is long enough
	// for slow models to emit their first token (openai's own SDK default read
	// timeout is longer), yet short enough that a wedged upstream is reclaimed.
	DefaultStreamIdleTimeoutMs = 120000
	DefaultMaxIdleConnsPerHost = 200
	DefaultMaxConnsPerHost     = 400
	DefaultTenantRPS           = 20.0
	DefaultTenantMaxConcurrent = 20
)

// pickInt returns *v if non-nil, else def.
func pickInt(v *int, def int) int {
	if v != nil {
		return *v
	}
	return def
}

// pickFloat returns *v if non-nil, else def.
func pickFloat(v *float64, def float64) float64 {
	if v != nil {
		return *v
	}
	return def
}

// pickBool returns *v if non-nil, else def.
func pickBool(v *bool, def bool) bool {
	if v != nil {
		return *v
	}
	return def
}
