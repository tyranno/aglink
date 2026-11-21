//go:build windows

package main

import (
	"fmt"
	"strings"

	ole "github.com/go-ole/go-ole"
)

// Background (no focus steal) operation.
//
// UIA patterns and window messages act on a window without synthesizing real
// mouse/keyboard input, so they need neither the foreground nor the user to
// pause. Such operations still take the cross-process control lease (two
// sessions must not drive the same app at once) but skip beginSyntheticInput's
// user-idle wait and start toast; the agent cursor marks where they acted.

var (
	procGetWindowLongPtrW = modUser32.NewProc("GetWindowLongPtrW")
	procIsWindowEnabled   = modUser32.NewProc("IsWindowEnabled")
)

const (
	gwlStyle = ^uintptr(15) // GWL_STYLE (-16)
	bmClick  = 0x00F5
)

// beginBackgroundOp gates a background operation: lease only. A var so tests
// can stub it.
var beginBackgroundOp = func() error { return acquireControlLease() }

// beginUIAOp is the gate for a UIA action rooted at hwnd: background when a
// window was named, the full synthetic-input gate (old behavior) otherwise.
func beginUIAOp(hwnd uintptr) error {
	if hwnd != 0 {
		return beginBackgroundOp()
	}
	return beginSyntheticInput()
}

// resolveTargetWindow maps a tool's optional `window` argument (title
// substring or hwnd, same matching as focus_window) to a handle. "" means "no
// window given" and returns 0 so callers keep the foreground-window behavior.
func resolveTargetWindow(window string) (uintptr, error) {
	w := strings.TrimSpace(window)
	if w == "" {
		return 0, nil
	}
	h, ok := findTopWindowProbe(w)
	if !ok || h == 0 {
		return 0, fmt.Errorf("no window matching %q", w)
	}
	return h, nil
}

// uiaTargetLabel names the UIA search root in messages.
func uiaTargetLabel(hwnd uintptr) string {
	if hwnd == 0 {
		return "foreground window"
	}
	return fmt.Sprintf("window %q", getWindowText(hwnd))
}

// backgroundSuffix is appended to a UIA tool's result when it ran rooted at a
// named window, so the caller knows focus was not touched.
func backgroundSuffix(hwnd uintptr) string {
	if hwnd == 0 {
		return ""
	}
	return fmt.Sprintf(" in %s (background: not focused, cursor untouched)", uiaTargetLabel(hwnd))
}

func windowStyle(hwnd uintptr) uint32 {
	r, _, _ := procGetWindowLongPtrW.Call(hwnd, gwlStyle)
	return uint32(r)
}

func isWindowEnabled(hwnd uintptr) bool {
	r, _, _ := procIsWindowEnabled.Call(hwnd)
	return r != 0
}

// postBMClick posts BM_CLICK. PostMessage (not SendMessage) so a handler that
// opens a modal dialog cannot block this call; it fails under UIPI (elevated
// target) or for a dead window.
func postBMClick(hwnd uintptr) error {
	if r, _, err := procPostMessage.Call(hwnd, bmClick, 0, 0); r == 0 {
		return fmt.Errorf("PostMessage(BM_CLICK): %v", err)
	}
	return nil
}

// uiaHwndInvokable reports whether the UIA element for a child window supports
// a pattern activateElement can use.
func uiaHwndInvokable(hwnd uintptr) bool {
	r, err := uiaDo(func(uia *ole.IUnknown) (string, error) {
		el, err := uiaRoot(uia, hwnd)
		if err != nil {
			return "", err
		}
		defer release(el)
		if invokeCapable(el) {
			return "yes", nil
		}
		return "", nil
	})
	return err == nil && r == "yes"
}

// uiaInvokeHwnd activates the UIA element of a child window.
func uiaInvokeHwnd(hwnd uintptr, label string) error {
	_, err := uiaDo(func(uia *ole.IUnknown) (string, error) {
		el, err := uiaRoot(uia, hwnd)
		if err != nil {
			return "", err
		}
		defer release(el)
		return "", activateElement(el, label)
	})
	return err
}

// backgroundClickControl tries to click c (a child of top) without focusing
// top or moving the cursor. handled=false means the caller should fall back to
// the focus+click path; reason then says why. A lease error (SCREEN_BUSY) is
// returned as err with handled=true — never a reason to steal focus instead.
func backgroundClickControl(top uintptr, c control) (detail string, handled bool, reason string, err error) {
	path, reason := chooseClickPath(true, clickTarget{
		Class:   c.Class,
		Style:   windowStyle(c.HWND),
		Enabled: isWindowEnabled(c.HWND),
	}, func() bool { return uiaHwndInvokable(c.HWND) })
	if path == clickPathFocus {
		return "", false, reason, nil
	}
	if err := beginBackgroundOp(); err != nil {
		return "", true, "", err
	}
	var actErr error
	if path == clickPathBMClick {
		actErr = postBMClick(c.HWND)
	} else {
		actErr = uiaInvokeHwnd(c.HWND, c.Text)
	}
	if actErr != nil {
		return "", false, fmt.Sprintf("%s failed: %v", path, actErr), nil
	}
	if iconic, _, _ := procIsIconic.Call(top); iconic == 0 {
		showAgentCursor(c.CenterX(), c.CenterY())
	}
	return fmt.Sprintf("clicked %s %q via %s (background: window not focused, cursor untouched — focus_window before typing into it)%s",
		c.Class, c.Text, path, uipiWarning(c.HWND, "click")), true, "", nil
}
