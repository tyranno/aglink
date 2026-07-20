//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const exeSuffix = ".exe"

func killTree(pid int) error {
	return exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid)).Run()
}

// killByImageName force-kills all processes matching the given image name.
func killByImageName(name string) {
	exec.Command("taskkill", "/F", "/IM", name).Run()
}

// restartAglinkWebDaemon force-kills the persistent aglink-web "serve" daemon
// (identified by command line, not just image name — an "aglink-web mcp"
// bridge process shares the same image name but is a per-worker-turn child
// that must not be touched) so the next tool call auto-spawns a fresh daemon
// from the just-rebuilt binary. No-op if none is currently running.
func restartAglinkWebDaemon() {
	exec.Command("powershell", "-NoProfile", "-Command",
		`Get-CimInstance Win32_Process -Filter "Name='aglink-web.exe'" | `+
			`Where-Object { $_.CommandLine -like '*serve*' } | `+
			`ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }`,
	).Run()
}

// pluginRunStatuses reports, for each requested plugin base name, whether any
// process for it (image <name>.exe) is currently running, how many, and how many
// look like the persistent "serve" daemon (same CommandLine heuristic
// restartAglinkWebDaemon uses). One CIM query covers all aglink-* processes. The
// bool is false only when the query itself fails → callers report "unknown".
func pluginRunStatuses(names []string) (map[string]pluginRun, bool) {
	// Emit "<Name>\t<CommandLine>" per process so we can both count by image and
	// spot the serve daemon. `t is a PowerShell tab escape (literal backtick+t
	// in this Go string); $ is not interpolated by Go.
	psCmd := "Get-CimInstance Win32_Process -Filter \"Name LIKE 'aglink-%'\" | " +
		"ForEach-Object { \"$($_.Name)`t$($_.CommandLine)\" }"
	out, err := exec.Command("powershell", "-NoProfile", "-Command", psCmd).Output()
	if err != nil {
		return nil, false
	}
	res := make(map[string]pluginRun, len(names))
	for _, name := range names {
		res[name] = pluginRun{}
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		img := strings.ToLower(strings.TrimSpace(parts[0]))
		cmdline := ""
		if len(parts) > 1 {
			cmdline = strings.ToLower(parts[1])
		}
		for _, name := range names {
			if img != strings.ToLower(name+exeSuffix) {
				continue
			}
			pr := res[name]
			pr.running = true
			pr.total++
			if strings.Contains(cmdline, "serve") {
				pr.serve++
			}
			res[name] = pr
		}
	}
	return res, true
}

// killPreviousInstance terminates any running aglink processes (except self).
func killPreviousInstance() {
	myPID := os.Getpid()
	killed := false

	if b, err := os.ReadFile(pidFilePath()); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 && pid != myPID {
			if exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid)).Run() == nil {
				log.Printf("[main] killed previous instance via PID file (PID %d)", pid)
				killed = true
			}
		}
	}

	// The pre-rename names stay in this list on purpose: the first aglink.exe to
	// start after the rename must still find and kill a teleclaude.exe left
	// running from before it, or both would poll Telegram at once.
	for _, name := range []string{
		"aglink" + exeSuffix, "aglink_new" + exeSuffix,
		"teleclaude" + exeSuffix, "teleclaude_new" + exeSuffix,
	} {
		out, _ := exec.Command("tasklist", "/FI", "IMAGENAME eq "+name, "/FO", "CSV", "/NH").CombinedOutput()
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(strings.ToLower(line), "info:") {
				continue
			}
			parts := strings.Split(line, ",")
			if len(parts) < 2 {
				continue
			}
			pid, err := strconv.Atoi(strings.Trim(parts[1], `"`))
			if err != nil || pid <= 0 || pid == myPID {
				continue
			}
			if exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid)).Run() == nil {
				log.Printf("[main] killed competing %s (PID %d)", name, pid)
				killed = true
			}
		}
	}

	if killed {
		time.Sleep(3 * time.Second)
	}
}

// waitForProcessExit polls until the given PID is gone, then force-kills on timeout.
func waitForProcessExit(pid int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		out, _ := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH").CombinedOutput()
		alive := false
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(strings.ToLower(line), "info:") {
				alive = true
				break
			}
		}
		if !alive {
			log.Printf("[main] old process (PID %d) has exited", pid)
			return
		}
	}
	exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid)).Run()
	log.Printf("[main] force-killed old process (PID %d) after timeout", pid)
}

// applyCmdLine sets cmd.SysProcAttr.CmdLine for cmd.exe /C invocation.
// This field exists only on Windows; callers must use this helper for cross-platform builds.
func applyCmdLine(cmd *exec.Cmd, cmdLine string) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: cmdLine}
}

// findClaudeOS returns Windows-specific candidate paths for the claude CLI.
func findClaudeOS(home string) []string {
	return []string{
		filepath.Join(home, "AppData", "Roaming", "npm", "claude.cmd"),
		filepath.Join(home, "AppData", "Roaming", "npm", "claude.exe"),
		filepath.Join(home, ".local", "bin", "claude.exe"),
		`C:\Program Files\nodejs\claude.cmd`,
	}
}

// preferNativeClaude maps an npm `claude.cmd`/`claude.ps1` shim to the native
// `bin\claude.exe` it wraps. Go runs a .cmd/.ps1 via cmd.exe/powershell, which
// mangles complex worker args — the inline --mcp-config JSON and the multiline
// --append-system-prompt that pluginWorkerArgs adds once an aglink-* MCP plugin
// (screen/web) is active — so the worker dies with a spurious "'C:\Program' is
// not recognized" error. Plain chat (simple args) survives, which is why this
// only bites when a plugin is enabled. Exec'ing the native .exe directly avoids
// the shell entirely, so every arg reaches claude intact. Falls back to the
// shim if the expected node_modules layout isn't found.
func preferNativeClaude(path string) string {
	lower := strings.ToLower(path)
	if !strings.HasSuffix(lower, ".cmd") && !strings.HasSuffix(lower, ".ps1") {
		return path // already native (or an unknown launcher) — leave as-is
	}
	exe := filepath.Join(filepath.Dir(path),
		"node_modules", "@anthropic-ai", "claude-code", "bin", "claude.exe")
	if _, err := os.Stat(exe); err == nil {
		return exe
	}
	return path
}
