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
