//go:build windows

package main

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

// Typing into the Chrome window from outside the browser: the last way past a
// certificate warning, since Chrome keeps extensions off its warning page.

const osKeysSupported = true

var (
	user32                         = syscall.NewLazyDLL("user32.dll")
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procGetForegroundWindow        = user32.NewProc("GetForegroundWindow")
	procGetWindowTextW             = user32.NewProc("GetWindowTextW")
	procGetWindowThreadProcessId   = user32.NewProc("GetWindowThreadProcessId")
	procSendInput                  = user32.NewProc("SendInput")
	procOpenProcess                = kernel32.NewProc("OpenProcess")
	procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
	procCloseHandle                = kernel32.NewProc("CloseHandle")
)

const (
	inputKeyboard                  = 1
	keyeventfKeyUp                 = 0x0002
	keyeventfUnicode               = 0x0004
	processQueryLimitedInformation = 0x1000
)

// keybdInput and winInput mirror KEYBDINPUT and INPUT. The trailing pad makes
// winInput as large as the union's biggest member (MOUSEINPUT), which
// SendInput checks against cbSize.
type keybdInput struct {
	wVk         uint16
	wScan       uint16
	dwFlags     uint32
	time        uint32
	dwExtraInfo uintptr
}

type winInput struct {
	typ uint32
	ki  keybdInput
	_   [8]byte
}

var (
	procEnumWindows         = user32.NewProc("EnumWindows")
	procIsWindowVisible     = user32.NewProc("IsWindowVisible")
	procIsIconic            = user32.NewProc("IsIconic")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procBringWindowToTop    = user32.NewProc("BringWindowToTop")
	procAttachThreadInput   = user32.NewProc("AttachThreadInput")
	procGetCurrentThreadId  = kernel32.NewProc("GetCurrentThreadId")
)

const swRestore = 9

// bringChromeForward finds the visible Chrome window whose title starts with
// wantTitle (Chrome titles a window after its active tab) and makes it the
// foreground window. Windows lets only the thread that owns the foreground
// hand it over, so this thread briefly shares input state with that one
// (AttachThreadInput) — no keystroke is sent, so nothing in the current
// window reacts. Best effort: the caller checks the result.
func bringChromeForward(wantTitle string) {
	runtime.LockOSThread() // AttachThreadInput is per OS thread
	defer runtime.UnlockOSThread()
	var found uintptr
	cb := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		if v, _, _ := procIsWindowVisible.Call(hwnd); v == 0 {
			return 1
		}
		title, exe := windowInfo(hwnd)
		if strings.EqualFold(filepath.Base(exe), "chrome.exe") && strings.HasPrefix(title, wantTitle) {
			found = hwnd
			return 0 // stop
		}
		return 1
	})
	procEnumWindows.Call(cb, 0)
	if found == 0 {
		return
	}
	if ic, _, _ := procIsIconic.Call(found); ic != 0 {
		procShowWindow.Call(found, swRestore)
	}
	fg, _, _ := procGetForegroundWindow.Call()
	fgThread, _, _ := procGetWindowThreadProcessId.Call(fg, 0)
	self, _, _ := procGetCurrentThreadId.Call()
	attached := false
	if fgThread != 0 && fgThread != self {
		r, _, _ := procAttachThreadInput.Call(self, fgThread, 1)
		attached = r != 0
	}
	procBringWindowToTop.Call(found)
	procSetForegroundWindow.Call(found)
	if attached {
		procAttachThreadInput.Call(self, fgThread, 0)
	}
	time.Sleep(200 * time.Millisecond)
}

// foregroundWindow returns the title and executable of the window that has
// the keyboard.
func foregroundWindow() (title, exe string) {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return "", ""
	}
	return windowInfo(hwnd)
}

// windowInfo returns a window's title and its process's executable path.
func windowInfo(hwnd uintptr) (title, exe string) {
	buf := make([]uint16, 512)
	n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	title = syscall.UTF16ToString(buf[:n])

	var pid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	h, _, _ := procOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if h == 0 {
		return title, ""
	}
	defer procCloseHandle.Call(h)
	path := make([]uint16, 1024)
	size := uint32(len(path))
	if ok, _, _ := procQueryFullProcessImageNameW.Call(h, 0, uintptr(unsafe.Pointer(&path[0])), uintptr(unsafe.Pointer(&size))); ok == 0 {
		return title, ""
	}
	return title, syscall.UTF16ToString(path[:size])
}

// typeIntoChrome types text into the foreground window — but only when that
// window is Chrome and is showing the tab titled wantTitle. Anything else in
// front (another app, or another Chrome window, such as a remote-desktop
// session) gets nothing: keystrokes that land in the wrong place are worse
// than none.
//
// Characters go in as Unicode (KEYEVENTF_UNICODE), not virtual keys, so the
// keyboard layout and a Korean IME left in Hangul mode cannot turn "t" into
// "ㅅ".
func typeIntoChrome(wantTitle, text string) error {
	return chromeKeys(wantTitle, text, 0)
}

// Virtual keys chromeKeys can finish with.
const (
	vkReturn = 0x0D
	vkEscape = 0x1B
)

// chromeKeys is typeIntoChrome followed by one virtual key (vkReturn,
// vkEscape; 0 for none) — how a JavaScript dialog is answered by hand.
func chromeKeys(wantTitle, text string, vk uint16) error {
	if wantTitle != "" {
		// activate_tab asks Chrome to come forward, but Windows refuses a
		// background app focus while the user works elsewhere. Bring it
		// forward from here if it is not already in front.
		if t, _ := foregroundWindow(); !strings.HasPrefix(t, wantTitle) {
			bringChromeForward(wantTitle)
		}
	}
	title, exe := foregroundWindow()
	if name := strings.ToLower(filepath.Base(exe)); name != "chrome.exe" {
		if name == "" || name == "." {
			name = "unknown"
		}
		return fmt.Errorf("the window in front is %s (%q), not Chrome — nothing was typed", name, title)
	}
	if wantTitle != "" && !strings.HasPrefix(title, wantTitle) {
		return fmt.Errorf("the Chrome window in front shows %q, not the warning tab %q — nothing was typed", title, wantTitle)
	}
	var ins []winInput
	for _, u := range utf16.Encode([]rune(text)) {
		ins = append(ins,
			winInput{typ: inputKeyboard, ki: keybdInput{wScan: u, dwFlags: keyeventfUnicode}},
			winInput{typ: inputKeyboard, ki: keybdInput{wScan: u, dwFlags: keyeventfUnicode | keyeventfKeyUp}},
		)
	}
	if vk != 0 {
		ins = append(ins,
			winInput{typ: inputKeyboard, ki: keybdInput{wVk: vk}},
			winInput{typ: inputKeyboard, ki: keybdInput{wVk: vk, dwFlags: keyeventfKeyUp}},
		)
	}
	if len(ins) == 0 {
		return nil
	}
	sent, _, err := procSendInput.Call(uintptr(len(ins)), uintptr(unsafe.Pointer(&ins[0])), unsafe.Sizeof(ins[0]))
	if int(sent) != len(ins) {
		return fmt.Errorf("SendInput delivered %d of %d key events (%v) — a locked screen or a higher-privileged window blocks it", sent, len(ins), err)
	}
	return nil
}
