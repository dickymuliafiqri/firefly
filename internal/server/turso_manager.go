package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/registry"
	"github.com/dickymuliafiqri/firefly/internal/storage/turso"
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
	syncer       *turso.Syncer
	syncerCancel context.CancelFunc
	// syncBase/syncMax hold the pull cadence resolved for the active client.
	// startSyncerLocked reuses them so a syncer started later (AttachRegistry)
	// keeps the operator's quota settings instead of reverting to the defaults.
	syncBase time.Duration
	syncMax  time.Duration
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
		syncBase:  turso.DefaultSyncInterval,
		syncMax:   turso.DefaultSyncMaxInterval,
	}
}

// resolveSyncIntervals maps the persisted Turso settings onto the syncer cadence,
// defaulting to a 60s base and a 5m idle ceiling when unset.
func resolveSyncIntervals(cfg config.TursoDTO) (base, maxInterval time.Duration) {
	base = turso.DefaultSyncInterval
	if cfg.SyncIntervalSec > 0 {
		base = time.Duration(cfg.SyncIntervalSec) * time.Second
	}
	maxInterval = turso.DefaultSyncMaxInterval
	if cfg.SyncMaxIntervalSec > 0 {
		maxInterval = time.Duration(cfg.SyncMaxIntervalSec) * time.Second
	}
	if maxInterval < base {
		maxInterval = base
	}
	return base, maxInterval
}

// resolveLocalPath makes the embedded-replica database path writable regardless of
// the process working directory. Under systemd the CWD is typically "/" (or an
// install dir owned by root), so a relative default like "data/firefly.db" fails
// with "mkdir data: permission denied". Anchoring relative paths to the configured
// (and writable) config directory keeps the local replica alongside turso.json.
func (m *TursoManager) resolveLocalPath(localPath string) string {
	if localPath == "" {
		localPath = "data/firefly.db"
	}
	if filepath.IsAbs(localPath) {
		return localPath
	}
	if m.configDir != "" {
		return filepath.Join(m.configDir, localPath)
	}
	return localPath
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
	m.syncer = nil
	if client == nil || store == nil || m.reg == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.syncerCancel = cancel

	syncer := turso.NewSyncer(client, store, m.reg, turso.SyncerConfig{
		Interval:    m.syncBase,
		MaxInterval: m.syncMax,
		EnvLookup:   os.LookupEnv,
		Logger:      m.logger,
	})
	initialRev, _ := store.GetCatalogRevision(ctx)
	syncer.SetInitialRevision(initialRev)
	m.syncer = syncer

	go func() {
		if err := syncer.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			m.logger.Warn("turso background syncer encountered warning", "err", err)
		}
	}()
}

// TriggerSync runs one pull-and-reload cycle immediately instead of waiting for
// the background syncer's next tick, so a harvester batch is visible to routing
// before the HTTP response returns.
//
// It returns nil when no syncer is running (file-storage mode, or a store that
// is not attached to a registry). That is not a failure: the caller's write has
// already bumped the catalog revision, which the 15s tick observes. The manager
// lock is held across the cycle so a concurrent Close/UpdateConfig cannot swap
// the client out from under it; SyncOnce serializes overlapping cycles.
func (m *TursoManager) TriggerSync(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.syncer == nil {
		return nil
	}
	return m.syncer.SyncOnce(ctx)
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
	localPath = m.resolveLocalPath(localPath)

	syncInterval, syncMaxInterval := resolveSyncIntervals(cfg)

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
	m.syncBase = syncInterval
	m.syncMax = syncMaxInterval
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
		m.syncer = nil
		return nil, nil
	}

	localPath := m.resolveLocalPath(cfg.LocalPath)
	syncInterval, syncMaxInterval := resolveSyncIntervals(cfg)

	// Close old client before opening new one
	if m.client != nil {
		_ = m.client.Close()
		m.client = nil
		m.store = nil
		m.syncer = nil
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
	m.syncBase = syncInterval
	m.syncMax = syncMaxInterval
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
		m.syncer = nil
		return err
	}
	return nil
}
