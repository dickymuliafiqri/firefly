// Package config loads and validates the JSON configuration set and translates
// it into immutable domain snapshots. It owns all JSON tags; the domain types
// stay transport-agnostic.
package config

// FileSet groups the raw bytes of the logical config files.
type FileSet struct {
	Upstreams []byte
	Models    []byte
	Tenants   []byte
	Combos    []byte
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

	CredentialRPS           *float64 `json:"credential_rps,omitempty"`
	CredentialMaxConcurrent *int     `json:"credential_max_concurrent,omitempty"`
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
	// AutoTLS is persisted separately in tls.json because it controls network
	// listeners rather than the hot-swappable routing catalog. A nil value on
	// update means "preserve the existing TLS configuration".
	AutoTLS *AutoTLSDTO `json:"auto_tls,omitempty"`
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
