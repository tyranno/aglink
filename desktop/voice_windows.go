package main

import (
	"os/exec"
	"syscall"
)

// hideConsole keeps a console child (whisper-cli.exe) from flashing a terminal
// window over this GUI-subsystem app.
func hideConsole(cmd *exec.Cmd) {
	const createNoWindow = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
