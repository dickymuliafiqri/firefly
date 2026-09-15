package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// FileNameTurso is the configuration file name for Turso database credentials.
const FileNameTurso = "turso.json"

// TursoDTO holds the credentials and replica options for Turso centralized database.
type TursoDTO struct {
	DatabaseURL     string `json:"database_url"`
	AuthToken       string `json:"auth_token"`
	LocalPath       string `json:"local_path,omitempty"`
	SyncIntervalSec int    `json:"sync_interval_sec,omitempty"`
}

// LoadTursoConfig loads turso.json from the configuration directory.
func LoadTursoConfig(dir string) (TursoDTO, error) {
	var cfg TursoDTO
	if dir == "" {
		return cfg, nil
	}

	raw, err := os.ReadFile(filepath.Join(dir, FileNameTurso))
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read turso config: %w", err)
	}
	if err := decodeStrict("turso", raw, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// SaveTursoConfig writes turso.json atomically into the configuration directory.
func SaveTursoConfig(dir string, cfg TursoDTO) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal turso config: %w", err)
	}
	raw = append(raw, '\n')

	tmp, err := os.CreateTemp(dir, ".turso.json-*")
	if err != nil {
		return fmt.Errorf("create temp turso config: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp turso config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp turso config: %w", err)
	}

	target := filepath.Join(dir, FileNameTurso)
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("commit turso config: %w", err)
	}

	return nil
}
