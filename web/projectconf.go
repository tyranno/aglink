package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// projectConfDir is the per-project settings directory the bridge looks for,
// walking up from its working directory the way git finds .git.
const projectConfDir = ".aglink-web"

// projectConfFile is the settings file inside projectConfDir.
const projectConfFile = "config"

// findProjectAccount walks up from startDir and returns the account the nearest
// .aglink-web/config pins, or "" when nothing up the tree pins one. A missing
// directory, missing file, unreadable file, or absent/blank account key all mean
// "no preference" — the caller falls back to the daemon's default profile, so
// this never reports an error. The walk does not stop at the first .aglink-web
// it sees: a directory whose config pins nothing keeps looking upward, so a
// subproject inherits its parent's pin instead of silently falling back.
func findProjectAccount(startDir string) string {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return ""
	}
	for {
		if acct := readAccountKey(filepath.Join(dir, projectConfDir, projectConfFile)); acct != "" {
			return acct
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "" // reached the volume root
		}
		dir = parent
	}
}

// readAccountKey parses `key = value` lines and returns the account value.
// Blank lines and # comments are skipped, as are keys we do not recognise.
func readAccountKey(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.TrimSpace(key) == "account" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
