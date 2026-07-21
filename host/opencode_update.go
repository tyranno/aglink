package main

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// opencodeUpdate is the cached result of comparing the installed opencode CLI
// against the latest published on npm. Both probes are slow (npm view is
// network-bound), so the check runs on a background timer and versionPayload
// only ever reads this cache — a UI version poll never blocks on npm. A stale
// but present cache is preferred to blocking; CheckedAt lets the UI show age.
type opencodeUpdate struct {
	Installed string    `json:"installed"`
	Latest    string    `json:"latest"`
	Available bool      `json:"available"`
	CheckedAt time.Time `json:"checkedAt"`
}

var (
	opencodeUpdMu   sync.RWMutex
	opencodeUpdData opencodeUpdate
)

// opencodeUpdateSnapshot returns the current cached update state.
func opencodeUpdateSnapshot() opencodeUpdate {
	opencodeUpdMu.RLock()
	defer opencodeUpdMu.RUnlock()
	return opencodeUpdData
}

// parseSemver splits "1.18.4" (tolerating a leading v, trailing "-beta" or extra
// tokens) into [major, minor, patch]. Returns nil when there's nothing numeric to
// compare, so semverNewer can refuse to nag on an unknown version.
func parseSemver(v string) []int {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if fields := strings.Fields(v); len(fields) > 0 {
		v = fields[0] // "opencode 1.18.4" → "1.18.4"
	}
	v = strings.TrimPrefix(v, "v")
	v = strings.SplitN(v, "-", 2)[0] // drop prerelease/build suffix
	v = strings.SplitN(v, "+", 2)[0]
	segs := strings.Split(v, ".")
	// The major segment must actually be a number; otherwise this isn't a version
	// (e.g. "garbage") and we return nil so semverNewer never nags on it.
	if len(segs) == 0 || !isAllDigits(strings.TrimSpace(segs[0])) {
		return nil
	}
	out := []int{0, 0, 0}
	for i := 0; i < 3 && i < len(segs); i++ {
		out[i] = atoiOr(strings.TrimSpace(segs[i]), 0)
	}
	return out
}

// isAllDigits reports whether s is non-empty and every rune is a decimal digit.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// semverNewer reports whether latest is strictly newer than installed. Either
// side unparseable → false (never surface a spurious "update available").
func semverNewer(installed, latest string) bool {
	ip, lp := parseSemver(installed), parseSemver(latest)
	if ip == nil || lp == nil {
		return false
	}
	for i := 0; i < 3; i++ {
		if lp[i] != ip[i] {
			return lp[i] > ip[i]
		}
	}
	return false
}

// runCmdOut runs a short command with a timeout and returns trimmed stdout ("" on
// any error). Indirected through a var so tests don't shell out.
var runCmdOut = func(timeout time.Duration, name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// refreshOpencodeUpdate probes the installed opencode version and the latest on
// npm, then stores the comparison in the cache. A machine without opencode simply
// records an empty Installed (versionPayload then omits the opencode fields).
func refreshOpencodeUpdate(cfg *Config) {
	upd := opencodeUpdate{CheckedAt: time.Now()}
	path, _ := findOpencode(cfg.OpencodePath)
	if path == "" {
		opencodeUpdMu.Lock()
		opencodeUpdData = upd
		opencodeUpdMu.Unlock()
		return
	}
	upd.Installed = parseVersionLine(runCmdOut(10*time.Second, path, "--version"))
	if npm, err := exec.LookPath("npm"); err == nil {
		upd.Latest = parseVersionLine(runCmdOut(30*time.Second, npm, "view", "opencode-ai", "version"))
	}
	if upd.Installed != "" && upd.Latest != "" {
		upd.Available = semverNewer(upd.Installed, upd.Latest)
	}
	opencodeUpdMu.Lock()
	opencodeUpdData = upd
	opencodeUpdMu.Unlock()
}

// parseVersionLine extracts the version token from a --version line, tolerating
// a "opencode 1.18.4" prefix or a bare "1.18.4". Returns the first whitespace
// token that parses as a version (leading v stripped), else the trimmed input.
func parseVersionLine(s string) string {
	s = strings.TrimSpace(s)
	for _, f := range strings.Fields(s) {
		tok := strings.TrimPrefix(f, "v")
		if parseSemver(tok) != nil {
			return tok
		}
	}
	return s
}

// startOpencodeUpdateChecker refreshes the cache shortly after boot and then on a
// long interval. Cheap to run always: with opencode absent it just records an
// empty snapshot and stops surfacing anything in the UI.
func startOpencodeUpdateChecker(cfgh *ConfigHolder) {
	go func() {
		time.Sleep(20 * time.Second) // let boot settle before the first (npm) probe
		refreshOpencodeUpdate(cfgh.Get())
		t := time.NewTicker(6 * time.Hour)
		defer t.Stop()
		for range t.C {
			refreshOpencodeUpdate(cfgh.Get())
		}
	}()
}
