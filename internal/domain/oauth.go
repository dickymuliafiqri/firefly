package domain

import (
	"time"
)

// FlowType defines the authorization flow supported by an OAuth provider.
type FlowType int

const (
	// FlowTypeUnknown is the zero-value sentinel for invalid or unspecified flow.
	FlowTypeUnknown FlowType = iota
	// FlowTypeAuthCodePKCE is RFC 7636 Authorization Code with Proof Key for Code Exchange (e.g. Claude, Codex).
	FlowTypeAuthCodePKCE
	// FlowTypeStandardAuthCode is RFC 6749 standard Authorization Code flow (e.g. Google Antigravity).
	FlowTypeStandardAuthCode
	// FlowTypeDeviceCode is RFC 8628 OAuth 2.0 Device Authorization Grant (e.g. GitHub Copilot, Kiro).
	FlowTypeDeviceCode
	// FlowTypeCustomEncrypted is a custom asymmetric cryptographic handshake (e.g. Xiaomi MiMo ECDH, Zed RSA).
	FlowTypeCustomEncrypted
	// FlowTypeImportToken is direct extraction of local stored sessions (e.g. Cursor).
	FlowTypeImportToken
)

// String returns the human-readable identifier of the flow type.
func (f FlowType) String() string {
	switch f {
	case FlowTypeAuthCodePKCE:
		return "authorization_code_pkce"
	case FlowTypeStandardAuthCode:
		return "authorization_code"
	case FlowTypeDeviceCode:
		return "device_code"
	case FlowTypeCustomEncrypted:
		return "custom_encrypted"
	case FlowTypeImportToken:
		return "import_token"
	default:
		return "unknown"
	}
}

// OAuthToken holds authenticated credential tokens and expiry metadata.
type OAuthToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
	Scope        string    `json:"scope,omitempty"`
}

// IsExpired reports whether the access token is expired or within lead duration of expiring.
func (t *OAuthToken) IsExpired(lead time.Duration) bool {
	if t == nil || t.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().Add(lead).After(t.ExpiresAt)
}

// OAuthConnection represents a persisted connected third-party AI provider account.
type OAuthConnection struct {
	ID                   string            `json:"id"`
	Provider             string            `json:"provider"` // e.g. "antigravity", "claude", "github"
	Email                string            `json:"email,omitempty"`
	Token                OAuthToken        `json:"token"`
	ProviderSpecificData map[string]string `json:"provider_specific_data,omitempty"` // e.g. ProjectID, Tier, MachineID
	CreatedAt            time.Time         `json:"created_at"`
	UpdatedAt            time.Time         `json:"updated_at"`
}
