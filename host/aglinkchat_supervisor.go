package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// aglinkChatUpdating suppresses the supervisor's respawn while !update rebuilds
// aglink-chat.exe (the running child is killed to release the file lock; the
// next aglink respawns it fresh).
var aglinkChatUpdating atomic.Bool

// fastExitWarnThreshold is how many back-to-back immediate exits the supervisor
// tolerates before warning that the failure looks permanent.
const fastExitWarnThreshold = 3

// healthyRunDuration is how long a child must stay up for its next crash to be
// treated as a one-off rather than a loop; maxRespawnBackoff caps the wait.
const (
	healthyRunDuration = 30 * time.Second
	maxRespawnBackoff  = 15 * time.Second
)

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
// aglink (a handoff's os.Exit does not reap children on Windows). Blocks
// until ctx is cancelled; the child is killed via CommandContext on cancel.
func startAglinkChat(ctx context.Context, binPath, addr, controlAddr, controlToken, browserToken string) {
	// Clear an orphan from a prior aglink. The by-image sweep is skipped for an
	// explicit parallel instance (AGLINK_HOME) — it would also reap the main
	// install's healthy aglink-chat, which is not ours to touch — so that case
	// relies on the recorded PID, which names only *our* child.
	killPreviousAglinkChat()
	if !isolatedDataDir() {
		killByImageName("aglink-chat" + exeSuffix)
	}
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
		var err error
		if err = cmd.Start(); err == nil {
			writeAglinkChatPID(cmd.Process.Pid)
			err = cmd.Wait()
		}
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
		// 2727 (a WinNAT-reserved range, which is why the default is 27271
		// now) behind a 15s backoff.
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
		backoff = nextBackoff(ran, backoff)
	}
}

// aglinkChatPIDPath is where the running aglink-chat child's PID is recorded, so
// the next aglink can reap it by PID. It sits in the data dir, which means a
// parallel AGLINK_HOME instance records only its own child.
func aglinkChatPIDPath() string {
	dir, err := dataDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "aglink-chat.pid")
}

func writeAglinkChatPID(pid int) {
	if p := aglinkChatPIDPath(); p != "" {
		_ = os.WriteFile(p, []byte(strconv.Itoa(pid)+"\n"), 0o600)
	}
}

// killPreviousAglinkChat reaps the aglink-chat left behind by a previous aglink
// that died without taking its child down (a /F kill of the parent, a crash).
// Such an orphan keeps holding the chat port, and the supervisor below would
// otherwise respawn-loop against it forever.
func killPreviousAglinkChat() {
	p := aglinkChatPIDPath()
	if p == "" {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 || pid == os.Getpid() {
		return
	}
	// The PID may have been reused since we wrote it (across a reboot, say), so
	// only kill it if it is still an aglink-chat.
	if !strings.EqualFold(processImageName(pid), "aglink-chat"+exeSuffix) {
		_ = os.Remove(p)
		return
	}
	if killTree(pid) == nil {
		log.Printf("[aglinkchat] reaped orphan child from previous run (PID %d)", pid)
	}
	_ = os.Remove(p)
}

// nextBackoff decides how long to wait before the next respawn. A child that
// stayed up long enough to be healthy resets the delay; anything shorter doubles
// it, up to a ceiling — so a child failing on something a restart cannot fix
// stops hammering, but a one-off crash recovers immediately.
func nextBackoff(ran, current time.Duration) time.Duration {
	if ran > healthyRunDuration {
		return time.Second
	}
	return min(current*2, maxRespawnBackoff)
}
