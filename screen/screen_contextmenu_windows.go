//go:build windows

package main

import (
	"fmt"
	"syscall"
	"time"
)

// click_menu — right-click to open a context menu, then pick one item from it
// by name, in one call. Without this, driving a right-click menu was a manual
// two-step dance the caller had to assemble itself: click(button=right), then
// guess whether the popup counts as a new "foreground window" for invoke's
// 'window' param (it usually does not look like one) — and the native
// right-click menu cannot be synthesized any other way (click's right/middle
// buttons only reach a page's own JS handler, not a real OS context menu; see
// click's tool description).

// contextMenuClasses are the window classes a right-click's popup can show up
// as: "#32768" for a classic (TrackPopupMenu-based) menu — Explorer, Office,
// the overwhelming majority of native Win32 apps — and
// "Microsoft.UI.Content.PopupWindowSiteBridge" for a WinUI3/XAML app's own
// flyout-drawn menu (confirmed against Windows 11's modern Notepad, which
// does not use a classic menu at all). Checked in this order since a classic
// menu is still the common case.
var contextMenuClasses = []string{"#32768", "Microsoft.UI.Content.PopupWindowSiteBridge"}

// contextMenuPollEvery is how often clickMenu re-checks for the popup. A var
// (not const) so tests can shrink it.
var contextMenuPollEvery = 30 * time.Millisecond

// findVisibleContextMenuWindows returns every visible top-level window
// currently matching one of contextMenuClasses, for clickMenu's before/after
// snapshot (so it reacts to the menu THIS click opened, not one already on
// screen from something else).
func findVisibleContextMenuWindows() map[uintptr]bool {
	out := map[uintptr]bool{}
	cb := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		if v, _, _ := procIsWindowVisible.Call(hwnd); v == 0 {
			return 1
		}
		class := getClassName(hwnd)
		for _, c := range contextMenuClasses {
			if class == c {
				out[hwnd] = true
				break
			}
		}
		return 1
	})
	procEnumWindows.Call(cb, 0)
	return out
}

// waitForNewContextMenu polls for a popup window not in before, for up to
// timeout. Returns 0 if none showed up.
func waitForNewContextMenu(before map[uintptr]bool, timeout time.Duration) uintptr {
	deadline := time.Now().Add(timeout)
	for {
		for hwnd := range findVisibleContextMenuWindows() {
			if !before[hwnd] {
				return hwnd
			}
		}
		if time.Now().After(deadline) {
			return 0
		}
		time.Sleep(contextMenuPollEvery)
	}
}

// clickMenu right-clicks (x,y), waits for the context menu that opens there,
// and invokes sel within it — the popup's own hwnd, not the window underneath
// (a menu is its own top-level window, so the usual "foreground window"
// targeting invoke otherwise uses would miss it entirely).
func clickMenu(x, y int, sel uiaSelector, timeout time.Duration) (string, error) {
	before := findVisibleContextMenuWindows()
	if err := mouseClick(x, y, "right"); err != nil {
		return "", fmt.Errorf("right-click at (%d,%d): %w", x, y, err)
	}
	menuHwnd := waitForNewContextMenu(before, timeout)
	if menuHwnd == 0 {
		return "", fmt.Errorf("no context menu appeared within %s after right-clicking (%d,%d) — the app may draw a popup under a class this doesn't recognize yet (try click(button=right) then snapshot to see what actually opened), or need a longer timeout_ms", timeout, x, y)
	}
	note, err := uiaInvokeIn(menuHwnd, sel)
	if err != nil {
		return "", err
	}
	return withNote(fmt.Sprintf("ok: right-clicked (%d,%d) and invoked %q", x, y, sel.Name), note), nil
}
