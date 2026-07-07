package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// pluginNames are the aglink-* sibling repos !update also rebuilds and
// deploys alongside teleclaude itself, so one command keeps the whole set
// (teleclaude + its Windows plugins) in sync instead of hand-building each
// repo and copying the binary over after every change.
var pluginNames = []string{"aglink-screen", "aglink-web", "aglink-chat"}

// updatePlugins rebuilds each plugin in pluginNames from its sibling source
// directory (../<name> relative to teleclaude's own source dir, srcDir) and
// drops the resulting binary directly into srcDir under <name>+exeSuffix —
// the exact layout resolveScreenBinaryPath/resolveWebBinaryPath auto-discover.
// A plugin whose sibling directory isn't checked out is silently skipped:
// most deployments (e.g. a headless NanoPi) don't have these Windows-only
// screen/browser-control plugins at all, and that must not block updating
// teleclaude itself. Returns a short per-plugin report for the Telegram
// progress message, or an error that aborts the whole !update (a broken
// plugin build shouldn't be silently deployed).
func updatePlugins(srcDir string) ([]string, error) {
	parent := filepath.Dir(srcDir)
	var report []string
	for _, name := range pluginNames {
		pluginDir := filepath.Join(parent, name)
		if _, statErr := os.Stat(pluginDir); statErr != nil {
			continue
		}
		target := filepath.Join(srcDir, name+exeSuffix)
		// aglink-chat runs as a supervised child; kill it (release the exe lock)
		// and pause the supervisor's respawn while we rebuild. The next teleclaude
		// respawns it from the fresh binary.
		if name == "aglink-chat" {
			aglinkChatUpdating.Store(true)
			killByImageName("aglink-chat" + exeSuffix)
		}
		buildCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		buildCmd := exec.CommandContext(buildCtx, "go", "build", "-o", target, ".")
		buildCmd.Dir = pluginDir
		out, buildErr := buildCmd.CombinedOutput()
		cancel()
		if name == "aglink-chat" {
			aglinkChatUpdating.Store(false)
		}
		if buildErr != nil {
			return report, fmt.Errorf("%s 빌드 실패:\n%s", name, strings.TrimSpace(string(out)))
		}
		report = append(report, name)
		if name == "aglink-web" {
			// The persistent "serve" daemon keeps running the old binary in
			// memory even after it's overwritten on disk; kill it so the next
			// tool call auto-spawns a fresh one from what was just built.
			// restartAglinkWebDaemon must NOT touch "mcp" bridge processes —
			// those are per-worker-turn children and killing one mid-turn
			// would break that conversation.
			restartAglinkWebDaemon()
		}
	}
	return report, nil
}
