package main

import (
	"log"
	"os"
	"path/filepath"
)

// defaultHomeDir is the service's default working home: <userHome>/teleclaude.
func defaultHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "teleclaude"
	}
	return filepath.Join(home, "teleclaude")
}

// resolveHomeDir returns the configured home dir (or the default) and ensures it
// exists. Used as the fallback working directory for any conversation whose
// WorkDir is unset.
func resolveHomeDir(cfg *Config) string {
	dir := cfg.HomeDir
	if dir == "" {
		dir = defaultHomeDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[home] could not create home dir %q: %v", dir, err)
	}
	return dir
}
