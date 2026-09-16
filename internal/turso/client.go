package turso

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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

// Client wraps TursoSyncDb and coordinates thread-safe database and sync operations.
type Client struct {
	cfg    Config
	syncDb *turso.TursoSyncDb
	db     *sql.DB
	logger *slog.Logger
	mu     sync.RWMutex
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

	// The tursogo loader extracts a native shared library at runtime. By default it
	// picks os.UserCacheDir() ($XDG_CACHE_HOME, else $HOME/.cache). A systemd unit
	// user such as "firefly" often has no writable $HOME, so extraction panics with
	// "mkdir /home/firefly: permission denied". Default the loader's cache dir to a
	// writable location next to the local replica unless the operator overrode it.
	if os.Getenv("TURSO_GO_CACHE_DIR") == "" {
		cacheDir := filepath.Join(dir, ".turso-cache")
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			return nil, fmt.Errorf("create turso library cache directory %q: %w", cacheDir, err)
		}
		_ = os.Setenv("TURSO_GO_CACHE_DIR", cacheDir)
	}

	logger.Info("initializing turso embedded sync db",
		"local_path", cfg.LocalPath,
		"remote_url", cfg.RemoteURL)

	syncDb, err := turso.NewTursoSyncDb(ctx, turso.TursoSyncDbConfig{
		Path:        cfg.LocalPath,
		RemoteUrl:   cfg.RemoteURL,
		AuthToken:   cfg.AuthToken,
		BusyTimeout: 10000,
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

	// Single connection ensures committed and rolled-back transactions share a pager
	// without multi-connection lock contention on the local SQLite replica.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

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

// Lock acquires the exclusive write lock on Client for database mutations and sync operations.
func (c *Client) Lock() {
	if c != nil {
		c.mu.Lock()
	}
}

// Unlock releases the exclusive write lock on Client.
func (c *Client) Unlock() {
	if c != nil {
		c.mu.Unlock()
	}
}

// RLock acquires the shared read lock on Client for database read operations.
func (c *Client) RLock() {
	if c != nil {
		c.mu.RLock()
	}
}

// RUnlock releases the shared read lock on Client.
func (c *Client) RUnlock() {
	if c != nil {
		c.mu.RUnlock()
	}
}

// Pull pulls latest changes from Turso Cloud to the local database file, retrying on busy conditions.
func (c *Client) Pull(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pullLocked(ctx)
}

// PullLocked pulls latest changes assuming caller already holds c.mu Lock.
func (c *Client) PullLocked(ctx context.Context) error {
	return c.pullLocked(ctx)
}

func (c *Client) pullLocked(ctx context.Context) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, err = c.syncDb.Pull(ctx)
		if err == nil {
			return nil
		}
		if isDatabaseBusyError(err) && attempt < 2 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(100*(attempt+1)) * time.Millisecond):
				continue
			}
		}
		break
	}
	return fmt.Errorf("turso pull: %w", err)
}

// Push pushes local database changes up to Turso Cloud primary, retrying on busy conditions.
func (c *Client) Push(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pushLocked(ctx)
}

// PushLocked pushes local database changes assuming caller already holds c.mu Lock.
func (c *Client) PushLocked(ctx context.Context) error {
	return c.pushLocked(ctx)
}

func (c *Client) pushLocked(ctx context.Context) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err = c.syncDb.Push(ctx)
		if err == nil {
			return nil
		}
		if isDatabaseBusyError(err) && attempt < 2 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(100*(attempt+1)) * time.Millisecond):
				continue
			}
		}
		break
	}
	return fmt.Errorf("turso push: %w", err)
}

func isDatabaseBusyError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is busy") || strings.Contains(msg, "busy")
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
