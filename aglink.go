package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

// resolveAglinkBinary locates an aglink-* helper executable (aglink-chat,
// aglink-screen, aglink-web) by base name. Every aglink feature resolves its
// binary through here so the rules are identical across them:
//
//  1. the explicit path from config (screen_control.binary_path,
//     web_control.binary_path, aglink_chat.binary_path) when it exists;
//  2. next to teleclaude's own executable — the expected deployment layout,
//     and where !update deploys rebuilt plugins;
//  3. the sibling source repo (../aglink-<name>/) for a dev checkout;
//  4. PATH, so a system-installed helper works with no config at all.
//
// Returns "" when none exists; callers then skip the feature rather than
// pointing a worker at a nonexistent path.
//
// A configured path that does not exist is logged and falls through rather than
// being handed downstream: the failure used to surface only as an opaque MCP
// exec error at turn time, and on the elevated instance that output was hidden.
func resolveAglinkBinary(name, configuredPath, selfExe string) string {
	exe := name + exeSuffix

	if configuredPath != "" {
		if _, err := os.Stat(configuredPath); err == nil {
			return configuredPath
		}
		log.Printf("[aglink] %s: configured binary_path %q not found — falling back to sibling/PATH lookup", name, configuredPath)
	}

	if selfExe != "" {
		dir := filepath.Dir(selfExe)
		candidates := []string{
			filepath.Join(dir, exe),
			filepath.Join(filepath.Dir(dir), name, exe),
		}
		for _, p := range candidates {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}

	if p, err := exec.LookPath(exe); err == nil {
		return p
	}
	return ""
}
