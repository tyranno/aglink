//go:build windows

package main

import (
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// Design Ref: screen_control.elevated — control elevated (admin) target apps.
//
// Windows UIPI (User Interface Privilege Isolation) silently drops synthetic
// input (SendInput button events, etc.) sent from a lower-integrity process to a
// higher-integrity (elevated) window. To drive elevated apps the whole aglink
// chain (aglink → claude worker → aglink-screen) must itself run elevated.
// These helpers detect our elevation and re-launch elevated via UAC; the
// per-window UIPI detection (windowIsElevated/uipiWarning) now lives in
// aglink-screen since only its screen tools need it.

// isElevated reports whether the current process token is elevated (admin).
func isElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// runAsAdmin launches target (an exe path, a .lnk, or an app name the shell can
// resolve) elevated via the "runas" verb, triggering a UAC prompt the user must
// approve. args may be empty. The elevated process is started with SW_HIDE so no
// console window pops up — aglink is a background bot and its only caller is
// the self-elevation relaunch (relaunchElevated); a visible console for the
// elevated instance is just noise. Logs still go to the (hidden) console's
// stderr, so redirect via the launcher/scheduled task if you need to capture them.
func runAsAdmin(target, args string) error {
	verbPtr, _ := windows.UTF16PtrFromString("runas")
	tgtPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	var argsPtr *uint16
	if args != "" {
		argsPtr, _ = windows.UTF16PtrFromString(args)
	}
	const swHide = 0
	return windows.ShellExecute(0, verbPtr, tgtPtr, argsPtr, nil, swHide)
}

// relaunchElevated re-launches this executable with the same arguments under the
// "runas" verb, triggering a one-time UAC prompt. The caller should exit the
// current (un-elevated) instance on success.
func relaunchElevated() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return runAsAdmin(exe, strings.Join(os.Args[1:], " "))
}
