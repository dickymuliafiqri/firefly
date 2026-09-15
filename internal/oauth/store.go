package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
)

var _ ports.TokenStore = (*Store)(nil)

const (
	// FileNameOAuth is the persistent configuration file for connected OAuth accounts.
	FileNameOAuth = "oauth.json"
)

type oauthFileDTO struct {
	Connections []*domain.OAuthConnection `json:"connections"`
}

// Store persists and manages connected OAuth accounts atomically.
type Store struct {
	mu          sync.RWMutex
	dir         string
	connections map[string]*domain.OAuthConnection
}

// NewStore initializes a Store and loads any existing connections from dir/oauth.json.
func NewStore(dir string) (*Store, error) {
	s := &Store{
		dir:         dir,
		connections: make(map[string]*domain.OAuthConnection),
	}

	if dir == "" {
		return s, nil
	}

	path := filepath.Join(dir, FileNameOAuth)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read oauth config: %w", err)
	}

	var fileDTO oauthFileDTO
	if err := json.Unmarshal(data, &fileDTO); err != nil {
		return nil, fmt.Errorf("decode oauth config: %w", err)
	}

	for _, conn := range fileDTO.Connections {
		if conn != nil && conn.ID != "" {
			s.connections[conn.ID] = conn
		}
	}

	return s, nil
}

// Get retrieves a connection by ID. Returns nil and ErrNotFound if absent.
func (s *Store) Get(ctx context.Context, id string) (*domain.OAuthConnection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	conn, ok := s.connections[id]
	if !ok {
		return nil, fmt.Errorf("connection %q: not found", id)
	}
	// Return shallow copy
	cp := *conn
	return &cp, nil
}

// Save stores or updates a connection and persists the state atomically.
func (s *Store) Save(ctx context.Context, conn *domain.OAuthConnection) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if conn == nil || conn.ID == "" {
		return errors.New("cannot save nil connection or connection without ID")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cp := *conn
	s.connections[conn.ID] = &cp

	return s.persistLocked()
}

// List returns a list of all active connections.
func (s *Store) List(ctx context.Context) ([]*domain.OAuthConnection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*domain.OAuthConnection, 0, len(s.connections))
	for _, conn := range s.connections {
		cp := *conn
		out = append(out, &cp)
	}
	return out, nil
}

// Delete removes a connection by ID.
func (s *Store) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.connections[id]; !ok {
		return nil
	}
	delete(s.connections, id)

	return s.persistLocked()
}

func (s *Store) persistLocked() error {
	if s.dir == "" {
		return nil
	}

	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	conns := make([]*domain.OAuthConnection, 0, len(s.connections))
	for _, c := range s.connections {
		conns = append(conns, c)
	}

	raw, err := json.MarshalIndent(oauthFileDTO{Connections: conns}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal oauth config: %w", err)
	}
	raw = append(raw, '\n')

	// Write atomically using temporary file with 0600 mode (sensitive tokens)
	tmp, err := os.CreateTemp(s.dir, ".oauth.json-*")
	if err != nil {
		return fmt.Errorf("create oauth temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set oauth temp file mode: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write oauth temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close oauth temp file: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(s.dir, FileNameOAuth)); err != nil {
		return fmt.Errorf("replace oauth config: %w", err)
	}

	return nil
}
