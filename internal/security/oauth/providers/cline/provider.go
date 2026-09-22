package cline

import (
	"bytes"
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
	"github.com/tidwall/gjson"
)

var _ ports.OAuthProvider = (*Provider)(nil)

const (
	defaultAuthorizeURL = "https://api.cline.bot/api/v1/auth/authorize"
	defaultTokenURL     = "https://api.cline.bot/api/v1/auth/token"
	defaultRefreshURL   = "https://api.cline.bot/api/v1/auth/refresh"
)

// Provider implements ports.OAuthProvider for Cline.
type Provider struct {
	authorizeURL string
	tokenURL     string
	refreshURL   string
	httpClient   *http.Client
}

// Option configures functional options for the Cline provider.
type Option func(*Provider)

// WithHTTPClient overrides the default HTTP client used for token exchange and refresh.
func WithHTTPClient(client *http.Client) Option {
	return func(p *Provider) {
		if client != nil {
			p.httpClient = client
		}
	}
}

// WithEndpoints overrides the default Cline OAuth endpoints (primarily for tests).
func WithEndpoints(authorizeURL, tokenURL, refreshURL string) Option {
	return func(p *Provider) {
		if authorizeURL != "" {
			p.authorizeURL = authorizeURL
		}
		if tokenURL != "" {
			p.tokenURL = tokenURL
		}
		if refreshURL != "" {
			p.refreshURL = refreshURL
		}
	}
}

