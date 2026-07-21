package main

import (
	"os"
	"os/exec"
	"strings"
)

// knownTools are the external executables aglink resolves through the tools:
// config section. The list drives the settings UI (one path field per tool) and
// documents what a `tools:` override is expected to point at; resolveToolPath
// itself works for any name, so an SSH host may reference a custom helper too.
var knownTools = []string{"ssh", "sshpass"}

// resolveToolPath returns the executable path for a named external tool:
// an explicit cfg.ToolPaths[name] override (only when it actually exists on disk)
// wins, otherwise a PATH lookup. An empty return means "not found" and the caller
// decides whether that's fatal. Unlike findClaude/findCodex this is one generic
// registry, so adding a new external tool (ssh, sshpass, future helpers) needs no
// bespoke finder. A configured-but-missing override deliberately falls through to
// PATH rather than hard-failing, so a stale path never breaks a machine where the
// tool is on PATH anyway.
func resolveToolPath(cfg *Config, name string) string {
	if cfg != nil {
		if p := strings.TrimSpace(cfg.ToolPaths[name]); p != "" {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}
