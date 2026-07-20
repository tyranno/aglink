//go:build windows

package main

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Design Ref: screen_control.elevated — control elevated (admin) target apps.
//
// Windows UIPI (User Interface Privilege Isolation) silently drops synthetic
// input (SendInput button events, etc.) sent from a lower-integrity process to
// a higher-integrity (elevated) window. This process inherits its elevation
// from the process tree that spawned it (teleclaude → claude worker →
// aglink-screen); self-relaunch lives in teleclaude, not here. These helpers
// only detect elevation to warn the caller when a click is likely to be
// silently dropped.

// isElevated reports whether the current process token is elevated (admin).
func isElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// runAsAdmin launches target (an exe path, a .lnk, or an app name the shell can
// resolve) elevated via the "runas" verb, triggering a UAC prompt the user must
// approve. args may be empty. Used by launch_app's elevated=true option.
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
	const swShowNormal = 1
	return windows.ShellExecute(0, verbPtr, tgtPtr, argsPtr, nil, swShowNormal)
}

// windowIsElevated reports whether the process owning hwnd is elevated. It is
// best-effort: if we cannot open the process (typical when it is higher
// integrity than us), we treat it as elevated, which is the useful signal.
func windowIsElevated(hwnd uintptr) bool {
	var pid uint32
	procGetWindowThreadPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return true // can't open → almost certainly higher integrity
	}
	defer windows.CloseHandle(h)
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		return true
	}
	defer tok.Close()
	return tok.IsElevated()
}

// uipiWarning returns a non-empty caveat when synthetic input (action, e.g.
// "click" or "scroll") into target hwnd is likely to be dropped by UIPI (target
// elevated, we are not). Empty otherwise.
func uipiWarning(hwnd uintptr, action string) string {
	if !isElevated() && windowIsElevated(hwnd) {
		return fmt.Sprintf(" — WARNING: target window is elevated but aglink-screen is not, so Windows UIPI likely ignored this %s. Set screen_control.elevated: true (or run teleclaude, and thus aglink-screen, as administrator).", action)
	}
	return ""
}
