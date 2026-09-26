package grokcli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/dickymuliafiqri/firefly/internal/security/oauth"
	"github.com/dickymuliafiqri/firefly/internal/textx"
	"github.com/tidwall/gjson"
)

var (
	_ ports.OAuthProvider       = (*Provider)(nil)
	_ ports.OAuthDeviceProvider = (*Provider)(nil)
)

const (
	defaultDeviceCodeURL = "https://auth.x.ai/oauth2/device/code"
	defaultTokenURL      = "https://auth.x.ai/oauth2/token"
	defaultUserURL       = "https://cli-chat-proxy.grok.com/v1/user"

	// defaultClientID is the public OAuth client shared by the official Grok CLI /
	// Grok Build. It is a public client: no client_secret is ever sent.
	defaultClientID = "b1a00492-073a-47ea-816f-4c329264a828"

	// defaultScope mirrors the official CLI HAR: offline_access is what makes xAI
	// issue a refresh_token alongside the short-lived access token (~40-45 min).
	defaultScope = "openid profile email offline_access grok-cli:access api:access conversations:read conversations:write"

	defaultReferrer = "grok-build"

	// defaultUserAgent mirrors the official grok CLI wire fingerprint.
	defaultUserAgent = "grok-pager/0.2.93 grok-shell/0.2.93 (linux; x86_64)"

	// deviceCodeGrant is the RFC 8628 device authorization grant URN.
	deviceCodeGrant = "urn:ietf:params:oauth:grant-type:device_code"

	// defaultTokenTTL is used when the token endpoint omits expires_in. A zero
	// ExpiresAt disables proactive refresh (OAuthToken.IsExpired ignores it), so
	// a conservative TTL is safer than leaving the token to expire silently.
	defaultTokenTTL = 30 * time.Minute
)

// maxErrorBodyChars bounds the upstream error body embedded in error messages.
const maxErrorBodyChars = 256

// Option configures a grok-cli Provider.
type Option func(*Provider)

// WithHTTPClient overrides the HTTP client.
func WithHTTPClient(client *http.Client) Option {
	return func(p *Provider) {
		if client != nil {
			p.client = client
		}
	}
}

// WithEndpoints overrides the xAI endpoints (primarily for tests).
func WithEndpoints(deviceCodeURL, tokenURL, userURL string) Option {
	return func(p *Provider) {
		if deviceCodeURL != "" {
			p.deviceCodeURL = deviceCodeURL
		}
		if tokenURL != "" {
			p.tokenURL = tokenURL
		}
		if userURL != "" {
			p.userURL = userURL
		}
	}
}

// Provider implements ports.OAuthDeviceProvider for Grok CLI / Grok Build
// (xAI device authorization flow, inference on cli-chat-proxy.grok.com).
type Provider struct {
	deviceCodeURL string
	tokenURL      string
	userURL       string
	clientID      string
	scope         string
	referrer      string
	userAgent     string
	client        *http.Client
}

