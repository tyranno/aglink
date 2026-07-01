package main

import (
	"os"
	"path/filepath"
)

// dataDir returns ~/.teleclaude, creating it if necessary. Duplicated from
// teleclaude's config.go (dataDir) so this module has no build dependency on
// the parent repo — the only thing shared is the on-disk convention for where
// presets.json lives.
func dataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".teleclaude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}
