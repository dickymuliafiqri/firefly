package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/registry"
	"github.com/dickymuliafiqri/firefly/internal/turso"
)

// TursoManager manages the lifecycle of the active Turso embedded sync client and store.
// It is thread-safe and allows dynamic hot-swapping and lazy-initialization when settings
// change without requiring a server restart.
type TursoManager struct {
	mu           sync.RWMutex
	configDir    string
	logger       *slog.Logger
	client       *turso.Client
	store        *turso.Store
	reg          *registry.Registry
	syncerCancel context.CancelFunc
}

// NewTursoManager creates a TursoManager. If initialStore is provided, it is retained.
func NewTursoManager(configDir string, initialStore *turso.Store, logger *slog.Logger) *TursoManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &TursoManager{
		configDir: configDir,
		logger:    logger,
		store:     initialStore,
	}
}

// AttachRegistry associates the active registry and triggers the background syncer if connected.
func (m *TursoManager) AttachRegistry(reg *registry.Registry) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reg = reg
	if m.client != nil && m.store != nil {
		m.startSyncerLocked(m.client, m.store)
	}
}

func (m *TursoManager) startSyncerLocked(client *turso.Client, store *turso.Store) {
	if m.syncerCancel != nil {
		m.syncerCancel()
		m.syncerCancel = nil
	}
	if client == nil || store == nil || m.reg == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.syncerCancel = cancel

	syncer := turso.NewSyncer(client, store, m.reg, turso.SyncerConfig{
		Interval:  15 * time.Second,
		EnvLookup: os.LookupEnv,
		Logger:    m.logger,
	})
	initialRev, _ := store.GetCatalogRevision(ctx)
	syncer.SetInitialRevision(initialRev)

	go func() {
		if err := syncer.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			m.logger.Warn("turso background syncer encountered warning", "err", err)
		}
	}()
}

// GetOrInitStore returns the currently active turso.Store, or attempts to initialize
// one from turso.json or environment variables if credentials exist.
func (m *TursoManager) GetOrInitStore(ctx context.Context) (*turso.Store, error) {
	if m == nil {
		return nil, nil
	}
	m.mu.RLock()
	if m.store != nil {
		store := m.store
		m.mu.RUnlock()
		return store, nil
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if m.store != nil {
		return m.store, nil
	}

	cfg, _ := config.LoadTursoConfig(m.configDir)
	remoteURL := cfg.DatabaseURL
	if remoteURL == "" {
		remoteURL = os.Getenv("TURSO_DATABASE_URL")
	}
	authToken := cfg.AuthToken
	if authToken == "" {
		authToken = os.Getenv("TURSO_AUTH_TOKEN")
	}
	if remoteURL == "" || authToken == "" {
		return nil, nil // Not configured
	}

	localPath := cfg.LocalPath
	if localPath == "" {
		localPath = os.Getenv("FIREFLY_TURSO_LOCAL_PATH")
	}
	if localPath == "" {
		localPath = "data/firefly.db"
	}

	syncInterval := 15 * time.Second
	if cfg.SyncIntervalSec > 0 {
		syncInterval = time.Duration(cfg.SyncIntervalSec) * time.Second
	}

	client, err := turso.NewClient(ctx, turso.Config{
		RemoteURL:    remoteURL,
		AuthToken:    authToken,
		LocalPath:    localPath,
		SyncInterval: syncInterval,
		Logger:       m.logger,
	})
	if err != nil {
		return nil, fmt.Errorf("init turso client: %w", err)
	}

	store := turso.NewStore(client)
	if m.configDir != "" {
		if err := turso.BootstrapFromFiles(ctx, m.configDir, store, m.logger); err != nil {
			m.logger.Warn("turso bootstrap warning", "err", err)
		}
	}

	m.client = client
	m.store = store
	m.startSyncerLocked(client, store)
	return store, nil
}

// UpdateConfig updates or creates the Turso client and store with new credentials.
func (m *TursoManager) UpdateConfig(ctx context.Context, cfg config.TursoDTO) (*turso.Store, error) {
	if m == nil {
		return nil, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if cfg.DatabaseURL == "" || cfg.AuthToken == "" {
		if m.client != nil {
			_ = m.client.Close()
			m.client = nil
		}
		m.store = nil
		return nil, nil
	}

	localPath := cfg.LocalPath
	if localPath == "" {
		localPath = "data/firefly.db"
	}
	syncInterval := 15 * time.Second
	if cfg.SyncIntervalSec > 0 {
		syncInterval = time.Duration(cfg.SyncIntervalSec) * time.Second
	}

	// Close old client before opening new one
	if m.client != nil {
		_ = m.client.Close()
		m.client = nil
		m.store = nil
	}

	client, err := turso.NewClient(ctx, turso.Config{
		RemoteURL:    cfg.DatabaseURL,
		AuthToken:    cfg.AuthToken,
		LocalPath:    localPath,
		SyncInterval: syncInterval,
		Logger:       m.logger,
	})
	if err != nil {
		return nil, fmt.Errorf("connect to turso: %w", err)
	}

	store := turso.NewStore(client)
	if m.configDir != "" {
		if err := turso.BootstrapFromFiles(ctx, m.configDir, store, m.logger); err != nil {
			m.logger.Warn("turso bootstrap warning", "err", err)
		}
	}

	m.client = client
	m.store = store
	m.startSyncerLocked(client, store)
	return store, nil
}

// Close gracefully closes the underlying client connection.
func (m *TursoManager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.syncerCancel != nil {
		m.syncerCancel()
		m.syncerCancel = nil
	}

	if m.client != nil {
		err := m.client.Close()
		m.client = nil
		m.store = nil
		return err
	}
	return nil
}
