// Package antigravity implements the Google Antigravity Cloud Code OAuth provider.
package antigravity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/oauth"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/tidwall/gjson"
)

var _ ports.OAuthProvider = (*Provider)(nil)

func defaultClientID() string {
	if v := os.Getenv("ANTIGRAVITY_CLIENT_ID"); v != "" {
		return v
	}
	enc := []byte{
		0x6b, 0x6a, 0x6d, 0x6b, 0x6a, 0x6a, 0x6c, 0x6a, 0x6c, 0x6a, 0x6f, 0x63, 0x6b, 0x77, 0x2e, 0x37,
		0x32, 0x29, 0x29, 0x33, 0x34, 0x68, 0x32, 0x68, 0x6b, 0x36, 0x39, 0x28, 0x3f, 0x68, 0x69, 0x6f,
		0x2c, 0x2e, 0x35, 0x36, 0x35, 0x30, 0x32, 0x6e, 0x3d, 0x6e, 0x6a, 0x69, 0x3f, 0x2a, 0x74, 0x3b,
		0x2a, 0x2a, 0x29, 0x74, 0x3d, 0x35, 0x35, 0x3d, 0x36, 0x3f, 0x2f, 0x29, 0x3f, 0x28, 0x39, 0x35,
		0x34, 0x2e, 0x3f, 0x34, 0x2e, 0x74, 0x39, 0x35, 0x37,
	}
	for i := range enc {
		enc[i] ^= 0x5a
	}
	return string(enc)
}

func defaultClientSecret() string {
	if v := os.Getenv("ANTIGRAVITY_CLIENT_SECRET"); v != "" {
		return v
	}
	enc := []byte{
		0x1d, 0x15, 0x19, 0x9, 0xa, 0x2, 0x77, 0x11, 0x6f, 0x62, 0x1c, 0xd, 0x8, 0x6e, 0x62, 0x6c,
		0x16, 0x3e, 0x16, 0x10, 0x6b, 0x37, 0x16, 0x18, 0x62, 0x29, 0x2, 0x19, 0x6e, 0x20, 0x6c, 0x2b,
		0x1e, 0x1b, 0x3c,
	}
	for i := range enc {
		enc[i] ^= 0x5a
	}
	return string(enc)
}

const (
	defaultAuthURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	defaultTokenURL    = "https://oauth2.googleapis.com/token"
	defaultUserInfoURL = "https://www.googleapis.com/oauth2/v1/userinfo"
	defaultProdBaseURL = "https://cloudcode-pa.googleapis.com"
)

var defaultScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

// Provider implements ports.OAuthProvider for Google Antigravity.
type Provider struct {
	clientID     string
	clientSecret string
	authURL      string
	tokenURL     string
	userInfoURL  string
	prodBaseURL  string
	scopes       []string
	httpClient   *http.Client
}

// Option configures functional options for Provider.
type Option func(*Provider)

// WithCredentials overrides the default Google OAuth client ID and Secret.
func WithCredentials(clientID, clientSecret string) Option {
	return func(p *Provider) {
		if clientID != "" {
			p.clientID = clientID
		}
		if clientSecret != "" {
			p.clientSecret = clientSecret
		}
	}
}

// WithHTTPClient overrides the default outbound HTTP client.
func WithHTTPClient(client *http.Client) Option {
	return func(p *Provider) {
		if client != nil {
			p.httpClient = client
		}
	}
}

// WithProdBaseURL overrides the Google Cloud Code PA endpoint (used in tests).
func WithProdBaseURL(u string) Option {
	return func(p *Provider) {
		if u != "" {
			p.prodBaseURL = u
		}
	}
}

// WithAuthURLs overrides the Google OAuth auth, token, and userinfo URLs (used in tests).
func WithAuthURLs(auth, token, userInfo string) Option {
	return func(p *Provider) {
		if auth != "" {
			p.authURL = auth
		}
		if token != "" {
			p.tokenURL = token
		}
		if userInfo != "" {
			p.userInfoURL = userInfo
		}
	}
}

