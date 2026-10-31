//go:build !windows

package main

import (
	"os"
	"os/exec"
)

func hideConsole(*exec.Cmd) {}

func killWithApp(*os.Process) {}
