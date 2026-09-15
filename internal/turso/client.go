package turso

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	turso "turso.tech/database/tursogo"
)

// Config configures the Turso embedded replica client.
type Config struct {
	RemoteURL    string
	AuthToken    string
	LocalPath    string
	SyncInterval time.Duration
	Logger       *slog.Logger
}

// Client wraps TursoSyncDb and provides thread-safe sync operations.
type Client struct {
	cfg    Config
	syncDb *turso.TursoSyncDb
	db     *sql.DB
	logger *slog.Logger
	mu     sync.Mutex
}

// NewClient initializes a TursoSyncDb instance, pulls the latest cloud snapshot,
// connects to the local database, and executes schema migrations.
func NewClient(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.LocalPath == "" {
		cfg.LocalPath = "data/firefly.db"
	}
	if cfg.SyncInterval <= 0 {
		cfg.SyncInterval = 15 * time.Second
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// Ensure parent directory for the local database exists
	dir := filepath.Dir(cfg.LocalPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create local db directory %q: %w", dir, err)
	}

	logger.Info("initializing turso embedded sync db",
		"local_path", cfg.LocalPath,
		"remote_url", cfg.RemoteURL)

	syncDb, err := turso.NewTursoSyncDb(ctx, turso.TursoSyncDbConfig{
		Path:      cfg.LocalPath,
		RemoteUrl: cfg.RemoteURL,
		AuthToken: cfg.AuthToken,
	})
	if err != nil {
		return nil, fmt.Errorf("init turso sync db: %w", err)
	}

	// Initial pull to fetch cloud state
	logger.Info("pulling latest database snapshot from turso cloud...")
	if _, err := syncDb.Pull(ctx); err != nil {
		logger.Warn("initial turso pull encountered an error (continuing if local replica exists)", "err", err)
	} else {
		logger.Info("turso cloud pull completed successfully")
	}

	db, err := syncDb.Connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to local replica db: %w", err)
	}

	client := &Client{
		cfg:    cfg,
		syncDb: syncDb,
		db:     db,
		logger: logger,
	}

	// Migrate schema
	logger.Info("checking/migrating turso database schema...")
	if err := MigrateSchema(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}

	// Push schema migrations to cloud
	if err := client.Push(ctx); err != nil {
		logger.Warn("initial push after migration encountered warning", "err", err)
	} else {
		logger.Info("turso database schema verified and synced with cloud")
	}

	return client, nil
}

// DB returns the underlying standard sql.DB handle.
func (c *Client) DB() *sql.DB {
	return c.db
}

// Pull pulls latest changes from Turso Cloud to the local database file.
func (c *Client) Pull(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	_, err := c.syncDb.Pull(ctx)
	if err != nil {
		return fmt.Errorf("turso pull: %w", err)
	}
	return nil
}

// Push pushes local database changes up to Turso Cloud primary.
func (c *Client) Push(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	err := c.syncDb.Push(ctx)
	if err != nil {
		return fmt.Errorf("turso push: %w", err)
	}
	return nil
}

// Close closes the local database connection.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.db != nil {
		return c.db.Close()
	}
	return nil
}
