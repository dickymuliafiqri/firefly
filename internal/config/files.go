package config

import (
	"errors"
	"fmt"
	"os"
)

// SecretFileMode is the permission applied to configuration files that carry raw
// credentials: upstream API keys (upstreams.json) and tenant gateway keys
// (tenants.json).
const SecretFileMode os.FileMode = 0o600

// WriteSecretFile writes raw to path with owner-only permissions. os.WriteFile
// applies perm only when it creates the file, so a pre-existing file — one
// written by an older release at the default umask — is tightened explicitly.
func WriteSecretFile(path string, raw []byte) error {
	if err := os.WriteFile(path, raw, SecretFileMode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return tightenSecretFile(path)
}

// tightenSecretFile lowers an existing credential file to owner-only
// permissions. A missing file is not an error: the caller (or a later settings
// save) creates it with SecretFileMode already.
func tightenSecretFile(path string) error {
	if err := os.Chmod(path, SecretFileMode); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("tighten %s: %w", path, err)
	}
	return nil
}
