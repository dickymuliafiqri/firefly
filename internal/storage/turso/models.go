package turso

import (
	"encoding/json"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

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
	ProbeModel          string            `json:"probe_model,omitempty"`
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
	APIKey        string            `json:"api_key"`
	KeyHash       string            `json:"key_hash,omitempty"`
	KeyHint       string            `json:"key_hint,omitempty"`
	Status        string            `json:"status"`
	MaxTokens     int64             `json:"max_tokens"`
	UsedTokens    int64             `json:"used_tokens"`
	ExpiresAt     *int64            `json:"expires_at,omitempty"`
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

// ProviderSyncEntry is one provider in a harvester snapshot. Name is the
// natural key: an entry upserts the row with that name and never renames it.
type ProviderSyncEntry struct {
	Name        string `json:"name"`
	BaseURL     string `json:"base_url"`
	Description string `json:"description,omitempty"`
	IsActive    *bool  `json:"is_active,omitempty"`
}

// ProviderKeySyncEntry is one credential in a harvester snapshot. Provider is
// the provider name (not id) so the client never has to track server ids.
// Status is authoritative on every write: an omitted status means "active" (so
// a batch can reactivate a key). Status values are the writer's lifecycle
// vocabulary and are stored verbatim — only "active" routes — so the endpoint
// checks their shape (<= 20 chars, no control characters or padding) rather
// than an enum, and refuses to freeze a batch over a state it has not seen.
// ExpiresAt carries three intents, distinguished on the wire exactly as the
// operator PATCH does: absent keeps the stored expiry, JSON null clears it, a
// number replaces it. Absence must not mean "clear" because one writer refreshes
// rows other writers created, and a table that a key silently stops expiring
// from is worse than one that keeps an expiry it was told to replace. It must be
// Unix milliseconds: a second-scale value is rejected, because in a millisecond
// column it reads as expired in 1970 and the key quietly leaves rotation.
// AccountMetadata follows the same absent-means-keep rule, because it is large
// write-only data the client may legitimately not resend. It is written verbatim
// and is never returned by any endpoint: it is a credential vault (private keys,
// mnemonics, OAuth tokens), not display metadata.
type ProviderKeySyncEntry struct {
	Provider        string          `json:"provider"`
	APIKey          string          `json:"api_key"`
	Status          string          `json:"status,omitempty"`
	ExpiresAt       json.RawMessage `json:"expires_at,omitempty"`
	AccountMetadata json.RawMessage `json:"account_metadata,omitempty"`
}

// ProviderSyncRequest is the harvester batch payload. Every write is idempotent:
// replaying the same payload creates nothing new and never changes api_keys.id
// (credential refs are derived from that id). Deactivation is explicit-id only —
// the server never infers deletions from absence, so the client stays the owner
// of "what disappeared".
type ProviderSyncRequest struct {
	Providers      []ProviderSyncEntry    `json:"providers"`
	Keys           []ProviderKeySyncEntry `json:"keys"`
	DeactivateKeys []int64                `json:"deactivate_keys,omitempty"`
}

// ProviderSyncResult reports what the batch changed. Created/Updated/Unchanged
// count API keys only; KeyIDs lists every currently active key id per provider
// touched by the request.
type ProviderSyncResult struct {
	Created     int                `json:"created"`
	Updated     int                `json:"updated"`
	Unchanged   int                `json:"unchanged"`
	Deactivated int                `json:"deactivated"`
	KeyIDs      map[string][]int64 `json:"key_ids"`
	Revision    int64              `json:"revision"`
	Pushed      bool               `json:"pushed"`
}
