// Package oauth coordinates third-party AI provider authentication flows,
// token refresh lifecycles, and persistent connection storage.
package oauth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/dickymuliafiqri/firefly/internal/singleflightx"
)

// DefaultSessionTTL is how long an unexchanged AuthSession remains valid.
const DefaultSessionTTL = 10 * time.Minute

// refreshFlightTimeout bounds a shared refresh flight. Detaching the flight from
// the caller's context removes the caller's deadline along with its
// cancellation, so the flight needs a deadline of its own.
const refreshFlightTimeout = 2 * time.Minute

// ProviderSummaryDTO describes an available OAuth provider for client/frontend discovery.
type ProviderSummaryDTO struct {
	Name        string `json:"name"`
	FlowType    string `json:"flow_type"`
	IsSensitive bool   `json:"is_sensitive"`
}

// Manager orchestrates OAuth providers, ephemeral auth sessions, and token persistence.
type Manager struct {
	mu        sync.RWMutex
	store     ports.TokenStore
	providers map[string]ports.OAuthProvider
	sessions  map[string]*ports.AuthSession // keyed by State
	sessionMu sync.Mutex
	sflight   singleflightx.Group[*domain.OAuthConnection]
	logger    *slog.Logger
}

// ManagerOption applies optional configuration to the Manager.
type ManagerOption func(*Manager)

// WithLogger sets the logger for Manager operations.
func WithLogger(logger *slog.Logger) ManagerOption {
	return func(m *Manager) {
		if logger != nil {
			m.logger = logger
		}
	}
}

