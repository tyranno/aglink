package main

import (
	"context"
	"log"
	"os/exec"
	"sync/atomic"
	"time"
)

// aglinkChatUpdating suppresses the supervisor's respawn while !update rebuilds
// aglink-chat.exe (the running child is killed to release the file lock; the
// next teleclaude respawns it fresh).
var aglinkChatUpdating atomic.Bool

// fastExitWarnThreshold is how many back-to-back immediate exits the supervisor
// tolerates before warning that the failure looks permanent.
const fastExitWarnThreshold = 3

// resolveAglinkChatBinary locates the aglink-chat executable. See
// resolveAglinkBinary for the shared lookup order.
func resolveAglinkChatBinary(cfg *Config, selfExe string) string {
	var configured string
	if cfg != nil {
		configured = cfg.AglinkChatBinaryPath
	}
	return resolveAglinkBinary("aglink-chat", configured, selfExe)
}

// startAglinkChat spawns and supervises `aglink-chat serve`, restarting it with
// backoff if it exits. It first kills any orphan aglink-chat from a previous
// teleclaude (a handoff's os.Exit does not reap children on Windows). Blocks
// until ctx is cancelled; the child is killed via CommandContext on cancel.
func startAglinkChat(ctx context.Context, binPath, addr, controlAddr, controlToken, browserToken string) {
	killByImageName("aglink-chat" + exeSuffix) // clear an orphan from a prior instance
	backoff := time.Second
	fastExits := 0
	for ctx.Err() == nil {
		cmd := exec.CommandContext(ctx, binPath, "serve",
			"--addr", addr,
			"--control-addr", controlAddr,
			"--control-token", controlToken,
			"--token", browserToken)
		cmd.Stdout = childLogWriter
		cmd.Stderr = childLogWriter
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

		// A child that never stays up is failing on something a restart cannot
		// fix (a port it may not bind, a missing dependency). Say so once, loudly,
		// instead of looping quietly forever — that silence hid an unbindable
		// 1717 (reserved by WinNAT) behind a 15s backoff.
		if ran < 5*time.Second {
			fastExits++
			if fastExits == fastExitWarnThreshold {
				log.Printf("[aglinkchat] WARNING: exited immediately %d times in a row — likely a permanent failure. "+
					"Check that %s is free (a Windows/WinNAT reserved port range can block it: "+
					"`netsh int ipv4 show excludedportrange protocol=tcp`) and see the child's output above.",
					fastExits, addr)
			}
		} else {
			fastExits = 0
		}

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
