package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// AutoTLSDTO is the user-managed Let’s Encrypt configuration. Certificate
// material is never stored here; autocert keeps it in a separate private cache.
type AutoTLSDTO struct {
	Enabled bool   `json:"enabled"`
	Domain  string `json:"domain,omitempty"`
	Email   string `json:"email,omitempty"`
}

// DefaultAutoTLS returns the fail-safe default: the existing HTTP listener is
// left unchanged until an administrator deliberately enables HTTPS.
func DefaultAutoTLS() AutoTLSDTO { return AutoTLSDTO{} }

// LoadAutoTLS loads tls.json. A missing file is equivalent to Auto-TLS being
// disabled, which keeps upgrades backward compatible.
func LoadAutoTLS(dir string) (AutoTLSDTO, error) {
	cfg := DefaultAutoTLS()
	if dir == "" {
		return cfg, nil
	}

	raw, err := os.ReadFile(filepath.Join(dir, FileNameTLS))
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read tls config: %w", err)
	}
	if err := decodeStrict("tls", raw, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// SaveAutoTLS writes tls.json atomically so a process crash cannot leave an
// invalid partial configuration behind. Domain and email are operational data,
// not credentials; the certificate cache itself is created with mode 0700.
func SaveAutoTLS(dir string, cfg AutoTLSDTO) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal tls config: %w", err)
	}
	raw = append(raw, '\n')

	tmp, err := os.CreateTemp(dir, ".tls.json-*")
	if err != nil {
		return fmt.Errorf("create tls temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set tls temp file mode: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write tls temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close tls temp file: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(dir, FileNameTLS)); err != nil {
		return fmt.Errorf("replace tls config: %w", err)
	}
	return nil
}