// New constructs a new grok-cli OAuthProvider.
func New(opts ...Option) *Provider {
	p := &Provider{
		deviceCodeURL: defaultDeviceCodeURL,
		tokenURL:      defaultTokenURL,
		userURL:       defaultUserURL,
		clientID:      defaultClientID,
		scope:         defaultScope,
		referrer:      defaultReferrer,
		userAgent:     defaultUserAgent,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Name returns the canonical provider name.
func (p *Provider) Name() string { return "grok-cli" }

// FlowType returns the RFC 8628 device authorization flow. xAI issues no PKCE
// challenge for this flow (plain device_code, matching the official CLI).
func (p *Provider) FlowType() domain.FlowType { return domain.FlowTypeDeviceCode }

// IsSensitive reports whether this provider requires sequential anti-abuse delays.
func (p *Provider) IsSensitive() bool { return false }

type deviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int64  `json:"expires_in"`
	Interval                int64  `json:"interval"`
	Error                   string `json:"error"`
	ErrorDescription        string `json:"error_description"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// PrepareAuth requests an RFC 8628 device code from xAI. The returned session
// carries the device_code (for PollToken) plus the user_code and verification
// URI surfaced to the operator via /api/oauth/authorize so they can approve
// the login in a browser.
func (p *Provider) PrepareAuth(ctx context.Context, redirectURI string) (*ports.AuthSession, error) {
	form := url.Values{}
	form.Set("client_id", p.clientID)
	form.Set("scope", p.scope)
	if p.referrer != "" {
		form.Set("referrer", p.referrer)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.deviceCodeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create device code request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", p.userAgent)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("execute device code request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, fmt.Errorf("read device code response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device code request failed with status %d: %s", resp.StatusCode, textx.Excerpt(body, maxErrorBodyChars))
	}

	var data deviceCodeResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("unmarshal device code response: %w", err)
	}
	if data.Error != "" {
		return nil, fmt.Errorf("device code error %q: %s", data.Error, data.ErrorDescription)
	}
	if data.DeviceCode == "" {
		return nil, errors.New("empty device code in device code response")
	}

	state, err := oauth.GenerateRandomString(32)
	if err != nil {
		return nil, fmt.Errorf("generate state: %w", err)
	}

	// Prefer the complete URI (it embeds the user code) so the operator only
	// has to open one link.
	authURL := data.VerificationURIComplete
	if authURL == "" {
		authURL = data.VerificationURI
	}

	return &ports.AuthSession{
		ID:              "grok-cli-" + state[:16],
		Provider:        p.Name(),
		State:           state,
		DeviceCode:      data.DeviceCode,
		UserCode:        data.UserCode,
		VerificationURI: data.VerificationURI,
		RedirectURI:     redirectURI,
		AuthURL:         authURL,
		CreatedAt:       time.Now(),
	}, nil
}

// PollToken polls the xAI token endpoint with the session's device_code.
// It returns pending=true while the user has not finished authorizing.
func (p *Provider) PollToken(ctx context.Context, session *ports.AuthSession) (*domain.OAuthConnection, bool, error) {
	if session == nil || session.DeviceCode == "" {
		return nil, false, errors.New("invalid or empty device code session")
	}

	form := url.Values{}
	form.Set("grant_type", deviceCodeGrant)
	form.Set("device_code", session.DeviceCode)
	form.Set("client_id", p.clientID)

	tokens, pending, err := p.postToken(ctx, form)
	if err != nil {
		return nil, false, err
	}
	if pending {
		return nil, true, nil
	}
	return p.buildConnection(ctx, tokens), false, nil
}

// ExchangeCode implements ports.OAuthProvider by polling once for device-flow
// sessions. Device authorization completes via PollToken; a direct exchange
// code is not part of this flow.
func (p *Provider) ExchangeCode(ctx context.Context, code string, session *ports.AuthSession) (*domain.OAuthConnection, error) {
	conn, pending, err := p.PollToken(ctx, session)
	if err != nil {
		return nil, err
	}
	if pending {
		return nil, errors.New("authorization pending: user has not completed authentication in browser")
	}
	return conn, nil
}

// RefreshToken exchanges the stored refresh token for fresh credentials.
// xAI rotates the refresh token on every refresh: the new value is returned
// when present, and the previous one is preserved when the endpoint omits it,
// so a refresh response can never wipe the only valid credential.
func (p *Provider) RefreshToken(ctx context.Context, conn *domain.OAuthConnection) (*domain.OAuthToken, error) {
	if conn == nil || conn.Token.RefreshToken == "" {
		return nil, errors.New("cannot refresh without valid refresh token")
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", p.clientID)
	form.Set("refresh_token", conn.Token.RefreshToken)

	tokens, pending, err := p.postToken(ctx, form)
	if err != nil {
		return nil, err
	}
	if pending {
		return nil, errors.New("unexpected pending state on refresh_token grant")
	}
	if tokens.AccessToken == "" {
		return nil, errors.New("empty access token in refresh response")
	}

	refreshToken := tokens.RefreshToken
	if refreshToken == "" {
		refreshToken = conn.Token.RefreshToken
	}

	return &domain.OAuthToken{
		AccessToken:  tokens.AccessToken,
		RefreshToken: refreshToken,
		TokenType:    tokenTypeOrDefault(tokens.TokenType),
		ExpiresAt:    expiresAt(tokens.ExpiresIn),
		Scope:        firstNonEmpty(tokens.Scope, conn.Token.Scope),
	}, nil
}

// postToken executes a form-encoded token request. Device-grant responses
// reporting authorization_pending/slow_down are mapped to pending=true; every
// other error is returned as a Go error so callers can log and fail closed.
func (p *Provider) postToken(ctx context.Context, form url.Values) (*tokenResponse, bool, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, false, fmt.Errorf("create token request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", p.userAgent)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, false, fmt.Errorf("execute token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, false, fmt.Errorf("read token response: %w", err)
	}

	var tokens tokenResponse
	if err := json.Unmarshal(body, &tokens); err != nil {
		return nil, false, fmt.Errorf("unmarshal token response (status %d): %w", resp.StatusCode, err)
	}

	// RFC 8628 pending states surface as 400 + error; authorization is still open.
	if tokens.Error == "authorization_pending" || tokens.Error == "slow_down" {
		return nil, true, nil
	}
	if tokens.Error != "" {
		return nil, false, fmt.Errorf("token error %q: %s", tokens.Error, tokens.ErrorDesc)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("token request failed with status %d: %s", resp.StatusCode, textx.Excerpt(body, maxErrorBodyChars))
	}
	return &tokens, false, nil
}

// buildConnection materializes an OAuthConnection from a fresh token set.
// Identity is resolved best-effort (never fatal): the /v1/user profile first,
// then the id_token email claim.
func (p *Provider) buildConnection(ctx context.Context, tokens *tokenResponse) *domain.OAuthConnection {
	email := decodeJWTEmail(tokens.IDToken)

	profile := map[string]string{}
	if tokens.AccessToken != "" {
		if user := p.fetchUser(ctx, tokens.AccessToken); user != nil {
			email = firstNonEmpty(emailFromUser(user), email)
			for k, v := range user {
				if v != "" {
					profile[k] = v
				}
			}
		}
	}

	id := fmt.Sprintf("grok-cli-%d", time.Now().UnixMilli())
	if email != "" {
		id = "grok-cli-" + email
	}

	providerData := map[string]string{"auth_method": "device_code"}
	if uid := profile["user_id"]; uid != "" {
		providerData["user_id"] = uid
	}
	if tier := profile["subscriptiontier"]; tier != "" {
		providerData["subscription_tier"] = tier
	}

	return &domain.OAuthConnection{
		ID:       id,
		Provider: p.Name(),
		Email:    email,
		Token: domain.OAuthToken{
			AccessToken:  tokens.AccessToken,
			RefreshToken: tokens.RefreshToken,
			TokenType:    tokenTypeOrDefault(tokens.TokenType),
			ExpiresAt:    expiresAt(tokens.ExpiresIn),
			Scope:        tokens.Scope,
		},
		ProviderSpecificData: providerData,
		CreatedAt:            time.Now(),
		UpdatedAt:            time.Now(),
	}
}

// fetchUser resolves the account profile behind an access token. Failures are
// swallowed by design: identity metadata is a dashboard nicety, not a login
// requirement.
func (p *Provider) fetchUser(ctx context.Context, accessToken string) map[string]string {
	if p.userURL == "" {
		return nil
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.userURL, nil)
	if err != nil {
		return nil
	}
	httpReq.Header.Set("Authorization", "Bearer "+accessToken)
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", p.userAgent)
	httpReq.Header.Set("x-xai-token-auth", "xai-grok-cli")
	httpReq.Header.Set("x-grok-client-version", "0.2.99")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil
	}
	result := gjson.ParseBytes(body)
	if !result.IsObject() {
		return nil
	}
	out := map[string]string{}
	for _, k := range []string{"email", "userId", "principalId", "firstName", "lastName", "subscriptionTier", "hasGrokCodeAccess"} {
		if v := result.Get(k).String(); v != "" {
			out[strings.ToLower(k)] = v
		}
	}
	// Normalize the identity keys the connection builder consumes.
	if out["userid"] != "" {
		out["user_id"] = out["userid"]
	}
	if out["principalid"] != "" && out["user_id"] == "" {
		out["user_id"] = out["principalid"]
	}
	return out
}

// emailFromUser extracts the dashboard identity from a fetched profile.
func emailFromUser(profile map[string]string) string {
	if profile == nil {
		return ""
	}
	return profile["email"]
}

// decodeJWTEmail extracts the email claim from an id_token without verifying
// the signature (identity display only, never an auth decision).
func decodeJWTEmail(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload := parts[1]
	if m := len(payload) % 4; m != 0 {
		payload += strings.Repeat("=", 4-m)
	}
	raw, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return ""
	}
	for _, k := range []string{"email", "preferred_username", "sub"} {
		if v := gjson.GetBytes(raw, k).String(); v != "" {
			return v
		}
	}
	return ""
}

func tokenTypeOrDefault(t string) string {
	if t != "" {
		return t
	}
	return "Bearer"
}

func expiresAt(expiresIn int64) time.Time {
	if expiresIn > 0 {
		return time.Now().Add(time.Duration(expiresIn) * time.Second)
	}
	return time.Now().Add(defaultTokenTTL)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
