package turso

import "github.com/dickymuliafiqri/firefly/internal/domain"

// UpstreamRecord represents a row in the upstreams table.
type UpstreamRecord struct {
	ID                  int64             `json:"id"`
	Name                string            `json:"name"`
	Protocol            string            `json:"protocol"`
	BaseURL             string            `json:"base_url"`
	FallbackBaseURLs    []string          `json:"fallback_base_urls,omitempty"`
	KeyStrategy         string            `json:"key_strategy"`
	ProviderID          *int64            `json:"provider_id,omitempty"`
	CredentialRef       string            `json:"credential_ref,omitempty"`
	TimeoutMs           int               `json:"timeout_ms"`
	IdleTimeoutMs       int               `json:"idle_timeout_ms"`
	StreamIdleTimeoutMs int               `json:"stream_idle_timeout_ms"`
	MaxIdleConnsPerHost int               `json:"max_idle_conns_per_host"`
	MaxConnsPerHost     int               `json:"max_conns_per_host"`
	ExtraHeaders        map[string]string `json:"extra_headers,omitempty"`
	AllowInsecure       bool              `json:"allow_insecure"`
	CredentialRPS       *float64          `json:"credential_rps,omitempty"`
	CredentialMaxConcur *int              `json:"credential_max_concurrent,omitempty"`
	Enabled             bool              `json:"enabled"`
	Version             int               `json:"version"`
	CreatedAt           int64             `json:"created_at"`
	UpdatedAt           int64             `json:"updated_at"`
}

// UpstreamCredentialRecord represents a row in the upstream_credentials table.
type UpstreamCredentialRecord struct {
	ID            int64    `json:"id"`
	UpstreamID    int64    `json:"upstream_id"`
	APIKeyID      *int64   `json:"api_key_id,omitempty"`
	Ref           string   `json:"ref"`
	Secret        string   `json:"secret,omitempty"`
	RPS           *float64 `json:"rps,omitempty"`
	MaxConcurrent *int     `json:"max_concurrent,omitempty"`
	Status        string   `json:"status"`
	IsActive      bool     `json:"is_active"`
	CreatedAt     int64    `json:"created_at"`
	UpdatedAt     int64    `json:"updated_at"`
}

// ModelRecord represents a row in the models table.
type ModelRecord struct {
	ID                int64               `json:"id"`
	PublicName        string              `json:"public_name"`
	UpstreamID        int64               `json:"upstream_id"`
	UpstreamName      string              `json:"upstream_name"`
	UpstreamModel     string              `json:"upstream_model"`
	FallbackUpstreams []string            `json:"fallback_upstreams,omitempty"`
	Capabilities      domain.Capabilities `json:"capabilities"`
	MaxContext        int                 `json:"max_context"`
	Enabled           bool                `json:"enabled"`
	Version           int                 `json:"version"`
	CreatedAt         int64               `json:"created_at"`
	UpdatedAt         int64               `json:"updated_at"`
}

// ComboRecord represents a row in the combos table.
type ComboRecord struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	Strategy  string   `json:"strategy"`
	Models    []string `json:"models"`
	Enabled   bool     `json:"enabled"`
	Version   int      `json:"version"`
	CreatedAt int64    `json:"created_at"`
	UpdatedAt int64    `json:"updated_at"`
}

// TenantRecord represents a row in the tenants table.
type TenantRecord struct {
	ID            int64             `json:"id"`
	Name          string            `json:"name"`
	KeyHash       string            `json:"key_hash"`
	KeyHint       string            `json:"key_hint"`
	Status        string            `json:"status"`
	RPS           *float64          `json:"rps,omitempty"`
	Burst         *int              `json:"burst,omitempty"`
	MaxConcurrent *int              `json:"max_concurrent,omitempty"`
	AllowedModels []string          `json:"allowed_models,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	Version       int               `json:"version"`
	CreatedAt     int64             `json:"created_at"`
	UpdatedAt     int64             `json:"updated_at"`
}

// RawAPIKeyRecord represents a row in the harvester's api_keys table.
type RawAPIKeyRecord struct {
	ID        int64  `json:"id"`
	APIKey    string `json:"api_key"`
	Status    string `json:"status"`
	IsActive  bool   `json:"is_active"`
	ExpiresAt *int64 `json:"expires_at,omitempty"`
}
