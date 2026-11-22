//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// anyChromeWindowRunning reports whether any chrome.exe window currently
// exists — not whether the aglink-web extension is connected, just whether
// the browser process itself is up. Reuses the EnumWindows/windowInfo
// machinery from oskeys_windows.go (same approach bringChromeForward uses to
// spot a Chrome window).
func anyChromeWindowRunning() bool {
	found := false
	cb := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		_, exe := windowInfo(hwnd)
		if strings.EqualFold(filepath.Base(exe), "chrome.exe") {
			found = true
			return 0 // stop
		}
		return 1
	})
	procEnumWindows.Call(cb, 0)
	return found
}

// chromeCandidatePaths lists where Chrome is installed on Windows, in the
// order setup.exe tries them (per-machine first, then per-user). No registry
// dependency — covers the overwhelming majority of real installs without
// pulling in golang.org/x/sys/windows/registry for one lookup.
func chromeCandidatePaths() []string {
	rel := filepath.Join("Google", "Chrome", "Application", "chrome.exe")
	var out []string
	for _, envVar := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
		if v := os.Getenv(envVar); v != "" {
			out = append(out, filepath.Join(v, rel))
		}
	}
	return out
}

// launchChrome starts Chrome with no arguments — the same as double-clicking
// its icon — so it opens whichever profile the user last used. It does not
// try to pick a specific profile: if the extension lives in a different one
// than Chrome opens by default, the caller still gets a clear "not connected"
// error afterward rather than this silently guessing wrong.
func launchChrome() error {
	for _, p := range chromeCandidatePaths() {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		cmd := exec.Command(p)
		if err := cmd.Start(); err == nil {
			_ = cmd.Process.Release()
			return nil
		}
	}
	return errChromeNotFound
}
