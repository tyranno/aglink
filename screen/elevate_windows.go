//go:build windows

package main

import (
	"fmt"
	"strings"
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

// windowElevation reports whether the process owning hwnd is elevated.
// known=false when that cannot be determined — typically the process could
// not be opened or its token not queried, which usually (not always) means it
// is higher integrity than us.
func windowElevation(hwnd uintptr) (elevated, known bool) {
	var pid uint32
	procGetWindowThreadPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return false, true
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false, false
	}
	defer windows.CloseHandle(h)
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		return false, false
	}
	defer tok.Close()
	return tok.IsElevated(), true
}

// windowIsElevated reports whether the process owning hwnd is elevated. It is
// best-effort: if we cannot tell (typical when it is higher integrity than
// us), we treat it as elevated, which is the useful signal for a warning.
func windowIsElevated(hwnd uintptr) bool {
	el, known := windowElevation(hwnd)
	return el || !known
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

// uipiBlockError fails a SendInput-based action up front when it is CERTAIN
// to be dropped: the target window's process is known to be elevated and we
// are not. SendInput reports success even when UIPI discards the events, so
// without this the caller only learns from a later screenshot that nothing
// happened. Uncertain cases (process cannot be inspected) are left to
// uipiWarning, and UIA / BM_CLICK paths never come through here.
func uipiBlockError(hwnd uintptr, action string) error {
	if hwnd == 0 || isElevated() {
		return nil
	}
	el, known := windowElevation(hwnd)
	if !known || !el {
		return nil
	}
	top := hwnd
	if r, _, _ := procGetAncestor.Call(hwnd, gaRoot); r != 0 {
		top = r
	}
	return fmt.Errorf("window %q runs as administrator but aglink-screen does not, so Windows (UIPI) silently drops synthetic %s input to it — nothing was sent. "+
		"Try invoke/set_value/set_state/click_control (UI Automation / BM_CLICK paths, which may still reach it), "+
		"or run aglink-screen elevated (screen_control.elevated: true, or run teleclaude as administrator)", getWindowText(top), action)
}

// uipiCheckPoint is uipiBlockError for the window under screen point (x,y) —
// where a mouse click lands.
func uipiCheckPoint(x, y int, action string) error {
	ensureDPIAware()
	h, _, _ := procWindowFromPoint.Call(packPoint(int32(x), int32(y)))
	return uipiBlockError(h, action)
}

// uipiCheckForeground is uipiBlockError for the foreground window — where
// keyboard input goes.
func uipiCheckForeground(action string) error {
	h, _, _ := procGetForegroundWindow.Call()
	return uipiBlockError(h, action)
}

// uipiExemptCombo reports key combos whose effect does not depend on the
// foreground window accepting input — media/volume keys, PrintScreen, Win-key
// shortcuts and Ctrl+Alt+Del are handled by the system/shell — so an elevated
// foreground window is no reason to refuse them.
func uipiExemptCombo(combo string) bool {
	parts := strings.Split(strings.ToLower(combo), "+")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	for _, p := range parts {
		if p == "win" || p == "lwin" || p == "rwin" {
			return true
		}
	}
	switch strings.Join(parts, "+") {
	case "ctrl+alt+del", "ctrl+alt+delete":
		return true
	}
	switch parts[len(parts)-1] {
	case "volumeup", "volumedown", "volumemute", "mute", "medianext", "mediaprev", "mediastop", "mediaplay", "playpause", "printscreen", "prtsc":
		return true
	}
	return false
}