// New constructs a Google Antigravity OAuth provider.
func New(opts ...Option) *Provider {
	p := &Provider{
		clientID:     defaultClientID(),
		clientSecret: defaultClientSecret(),
		authURL:      defaultAuthURL,
		tokenURL:     defaultTokenURL,
		userInfoURL:  defaultUserInfoURL,
		prodBaseURL:  defaultProdBaseURL,
		scopes:       defaultScopes,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (p *Provider) Name() string              { return "antigravity" }
func (p *Provider) FlowType() domain.FlowType { return domain.FlowTypeStandardAuthCode }
func (p *Provider) IsSensitive() bool          { return true }

// PrepareAuth builds the Google OAuth2 consent URL.
func (p *Provider) PrepareAuth(ctx context.Context, redirectURI string) (*ports.AuthSession, error) {
	state, err := oauth.GenerateRandomString(32)
	if err != nil {
		return nil, fmt.Errorf("generate oauth state: %w", err)
	}

	q := url.Values{}
	q.Set("client_id", p.clientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", strings.Join(p.scopes, " "))
	q.Set("state", state)
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")

	authURL := p.authURL + "?" + q.Encode()

	return &ports.AuthSession{
		ID:          "antigravity-" + state[:8],
		Provider:    p.Name(),
		State:       state,
		RedirectURI: redirectURI,
		AuthURL:     authURL,
		CreatedAt:   time.Now(),
	}, nil
}

type tokenExchangeResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
}

// ExchangeCode exchanges the authorization code for tokens, retrieves userinfo, and onboard Code Assist.
func (p *Provider) ExchangeCode(ctx context.Context, code string, session *ports.AuthSession) (*domain.OAuthConnection, error) {
	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("client_id", p.clientID)
	data.Set("client_secret", p.clientSecret)
	data.Set("code", code)
	data.Set("redirect_uri", session.RedirectURI)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read token exchange response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("token exchange failed (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var tokResp tokenExchangeResponse
	if err := json.Unmarshal(body, &tokResp); err != nil {
		return nil, fmt.Errorf("decode token exchange JSON: %w", err)
	}
	if tokResp.AccessToken == "" {
		return nil, errors.New("empty access token returned from token endpoint")
	}

	// 1. Fetch User Info (Email)
	email := ""
	userReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.userInfoURL+"?alt=json", nil)
	if err == nil {
		userReq.Header.Set("Authorization", "Bearer "+tokResp.AccessToken)
		if uResp, uErr := p.httpClient.Do(userReq); uErr == nil {
			uBody, _ := io.ReadAll(io.LimitReader(uResp.Body, 1<<20))
			_ = uResp.Body.Close()
			if uResp.StatusCode == http.StatusOK {
				email = gjson.GetBytes(uBody, "email").String()
			}
		}
	}

	// 2. Discover Google Cloud Project & Tier via loadCodeAssist
	codeAssist, caErr := LoadCodeAssist(ctx, p.httpClient, tokResp.AccessToken, p.prodBaseURL)
	projectID := ""
	tierID := "legacy-tier"
	if caErr == nil && codeAssist != nil {
		projectID = codeAssist.ProjectID
		if codeAssist.TierID != "" {
			tierID = codeAssist.TierID
		}
	}

	// 3. Fire-and-forget background onboarding (matching 9router PR #3813).
	// Non-blocking so OAuth code exchange returns immediately to client.
	client := p.httpClient
	baseURL := p.prodBaseURL
	token := tokResp.AccessToken
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_, _ = OnboardUser(bgCtx, client, token, baseURL, tierID, DefaultOnboardMaxAttempts)
	}()

	now := time.Now()
	expiresAt := now.Add(time.Duration(tokResp.ExpiresIn) * time.Second)
	if tokResp.ExpiresIn <= 0 {
		expiresAt = now.Add(1 * time.Hour)
	}

	connID := "antigravity"
	if email != "" {
		connID = "antigravity-" + email
	}

	conn := &domain.OAuthConnection{
		ID:       connID,
		Provider: p.Name(),
		Email:    email,
		Token: domain.OAuthToken{
			AccessToken:  tokResp.AccessToken,
			RefreshToken: tokResp.RefreshToken,
			TokenType:    tokResp.TokenType,
			ExpiresAt:    expiresAt,
			Scope:        tokResp.Scope,
		},
		ProviderSpecificData: map[string]string{
			"project_id": projectID,
			"tier_id":    tierID,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	return conn, nil
}

// RefreshToken exchanges an active refresh_token for a fresh access token.
func (p *Provider) RefreshToken(ctx context.Context, conn *domain.OAuthConnection) (*domain.OAuthToken, error) {
	if conn == nil || conn.Token.RefreshToken == "" {
		return nil, errors.New("cannot refresh token without refresh_token")
	}

	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("client_id", p.clientID)
	data.Set("client_secret", p.clientSecret)
	data.Set("refresh_token", conn.Token.RefreshToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read token refresh response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("token refresh failed (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var tokResp tokenExchangeResponse
	if err := json.Unmarshal(body, &tokResp); err != nil {
		return nil, fmt.Errorf("decode token refresh JSON: %w", err)
	}
	if tokResp.AccessToken == "" {
		return nil, errors.New("empty access token returned on refresh")
	}

	refreshToken := tokResp.RefreshToken
	if refreshToken == "" {
		// Google does not always reissue a new refresh_token; keep existing
		refreshToken = conn.Token.RefreshToken
	}

	expiresIn := tokResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}

	return &domain.OAuthToken{
		AccessToken:  tokResp.AccessToken,
		RefreshToken: refreshToken,
		TokenType:    tokResp.TokenType,
		ExpiresAt:    time.Now().Add(time.Duration(expiresIn) * time.Second),
		Scope:        tokResp.Scope,
	}, nil
}
