package codebuddy

import (
	"bytes"
	"context"
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
	"github.com/dickymuliafiqri/firefly/internal/textx"
)

// CodeBuddy return codes
const (
	CodeSuccess = 0
	CodePending = 11217 // RetryFetchToken
)

// maxErrorBodyChars bounds the upstream error body embedded in error messages.
// These errors are logged, and an unbounded body can be a whole HTML page.
const maxErrorBodyChars = 256

// Region defines regional configurations for Tencent CodeBuddy.
type Region string

const (
	RegionCN   Region = "cn"
	RegionIntl Region = "intl"
)

type regionalConfig struct {
	Name       string
	BaseURL    string
	Platform   string
	UserAgent  string
	Domain     string
}

var regions = map[Region]regionalConfig{
	RegionCN: {
		Name:      "codebuddy-cn",
		BaseURL:   "https://copilot.tencent.com",
		Platform:  "CLI",
		UserAgent: "CLI/2.108.1 CodeBuddy/2.108.1",
		Domain:    "copilot.tencent.com",
	},
	RegionIntl: {
		Name:      "codebuddy-intl",
		BaseURL:   "https://www.codebuddy.ai",
		Platform:  "ide",
		UserAgent: "IDE/2.108.1 CodeBuddy/2.108.1",
		Domain:    "www.codebuddy.ai",
	},
}

// Option configures a CodeBuddy Provider.
type Option func(*Provider)

// WithHTTPClient overrides the HTTP client.
func WithHTTPClient(client *http.Client) Option {
	return func(p *Provider) {
		if client != nil {
			p.client = client
		}
	}
}

// WithBaseURL overrides the base endpoint (useful for mock testing).
func WithBaseURL(rawURL string) Option {
	return func(p *Provider) {
		if rawURL != "" {
			p.baseURL = strings.TrimRight(rawURL, "/")
		}
	}
}

// Provider implements ports.OAuthDeviceProvider for CodeBuddy.
type Provider struct {
	region    Region
	name      string
	baseURL   string
	platform  string
	userAgent string
	domain    string
	client    *http.Client
}

// NewCN returns a new CodeBuddy China provider.
func NewCN(opts ...Option) *Provider {
	return newProvider(RegionCN, opts...)
}

// NewIntl returns a new CodeBuddy International provider.
func NewIntl(opts ...Option) *Provider {
	return newProvider(RegionIntl, opts...)
}

func newProvider(region Region, opts ...Option) *Provider {
	cfg, ok := regions[region]
	if !ok {
		cfg = regions[RegionCN]
	}

	p := &Provider{
		region:    region,
		name:      cfg.Name,
		baseURL:   cfg.BaseURL,
		platform:  cfg.Platform,
		userAgent: cfg.UserAgent,
		domain:    cfg.Domain,
		client: &http.Client{
			Timeout: 20 * time.Second,
		},
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

// Name returns the provider canonical name ("codebuddy-cn" or "codebuddy-intl").
func (p *Provider) Name() string {
	return p.name
}

// FlowType returns domain.FlowTypeDeviceCode.
func (p *Provider) FlowType() domain.FlowType {
	return domain.FlowTypeDeviceCode
}

// IsSensitive returns false as CodeBuddy does not require sequential delay throttling.
func (p *Provider) IsSensitive() bool {
	return false
}

type stateResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		State   string `json:"state"`
		AuthURL string `json:"authUrl"`
	} `json:"data"`
}

// PrepareAuth requests an auth state and URL from CodeBuddy.
func (p *Provider) PrepareAuth(ctx context.Context, redirectURI string) (*ports.AuthSession, error) {
	reqURL := fmt.Sprintf("%s/v2/plugin/auth/state?platform=%s", p.baseURL, url.QueryEscape(p.platform))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, fmt.Errorf("create state request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", p.userAgent)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-Domain", p.domain)
	req.Header.Set("X-No-Authorization", "true")
	req.Header.Set("X-No-User-Id", "true")
	req.Header.Set("X-Product", "SaaS")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute state request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, fmt.Errorf("read state response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("state request failed with status %d: %s", resp.StatusCode, textx.Excerpt(body, maxErrorBodyChars))
	}

	var data stateResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("unmarshal state response: %w", err)
	}

	if data.Code != CodeSuccess || data.Data.State == "" || data.Data.AuthURL == "" {
		return nil, fmt.Errorf("codebuddy state error (code %d): %s", data.Code, data.Msg)
	}

	sessionID := fmt.Sprintf("%s-%s", p.name, data.Data.State)
	return &ports.AuthSession{
		ID:          sessionID,
		Provider:    p.name,
		State:       data.Data.State,
		AuthURL:     data.Data.AuthURL,
		RedirectURI: redirectURI,
		CreatedAt:   time.Now(),
	}, nil
}

type tokenResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		TokenType    string `json:"tokenType"`
		ExpiresIn    int64  `json:"expiresIn"`
	} `json:"data"`
}

// PollToken polls the CodeBuddy token endpoint for the given auth session.
func (p *Provider) PollToken(ctx context.Context, session *ports.AuthSession) (*domain.OAuthConnection, bool, error) {
	if session == nil || session.State == "" {
		return nil, false, errors.New("invalid or empty session state")
	}

	reqURL := fmt.Sprintf("%s/v2/plugin/auth/token?state=%s", p.baseURL, url.QueryEscape(session.State))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, false, fmt.Errorf("create poll request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", p.userAgent)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-Domain", p.domain)
	req.Header.Set("X-No-Authorization", "true")
	req.Header.Set("X-No-User-Id", "true")
	req.Header.Set("X-No-Enterprise-Id", "true")
	req.Header.Set("X-No-Department-Info", "true")
	req.Header.Set("X-Product", "SaaS")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("execute poll request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, false, fmt.Errorf("read poll response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("poll request failed with status %d: %s", resp.StatusCode, textx.Excerpt(body, maxErrorBodyChars))
	}

	var data tokenResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, false, fmt.Errorf("unmarshal poll response: %w", err)
	}

	// Code 11217: Authorization Pending
	if data.Code == CodePending {
		return nil, true, nil
	}

	if data.Code != CodeSuccess || data.Data.AccessToken == "" {
		msg := data.Msg
		if msg == "" {
			msg = "missing access token"
		}
		return nil, false, fmt.Errorf("codebuddy auth error (code %d): %s", data.Code, msg)
	}

	expiresIn := data.Data.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 86400 // Default 24h
	}

	tokenType := data.Data.TokenType
	if tokenType == "" {
		tokenType = "Bearer"
	}

	statePrefix := session.State
	if len(statePrefix) > 8 {
		statePrefix = statePrefix[:8]
	}
	connID := fmt.Sprintf("%s-%s", p.name, statePrefix)

	conn := &domain.OAuthConnection{
		ID:       connID,
		Provider: p.name,
		Token: domain.OAuthToken{
			AccessToken:  data.Data.AccessToken,
			RefreshToken: data.Data.RefreshToken,
			TokenType:    tokenType,
			ExpiresAt:    time.Now().Add(time.Duration(expiresIn) * time.Second),
		},
		ProviderSpecificData: map[string]string{
			"region":   string(p.region),
			"platform": p.platform,
			"domain":   p.domain,
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	return conn, false, nil
}

// ExchangeCode implements ports.OAuthProvider by calling PollToken once.
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

// RefreshToken refreshes an active CodeBuddy connection.
func (p *Provider) RefreshToken(ctx context.Context, conn *domain.OAuthConnection) (*domain.OAuthToken, error) {
	if conn == nil || conn.Token.RefreshToken == "" {
		return nil, errors.New("cannot refresh without valid refresh token")
	}

	reqURL := fmt.Sprintf("%s/v2/plugin/auth/token/refresh", p.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, fmt.Errorf("create refresh request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", p.userAgent)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-Domain", p.domain)
	req.Header.Set("X-Refresh-Token", conn.Token.RefreshToken)
	req.Header.Set("X-Auth-Refresh-Source", "plugin")
	req.Header.Set("X-Product", "SaaS")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute refresh request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, fmt.Errorf("read refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh request failed with status %d: %s", resp.StatusCode, textx.Excerpt(body, maxErrorBodyChars))
	}

	var data tokenResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("unmarshal refresh response: %w", err)
	}

	if data.Code != CodeSuccess || data.Data.AccessToken == "" {
		msg := data.Msg
		if msg == "" {
			msg = "missing access token on refresh"
		}
		return nil, fmt.Errorf("codebuddy refresh error (code %d): %s", data.Code, msg)
	}

	expiresIn := data.Data.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 86400
	}

	refreshToken := data.Data.RefreshToken
	if refreshToken == "" {
		refreshToken = conn.Token.RefreshToken
	}

	tokenType := data.Data.TokenType
	if tokenType == "" {
		tokenType = "Bearer"
	}

	return &domain.OAuthToken{
		AccessToken:  data.Data.AccessToken,
		RefreshToken: refreshToken,
		TokenType:    tokenType,
		ExpiresAt:    time.Now().Add(time.Duration(expiresIn) * time.Second),
	}, nil
}
