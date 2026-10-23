package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// FileNameWarp is the configuration file name for the embedded Cloudflare WARP
// egress engine. It persists operator tuning chosen from the dashboard so a
// restart resumes the same pool instead of silently reverting to the
// compiled-in default.
const FileNameWarp = "warp.json"

// WarpDTO is the persisted WARP tuning. It can also persist an optional
// WARP+ license key configured via the dashboard. When a license key is
// present, warp.json is written with owner-only permissions (SecretFileMode).
type WarpDTO struct {
	// PoolSize is the number of concurrent WARP slots kept active for
	// round-robin egress. Zero means "unset": the engine's built-in default
	// applies. Range policy (the dashboard's 1..64 window) belongs to the
	// warp package — see warp.ValidPoolSize — so this loader stays a plain
	// strict decoder and does not duplicate the bound.
	PoolSize int `json:"pool_size,omitempty"`

	// LicenseKey is an optional Cloudflare WARP+ / Zero Trust license key
	// configured by the operator via the dashboard. When set, it is used
	// during device registration to attach WARP+ privileges.
	LicenseKey string `json:"license_key,omitempty"`
}

// LoadWarp loads warp.json. A missing file means the operator never tuned the
// engine from the dashboard, which keeps upgrades backward compatible.
func LoadWarp(dir string) (WarpDTO, error) {
	var cfg WarpDTO
	if dir == "" {
		return cfg, nil
	}

	raw, err := os.ReadFile(filepath.Join(dir, FileNameWarp))
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read warp config: %w", err)
	}
	if err := decodeStrict("warp", raw, &cfg); err != nil {
		return cfg, err
	}
	// A negative pool is not a range-policy question: no engine version ever
	// supported it, so it can only be a corrupt or hand-mangled file.
	if cfg.PoolSize < 0 {
		return WarpDTO{}, fmt.Errorf("warp config: pool_size must not be negative (got %d)", cfg.PoolSize)
	}
	return cfg, nil
}

// SaveWarp writes warp.json atomically so a crash cannot leave a partial
// configuration behind. When a license key is configured, the file is written
// owner-only (SecretFileMode); a license-free configuration uses the shared
// read mode (0644).
func SaveWarp(dir string, cfg WarpDTO) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal warp config: %w", err)
	}
	raw = append(raw, '\n')

	tmp, err := os.CreateTemp(dir, ".warp.json-*")
	if err != nil {
		return fmt.Errorf("create warp temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	perm := os.FileMode(0o644)
	if cfg.LicenseKey != "" {
		perm = SecretFileMode
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set warp temp file mode: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write warp temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close warp temp file: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(dir, FileNameWarp)); err != nil {
		return fmt.Errorf("commit warp config: %w", err)
	}
	return nil
}
