package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileNameTunnel is the configuration file name for the Cloudflare Tunnel
// ingress engine. It persists the chosen mode and (for named tunnels) the
// token so a restart re-runs the same tunnel instead of falling back to a
// fresh quick tunnel with a new random trycloudflare.com hostname.
const FileNameTunnel = "tunnel.json"

// TunnelDTO is the persisted Cloudflare Tunnel configuration. Token is a
// credential, so a file that carries one is written owner-only.
type TunnelDTO struct {
	Mode   string `json:"mode,omitempty"`    // quick | named
	Token  string `json:"token,omitempty"`   // named tunnel token (SECRET)
	BinDir string `json:"bin_dir,omitempty"` // directory holding the cloudflared binary
}

// NormalizeMode lowercases and trims the mode, returning "" for anything that
// is not a supported tunnel mode so callers can treat it as "unset".
func (c TunnelDTO) NormalizeMode() string {
	switch strings.ToLower(strings.TrimSpace(c.Mode)) {
	case "quick":
		return "quick"
	case "named":
		return "named"
	default:
		return ""
	}
}

// LoadTunnel loads tunnel.json. A missing file means the tunnel engine has no
// persisted configuration, which keeps upgrades backward compatible.
func LoadTunnel(dir string) (TunnelDTO, error) {
	var cfg TunnelDTO
	if dir == "" {
		return cfg, nil
	}

	raw, err := os.ReadFile(filepath.Join(dir, FileNameTunnel))
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read tunnel config: %w", err)
	}
	if err := decodeStrict("tunnel", raw, &cfg); err != nil {
		return cfg, err
	}
	cfg.Mode = cfg.NormalizeMode()
	return cfg, nil
}

// SaveTunnel writes tunnel.json atomically so a crash cannot leave a partial
// configuration behind. Named tunnels carry a bearer token, so the file is
// written owner-only (SecretFileMode); a token-free configuration uses the
// normal config permission.
func SaveTunnel(dir string, cfg TunnelDTO) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	cfg.Mode = cfg.NormalizeMode()
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal tunnel config: %w", err)
	}
	raw = append(raw, '\n')

	tmp, err := os.CreateTemp(dir, ".tunnel.json-*")
	if err != nil {
		return fmt.Errorf("create tunnel temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	perm := os.FileMode(0o644)
	if strings.TrimSpace(cfg.Token) != "" {
		perm = SecretFileMode
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set tunnel temp file mode: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write tunnel temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close tunnel temp file: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(dir, FileNameTunnel)); err != nil {
		return fmt.Errorf("commit tunnel config: %w", err)
	}
	return nil
}
