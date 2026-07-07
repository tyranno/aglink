package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"
)

// aglinkChatUpdating suppresses the supervisor's respawn while !update rebuilds
// aglink-chat.exe (the running child is killed to release the file lock; the
// next teleclaude respawns it fresh).
var aglinkChatUpdating atomic.Bool

// resolveAglinkChatBinary locates the aglink-chat executable: explicit config
// path first, then next to teleclaude (where !update deploys it), then the
// sibling source repo. Returns "" when none exists.
func resolveAglinkChatBinary(cfg *Config, selfExe string) string {
	if cfg != nil && cfg.AglinkChatBinaryPath != "" {
		if _, err := os.Stat(cfg.AglinkChatBinaryPath); err == nil {
			return cfg.AglinkChatBinaryPath
		}
	}
	name := "aglink-chat" + exeSuffix
	srcDir := filepath.Dir(selfExe)
	candidates := []string{
		filepath.Join(srcDir, name),
		filepath.Join(filepath.Dir(srcDir), "aglink-chat", name),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// startAglinkChat spawns and supervises `aglink-chat serve`, restarting it with
// backoff if it exits. It first kills any orphan aglink-chat from a previous
// teleclaude (a handoff's os.Exit does not reap children on Windows). Blocks
// until ctx is cancelled; the child is killed via CommandContext on cancel.
func startAglinkChat(ctx context.Context, binPath, addr, controlAddr, controlToken, browserToken string) {
	killByImageName("aglink-chat" + exeSuffix) // clear an orphan from a prior instance
	backoff := time.Second
	for ctx.Err() == nil {
		cmd := exec.CommandContext(ctx, binPath, "serve",
			"--addr", addr,
			"--control-addr", controlAddr,
			"--control-token", controlToken,
			"--token", browserToken)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		log.Printf("[aglinkchat] starting %s serve on %s", binPath, addr)
		start := time.Now()
		err := cmd.Run()
		if ctx.Err() != nil {
			return
		}
		ran := time.Since(start)

		if aglinkChatUpdating.Load() {
			log.Printf("[aglinkchat] child stopped for update — pausing respawn")
			for aglinkChatUpdating.Load() && ctx.Err() == nil {
				time.Sleep(500 * time.Millisecond)
			}
			backoff = time.Second
			continue
		}

		log.Printf("[aglinkchat] serve exited after %s: %v — restarting in %s", ran.Round(time.Second), err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if ran > 30*time.Second {
			backoff = time.Second // healthy run → reset backoff
		} else {
			backoff = min(backoff*2, 15*time.Second)
		}
	}
}
