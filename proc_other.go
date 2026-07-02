//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// applyDetach starts the daemon in its own session so it is not killed when the
// bridge's process group goes away.
func applyDetach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
