package turso

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/dickymuliafiqri/firefly/internal/config"
)

// BootstrapFromFiles inspects the database to see if it is empty of upstreams.
// If empty, and JSON configuration files exist in configDir, it parses them
// and populates the Turso database, then pushes to Turso Cloud.
func BootstrapFromFiles(ctx context.Context, configDir string, store *Store, logger *slog.Logger) error {
	if store == nil || store.DB() == nil {
		return fmt.Errorf("turso store is nil or uninitialized")
	}
	if logger == nil {
		logger = slog.Default()
	}

	var upstreamCount int
	err := store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM upstreams").Scan(&upstreamCount)
	if err != nil {
		return fmt.Errorf("check upstreams count: %w", err)
	}

	if upstreamCount > 0 {
		// Database already populated
		logger.Info("turso database already contains upstreams, skipping file bootstrap", "count", upstreamCount)
		return nil
	}

	upPath := filepath.Join(configDir, config.FileNameUpstreams)
	if _, err := os.Stat(upPath); os.IsNotExist(err) {
		logger.Info("no existing upstreams.json found to bootstrap Turso database", "path", upPath)
		return nil
	}

	logger.Info("bootstrapping turso database from existing config files...", "config_dir", configDir)

	var settings config.SettingsDTO

	// 1. Read upstreams.json
	if data, err := os.ReadFile(upPath); err == nil && len(data) > 0 {
		var f config.UpstreamsFile
		if err := json.Unmarshal(data, &f); err == nil {
			settings.Upstreams = f.Upstreams
		}
	}

	// 2. Read models.json
	modPath := filepath.Join(configDir, config.FileNameModels)
	if data, err := os.ReadFile(modPath); err == nil && len(data) > 0 {
		var f config.ModelsFile
		if err := json.Unmarshal(data, &f); err == nil {
			settings.Models = f.Models
		}
	}

	// 3. Read tenants.json
	tenPath := filepath.Join(configDir, config.FileNameTenants)
	if data, err := os.ReadFile(tenPath); err == nil && len(data) > 0 {
		var f config.TenantsFile
		if err := json.Unmarshal(data, &f); err == nil {
			settings.Tenants = f.Tenants
		}
	}

	// 4. Read combos.json
	combPath := filepath.Join(configDir, config.FileNameCombos)
	if data, err := os.ReadFile(combPath); err == nil && len(data) > 0 {
		var f config.CombosFile
		if err := json.Unmarshal(data, &f); err == nil {
			settings.Combos = f.Combos
		}
	}

	if len(settings.Upstreams) == 0 && len(settings.Models) == 0 && len(settings.Tenants) == 0 {
		logger.Info("config files contain no records to bootstrap")
		return nil
	}

	if err := store.SaveSettings(ctx, settings); err != nil {
		return fmt.Errorf("bootstrap save settings to turso: %w", err)
	}

	logger.Info("turso database successfully bootstrapped from local config files",
		"upstreams", len(settings.Upstreams),
		"models", len(settings.Models),
		"combos", len(settings.Combos),
		"tenants", len(settings.Tenants),
	)

	return nil
}