// New constructs a new Cline OAuthProvider.
func New(opts ...Option) *Provider {
	p := &Provider{
		authorizeURL: defaultAuthorizeURL,
		tokenURL:     defaultTokenURL,
		refreshURL:   defaultRefreshURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Name returns the canonical provider name.
func (p *Provider) Name() string { return "cline" }

// FlowType returns the authorization code flow type.
func (p *Provider) FlowType() domain.FlowType { return domain.FlowTypeStandardAuthCode }

// IsSensitive indicates whether this provider requires sequential anti-abuse delays.
func (p *Provider) IsSensitive() bool { return false }

// PrepareAuth constructs the authorization URL for Cline.
func (p *Provider) PrepareAuth(ctx context.Context, redirectURI string) (*ports.AuthSession, error) {
	state, err := oauth.GenerateRandomString(32)
	if err != nil {
		return nil, fmt.Errorf("generate state: %w", err)
	}

	params := url.Values{}
	params.Set("client_type", "extension")
	params.Set("callback_url", redirectURI)
	params.Set("redirect_uri", redirectURI)
	params.Set("state", state)

	authURL := fmt.Sprintf("%s?%s", p.authorizeURL, params.Encode())

	sessionID, err := oauth.GenerateRandomString(16)
	if err != nil {
		sessionID = fmt.Sprintf("cline_sess_%d", time.Now().UnixNano())
	}

	return &ports.AuthSession{
		ID:          sessionID,
		Provider:    p.Name(),
		State:       state,
		RedirectURI: redirectURI,
		AuthURL:     authURL,
		CreatedAt:   time.Now(),
	}, nil
}

// ExchangeCode handles Cline's unique dual-mode token resolution:
// 1. Fast path: base64 decodes the code to check if it already contains the JSON credentials payload.
// 2. Fallback: issues a standard HTTP POST request to Cline's token endpoint.
func (p *Provider) ExchangeCode(ctx context.Context, code string, session *ports.AuthSession) (*domain.OAuthConnection, error) {
	if code == "" {
		return nil, errors.New("empty authorization code")
	}

	// 1. Fast-path: check if code is Base64 encoded JSON
	if conn, ok := p.tryBase64DecodeCode(code); ok && conn != nil {
		return conn, nil
	}

	// 2. Fallback: HTTP exchange
	redirectURI := ""
	if session != nil {
		redirectURI = session.RedirectURI
	}
	return p.exchangeHTTP(ctx, code, redirectURI)
}

func (p *Provider) tryBase64DecodeCode(code string) (*domain.OAuthConnection, bool) {
	trimmed := strings.TrimSpace(code)
	padding := (4 - (len(trimmed) % 4)) % 4
	if padding > 0 {
		trimmed += strings.Repeat("=", padding)
	}

	decodedBytes, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		decodedBytes, err = base64.URLEncoding.DecodeString(trimmed)
		if err != nil {
			return nil, false
		}
	}

	lastBrace := bytes.LastIndexByte(decodedBytes, '}')
	if lastBrace == -1 {
		return nil, false
	}
	validJSON := decodedBytes[:lastBrace+1]

	if !gjson.ValidBytes(validJSON) {
		return nil, false
	}

	accessToken := gjson.GetBytes(validJSON, "accessToken").String()
	if accessToken == "" {
		accessToken = gjson.GetBytes(validJSON, "access_token").String()
	}
	if accessToken == "" {
		return nil, false
	}

	refreshToken := gjson.GetBytes(validJSON, "refreshToken").String()
	if refreshToken == "" {
		refreshToken = gjson.GetBytes(validJSON, "refresh_token").String()
	}

	email := gjson.GetBytes(validJSON, "email").String()
	firstName := gjson.GetBytes(validJSON, "firstName").String()
	lastName := gjson.GetBytes(validJSON, "lastName").String()

	expiresAt := parseExpiry(gjson.GetBytes(validJSON, "expiresAt"))
	if expiresAt.IsZero() {
		expiresAt = parseExpiry(gjson.GetBytes(validJSON, "expires_at"))
	}
	if expiresAt.IsZero() {
		expiresAt = time.Now().Add(1 * time.Hour)
	}

	id := "cline-" + email
	if email == "" {
		id = fmt.Sprintf("cline-%d", time.Now().UnixMilli())
	}

	return &domain.OAuthConnection{
		ID:       id,
		Provider: p.Name(),
		Email:    email,
		Token: domain.OAuthToken{
			AccessToken:  accessToken,
			RefreshToken: refreshToken,
			TokenType:    "Bearer",
			ExpiresAt:    expiresAt,
		},
		ProviderSpecificData: map[string]string{
			"firstName": firstName,
			"lastName":  lastName,
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}, true
}

func (p *Provider) exchangeHTTP(ctx context.Context, code, redirectURI string) (*domain.OAuthConnection, error) {
	reqBody := map[string]string{
		"grant_type":   "authorization_code",
		"code":         code,
		"client_type":  "extension",
		"redirect_uri": redirectURI,
	}
	b, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("exchange http request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed (status %d): %s", resp.StatusCode, string(respBytes))
	}

	dataObj := gjson.GetBytes(respBytes, "data")
	target := gjson.ParseBytes(respBytes)
	if dataObj.Exists() && dataObj.IsObject() {
		target = dataObj
	}

	accessToken := target.Get("accessToken").String()
	if accessToken == "" {
		accessToken = target.Get("access_token").String()
	}
	if accessToken == "" {
		return nil, errors.New("empty access token in exchange response")
	}

	refreshToken := target.Get("refreshToken").String()
	if refreshToken == "" {
		refreshToken = target.Get("refresh_token").String()
	}

	email := target.Get("userInfo.email").String()
	if email == "" {
		email = target.Get("email").String()
	}

	expiresAt := parseExpiry(target.Get("expiresAt"))
	if expiresAt.IsZero() {
		expiresAt = parseExpiry(target.Get("expires_at"))
	}
	if expiresAt.IsZero() {
		expiresAt = time.Now().Add(1 * time.Hour)
	}

	id := "cline-" + email
	if email == "" {
		id = fmt.Sprintf("cline-%d", time.Now().UnixMilli())
	}

	return &domain.OAuthConnection{
		ID:       id,
		Provider: p.Name(),
		Email:    email,
		Token: domain.OAuthToken{
			AccessToken:  accessToken,
			RefreshToken: refreshToken,
			TokenType:    "Bearer",
			ExpiresAt:    expiresAt,
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}, nil
}

// RefreshToken exchanges an existing refresh token for a new set of credentials.
func (p *Provider) RefreshToken(ctx context.Context, conn *domain.OAuthConnection) (*domain.OAuthToken, error) {
	if conn == nil || conn.Token.RefreshToken == "" {
		return nil, errors.New("no refresh token available")
	}

	reqBody := map[string]string{
		"refreshToken": conn.Token.RefreshToken,
		"grantType":    "refresh_token",
		"clientType":   "extension",
	}
	b, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.refreshURL, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token refresh failed (status %d): %s", resp.StatusCode, string(respBytes))
	}

	dataObj := gjson.GetBytes(respBytes, "data")
	target := gjson.ParseBytes(respBytes)
	if dataObj.Exists() && dataObj.IsObject() {
		target = dataObj
	}

	accessToken := target.Get("accessToken").String()
	if accessToken == "" {
		accessToken = target.Get("access_token").String()
	}
	if accessToken == "" {
		return nil, errors.New("empty access token in refresh response")
	}

	newRefreshToken := target.Get("refreshToken").String()
	if newRefreshToken == "" {
		newRefreshToken = target.Get("refresh_token").String()
	}
	if newRefreshToken == "" {
		newRefreshToken = conn.Token.RefreshToken
	}

	expiresAt := parseExpiry(target.Get("expiresAt"))
	if expiresAt.IsZero() {
		expiresAt = parseExpiry(target.Get("expires_at"))
	}
	if expiresAt.IsZero() {
		expiresAt = time.Now().Add(1 * time.Hour)
	}

	return &domain.OAuthToken{
		AccessToken:  accessToken,
		RefreshToken: newRefreshToken,
		TokenType:    "Bearer",
		ExpiresAt:    expiresAt,
	}, nil
}

func parseExpiry(val gjson.Result) time.Time {
	if !val.Exists() {
		return time.Time{}
	}
	if val.Type == gjson.Number {
		secs := val.Int()
		if secs > 1e12 { // timestamp in millis
			return time.UnixMilli(secs)
		}
		if secs > 0 {
			return time.Unix(secs, 0)
		}
	}
	if val.Type == gjson.String {
		str := val.String()
		if t, err := time.Parse(time.RFC3339, str); err == nil {
			return t
		}
		if t, err := time.Parse(time.RFC3339Nano, str); err == nil {
			return t
		}
	}
	return time.Time{}
}
