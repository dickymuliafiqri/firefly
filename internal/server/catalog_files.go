package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dickymuliafiqri/firefly/internal/config"
)

// catalogFileSet holds the marshaled catalog JSON written to the config directory.
// A nil field is skipped, which is how an omitted TokenSaver section is preserved.
type catalogFileSet struct {
	upstreams  []byte
	models     []byte
	tenants    []byte
	combos     []byte
	tokenSaver []byte
}

// writeCatalogFiles persists the catalog snapshot into dir. upstreams.json holds
// raw provider keys and tenants.json holds tenant gateway keys, so both are
// written owner-only; the remaining files carry no credentials.
func writeCatalogFiles(dir string, files catalogFileSet) error {
	writes := []struct {
		name   string
		raw    []byte
		secret bool
	}{
		{config.FileNameUpstreams, files.upstreams, true},
		{config.FileNameModels, files.models, false},
		{config.FileNameTenants, files.tenants, true},
		{config.FileNameCombos, files.combos, false},
		{config.FileNameTokenSaver, files.tokenSaver, false},
	}
	for _, wr := range writes {
		if wr.raw == nil {
			continue
		}
		path := filepath.Join(dir, wr.name)
		var err error
		if wr.secret {
			err = config.WriteSecretFile(path, wr.raw)
		} else {
			err = os.WriteFile(path, wr.raw, 0o644)
		}
		if err != nil {
			return fmt.Errorf("write %s config: %w", strings.TrimSuffix(wr.name, ".json"), err)
		}
	}
	return nil
}
