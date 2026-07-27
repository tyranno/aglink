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

// pluginBuilds maps each aglink-* plugin to the MERGED sub-directory that now
// holds its source (screen/, web/, chat/ under aglink's own srcDir) and the
// binary name it produces. The five repos were merged into aglink (see README),
// and config.yaml points each plugin's binary_path at its sub-dir build
// (aglink/<subdir>/<name>.exe) — so !update must rebuild THOSE, not the stale
// standalone sibling checkouts (../aglink-screen, ...) that predate the merge.
// Building the siblings dropped the binary at aglink/<name>.exe, a path nothing
// runs, so plugin changes silently never deployed — the swap looked to succeed
// yet shipped nothing.
var pluginBuilds = []struct{ subdir, exe string }{
	{"screen", "aglink-screen"},
	{"web", "aglink-web"},
	{"chat", "aglink-chat"},
}

// pluginNames is the plugin binary-name list used by the setup/aux-status flows
// (setup_plugins_windows.go, auxfeatures.go). Kept in sync with pluginBuilds.
var pluginNames = []string{"aglink-screen", "aglink-web", "aglink-chat"}

// updatePlugins rebuilds each merged plugin from its sub-directory under srcDir
// and writes the binary to that sub-dir (aglink/<subdir>/<name>+exeSuffix) — the
// exact path config.yaml's binary_path names and the host spawns from. A plugin
// whose sub-dir isn't present is silently skipped: a headless deployment without
// the Windows-only screen/browser plugins must not block updating aglink itself.
// Returns a short per-plugin report for the Telegram progress message, or an
// error that aborts the whole !update (a broken plugin build shouldn't ship).
func updatePlugins(srcDir string) ([]string, error) {
	var report []string
	for _, pb := range pluginBuilds {
		pluginDir := filepath.Join(srcDir, pb.subdir)
		if _, statErr := os.Stat(filepath.Join(pluginDir, "go.mod")); statErr != nil {
			continue
		}
		name := pb.exe
		target := filepath.Join(pluginDir, name+exeSuffix)
		// aglink-chat runs as a supervised child; kill it (release the exe lock)
		// and pause the supervisor's respawn while we rebuild. The next aglink
		// respawns it from the fresh binary.
		if name == "aglink-chat" {
			aglinkChatUpdating.Store(true)
			killByImageName("aglink-chat" + exeSuffix)
		}
		buildCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		buildCmd := exec.CommandContext(buildCtx, "go", "build", "-o", target, ".")
		buildCmd.Dir = pluginDir
		// Build each plugin module alone via its own go.mod. With the monorepo
		// go.work active, `go build` runs in workspace mode (which also rejects the
		// module-mode -mod flag), so pin it off — matches the host build above.
		buildCmd.Env = append(os.Environ(), "GOWORK=off")
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
