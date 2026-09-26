package ports

import (
	"context"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

// AuthSession tracks ephemeral handshake parameters between initiating the OAuth
// flow and receiving the browser callback.
type AuthSession struct {
	ID            string                  `json:"id"`
	Provider      string                  `json:"provider"`
	State         string                  `json:"state"`
	CodeVerifier  string                  `json:"code_verifier,omitempty"`
	RedirectURI   string                  `json:"redirect_uri"`
	AuthURL       string                  `json:"auth_url"`
	CompletedConn *domain.OAuthConnection `json:"completed_conn,omitempty"`
	CreatedAt     time.Time               `json:"created_at"`

	// Device-code flow (RFC 8628) parameters, retained between PrepareAuth and
	// PollToken. DeviceCode is the secret poll handle; UserCode and
	// VerificationURI are the human-facing approval material.
	DeviceCode      string `json:"device_code,omitempty"`
	UserCode        string `json:"user_code,omitempty"`
	VerificationURI string `json:"verification_uri,omitempty"`
}

// OAuthProvider represents the contract implemented by each external AI OAuth provider.
type OAuthProvider interface {
	// Name returns the unique canonical provider name (e.g. "antigravity", "claude", "github").
	Name() string

	// FlowType returns the underlying authorization flow mechanism.
	FlowType() domain.FlowType

	// PrepareAuth generates authorization URL and state/PKCE verifiers for an auth session.
	PrepareAuth(ctx context.Context, redirectURI string) (*AuthSession, error)

	// ExchangeCode exchanges an authorization code (or callback payload) for a fully populated connection.
	ExchangeCode(ctx context.Context, code string, session *AuthSession) (*domain.OAuthConnection, error)

	// RefreshToken exchanges a refresh token for fresh access credentials.
	RefreshToken(ctx context.Context, conn *domain.OAuthConnection) (*domain.OAuthToken, error)

	// IsSensitive reports whether this provider requires sequential anti-abuse delays (e.g. Google Cloud).
	IsSensitive() bool
}

// OAuthDeviceProvider is implemented by providers supporting browser/device-code polling
// (e.g. CodeBuddy, GitHub Copilot, RFC 8628 Device Authorization Grant).
type OAuthDeviceProvider interface {
	OAuthProvider

	// PollToken polls the provider's token endpoint using the existing auth session.
	// It returns pending=true if the user has not yet completed authorization,
	// or the finished connection with pending=false upon success.
	PollToken(ctx context.Context, session *AuthSession) (conn *domain.OAuthConnection, pending bool, err error)
}

// TokenStore defines persistence operations for connected OAuth accounts.
type TokenStore interface {
	Get(ctx context.Context, id string) (*domain.OAuthConnection, error)
	Save(ctx context.Context, conn *domain.OAuthConnection) error
	List(ctx context.Context) ([]*domain.OAuthConnection, error)
	Delete(ctx context.Context, id string) error
}

// TokenSource provides valid, up-to-date bearer access tokens on demand for outbound adapters.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}