// NewManager constructs a Manager instance.
func NewManager(store ports.TokenStore, opts ...ManagerOption) *Manager {
	m := &Manager{
		store:     store,
		providers: make(map[string]ports.OAuthProvider),
		sessions:  make(map[string]*ports.AuthSession),
		logger:    slog.Default(),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// RegisterProvider registers an OAuthProvider under its canonical name.
func (m *Manager) RegisterProvider(p ports.OAuthProvider) error {
	if p == nil || p.Name() == "" {
		return errors.New("cannot register nil provider or provider without name")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.providers[p.Name()]; exists {
		return fmt.Errorf("provider %q already registered", p.Name())
	}
	m.providers[p.Name()] = p
	return nil
}

// GetProvider returns the registered provider for the given name.
func (m *Manager) GetProvider(name string) (ports.OAuthProvider, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.providers[name]
	return p, ok
}

// ListProviders returns a list of supported providers and their flow types.
func (m *Manager) ListProviders() []ProviderSummaryDTO {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]ProviderSummaryDTO, 0, len(m.providers))
	for _, p := range m.providers {
		out = append(out, ProviderSummaryDTO{
			Name:        p.Name(),
			FlowType:    p.FlowType().String(),
			IsSensitive: p.IsSensitive(),
		})
	}
	return out
}

// PrepareAuth initiates an auth session with the specified provider and returns the AuthURL.
func (m *Manager) PrepareAuth(ctx context.Context, providerName, redirectURI string) (*ports.AuthSession, error) {
	p, ok := m.GetProvider(providerName)
	if !ok {
		return nil, fmt.Errorf("provider %q not found", providerName)
	}

	session, err := p.PrepareAuth(ctx, redirectURI)
	if err != nil {
		return nil, fmt.Errorf("prepare auth for %q: %w", providerName, err)
	}
	if session == nil || session.State == "" {
		return nil, errors.New("provider returned invalid session or empty state")
	}

	m.sessionMu.Lock()
	// Prune expired sessions or completed sessions older than 1 minute
	now := time.Now()
	for k, s := range m.sessions {
		if now.Sub(s.CreatedAt) > DefaultSessionTTL || (s.CompletedConn != nil && now.Sub(s.CreatedAt) > time.Minute) {
			delete(m.sessions, k)
		}
	}
	m.sessions[session.State] = session
	m.sessionMu.Unlock()

	return session, nil
}

// ExchangeCallback validates the state, retrieves the session, exchanges the authorization code,
// and saves the resulting connection.
func (m *Manager) ExchangeCallback(ctx context.Context, state, code string) (*domain.OAuthConnection, error) {
	if code == "" {
		return nil, errors.New("empty authorization code")
	}

	m.sessionMu.Lock()
	var matchedKey string
	var session *ports.AuthSession

	if state != "" {
		if s, ok := m.sessions[state]; ok {
			matchedKey = state
			session = s
		}
	} else {
		// State is empty (e.g. Cline callback redirect which omits state).
		// Match the most recently created pending session for provider "cline", or newest pending session.
		var newestTime time.Time
		for k, s := range m.sessions {
			if s.CompletedConn != nil {
				continue
			}
			if s.Provider == "cline" {
				if s.CreatedAt.After(newestTime) {
					newestTime = s.CreatedAt
					matchedKey = k
					session = s
				}
			}
		}
		if session == nil {
			for k, s := range m.sessions {
				if s.CompletedConn != nil {
					continue
				}
				if s.CreatedAt.After(newestTime) {
					newestTime = s.CreatedAt
					matchedKey = k
					session = s
				}
			}
		}
	}

	if session != nil && session.CompletedConn != nil {
		m.sessionMu.Unlock()
		return nil, errors.New("authorization session already completed")
	}
	m.sessionMu.Unlock()

	var p ports.OAuthProvider
	var ok bool
	if session != nil {
		p, ok = m.GetProvider(session.Provider)
		if !ok {
			return nil, fmt.Errorf("provider %q no longer registered", session.Provider)
		}
	} else {
		// If no session exists in memory (e.g. direct callback navigation or expired session),
		// attempt fallback to "cline" if registered since Cline's code is self-contained.
		p, ok = m.GetProvider("cline")
		if !ok {
			return nil, errors.New("invalid or expired OAuth state")
		}
	}

	conn, err := p.ExchangeCode(ctx, code, session)
	if err != nil {
		return nil, fmt.Errorf("exchange code with %q: %w", p.Name(), err)
	}
	if conn == nil || conn.ID == "" {
		return nil, errors.New("provider returned invalid or empty connection")
	}

	if m.store != nil {
		if err := m.store.Save(ctx, conn); err != nil {
			return nil, fmt.Errorf("save connection %q: %w", conn.ID, err)
		}
	}

	// Update session with completed connection for polling consumers
	if matchedKey != "" {
		m.sessionMu.Lock()
		if s, ok := m.sessions[matchedKey]; ok {
			s.CompletedConn = conn
		}
		m.sessionMu.Unlock()
	}

	return conn, nil
}

// PollAuth checks whether a device/browser polling auth session has been completed by the user.
// If the user has not yet finished authorizing, it returns (nil, true, nil) and preserves the session.
// If authorization succeeded, it saves the resulting connection, prunes the session, and returns (conn, false, nil).
// If an error occurred or the state is invalid, it returns (nil, false, err).
func (m *Manager) PollAuth(ctx context.Context, state string) (*domain.OAuthConnection, bool, error) {
	m.sessionMu.Lock()
	session, ok := m.sessions[state]
	if !ok || session == nil {
		m.sessionMu.Unlock()
		return nil, false, errors.New("invalid or expired OAuth state")
	}

	// If the browser callback has already completed for this session, return the connection and prune session
	if session.CompletedConn != nil {
		conn := session.CompletedConn
		delete(m.sessions, state)
		m.sessionMu.Unlock()
		return conn, false, nil
	}
	m.sessionMu.Unlock()

	p, ok := m.GetProvider(session.Provider)
	if !ok {
		return nil, false, fmt.Errorf("provider %q no longer registered", session.Provider)
	}

	devProvider, ok := p.(ports.OAuthDeviceProvider)
	if !ok {
		// Non-device provider waiting for browser redirect callback: report pending
		return nil, true, nil
	}

	conn, pending, err := devProvider.PollToken(ctx, session)
	if err != nil {
		return nil, false, fmt.Errorf("poll token for %q: %w", session.Provider, err)
	}
	if pending {
		return nil, true, nil
	}
	if conn == nil || conn.ID == "" {
		return nil, false, errors.New("provider returned invalid or empty connection")
	}

	// Succeeded: remove session and persist connection
	m.sessionMu.Lock()
	delete(m.sessions, state)
	m.sessionMu.Unlock()

	if m.store != nil {
		if err := m.store.Save(ctx, conn); err != nil {
			return nil, false, fmt.Errorf("save connection %q: %w", conn.ID, err)
		}
	}

	return conn, false, nil
}

// GetConnection returns a connection by ID.
func (m *Manager) GetConnection(ctx context.Context, id string) (*domain.OAuthConnection, error) {
	if m.store == nil {
		return nil, errors.New("no token store configured")
	}
	return m.store.Get(ctx, id)
}

// ListConnections returns all saved OAuth connections.
func (m *Manager) ListConnections(ctx context.Context) ([]*domain.OAuthConnection, error) {
	if m.store == nil {
		return nil, errors.New("no token store configured")
	}
	return m.store.List(ctx)
}

// DeleteConnection deletes an OAuth connection by ID.
func (m *Manager) DeleteConnection(ctx context.Context, id string) error {
	if m.store == nil {
		return errors.New("no token store configured")
	}
	return m.store.Delete(ctx, id)
}

// RefreshToken refreshes an active connection's token and updates the store.
func (m *Manager) RefreshToken(ctx context.Context, connectionID string) (*domain.OAuthConnection, error) {
	if m.store == nil {
		return nil, errors.New("no token store configured")
	}

	// Concurrent refreshes for one connection are coalesced into a single
	// provider call. The flight runs detached from the caller's context: a
	// request that has already ended must not abort a refresh the other waiters
	// are sharing, nor fail the store write after the provider has already
	// rotated the refresh token. Waiters still stop waiting on their own ctx.
	conn, err := m.sflight.Do(ctx, connectionID, func() (*domain.OAuthConnection, error) {
		flightCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshFlightTimeout)
		defer cancel()

		conn, err := m.store.Get(flightCtx, connectionID)
		if err != nil {
			return nil, err
		}

		p, ok := m.GetProvider(conn.Provider)
		if !ok {
			return nil, fmt.Errorf("provider %q not found", conn.Provider)
		}

		newToken, err := p.RefreshToken(flightCtx, conn)
		if err != nil {
			return nil, fmt.Errorf("refresh token for %q: %w", conn.ID, err)
		}
		if newToken == nil || newToken.AccessToken == "" {
			return nil, errors.New("provider returned empty token on refresh")
		}

		conn.Token = *newToken
		conn.UpdatedAt = time.Now()

		if err := m.store.Save(flightCtx, conn); err != nil {
			return nil, fmt.Errorf("save refreshed connection %q: %w", conn.ID, err)
		}

		return conn, nil
	})
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// TokenSource returns a thread-safe ports.TokenSource implementation that automatically
// yields a valid access token, proactively refreshing when within 5 minutes of expiration.
func (m *Manager) TokenSource(connectionID string) ports.TokenSource {
	return &dynamicTokenSource{
		manager:      m,
		connectionID: connectionID,
	}
}

type dynamicTokenSource struct {
	manager      *Manager
	connectionID string
}

func (s *dynamicTokenSource) Token(ctx context.Context) (string, error) {
	conn, err := s.manager.GetConnection(ctx, s.connectionID)
	if err != nil {
		return "", err
	}

	// If token expires within 5 minutes, trigger refresh
	if conn.Token.IsExpired(5 * time.Minute) && conn.Token.RefreshToken != "" {
		refreshed, err := s.manager.RefreshToken(ctx, s.connectionID)
		if err == nil && refreshed != nil {
			return refreshed.Token.AccessToken, nil
		}
		// If refresh fails, fall back to current token if not completely expired
		if !conn.Token.IsExpired(0) {
			return conn.Token.AccessToken, nil
		}
		return "", fmt.Errorf("token expired and refresh failed: %w", err)
	}

	return conn.Token.AccessToken, nil
}

// ResolveToken resolves a token reference (e.g. "oauth:antigravity" or "antigravity"), returning a fresh access token.
func (m *Manager) ResolveToken(ctx context.Context, ref string) (string, error) {
	connID := strings.TrimPrefix(ref, "oauth:")
	return m.TokenSource(connID).Token(ctx)
}

// ResolveConnection resolves an OAuth connection matching the ref (e.g. "oauth:antigravity" or "antigravity").
func (m *Manager) ResolveConnection(ctx context.Context, ref string) (*domain.OAuthConnection, error) {
	if m.store == nil {
		return nil, errors.New("no token store configured")
	}
	connID := strings.TrimPrefix(ref, "oauth:")
	return m.store.Get(ctx, connID)
}

