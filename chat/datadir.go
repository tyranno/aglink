package main

import (
	"os"
	"path/filepath"
	"strings"
)

// dataDir returns the aglink data directory, creating it if necessary:
// $AGLINK_HOME if set, else ~/.aglink, else the pre-rename ~/.teleclaude when
// that exists and ~/.aglink does not. Duplicated from host/config.go so this
// module has no build dependency on its siblings — what is shared is only the
// on-disk convention for where the control token and attachments live.
func dataDir() (string, error) {
	if v := strings.TrimSpace(os.Getenv("AGLINK_HOME")); v != "" {
		if err := os.MkdirAll(v, 0o700); err != nil {
			return "", err
		}
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".aglink")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		legacy := filepath.Join(home, ".teleclaude")
		if st, lerr := os.Stat(legacy); lerr == nil && st.IsDir() {
			return legacy, nil
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}
