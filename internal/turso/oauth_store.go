package turso

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
)

var _ ports.TokenStore = (*OAuthStore)(nil)

// OAuthStore persists connected OAuth accounts in the Turso database.
type OAuthStore struct {
	client *Client
	db     *sql.DB
	mu     sync.RWMutex
}

// NewOAuthStore creates a new OAuthStore.
func NewOAuthStore(client *Client) *OAuthStore {
	var db *sql.DB
	if client != nil {
		db = client.DB()
	}
	return &OAuthStore{
		client: client,
		db:     db,
	}
}

func (s *OAuthStore) rLock() {
	if s.client != nil {
		s.client.RLock()
	} else {
		s.mu.RLock()
	}
}

func (s *OAuthStore) rUnlock() {
	if s.client != nil {
		s.client.RUnlock()
	} else {
		s.mu.RUnlock()
	}
}

func (s *OAuthStore) lock() {
	if s.client != nil {
		s.client.Lock()
	} else {
		s.mu.Lock()
	}
}

func (s *OAuthStore) unlock() {
	if s.client != nil {
		s.client.Unlock()
	} else {
		s.mu.Unlock()
	}
}

// Get retrieves an OAuthConnection by ID.
func (s *OAuthStore) Get(ctx context.Context, id string) (*domain.OAuthConnection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.rLock()
	defer s.rUnlock()

	var (
		provider, accessToken string
		email, refreshToken, psDataJSON sql.NullString
		expiresAt, createdAt, updatedAt int64
	)

	err := s.db.QueryRowContext(ctx, `
		SELECT provider, email, access_token, refresh_token, expires_at,
		       provider_specific_data, created_at, updated_at
		FROM oauth_connections
		WHERE id = ?
	`, id).Scan(
		&provider, &email, &accessToken, &refreshToken, &expiresAt,
		&psDataJSON, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("connection %q: not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("query oauth connection: %w", err)
	}

	var psData map[string]string
	if psDataJSON.Valid && psDataJSON.String != "" {
		_ = json.Unmarshal([]byte(psDataJSON.String), &psData)
	}

	var expTime time.Time
	if expiresAt > 0 {
		expTime = time.UnixMilli(expiresAt)
	}

	conn := &domain.OAuthConnection{
		ID:       id,
		Provider: provider,
		Email:    email.String,
		Token: domain.OAuthToken{
			AccessToken:  accessToken,
			RefreshToken: refreshToken.String,
			ExpiresAt:    expTime,
		},
		ProviderSpecificData: psData,
		CreatedAt:            time.UnixMilli(createdAt),
		UpdatedAt:            time.UnixMilli(updatedAt),
	}

	return conn, nil
}

// Save stores or updates an OAuth connection in Turso.
func (s *OAuthStore) Save(ctx context.Context, conn *domain.OAuthConnection) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if conn == nil || conn.ID == "" {
		return errors.New("cannot save nil connection or connection without ID")
	}

	s.lock()
	defer s.unlock()

	now := time.Now().UnixMilli()
	created := conn.CreatedAt.UnixMilli()
	if created <= 0 {
		created = now
	}

	var expMs int64
	if !conn.Token.ExpiresAt.IsZero() {
		expMs = conn.Token.ExpiresAt.UnixMilli()
	}

	psJSON, _ := json.Marshal(conn.ProviderSpecificData)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO oauth_connections (
			id, provider, email, access_token, refresh_token, expires_at,
			provider_specific_data, version, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			provider = excluded.provider,
			email = excluded.email,
			access_token = excluded.access_token,
			refresh_token = excluded.refresh_token,
			expires_at = excluded.expires_at,
			provider_specific_data = excluded.provider_specific_data,
			version = oauth_connections.version + 1,
			updated_at = excluded.updated_at
	`, conn.ID, conn.Provider, conn.Email, conn.Token.AccessToken, conn.Token.RefreshToken,
		expMs, string(psJSON), created, now)
	if err != nil {
		return fmt.Errorf("save oauth connection: %w", err)
	}

	if s.client != nil {
		_ = s.client.PushLocked(ctx)
	}

	return nil
}

// List returns all active OAuth connections.
func (s *OAuthStore) List(ctx context.Context) ([]*domain.OAuthConnection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.rLock()
	defer s.rUnlock()

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, provider, email, access_token, refresh_token, expires_at,
		       provider_specific_data, created_at, updated_at
		FROM oauth_connections
		ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list oauth connections: %w", err)
	}
	defer rows.Close()

	var conns []*domain.OAuthConnection
	for rows.Next() {
		var (
			id, provider, accessToken       string
			email, refreshToken, psDataJSON sql.NullString
			expiresAt, createdAt, updatedAt int64
		)

		if err := rows.Scan(&id, &provider, &email, &accessToken, &refreshToken, &expiresAt, &psDataJSON, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan oauth connection: %w", err)
		}

		var psData map[string]string
		if psDataJSON.Valid && psDataJSON.String != "" {
			_ = json.Unmarshal([]byte(psDataJSON.String), &psData)
		}

		var expTime time.Time
		if expiresAt > 0 {
			expTime = time.UnixMilli(expiresAt)
		}

		conns = append(conns, &domain.OAuthConnection{
			ID:       id,
			Provider: provider,
			Email:    email.String,
			Token: domain.OAuthToken{
				AccessToken:  accessToken,
				RefreshToken: refreshToken.String,
				ExpiresAt:    expTime,
			},
			ProviderSpecificData: psData,
			CreatedAt:            time.UnixMilli(createdAt),
			UpdatedAt:            time.UnixMilli(updatedAt),
		})
	}

	return conns, nil
}

// Delete removes an OAuth connection by ID.
func (s *OAuthStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.lock()
	defer s.unlock()

	_, err := s.db.ExecContext(ctx, "DELETE FROM oauth_connections WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete oauth connection: %w", err)
	}

	if s.client != nil {
		_ = s.client.PushLocked(ctx)
	}

	return nil
}
