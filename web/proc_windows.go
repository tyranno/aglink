//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// Windows process-creation flags (from processthreadsapi.h) used to fully
// detach the spawned daemon from the bridge's console so it survives the
// bridge exiting and shows no window.
const (
	createNewProcessGroup = 0x00000200
	detachedProcess       = 0x00000008
	createNoWindow        = 0x08000000
)

func applyDetach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup | detachedProcess | createNoWindow,
		HideWindow:    true,
	}
}

// createBreakawayFromJob lets the daemon outlive a job object its starter runs
// in — Chrome may place a native messaging host in one that is torn down with
// the port.
const createBreakawayFromJob = 0x01000000

// breakawayFromJob adds that flag and reports that it did. A job that forbids
// breakaway makes Start fail with it, so the caller retries without.
func breakawayFromJob(cmd *exec.Cmd) bool {
	cmd.SysProcAttr.CreationFlags |= createBreakawayFromJob
	return true
}
