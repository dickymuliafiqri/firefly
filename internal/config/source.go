package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dickymuliafiqri/firefly/internal/ports"
)

// FileConfigSource reads the three JSON files from a directory. It satisfies
// ports.ConfigSource.
type FileConfigSource struct {
	Dir string
}

// NewFileConfigSource returns a source rooted at dir.
func NewFileConfigSource(dir string) *FileConfigSource {
	return &FileConfigSource{Dir: dir}
}

// Filenames for each logical config file.
const (
	FileNameUpstreams  = "upstreams.json"
	FileNameModels     = "models.json"
	FileNameTenants    = "tenants.json"
	FileNameCombos     = "combos.json"
	FileNameTLS        = "tls.json"
	FileNameTokenSaver = "tokensaver.json"
)

// IsConfigFile reports whether base (a filename, not a path) is one of the
// tracked config files. The watcher uses this to ignore unrelated writes in the
// config directory (log files, editor swap files, etc.).
func IsConfigFile(base string) bool {
	switch base {
	case FileNameUpstreams, FileNameModels, FileNameTenants, FileNameCombos, FileNameTokenSaver:
		return true
	default:
		return false
	}
}

// Load reads all config files. It aggregates any read errors using errors.Join
// so operators can see all missing or unreadable files at once.
func (s *FileConfigSource) Load(ctx context.Context) (map[string][]byte, error) {
	fileOrder := []struct {
		logical string
		name    string
	}{
		{"upstreams", FileNameUpstreams},
		{"models", FileNameModels},
		{"tenants", FileNameTenants},
		{"combos", FileNameCombos},
		{"tokensaver", FileNameTokenSaver},
	}
	out := make(map[string][]byte, len(fileOrder))
	var errs []error
	for _, f := range fileOrder {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := filepath.Join(s.Dir, f.name)
		b, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				out[f.logical] = []byte("{}")
				continue
			}
			errs = append(errs, fmt.Errorf("read %s config: %w", f.logical, err))
			continue
		}
		out[f.logical] = b
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

var _ ports.ConfigSource = (*FileConfigSource)(nil)

// FileSetFromMap converts a ConfigSource map into a FileSet.
func FileSetFromMap(m map[string][]byte) FileSet {
	return FileSet{
		Upstreams:  m["upstreams"],
		Models:     m["models"],
		Tenants:    m["tenants"],
		Combos:     m["combos"],
		TokenSaver: m["tokensaver"],
	}
}

// EnsureConfigFiles ensures that the configuration directory and the JSON
// configuration files exist. If any file does not exist, an empty initial JSON
// template is created so that the directory is immediately valid and observable.
func EnsureConfigFiles(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	defaults := []struct {
		name    string
		content []byte
	}{
		{FileNameUpstreams, []byte("{\n  \"upstreams\": []\n}\n")},
		{FileNameModels, []byte("{\n  \"models\": []\n}\n")},
		{FileNameTenants, []byte("{\n  \"tenants\": []\n}\n")},
		{FileNameCombos, []byte("{\n  \"combos\": []\n}\n")},
		{FileNameTLS, []byte("{\n  \"enabled\": false\n}\n")},
		{FileNameTokenSaver, []byte("{\n  \"enabled\": false,\n  \"compress_tool_output\": true,\n  \"terse_output\": false,\n  \"minimal_code\": false,\n  \"compress_context\": false,\n  \"max_tool_output_chars\": 12000,\n  \"context_threshold\": 32000\n}\n")},
	}

	for _, d := range defaults {
		path := filepath.Join(dir, d.name)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(path, d.content, 0o644); err != nil {
				return fmt.Errorf("create default %s: %w", d.name, err)
			}
		}
	}
	return nil
}
